package engine

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/pocketbase/dbx"
)

// runIdempotencyPeriod is the dedup window for CreateWorkflowRun calls that
// supply an idempotency key.
const runIdempotencyPeriod = 24 * time.Hour

// CreateWorkflowRunParams holds the inputs for CreateWorkflowRun.
type CreateWorkflowRunParams struct {
	WorkflowName                 string
	Version                      *string
	IdempotencyKey               *string
	Config                       json.RawMessage
	Context                      json.RawMessage
	Input                        json.RawMessage
	ParentStepAttemptNamespaceID *string
	ParentStepAttemptID          *string
	AvailableAt                  *string // ISO; nil = now (immediately available)
	DeadlineAt                   *string // ISO; nil = no deadline
}

// FailWorkflowRunParams holds the inputs for FailWorkflowRun.
type FailWorkflowRunParams struct {
	WorkerID    string
	Error       json.RawMessage
	RetryPolicy RetryPolicy
	Attempts    *int    // nil (or DeadlineAt nil) => fall back to reading the run
	DeadlineAt  *string // ISO; used only when Attempts is also provided
}

// ListParams is the cursor pagination input.
type ListParams struct {
	Limit  int
	After  string
	Before string
}

// ListRunsParams is the workflow-run list input: cursor pagination plus the
// optional filters. An empty filter string means "unset".
type ListRunsParams struct {
	ListParams
	Status       string
	WorkflowName string
}

// CreateWorkflowRun inserts a new run, deduplicating on the idempotency key
// within the dedup window.
func (e *Engine) CreateWorkflowRun(ns string, p CreateWorkflowRunParams) (*WorkflowRun, error) {
	if p.IdempotencyKey == nil {
		return e.insertWorkflowRun(e.writeDB, ns, p)
	}

	var run *WorkflowRun
	err := e.tx(func(tx *dbx.Tx) error {
		threshold := isoFromTime(time.Now().UTC().Add(-runIdempotencyPeriod))
		existing, err := e.getWorkflowRunByIdempotencyKey(tx, ns, p.WorkflowName, *p.IdempotencyKey, threshold)
		if err != nil {
			return err
		}
		if existing != nil {
			run = existing
			return nil
		}
		run, err = e.insertWorkflowRun(tx, ns, p)
		return err
	})
	if err != nil {
		return nil, err
	}
	return run, nil
}

func (e *Engine) insertWorkflowRun(b dbx.Builder, ns string, p CreateWorkflowRunParams) (*WorkflowRun, error) {
	id := generateUUID()
	now := isoNow()

	// Normalize client timestamps to the canonical ISO form; the admin UI sends a
	// space-separated form that would otherwise sort wrong in the lexical
	// available_at/deadline_at comparisons.
	availableAt := now
	if p.AvailableAt != nil {
		v, err := normalizeISO(*p.AvailableAt)
		if err != nil {
			return nil, err
		}
		availableAt = v
	}
	deadline, err := normalizeISOPtr(p.DeadlineAt)
	if err != nil {
		return nil, err
	}

	_, err = b.NewQuery(`
		INSERT INTO "workflow_runs" (
			"namespace_id", "id", "workflow_name", "version", "status", "idempotency_key",
			"config", "context", "input", "attempts",
			"parent_step_attempt_namespace_id", "parent_step_attempt_id",
			"available_at", "deadline_at", "created_at", "updated_at"
		) VALUES (
			{:ns}, {:id}, {:name}, {:version}, 'pending', {:idem},
			{:config}, {:context}, {:input}, 0,
			{:pns}, {:pid},
			{:available}, {:deadline}, {:now}, {:now}
		)`).Bind(dbx.Params{
		"ns":        ns,
		"id":        id,
		"name":      p.WorkflowName,
		"version":   ptrToNull(p.Version),
		"idem":      ptrToNull(p.IdempotencyKey),
		"config":    rawToNull(p.Config),
		"context":   rawToNull(p.Context),
		"input":     rawToNull(p.Input),
		"pns":       ptrToNull(p.ParentStepAttemptNamespaceID),
		"pid":       ptrToNull(p.ParentStepAttemptID),
		"available": availableAt,
		"deadline":  deadline,
		"now":       now,
	}).Execute()
	if err != nil {
		return nil, err
	}

	run, err := e.getWorkflowRunBy(b, ns, id)
	if err != nil {
		return nil, err
	}
	if err := requireRow(run != nil, "create workflow run"); err != nil {
		return nil, err
	}
	return run, nil
}

