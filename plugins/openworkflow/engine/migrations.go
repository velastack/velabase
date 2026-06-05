package engine

import (
	"database/sql"

	"github.com/pocketbase/dbx"
)

// owMigration is a single versioned schema migration. The full set in
// owMigrations is the OpenWorkflow SQLite schema (migrations 0-5).
//
// Versions are recorded in OpenWorkflow's own "openworkflow_migrations" table
// (seeded through v5) so a native OpenWorkflow SQLite backend opening the same
// file sees it as up to date and no-ops, preserving byte-level interop.
// Versions 0/2/3 have no DDL (markers kept for parity with the Postgres backend).
type owMigration struct {
	version int
	stmts   []string
}

var owMigrations = []owMigration{
	{version: 0},
	{version: 1, stmts: []string{
		`CREATE TABLE IF NOT EXISTS "workflow_runs" (
			"namespace_id" TEXT NOT NULL,
			"id" TEXT NOT NULL,
			"workflow_name" TEXT NOT NULL,
			"version" TEXT,
			"status" TEXT NOT NULL,
			"idempotency_key" TEXT,
			"config" TEXT NOT NULL,
			"context" TEXT,
			"input" TEXT,
			"output" TEXT,
			"error" TEXT,
			"attempts" INTEGER NOT NULL,
			"parent_step_attempt_namespace_id" TEXT,
			"parent_step_attempt_id" TEXT,
			"worker_id" TEXT,
			"available_at" TEXT,
			"deadline_at" TEXT,
			"started_at" TEXT,
			"finished_at" TEXT,
			"created_at" TEXT NOT NULL,
			"updated_at" TEXT NOT NULL,
			PRIMARY KEY ("namespace_id", "id"),
			FOREIGN KEY ("parent_step_attempt_namespace_id", "parent_step_attempt_id")
				REFERENCES "step_attempts" ("namespace_id", "id")
				ON DELETE SET NULL
		)`,
		`CREATE TABLE IF NOT EXISTS "step_attempts" (
			"namespace_id" TEXT NOT NULL,
			"id" TEXT NOT NULL,
			"workflow_run_id" TEXT NOT NULL,
			"step_name" TEXT NOT NULL,
			"kind" TEXT NOT NULL,
			"status" TEXT NOT NULL,
			"config" TEXT NOT NULL,
			"context" TEXT,
			"output" TEXT,
			"error" TEXT,
			"child_workflow_run_namespace_id" TEXT,
			"child_workflow_run_id" TEXT,
			"started_at" TEXT,
			"finished_at" TEXT,
			"created_at" TEXT NOT NULL,
			"updated_at" TEXT NOT NULL,
			PRIMARY KEY ("namespace_id", "id"),
			FOREIGN KEY ("namespace_id", "workflow_run_id")
				REFERENCES "workflow_runs" ("namespace_id", "id")
				ON DELETE CASCADE,
			FOREIGN KEY ("child_workflow_run_namespace_id", "child_workflow_run_id")
				REFERENCES "workflow_runs" ("namespace_id", "id")
				ON DELETE SET NULL
		)`,
	}},
	{version: 2},
	{version: 3},
	{version: 4, stmts: []string{
		`CREATE INDEX IF NOT EXISTS "workflow_runs_status_available_at_created_at_idx"
			ON "workflow_runs" ("namespace_id", "status", "available_at", "created_at")`,
		`CREATE INDEX IF NOT EXISTS "workflow_runs_workflow_name_idempotency_key_created_at_idx"
			ON "workflow_runs" ("namespace_id", "workflow_name", "idempotency_key", "created_at")`,
		`CREATE INDEX IF NOT EXISTS "workflow_runs_parent_step_idx"
			ON "workflow_runs" ("parent_step_attempt_namespace_id", "parent_step_attempt_id")
			WHERE parent_step_attempt_namespace_id IS NOT NULL AND parent_step_attempt_id IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS "workflow_runs_created_at_desc_idx"
			ON "workflow_runs" ("namespace_id", "created_at" DESC)`,
		`CREATE INDEX IF NOT EXISTS "workflow_runs_status_created_at_desc_idx"
			ON "workflow_runs" ("namespace_id", "status", "created_at" DESC)`,
		`CREATE INDEX IF NOT EXISTS "workflow_runs_workflow_name_status_created_at_desc_idx"
			ON "workflow_runs" ("namespace_id", "workflow_name", "status", "created_at" DESC)`,
		`CREATE INDEX IF NOT EXISTS "step_attempts_workflow_run_created_at_idx"
			ON "step_attempts" ("namespace_id", "workflow_run_id", "created_at")`,
		`CREATE INDEX IF NOT EXISTS "step_attempts_workflow_run_step_name_created_at_idx"
			ON "step_attempts" ("namespace_id", "workflow_run_id", "step_name", "created_at")`,
		`CREATE INDEX IF NOT EXISTS "step_attempts_child_workflow_run_idx"
			ON "step_attempts" ("child_workflow_run_namespace_id", "child_workflow_run_id")
			WHERE child_workflow_run_namespace_id IS NOT NULL AND child_workflow_run_id IS NOT NULL`,
	}},
	{version: 5, stmts: []string{
		`CREATE TABLE IF NOT EXISTS "workflow_signals" (
			"namespace_id" TEXT NOT NULL,
			"id" TEXT NOT NULL,
			"signal" TEXT NOT NULL,
			"data" TEXT,
			"sender_idempotency_key" TEXT,
			"workflow_run_id" TEXT NOT NULL,
			"step_attempt_id" TEXT NOT NULL,
			"created_at" TEXT NOT NULL,
			PRIMARY KEY ("namespace_id", "id")
		)`,
		`CREATE UNIQUE INDEX IF NOT EXISTS "workflow_signals_step_attempt_idx"
			ON "workflow_signals" ("namespace_id", "step_attempt_id")`,
		`CREATE INDEX IF NOT EXISTS "workflow_signals_idempotency_idx"
			ON "workflow_signals" ("namespace_id", "signal", "sender_idempotency_key")
			WHERE "sender_idempotency_key" IS NOT NULL`,
		`CREATE INDEX IF NOT EXISTS "step_attempts_signal_wait_idx"
			ON "step_attempts" ("namespace_id", json_extract("context", '$.signal'))
			WHERE "kind" = 'signal-wait' AND "status" = 'running'`,
	}},
}

