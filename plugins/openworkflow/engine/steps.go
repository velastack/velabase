package engine

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/pocketbase/dbx"
)

// CreateStepAttemptParams holds the inputs for CreateStepAttempt.
type CreateStepAttemptParams struct {
	WorkflowRunID string
	WorkerID      string // accepted for API parity; the insert is unguarded
	StepName      string
	Kind          string
	Config        json.RawMessage
	Context       json.RawMessage
}

// runningStepAttemptOwnedWhere matches a running step attempt whose parent run
// is also running and held by the given worker. Uses the same namespace for the
// step and its run.
const runningStepAttemptOwnedWhere = `
	"namespace_id" = {:ns}
	AND "workflow_run_id" = {:runId}
	AND "id" = {:stepId}
	AND "status" = 'running'
	AND EXISTS (
		SELECT 1
		FROM "workflow_runs" wr
		WHERE wr."namespace_id" = {:ns}
		AND wr."id" = {:runId}
		AND wr."status" = 'running'
		AND wr."worker_id" = {:worker}
	)`

// CreateStepAttempt inserts a new running step attempt.
func (e *Engine) CreateStepAttempt(ns string, p CreateStepAttemptParams) (*StepAttempt, error) {
	id := generateUUID()
	now := isoNow()
	_, err := e.writeDB.NewQuery(`
		INSERT INTO "step_attempts" (
			"namespace_id", "id", "workflow_run_id", "step_name", "kind", "status",
			"config", "context", "started_at", "created_at", "updated_at"
		) VALUES (
			{:ns}, {:id}, {:runId}, {:stepName}, {:kind}, 'running',
			{:config}, {:context}, {:now}, {:now}, {:now}
		)`).Bind(dbx.Params{
		"ns":       ns,
		"id":       id,
		"runId":    p.WorkflowRunID,
		"stepName": p.StepName,
		"kind":     p.Kind,
		"config":   rawToNull(p.Config),
		"context":  rawToNull(p.Context),
		"now":      now,
	}).Execute()
	if err != nil {
		return nil, err
	}
	step, err := e.getStepAttemptBy(e.writeDB, ns, id)
	if err != nil {
		return nil, err
	}
	if err := requireRow(step != nil, "create step attempt"); err != nil {
		return nil, err
	}
	return step, nil
}

// GetStepAttempt returns a step attempt or nil if not found.
func (e *Engine) GetStepAttempt(ns, id string) (*StepAttempt, error) {
	return e.getStepAttemptBy(e.readDB, ns, id)
}

func (e *Engine) getStepAttemptBy(b dbx.Builder, ns, id string) (*StepAttempt, error) {
	var row stepAttemptRow
	err := b.NewQuery(`SELECT * FROM "step_attempts" WHERE "namespace_id" = {:ns} AND "id" = {:id} LIMIT 1`).
		Bind(dbx.Params{"ns": ns, "id": id}).
		One(&row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return row.toStepAttempt(), nil
}

func (e *Engine) updateAndFetchStep(ns, stepID, operation, updateSQL string, params dbx.Params) (*StepAttempt, error) {
	var step *StepAttempt
	err := e.tx(func(tx *dbx.Tx) error {
		var row stepAttemptRow
		err := tx.NewQuery(updateSQL + ` RETURNING *`).Bind(params).One(&row)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				return requireRow(false, operation)
			}
			return err
		}
		step = row.toStepAttempt()
		return nil
	})
	if err != nil {
		return nil, err
	}
	return step, nil
}

// CompleteStepAttempt marks a worker-owned step completed.
func (e *Engine) CompleteStepAttempt(ns, runID, stepID, workerID string, output json.RawMessage) (*StepAttempt, error) {
	now := isoNow()
	return e.updateAndFetchStep(ns, stepID, "mark step attempt completed", `
		UPDATE "step_attempts"
		SET "status" = 'completed', "output" = {:output}, "error" = NULL,
		    "finished_at" = {:now}, "updated_at" = {:now}
		WHERE `+runningStepAttemptOwnedWhere,
		dbx.Params{"output": rawToNull(output), "now": now, "ns": ns, "runId": runID, "stepId": stepID, "worker": workerID})
}

// FailStepAttempt marks a worker-owned step failed.
func (e *Engine) FailStepAttempt(ns, runID, stepID, workerID string, errJSON json.RawMessage) (*StepAttempt, error) {
	now := isoNow()
	return e.updateAndFetchStep(ns, stepID, "mark step attempt failed", `
		UPDATE "step_attempts"
		SET "status" = 'failed', "output" = NULL, "error" = {:err},
		    "finished_at" = {:now}, "updated_at" = {:now}
		WHERE `+runningStepAttemptOwnedWhere,
		dbx.Params{"err": rawToNull(errJSON), "now": now, "ns": ns, "runId": runID, "stepId": stepID, "worker": workerID})
}

// SetStepAttemptChildWorkflowRun links a worker-owned step to its child run.
func (e *Engine) SetStepAttemptChildWorkflowRun(ns, runID, stepID, workerID, childNs, childID string) (*StepAttempt, error) {
	now := isoNow()
	return e.updateAndFetchStep(ns, stepID, "set step attempt child workflow run", `
		UPDATE "step_attempts"
		SET "child_workflow_run_namespace_id" = {:cns}, "child_workflow_run_id" = {:cid}, "updated_at" = {:now}
		WHERE `+runningStepAttemptOwnedWhere,
		dbx.Params{"cns": childNs, "cid": childID, "now": now, "ns": ns, "runId": runID, "stepId": stepID, "worker": workerID})
}

// ListStepAttempts returns a cursor-paginated page of a run's step attempts
// (oldest first).
func (e *Engine) ListStepAttempts(ns, workflowRunID string, p ListParams) ([]*StepAttempt, paginationMeta, error) {
	limit := clampLimit(p.Limit)
	cur, err := decodeListCursor(p.After, p.Before)
	if err != nil {
		return nil, paginationMeta{}, err
	}
	hasAfter := p.After != ""
	hasBefore := p.Before != ""
	order, op := paginationOrder("ASC", hasBefore)

	where := `"namespace_id" = {:ns} AND "workflow_run_id" = {:runId}`
	params := dbx.Params{"ns": ns, "runId": workflowRunID}
	if cur != nil {
		where += ` AND ("created_at", "id") ` + op + ` ({:cca}, {:cid})`
		params["cca"] = cur.CreatedAt
		params["cid"] = cur.ID
	}
	params["lim"] = limit + 1

	query := `SELECT * FROM "step_attempts" WHERE ` + where +
		` ORDER BY "created_at" ` + order + `, "id" ` + order + ` LIMIT {:lim}`

	var rows []stepAttemptRow
	if err := e.readDB.NewQuery(query).Bind(params).All(&rows); err != nil {
		return nil, paginationMeta{}, err
	}

	steps := make([]*StepAttempt, len(rows))
	for i := range rows {
		steps[i] = rows[i].toStepAttempt()
	}
	data, meta := buildPaginatedResponse(steps, limit, hasAfter, hasBefore, func(s *StepAttempt) (string, string) {
		return s.CreatedAt, s.ID
	})
	return data, meta, nil
}