func (e *Engine) getWorkflowRunByIdempotencyKey(b dbx.Builder, ns, name, key, createdAtThreshold string) (*WorkflowRun, error) {
	var row workflowRunRow
	err := b.NewQuery(`
		SELECT * FROM "workflow_runs"
		WHERE "namespace_id" = {:ns}
		  AND "workflow_name" = {:name}
		  AND "idempotency_key" = {:key}
		  AND "created_at" >= {:threshold}
		ORDER BY "created_at" ASC, "id" ASC
		LIMIT 1`).
		Bind(dbx.Params{"ns": ns, "name": name, "key": key, "threshold": createdAtThreshold}).
		One(&row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return row.toWorkflowRun(), nil
}

// GetWorkflowRun returns a run or nil if not found.
func (e *Engine) GetWorkflowRun(ns, id string) (*WorkflowRun, error) {
	return e.getWorkflowRunBy(e.readDB, ns, id)
}

func (e *Engine) getWorkflowRunBy(b dbx.Builder, ns, id string) (*WorkflowRun, error) {
	var row workflowRunRow
	err := b.NewQuery(`SELECT * FROM "workflow_runs" WHERE "namespace_id" = {:ns} AND "id" = {:id} LIMIT 1`).
		Bind(dbx.Params{"ns": ns, "id": id}).
		One(&row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return row.toWorkflowRun(), nil
}

// ClaimWorkflowRun atomically expires deadline-exceeded runs, finds the next
// available run (pending first, then by available_at/created_at), and claims it
// for workerID, extending the lease. Returns nil on a claim miss.
func (e *Engine) ClaimWorkflowRun(ns, workerID string, leaseDurationMs int64) (*WorkflowRun, error) {
	now := time.Now().UTC()
	nowISO := isoFromTime(now)
	newAvailableAt := isoFromTime(now.Add(time.Duration(leaseDurationMs) * time.Millisecond))

	var run *WorkflowRun
	err := e.tx(func(tx *dbx.Tx) error {
		// 1. mark any deadline-expired workflow runs as failed
		if _, err := tx.NewQuery(`
			UPDATE "workflow_runs"
			SET "status" = 'failed', "error" = {:err}, "worker_id" = NULL,
			    "available_at" = NULL, "finished_at" = {:now}, "updated_at" = {:now}
			WHERE "namespace_id" = {:ns}
			  AND "status" IN ('pending', 'running', 'sleeping')
			  AND "deadline_at" IS NOT NULL AND "deadline_at" <= {:now}`).
			Bind(dbx.Params{
				"err": string(mustJSON(serializedError{Message: "Workflow run deadline exceeded"})),
				"now": nowISO,
				"ns":  ns,
			}).Execute(); err != nil {
			return err
		}

		// 2. find an available workflow run to claim
		var candidate struct {
			ID string `db:"id"`
		}
		err := tx.NewQuery(`
			SELECT "id" FROM "workflow_runs"
			WHERE "namespace_id" = {:ns}
			  AND "status" IN ('pending', 'running', 'sleeping')
			  AND "available_at" <= {:now}
			  AND ("deadline_at" IS NULL OR "deadline_at" > {:now})
			ORDER BY
			  CASE WHEN "status" = 'pending' THEN 0 ELSE 1 END,
			  "available_at",
			  "created_at"
			LIMIT 1`).
			Bind(dbx.Params{"ns": ns, "now": nowISO}).
			One(&candidate)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return nil // claim miss: commit the expiry sweep, return nil
			}
			return err
		}

		// 3. claim the workflow run (RETURNING * avoids a follow-up SELECT)
		var row workflowRunRow
		if err := tx.NewQuery(`
			UPDATE "workflow_runs"
			SET "status" = 'running', "attempts" = "attempts" + 1, "worker_id" = {:worker},
			    "available_at" = {:available}, "started_at" = COALESCE("started_at", {:now}),
			    "updated_at" = {:now}
			WHERE "id" = {:id} AND "namespace_id" = {:ns}
			RETURNING *`).
			Bind(dbx.Params{"worker": workerID, "available": newAvailableAt, "now": nowISO, "id": candidate.ID, "ns": ns}).
			One(&row); err != nil {
			return err
		}

		run = row.toWorkflowRun()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return run, nil
}

// updateAndFetchRun runs a guarded UPDATE on the write pool and returns the
// post-update row, or ErrNotFound (via requireRow) if the guard matched no row.
func (e *Engine) updateAndFetchRun(ns, id, operation, updateSQL string, params dbx.Params) (*WorkflowRun, error) {
	var run *WorkflowRun
	err := e.tx(func(tx *dbx.Tx) error {
		var row workflowRunRow
		err := tx.NewQuery(updateSQL + ` RETURNING *`).Bind(params).One(&row)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return requireRow(false, operation)
			}
			return err
		}
		run = row.toWorkflowRun()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return run, nil
}

// ExtendWorkflowRunLease extends the lease for a run owned by workerID.
func (e *Engine) ExtendWorkflowRunLease(ns, id, workerID string, leaseDurationMs int64) (*WorkflowRun, error) {
	now := time.Now().UTC()
	newAvailableAt := isoFromTime(now.Add(time.Duration(leaseDurationMs) * time.Millisecond))
	return e.updateAndFetchRun(ns, id, "extend lease for workflow run", `
		UPDATE "workflow_runs"
		SET "available_at" = {:available}, "updated_at" = {:now}
		WHERE "namespace_id" = {:ns} AND "id" = {:id} AND "status" = 'running' AND "worker_id" = {:worker}`,
		dbx.Params{"available": newAvailableAt, "now": isoFromTime(now), "ns": ns, "id": id, "worker": workerID})
}

// sleepSQL parks a run until availableAt, but resumes immediately when a child
// workflow already finished or a waited-for signal was already delivered.
const sleepSQL = `
	UPDATE "workflow_runs"
	SET
		"status" = 'running',
		"available_at" = CASE
			WHEN (
				EXISTS (
					SELECT 1
					FROM "step_attempts" sa
					JOIN "workflow_runs" child
						ON child."namespace_id" = sa."child_workflow_run_namespace_id"
						AND child."id" = sa."child_workflow_run_id"
					WHERE sa."namespace_id" = "workflow_runs"."namespace_id"
					AND sa."workflow_run_id" = "workflow_runs"."id"
					AND sa."kind" = 'workflow'
					AND sa."status" = 'running'
					AND child."status" IN ('completed', 'succeeded', 'failed', 'canceled')
				)
				OR EXISTS (
					SELECT 1
					FROM "step_attempts" sa
					JOIN "workflow_signals" ws
						ON ws."namespace_id" = sa."namespace_id"
						AND ws."step_attempt_id" = sa."id"
					WHERE sa."namespace_id" = "workflow_runs"."namespace_id"
					AND sa."workflow_run_id" = "workflow_runs"."id"
					AND sa."kind" = 'signal-wait'
					AND sa."status" = 'running'
				)
			) AND {:resumeAt} > {:now} THEN {:now}
			ELSE {:resumeAt}
		END,
		"worker_id" = NULL,
		"updated_at" = {:now}
	WHERE "namespace_id" = {:ns}
	AND "id" = {:id}
	AND "status" NOT IN ('succeeded', 'completed', 'failed', 'canceled')
	AND "worker_id" = {:worker}`

// SleepWorkflowRun parks a running, worker-owned run until availableAt.
func (e *Engine) SleepWorkflowRun(ns, id, workerID, availableAtISO string) (*WorkflowRun, error) {
	resumeAt, err := normalizeISO(availableAtISO)
	if err != nil {
		return nil, err
	}
	now := isoNow()
	return e.updateAndFetchRun(ns, id, "sleep workflow run", sleepSQL, dbx.Params{
		"resumeAt": resumeAt, "now": now, "ns": ns, "id": id, "worker": workerID,
	})
}

// CompleteWorkflowRun marks a worker-owned run completed and wakes its parent.
func (e *Engine) CompleteWorkflowRun(ns, id, workerID string, output json.RawMessage) (*WorkflowRun, error) {
	now := isoNow()
	var run *WorkflowRun
	err := e.tx(func(tx *dbx.Tx) error {
		var row workflowRunRow
		err := tx.NewQuery(`
			UPDATE "workflow_runs"
			SET "status" = 'completed', "output" = {:output}, "error" = NULL, "worker_id" = {:worker},
			    "available_at" = NULL, "finished_at" = {:now}, "updated_at" = {:now}
			WHERE "namespace_id" = {:ns} AND "id" = {:id} AND "status" = 'running' AND "worker_id" = {:worker}
			RETURNING *`).
			Bind(dbx.Params{"output": rawToNull(output), "worker": workerID, "now": now, "ns": ns, "id": id}).
			One(&row)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return requireRow(false, "mark workflow run completed")
			}
			return err
		}
		run = row.toWorkflowRun()
		return e.wakeParentWorkflowRun(tx, run)
	})
	if err != nil {
		return nil, err
	}
	return run, nil
}

