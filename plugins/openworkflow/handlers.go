package openworkflow

import (
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/pocketbase/pocketbase/apis"
	"github.com/pocketbase/pocketbase/core"
	"github.com/pocketbase/pocketbase/plugins/openworkflow/engine"
)

// registerRoutes mounts the OpenWorkflow Backend HTTP API. The whole group is
// superuser-only: a superuser-authed client is the trusted "DB connection".
func (p *plugin) registerRoutes(se *core.ServeEvent) {
	g := se.Router.Group(p.config.RoutePrefix + "/{namespace}")
	g.Bind(apis.RequireSuperuserAuth())

	// Workflow runs
	g.POST("/runs", p.handleCreateRun)
	g.GET("/runs", p.handleListRuns)
	g.GET("/runs/counts", p.handleCountRuns)
	g.GET("/runs/{id}", p.handleGetRun)
	g.POST("/claim", p.handleClaim)
	g.POST("/runs/{id}/lease", p.handleExtendLease)
	g.POST("/runs/{id}/sleep", p.handleSleep)
	g.POST("/runs/{id}/complete", p.handleCompleteRun)
	g.POST("/runs/{id}/fail", p.handleFailRun)
	g.POST("/runs/{id}/reschedule", p.handleReschedule)
	g.POST("/runs/{id}/cancel", p.handleCancel)

	// Step attempts
	g.POST("/runs/{id}/steps", p.handleCreateStep)
	g.GET("/runs/{id}/steps", p.handleListSteps)
	g.GET("/steps/{stepId}", p.handleGetStep)
	g.POST("/runs/{id}/steps/{stepId}/complete", p.handleCompleteStep)
	g.POST("/runs/{id}/steps/{stepId}/fail", p.handleFailStep)
	g.POST("/runs/{id}/steps/{stepId}/child", p.handleSetStepChild)

	// Signals
	g.POST("/signals", p.handleSendSignal)
	g.GET("/steps/{stepId}/signal", p.handleGetSignalDelivery)
}

// --- helpers ---

func (p *plugin) ready() error {
	if p.engine == nil {
		return apis.NewApiError(http.StatusServiceUnavailable, "openworkflow engine not initialized", nil)
	}
	return nil
}

// begin validates engine readiness + namespace and returns the namespace.
func (p *plugin) begin(e *core.RequestEvent) (string, error) {
	if err := p.ready(); err != nil {
		return "", err
	}
	ns := e.Request.PathValue("namespace")
	if err := p.checkNamespace(ns); err != nil {
		return "", err
	}
	return ns, nil
}

func bind(e *core.RequestEvent, dst any) error {
	if err := e.BindBody(dst); err != nil {
		return apis.NewBadRequestError("invalid request body", err)
	}
	return nil
}

func toAPIError(err error) error {
	switch {
	case errors.Is(err, engine.ErrNotFound):
		// Surface the engine's message (e.g. "failed to sleep workflow run",
		// "workflow run X does not exist") so OpenWorkflow clients can match the
		// reference Backend error contract. Sentenize (in NewApiError) capitalizes
		// it and appends a period; this stays a 404.
		return apis.NewNotFoundError(err.Error(), nil)
	case errors.Is(err, engine.ErrConflict):
		return apis.NewApiError(http.StatusConflict, err.Error(), nil)
	case errors.Is(err, engine.ErrBadRequest):
		return apis.NewBadRequestError(err.Error(), nil)
	default:
		return apis.NewApiError(http.StatusInternalServerError, "", err)
	}
}

func listParams(e *core.RequestEvent) engine.ListParams {
	q := e.Request.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	return engine.ListParams{Limit: limit, After: q.Get("after"), Before: q.Get("before")}
}

// defaultJSON ensures a NOT NULL json column has a value. An absent field or an
// explicit JSON null both default to {} (rawToNull would otherwise map "null"
// back to SQL NULL and violate the NOT NULL constraint).
func defaultJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 || string(raw) == "null" {
		return json.RawMessage("{}")
	}
	return raw
}

// --- workflow run handlers ---

