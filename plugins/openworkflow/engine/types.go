package engine

import (
	"database/sql"
	"encoding/json"
	"time"
)

// isoLayout matches JavaScript's Date.toISOString() output: millisecond
// precision, UTC "Z". Keeping this identical to OpenWorkflow is interop- and
// correctness-critical: cursor pagination and the idempotency window compare
// created_at lexically as TEXT.
const isoLayout = "2006-01-02T15:04:05.000Z07:00"

func isoNow() string { return time.Now().UTC().Format(isoLayout) }

func isoFromTime(t time.Time) string { return t.UTC().Format(isoLayout) }

// parseISO parses an ISO-8601 timestamp (RFC3339 with optional fractional secs),
// tolerating a space date/time separator as emitted by some clients (e.g. the
// admin UI's app.utils.toRFC3339Datetime, which returns "YYYY-MM-DD HH:mm:ss.nnnZ").
func parseISO(s string) (time.Time, error) {
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t, nil
	}
	return time.Parse("2006-01-02 15:04:05.999999999Z07:00", s)
}

// normalizeISO re-emits a client-supplied ISO timestamp in the canonical
// isoLayout form, so all the lexical TEXT comparisons (claim availability,
// deadline expiry, cursor pagination) stay consistent regardless of the
// separator/precision the client sent. Mirrors OpenWorkflow's toISO(), which
// always persists Date.toISOString().
func normalizeISO(s string) (string, error) {
	t, err := parseISO(s)
	if err != nil {
		return "", err
	}
	return isoFromTime(t), nil
}

// normalizeISOPtr is normalizeISO for an optional timestamp, returning SQL NULL
// when the pointer is nil.
func normalizeISOPtr(p *string) (sql.NullString, error) {
	if p == nil {
		return sql.NullString{}, nil
	}
	norm, err := normalizeISO(*p)
	if err != nil {
		return sql.NullString{}, err
	}
	return sql.NullString{String: norm, Valid: true}, nil
}

// WorkflowRunStatus values. "sleeping" and "succeeded" are deprecated but still
// read/handled for compatibility.
const (
	statusPending   = "pending"
	statusRunning   = "running"
	statusSleeping  = "sleeping"  // deprecated
	statusSucceeded = "succeeded" // deprecated
	statusCompleted = "completed"
	statusFailed    = "failed"
	statusCanceled  = "canceled"
)

// StepKind values.
const (
	kindFunction   = "function"
	kindSleep      = "sleep"
	kindWorkflow   = "workflow"
	kindSignalSend = "signal-send"
	kindSignalWait = "signal-wait"
)

// WorkflowRun is the wire/domain representation of a workflow run. JSON columns
// are emitted verbatim (json.RawMessage); timestamps are ISO-8601 strings.
type WorkflowRun struct {
	NamespaceID                  string          `json:"namespaceId"`
	ID                           string          `json:"id"`
	WorkflowName                 string          `json:"workflowName"`
	Version                      *string         `json:"version"`
	Status                       string          `json:"status"`
	IdempotencyKey               *string         `json:"idempotencyKey"`
	Config                       json.RawMessage `json:"config"`
	Context                      json.RawMessage `json:"context"`
	Input                        json.RawMessage `json:"input"`
	Output                       json.RawMessage `json:"output"`
	Error                        json.RawMessage `json:"error"`
	Attempts                     int             `json:"attempts"`
	ParentStepAttemptNamespaceID *string         `json:"parentStepAttemptNamespaceId"`
	ParentStepAttemptID          *string         `json:"parentStepAttemptId"`
	WorkerID                     *string         `json:"workerId"`
	AvailableAt                  *string         `json:"availableAt"`
	DeadlineAt                   *string         `json:"deadlineAt"`
	StartedAt                    *string         `json:"startedAt"`
	FinishedAt                   *string         `json:"finishedAt"`
	CreatedAt                    string          `json:"createdAt"`
	UpdatedAt                    string          `json:"updatedAt"`
}

// StepAttempt is the wire/domain representation of a step attempt.
type StepAttempt struct {
	NamespaceID                 string          `json:"namespaceId"`
	ID                          string          `json:"id"`
	WorkflowRunID               string          `json:"workflowRunId"`
	StepName                    string          `json:"stepName"`
	Kind                        string          `json:"kind"`
	Status                      string          `json:"status"`
	Config                      json.RawMessage `json:"config"`
	Context                     json.RawMessage `json:"context"`
	Output                      json.RawMessage `json:"output"`
	Error                       json.RawMessage `json:"error"`
	ChildWorkflowRunNamespaceID *string         `json:"childWorkflowRunNamespaceId"`
	ChildWorkflowRunID          *string         `json:"childWorkflowRunId"`
	StartedAt                   *string         `json:"startedAt"`
	FinishedAt                  *string         `json:"finishedAt"`
	CreatedAt                   string          `json:"createdAt"`
	UpdatedAt                   string          `json:"updatedAt"`
}

// WorkflowRunCounts is the normalized status->count map (deprecated statuses
// folded into their replacements).
type WorkflowRunCounts struct {
	Pending   int `json:"pending"`
	Running   int `json:"running"`
	Completed int `json:"completed"`
	Failed    int `json:"failed"`
	Canceled  int `json:"canceled"`
}

// --- DB row structs (SELECT * scanning via dbx struct mapping) ---