// FailWorkflowRun marks a worker-owned run failed/pending per the retry policy
// and wakes the parent on terminal failure.
func (e *Engine) FailWorkflowRun(ns, id string, p FailWorkflowRunParams) (*WorkflowRun, error) {
	now := time.Now().UTC()
	var run *WorkflowRun
	err := e.tx(func(tx *dbx.Tx) error {
		attempts := 0
		var deadlineAt *time.Time

		// Use caller-provided state only when BOTH attempts and deadline are
		// present; otherwise fall back to the persisted run, so a caller that
		// passes attempts but omits deadlineAt still honors the run's deadline
		// instead of treating the run as deadline-less.
		if p.Attempts != nil && p.DeadlineAt != nil {
			attempts = *p.Attempts
			t, err := parseISO(*p.DeadlineAt)
			if err != nil {
				return err
			}
			deadlineAt = &t
		} else {
			existing, err := e.getWorkflowRunBy(tx, ns, id)
			if err != nil {
				return err
			}
			if existing == nil {
				return fmt.Errorf("workflow run %s not found: %w", id, ErrNotFound)
			}
			attempts = existing.Attempts
			if existing.DeadlineAt != nil {
				t, err := parseISO(*existing.DeadlineAt)
				if err != nil {
					return err
				}
				deadlineAt = &t
			}
		}

		upd := computeFailedWorkflowRunUpdate(p.RetryPolicy, attempts, deadlineAt, p.Error, now)

		var row workflowRunRow
		err := tx.NewQuery(`
			UPDATE "workflow_runs"
			SET "status" = {:status}, "available_at" = {:available}, "finished_at" = {:finished},
			    "error" = {:err}, "worker_id" = NULL, "started_at" = NULL, "updated_at" = {:now}
			WHERE "namespace_id" = {:ns} AND "id" = {:id} AND "status" = 'running' AND "worker_id" = {:worker}
			RETURNING *`).
			Bind(dbx.Params{
				"status":    upd.Status,
				"available": timePtrToNull(upd.AvailableAt),
				"finished":  timePtrToNull(upd.FinishedAt),
				"err":       rawToNull(upd.Error),
				"now":       isoFromTime(now),
				"ns":        ns,
				"id":        id,
				"worker":    p.WorkerID,
			}).One(&row)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return requireRow(false, "mark workflow run failed")
			}
			return err
		}
		run = row.toWorkflowRun()
		if run.Status == statusFailed {
			return e.wakeParentWorkflowRun(tx, run)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return run, nil
}

// RescheduleWorkflowRunAfterFailedStepAttempt resets a worker-owned run to
// pending with the worker-computed availableAt.
func (e *Engine) RescheduleWorkflowRunAfterFailedStepAttempt(ns, id, workerID, availableAtISO string, errJSON json.RawMessage) (*WorkflowRun, error) {
	available, err := normalizeISO(availableAtISO)
	if err != nil {
		return nil, err
	}
	now := isoNow()
	return e.updateAndFetchRun(ns, id, "reschedule workflow run after failed step attempt", `
		UPDATE "workflow_runs"
		SET "status" = 'pending', "available_at" = {:available}, "finished_at" = NULL,
		    "error" = {:err}, "worker_id" = NULL, "started_at" = NULL, "updated_at" = {:now}
		WHERE "namespace_id" = {:ns} AND "id" = {:id} AND "status" = 'running' AND "worker_id" = {:worker}`,
		dbx.Params{"available": available, "err": rawToNull(errJSON), "now": now, "ns": ns, "id": id, "worker": workerID})
}

// CancelWorkflowRun cancels a non-terminal run (idempotent if already canceled)
// and wakes the parent.
func (e *Engine) CancelWorkflowRun(ns, id string) (*WorkflowRun, error) {
	now := isoNow()
	var run *WorkflowRun
	err := e.tx(func(tx *dbx.Tx) error {
		var row workflowRunRow
		err := tx.NewQuery(`
			UPDATE "workflow_runs"
			SET "status" = 'canceled', "worker_id" = NULL, "available_at" = NULL,
			    "finished_at" = {:now}, "updated_at" = {:now}
			WHERE "namespace_id" = {:ns} AND "id" = {:id}
			  AND "status" IN ('pending', 'running', 'sleeping')
			RETURNING *`).
			Bind(dbx.Params{"now": now, "ns": ns, "id": id}).
			One(&row)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				existing, err := e.getWorkflowRunBy(tx, ns, id)
				if err != nil {
					return err
				}
				run, err = resolveCancelConflict(id, existing)
				return err
			}
			return err
		}
		run = row.toWorkflowRun()
		return e.wakeParentWorkflowRun(tx, run)
	})
	if err != nil {
		return nil, err
	}
	return run, nil
}