func (p *plugin) handleCreateRun(e *core.RequestEvent) error {
	ns, err := p.begin(e)
	if err != nil {
		return err
	}
	var b struct {
		WorkflowName                 string          `json:"workflowName"`
		Version                      *string         `json:"version"`
		IdempotencyKey               *string         `json:"idempotencyKey"`
		Config                       json.RawMessage `json:"config"`
		Context                      json.RawMessage `json:"context"`
		Input                        json.RawMessage `json:"input"`
		ParentStepAttemptNamespaceID *string         `json:"parentStepAttemptNamespaceId"`
		ParentStepAttemptID          *string         `json:"parentStepAttemptId"`
		AvailableAt                  *string         `json:"availableAt"`
		DeadlineAt                   *string         `json:"deadlineAt"`
	}
	if err := bind(e, &b); err != nil {
		return err
	}
	if b.WorkflowName == "" {
		return apis.NewBadRequestError("workflowName is required", nil)
	}
	run, err := p.engine.CreateWorkflowRun(ns, engine.CreateWorkflowRunParams{
		WorkflowName:                 b.WorkflowName,
		Version:                      b.Version,
		IdempotencyKey:               b.IdempotencyKey,
		Config:                       defaultJSON(b.Config),
		Context:                      b.Context,
		Input:                        b.Input,
		ParentStepAttemptNamespaceID: b.ParentStepAttemptNamespaceID,
		ParentStepAttemptID:          b.ParentStepAttemptID,
		AvailableAt:                  b.AvailableAt,
		DeadlineAt:                   b.DeadlineAt,
	})
	if err != nil {
		return toAPIError(err)
	}
	return e.JSON(http.StatusOK, map[string]any{"run": run})
}

func (p *plugin) handleGetRun(e *core.RequestEvent) error {
	ns, err := p.begin(e)
	if err != nil {
		return err
	}
	run, err := p.engine.GetWorkflowRun(ns, e.Request.PathValue("id"))
	if err != nil {
		return toAPIError(err)
	}
	return e.JSON(http.StatusOK, map[string]any{"run": run})
}

func (p *plugin) handleListRuns(e *core.RequestEvent) error {
	ns, err := p.begin(e)
	if err != nil {
		return err
	}
	data, meta, err := p.engine.ListWorkflowRuns(ns, listParams(e))
	if err != nil {
		return toAPIError(err)
	}
	return e.JSON(http.StatusOK, map[string]any{"data": data, "pagination": meta})
}

func (p *plugin) handleCountRuns(e *core.RequestEvent) error {
	ns, err := p.begin(e)
	if err != nil {
		return err
	}
	counts, err := p.engine.CountWorkflowRuns(ns)
	if err != nil {
		return toAPIError(err)
	}
	return e.JSON(http.StatusOK, map[string]any{"counts": counts})
}

func (p *plugin) handleClaim(e *core.RequestEvent) error {
	ns, err := p.begin(e)
	if err != nil {
		return err
	}
	var b struct {
		WorkerID        string `json:"workerId"`
		LeaseDurationMs int64  `json:"leaseDurationMs"`
	}
	if err := bind(e, &b); err != nil {
		return err
	}
	run, err := p.engine.ClaimWorkflowRun(ns, b.WorkerID, b.LeaseDurationMs)
	if err != nil {
		return toAPIError(err)
	}
	return e.JSON(http.StatusOK, map[string]any{"run": run})
}

func (p *plugin) handleExtendLease(e *core.RequestEvent) error {
	ns, err := p.begin(e)
	if err != nil {
		return err
	}
	var b struct {
		WorkerID        string `json:"workerId"`
		LeaseDurationMs int64  `json:"leaseDurationMs"`
	}
	if err := bind(e, &b); err != nil {
		return err
	}
	run, err := p.engine.ExtendWorkflowRunLease(ns, e.Request.PathValue("id"), b.WorkerID, b.LeaseDurationMs)
	if err != nil {
		return toAPIError(err)
	}
	return e.JSON(http.StatusOK, map[string]any{"run": run})
}

