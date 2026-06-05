package engine

import (
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/pocketbase/dbx"
)

// SendSignalParams holds the inputs for SendSignal.
type SendSignalParams struct {
	Signal         string
	Data           json.RawMessage
	IdempotencyKey *string
}

// SendSignal delivers a signal to all running signal-wait steps matching the
// signal name and wakes their workflow runs. Idempotent on the sender
// idempotency key. Returns the woken workflow run ids.
func (e *Engine) SendSignal(ns string, p SendSignalParams) ([]string, error) {
	now := isoNow()
	result := []string{}

	err := e.tx(func(tx *dbx.Tx) error {
		// Idempotent replay: return the previously-delivered run ids.
		if p.IdempotencyKey != nil {
			var existing []struct {
				WorkflowRunID string `db:"workflow_run_id"`
			}
			if err := tx.NewQuery(`
				SELECT DISTINCT "workflow_run_id"
				FROM "workflow_signals"
				WHERE "namespace_id" = {:ns} AND "signal" = {:signal} AND "sender_idempotency_key" = {:idem}`).
				Bind(dbx.Params{"ns": ns, "signal": p.Signal, "idem": *p.IdempotencyKey}).
				All(&existing); err != nil {
				return err
			}
			if len(existing) > 0 {
				for _, r := range existing {
					result = append(result, r.WorkflowRunID)
				}
				return nil
			}
		}

		// Find all running waiters for this signal.
		var waiters []struct {
			ID            string `db:"id"`
			WorkflowRunID string `db:"workflow_run_id"`
		}
		if err := tx.NewQuery(`
			SELECT "id", "workflow_run_id"
			FROM "step_attempts"
			WHERE "namespace_id" = {:ns}
			  AND "kind" = 'signal-wait'
			  AND "status" = 'running'
			  AND json_extract("context", '$.signal') = {:signal}`).
			Bind(dbx.Params{"ns": ns, "signal": p.Signal}).
			All(&waiters); err != nil {
			return err
		}
		if len(waiters) == 0 {
			return nil
		}

		// Deliver one signal per waiting step (unique per step attempt).
		delivered := []string{}
		seen := map[string]bool{}
		for _, w := range waiters {
			res, err := tx.NewQuery(`
				INSERT OR IGNORE INTO "workflow_signals" (
					"namespace_id", "id", "signal", "data",
					"sender_idempotency_key", "workflow_run_id",
					"step_attempt_id", "created_at"
				) VALUES ({:ns}, {:id}, {:signal}, {:data}, {:idem}, {:runId}, {:stepId}, {:now})`).
				Bind(dbx.Params{
					"ns":     ns,
					"id":     generateUUID(),
					"signal": p.Signal,
					"data":   rawToNull(p.Data),
					"idem":   ptrToNull(p.IdempotencyKey),
					"runId":  w.WorkflowRunID,
					"stepId": w.ID,
					"now":    now,
				}).Execute()
			if err != nil {
				return err
			}
			n, err := res.RowsAffected()
			if err != nil {
				return err
			}
			if n > 0 && !seen[w.WorkflowRunID] {
				seen[w.WorkflowRunID] = true
				delivered = append(delivered, w.WorkflowRunID)
			}
		}

		if len(delivered) == 0 {
			return nil
		}

		// Wake each waiting workflow run.
		for _, runID := range delivered {
			if _, err := tx.NewQuery(`
				UPDATE "workflow_runs"
				SET "available_at" = CASE
						WHEN "available_at" IS NULL OR "available_at" > {:now} THEN {:now}
						ELSE "available_at"
					END,
					"updated_at" = {:now}
				WHERE "namespace_id" = {:ns}
				  AND "id" = {:runId}
				  AND "status" IN ('pending', 'running', 'sleeping')
				  AND "worker_id" IS NULL`).
				Bind(dbx.Params{"now": now, "ns": ns, "runId": runID}).
				Execute(); err != nil {
				return err
			}
		}

		result = delivered
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// GetSignalDelivery returns the delivered signal data for a step attempt. The
// bool reports whether a delivery exists (a delivery may carry null data).
func (e *Engine) GetSignalDelivery(ns, stepAttemptID string) (json.RawMessage, bool, error) {
	var row struct {
		Data sql.NullString `db:"data"`
	}
	err := e.readDB.NewQuery(`
		SELECT "data" FROM "workflow_signals"
		WHERE "namespace_id" = {:ns} AND "step_attempt_id" = {:stepId} LIMIT 1`).
		Bind(dbx.Params{"ns": ns, "stepId": stepAttemptID}).
		One(&row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	return nullToRaw(row.Data), true, nil
}