// resolveCancelConflict maps a cancel that matched no row to an idempotent
// success (already canceled) or a conflict (already in another terminal state).
func resolveCancelConflict(id string, existing *WorkflowRun) (*WorkflowRun, error) {
	if existing == nil {
		return nil, fmt.Errorf("workflow run %s does not exist: %w", id, ErrNotFound)
	}
	if existing.Status == statusCanceled {
		return existing, nil
	}
	if existing.Status == statusSucceeded || existing.Status == statusCompleted || existing.Status == statusFailed {
		return nil, fmt.Errorf("cannot cancel workflow run %s with status %s: %w", id, existing.Status, ErrConflict)
	}
	return nil, fmt.Errorf("failed to cancel workflow run: %w", ErrConflict)
}

// wakeParentWorkflowRun nudges a sleeping/unclaimed parent run to become
// available now when a child reaches a terminal state.
func (e *Engine) wakeParentWorkflowRun(b dbx.Builder, child *WorkflowRun) error {
	if child.ParentStepAttemptNamespaceID == nil || child.ParentStepAttemptID == nil {
		return nil
	}
	now := isoNow()
	_, err := b.NewQuery(`
		UPDATE "workflow_runs"
		SET
			"available_at" = CASE
				WHEN "available_at" IS NULL OR "available_at" > {:now} THEN {:now}
				ELSE "available_at"
			END,
			"updated_at" = {:now}
		WHERE "namespace_id" = {:pns}
		AND "id" = (
			SELECT "workflow_run_id"
			FROM "step_attempts"
			WHERE "namespace_id" = {:pns}
			AND "id" = {:pid}
			AND "kind" = 'workflow'
			AND "status" = 'running'
			AND "child_workflow_run_namespace_id" = {:cns}
			AND "child_workflow_run_id" = {:cid}
			LIMIT 1
		)
		AND (
			"status" = 'sleeping'
			OR ("status" = 'running' AND "worker_id" IS NULL)
		)`).
		Bind(dbx.Params{
			"now": now,
			"pns": *child.ParentStepAttemptNamespaceID,
			"pid": *child.ParentStepAttemptID,
			"cns": child.NamespaceID,
			"cid": child.ID,
		}).Execute()
	return err
}