func (p *plugin) handleSleep(e *core.RequestEvent) error {
	ns, err := p.begin(e)
	if err != nil {
		return err
	}
	var b struct {
		WorkerID    string `json:"workerId"`
		AvailableAt string `json:"availableAt"`
	}
	if err := bind(e, &b); err != nil {
		return err
	}
	run, err := p.engine.SleepWorkflowRun(ns, e.Request.PathValue("id"), b.WorkerID, b.AvailableAt)
	if err != nil {
		return toAPIError(err)
	}
	return e.JSON(http.StatusOK, map[string]any{"run": run})
}

func (p *plugin) handleCompleteRun(e *core.RequestEvent) error {
	ns, err := p.begin(e)
	if err != nil {
		return err
	}
	var b struct {
		WorkerID string          `json:"workerId"`
		Output   json.RawMessage `json:"output"`
	}
	if err := bind(e, &b); err != nil {
		return err
	}
	run, err := p.engine.CompleteWorkflowRun(ns, e.Request.PathValue("id"), b.WorkerID, b.Output)
	if err != nil {
		return toAPIError(err)
	}
	return e.JSON(http.StatusOK, map[string]any{"run": run})
}

func (p *plugin) handleFailRun(e *core.RequestEvent) error {
	ns, err := p.begin(e)
	if err != nil {
		return err
	}
	var b struct {
		WorkerID    string             `json:"workerId"`
		Error       json.RawMessage    `json:"error"`
		RetryPolicy engine.RetryPolicy `json:"retryPolicy"`
		Attempts    *int               `json:"attempts"`
		DeadlineAt  *string            `json:"deadlineAt"`
	}
	if err := bind(e, &b); err != nil {
		return err
	}
	run, err := p.engine.FailWorkflowRun(ns, e.Request.PathValue("id"), engine.FailWorkflowRunParams{
		WorkerID:    b.WorkerID,
		Error:       b.Error,
		RetryPolicy: b.RetryPolicy,
		Attempts:    b.Attempts,
		DeadlineAt:  b.DeadlineAt,
	})
	if err != nil {
		return toAPIError(err)
	}
	return e.JSON(http.StatusOK, map[string]any{"run": run})
}

func (p *plugin) handleReschedule(e *core.RequestEvent) error {
	ns, err := p.begin(e)
	if err != nil {
		return err
	}
	var b struct {
		WorkerID    string          `json:"workerId"`
		Error       json.RawMessage `json:"error"`
		AvailableAt string          `json:"availableAt"`
	}
	if err := bind(e, &b); err != nil {
		return err
	}
	run, err := p.engine.RescheduleWorkflowRunAfterFailedStepAttempt(ns, e.Request.PathValue("id"), b.WorkerID, b.AvailableAt, b.Error)
	if err != nil {
		return toAPIError(err)
	}
	return e.JSON(http.StatusOK, map[string]any{"run": run})
}

func (p *plugin) handleCancel(e *core.RequestEvent) error {
	ns, err := p.begin(e)
	if err != nil {
		return err
	}
	run, err := p.engine.CancelWorkflowRun(ns, e.Request.PathValue("id"))
	if err != nil {
		return toAPIError(err)
	}
	return e.JSON(http.StatusOK, map[string]any{"run": run})
}

// --- step attempt handlers ---

func (p *plugin) handleCreateStep(e *core.RequestEvent) error {
	ns, err := p.begin(e)
	if err != nil {
		return err
	}
	var b struct {
		WorkerID string          `json:"workerId"`
		StepName string          `json:"stepName"`
		Kind     string          `json:"kind"`
		Config   json.RawMessage `json:"config"`
		Context  json.RawMessage `json:"context"`
	}
	if err := bind(e, &b); err != nil {
		return err
	}
	step, err := p.engine.CreateStepAttempt(ns, engine.CreateStepAttemptParams{
		WorkflowRunID: e.Request.PathValue("id"),
		WorkerID:      b.WorkerID,
		StepName:      b.StepName,
		Kind:          b.Kind,
		Config:        defaultJSON(b.Config),
		Context:       b.Context,
	})
	if err != nil {
		return toAPIError(err)
	}
	return e.JSON(http.StatusOK, map[string]any{"stepAttempt": step})
}