type workflowRunRow struct {
	NamespaceID                  string         `db:"namespace_id"`
	ID                           string         `db:"id"`
	WorkflowName                 string         `db:"workflow_name"`
	Version                      sql.NullString `db:"version"`
	Status                       string         `db:"status"`
	IdempotencyKey               sql.NullString `db:"idempotency_key"`
	Config                       sql.NullString `db:"config"`
	Context                      sql.NullString `db:"context"`
	Input                        sql.NullString `db:"input"`
	Output                       sql.NullString `db:"output"`
	Error                        sql.NullString `db:"error"`
	Attempts                     int            `db:"attempts"`
	ParentStepAttemptNamespaceID sql.NullString `db:"parent_step_attempt_namespace_id"`
	ParentStepAttemptID          sql.NullString `db:"parent_step_attempt_id"`
	WorkerID                     sql.NullString `db:"worker_id"`
	AvailableAt                  sql.NullString `db:"available_at"`
	DeadlineAt                   sql.NullString `db:"deadline_at"`
	StartedAt                    sql.NullString `db:"started_at"`
	FinishedAt                   sql.NullString `db:"finished_at"`
	CreatedAt                    string         `db:"created_at"`
	UpdatedAt                    string         `db:"updated_at"`
}

type stepAttemptRow struct {
	NamespaceID                 string         `db:"namespace_id"`
	ID                          string         `db:"id"`
	WorkflowRunID               string         `db:"workflow_run_id"`
	StepName                    string         `db:"step_name"`
	Kind                        string         `db:"kind"`
	Status                      string         `db:"status"`
	Config                      sql.NullString `db:"config"`
	Context                     sql.NullString `db:"context"`
	Output                      sql.NullString `db:"output"`
	Error                       sql.NullString `db:"error"`
	ChildWorkflowRunNamespaceID sql.NullString `db:"child_workflow_run_namespace_id"`
	ChildWorkflowRunID          sql.NullString `db:"child_workflow_run_id"`
	StartedAt                   sql.NullString `db:"started_at"`
	FinishedAt                  sql.NullString `db:"finished_at"`
	CreatedAt                   string         `db:"created_at"`
	UpdatedAt                   string         `db:"updated_at"`
}

func (r *workflowRunRow) toWorkflowRun() *WorkflowRun {
	return &WorkflowRun{
		NamespaceID:                  r.NamespaceID,
		ID:                           r.ID,
		WorkflowName:                 r.WorkflowName,
		Version:                      nullToPtr(r.Version),
		Status:                       r.Status,
		IdempotencyKey:               nullToPtr(r.IdempotencyKey),
		Config:                       nullToRaw(r.Config),
		Context:                      nullToRaw(r.Context),
		Input:                        nullToRaw(r.Input),
		Output:                       nullToRaw(r.Output),
		Error:                        nullToRaw(r.Error),
		Attempts:                     r.Attempts,
		ParentStepAttemptNamespaceID: nullToPtr(r.ParentStepAttemptNamespaceID),
		ParentStepAttemptID:          nullToPtr(r.ParentStepAttemptID),
		WorkerID:                     nullToPtr(r.WorkerID),
		AvailableAt:                  nullToPtr(r.AvailableAt),
		DeadlineAt:                   nullToPtr(r.DeadlineAt),
		StartedAt:                    nullToPtr(r.StartedAt),
		FinishedAt:                   nullToPtr(r.FinishedAt),
		CreatedAt:                    r.CreatedAt,
		UpdatedAt:                    r.UpdatedAt,
	}
}

func (r *stepAttemptRow) toStepAttempt() *StepAttempt {
	return &StepAttempt{
		NamespaceID:                 r.NamespaceID,
		ID:                          r.ID,
		WorkflowRunID:               r.WorkflowRunID,
		StepName:                    r.StepName,
		Kind:                        r.Kind,
		Status:                      r.Status,
		Config:                      nullToRaw(r.Config),
		Context:                     nullToRaw(r.Context),
		Output:                      nullToRaw(r.Output),
		Error:                       nullToRaw(r.Error),
		ChildWorkflowRunNamespaceID: nullToPtr(r.ChildWorkflowRunNamespaceID),
		ChildWorkflowRunID:          nullToPtr(r.ChildWorkflowRunID),
		StartedAt:                   nullToPtr(r.StartedAt),
		FinishedAt:                  nullToPtr(r.FinishedAt),
		CreatedAt:                   r.CreatedAt,
		UpdatedAt:                   r.UpdatedAt,
	}
}

// --- small conversion helpers ---

func nullToPtr(n sql.NullString) *string {
	if !n.Valid {
		return nil
	}
	v := n.String
	return &v
}

func nullToRaw(n sql.NullString) json.RawMessage {
	if !n.Valid {
		return nil
	}
	return json.RawMessage(n.String)
}

// rawToNull converts request JSON to a nullable TEXT value, treating an absent
// value or an explicit JSON null as SQL NULL.
func rawToNull(raw json.RawMessage) sql.NullString {
	if len(raw) == 0 || string(raw) == "null" {
		return sql.NullString{}
	}
	return sql.NullString{String: string(raw), Valid: true}
}

func ptrToNull(p *string) sql.NullString {
	if p == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: *p, Valid: true}
}

func timePtrToNull(t *time.Time) sql.NullString {
	if t == nil {
		return sql.NullString{}
	}
	return sql.NullString{String: isoFromTime(*t), Valid: true}
}