// CountWorkflowRuns returns status counts, folding the deprecated statuses into
// their replacements.
func (e *Engine) CountWorkflowRuns(ns string) (WorkflowRunCounts, error) {
	var rows []struct {
		Status string `db:"status"`
		Count  int    `db:"count"`
	}
	err := e.readDB.NewQuery(`
		SELECT "status", COUNT(*) AS "count" FROM "workflow_runs"
		WHERE "namespace_id" = {:ns} GROUP BY "status"`).
		Bind(dbx.Params{"ns": ns}).
		All(&rows)
	if err != nil {
		return WorkflowRunCounts{}, err
	}

	counts := WorkflowRunCounts{}
	for _, r := range rows {
		switch r.Status {
		case statusSucceeded: // deprecated -> completed
			counts.Completed += r.Count
		case statusSleeping: // deprecated -> running
			counts.Running += r.Count
		case statusPending:
			counts.Pending += r.Count
		case statusRunning:
			counts.Running += r.Count
		case statusCompleted:
			counts.Completed += r.Count
		case statusFailed:
			counts.Failed += r.Count
		case statusCanceled:
			counts.Canceled += r.Count
		}
	}
	return counts, nil
}

// ListWorkflowRuns returns a cursor-paginated page of runs (newest first).
func (e *Engine) ListWorkflowRuns(ns string, p ListRunsParams) ([]*WorkflowRun, paginationMeta, error) {
	limit := clampLimit(p.Limit)
	cur, err := decodeListCursor(p.After, p.Before)
	if err != nil {
		return nil, paginationMeta{}, err
	}
	hasAfter := p.After != ""
	hasBefore := p.Before != ""
	order, op := paginationOrder("DESC", hasBefore)

	where := `"namespace_id" = {:ns}`
	params := dbx.Params{"ns": ns}
	// Filters are exact matches on the stored value. Unlike CountWorkflowRuns,
	// the deprecated statuses are deliberately NOT folded into their
	// replacements: this engine never writes them (a parked run stays
	// "running"), so folding would only affect rows written by a foreign
	// OpenWorkflow backend sharing the file, and the reference backend filters
	// on the stored value verbatim.
	if p.Status != "" {
		where += ` AND "status" = {:status}`
		params["status"] = p.Status
	}
	if p.WorkflowName != "" {
		where += ` AND "workflow_name" = {:wfName}`
		params["wfName"] = p.WorkflowName
	}
	if cur != nil {
		where += ` AND ("created_at", "id") ` + op + ` ({:cca}, {:cid})`
		params["cca"] = cur.CreatedAt
		params["cid"] = cur.ID
	}
	params["lim"] = limit + 1

	query := `SELECT * FROM "workflow_runs" WHERE ` + where +
		` ORDER BY "created_at" ` + order + `, "id" ` + order + ` LIMIT {:lim}`

	var rows []workflowRunRow
	if err := e.readDB.NewQuery(query).Bind(params).All(&rows); err != nil {
		return nil, paginationMeta{}, err
	}

	runs := make([]*WorkflowRun, len(rows))
	for i := range rows {
		runs[i] = rows[i].toWorkflowRun()
	}
	data, meta := buildPaginatedResponse(runs, limit, hasAfter, hasBefore, func(r *WorkflowRun) (string, string) {
		return r.CreatedAt, r.ID
	})
	return data, meta, nil
}