func (p *plugin) handleGetStep(e *core.RequestEvent) error {
	ns, err := p.begin(e)
	if err != nil {
		return err
	}
	step, err := p.engine.GetStepAttempt(ns, e.Request.PathValue("stepId"))
	if err != nil {
		return toAPIError(err)
	}
	return e.JSON(http.StatusOK, map[string]any{"stepAttempt": step})
}

func (p *plugin) handleListSteps(e *core.RequestEvent) error {
	ns, err := p.begin(e)
	if err != nil {
		return err
	}
	data, meta, err := p.engine.ListStepAttempts(ns, e.Request.PathValue("id"), listParams(e))
	if err != nil {
		return toAPIError(err)
	}
	return e.JSON(http.StatusOK, map[string]any{"data": data, "pagination": meta})
}

func (p *plugin) handleCompleteStep(e *core.RequestEvent) error {
	ns, err := p.begin(e)
	if err != nil {
		return err
	}
	var b struct {
		WorkerID string          `json:"workerId"`
		Output   json.RawMessage `json:"output"`
	}
	if err := bind(e, &b); err != nil {
		return err
	}
	step, err := p.engine.CompleteStepAttempt(ns, e.Request.PathValue("id"), e.Request.PathValue("stepId"), b.WorkerID, b.Output)
	if err != nil {
		return toAPIError(err)
	}
	return e.JSON(http.StatusOK, map[string]any{"stepAttempt": step})
}

func (p *plugin) handleFailStep(e *core.RequestEvent) error {
	ns, err := p.begin(e)
	if err != nil {
		return err
	}
	var b struct {
		WorkerID string          `json:"workerId"`
		Error    json.RawMessage `json:"error"`
	}
	if err := bind(e, &b); err != nil {
		return err
	}
	step, err := p.engine.FailStepAttempt(ns, e.Request.PathValue("id"), e.Request.PathValue("stepId"), b.WorkerID, b.Error)
	if err != nil {
		return toAPIError(err)
	}
	return e.JSON(http.StatusOK, map[string]any{"stepAttempt": step})
}

func (p *plugin) handleSetStepChild(e *core.RequestEvent) error {
	ns, err := p.begin(e)
	if err != nil {
		return err
	}
	var b struct {
		WorkerID                    string `json:"workerId"`
		ChildWorkflowRunNamespaceID string `json:"childWorkflowRunNamespaceId"`
		ChildWorkflowRunID          string `json:"childWorkflowRunId"`
	}
	if err := bind(e, &b); err != nil {
		return err
	}
	step, err := p.engine.SetStepAttemptChildWorkflowRun(ns, e.Request.PathValue("id"), e.Request.PathValue("stepId"), b.WorkerID, b.ChildWorkflowRunNamespaceID, b.ChildWorkflowRunID)
	if err != nil {
		return toAPIError(err)
	}
	return e.JSON(http.StatusOK, map[string]any{"stepAttempt": step})
}

// --- signal handlers ---

func (p *plugin) handleSendSignal(e *core.RequestEvent) error {
	ns, err := p.begin(e)
	if err != nil {
		return err
	}
	var b struct {
		Signal         string          `json:"signal"`
		Data           json.RawMessage `json:"data"`
		IdempotencyKey *string         `json:"idempotencyKey"`
	}
	if err := bind(e, &b); err != nil {
		return err
	}
	if b.Signal == "" {
		return apis.NewBadRequestError("signal is required", nil)
	}
	ids, err := p.engine.SendSignal(ns, engine.SendSignalParams{Signal: b.Signal, Data: b.Data, IdempotencyKey: b.IdempotencyKey})
	if err != nil {
		return toAPIError(err)
	}
	return e.JSON(http.StatusOK, map[string]any{"workflowRunIds": ids})
}

func (p *plugin) handleGetSignalDelivery(e *core.RequestEvent) error {
	ns, err := p.begin(e)
	if err != nil {
		return err
	}
	data, found, err := p.engine.GetSignalDelivery(ns, e.Request.PathValue("stepId"))
	if err != nil {
		return toAPIError(err)
	}
	return e.JSON(http.StatusOK, map[string]any{"delivered": found, "data": data})
}