// migrate applies pending migrations, recording each version in
// openworkflow_migrations.
func (e *Engine) migrate() error {
	if _, err := e.writeDB.NewQuery(
		`CREATE TABLE IF NOT EXISTS "openworkflow_migrations" ("version" INTEGER NOT NULL PRIMARY KEY)`,
	).Execute(); err != nil {
		return err
	}

	current, err := e.currentMigrationVersion()
	if err != nil {
		return err
	}

	for _, m := range owMigrations {
		if m.version <= current {
			continue
		}
		mig := m
		if err := e.tx(func(tx *dbx.Tx) error {
			for _, stmt := range mig.stmts {
				if _, err := tx.NewQuery(stmt).Execute(); err != nil {
					return err
				}
			}
			_, err := tx.NewQuery(
				`INSERT OR IGNORE INTO "openworkflow_migrations" ("version") VALUES ({:v})`,
			).Bind(dbx.Params{"v": mig.version}).Execute()
			return err
		}); err != nil {
			return err
		}
	}

	return nil
}

func (e *Engine) currentMigrationVersion() (int, error) {
	var v sql.NullInt64
	if err := e.writeDB.NewQuery(`SELECT MAX("version") FROM "openworkflow_migrations"`).Row(&v); err != nil {
		return -1, err
	}
	if !v.Valid {
		return -1, nil
	}
	return int(v.Int64), nil
}
