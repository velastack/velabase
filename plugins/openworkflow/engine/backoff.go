package engine

import (
	"encoding/json"
	"math"
	"time"
)

// RetryPolicy is a workflow's retry configuration (backoff policy + maximum
// attempts). Intervals are duration strings (e.g. "1s", "100s").
type RetryPolicy struct {
	InitialInterval    string  `json:"initialInterval"`
	BackoffCoefficient float64 `json:"backoffCoefficient"`
	MaximumInterval    string  `json:"maximumInterval"`
	MaximumAttempts    int     `json:"maximumAttempts"`
}

// parseBackoffIntervalMs parses a duration string to milliseconds; an invalid
// or empty interval defaults to 0.
func parseBackoffIntervalMs(interval string) float64 {
	v, err := parseDuration(interval)
	if err != nil {
		return 0
	}
	return v
}

// computeBackoffDelayMs returns the capped exponential backoff delay in
// milliseconds for a 1-based attempt number.
func computeBackoffDelayMs(p RetryPolicy, attempt int) float64 {
	initialMS := parseBackoffIntervalMs(p.InitialInterval)
	maximumMS := parseBackoffIntervalMs(p.MaximumInterval)

	exponential := initialMS * math.Pow(p.BackoffCoefficient, math.Max(0, float64(attempt-1)))

	return math.Min(exponential, maximumMS)
}

// failedWorkflowRunUpdate is the computed persisted state after a workflow run
// fails.
type failedWorkflowRunUpdate struct {
	Status      string // "pending" | "failed"
	AvailableAt *time.Time
	FinishedAt  *time.Time
	Error       json.RawMessage
}

// computeFailedWorkflowRunUpdate decides whether a failed run is terminal or
// should be rescheduled with backoff, honoring the deadline.
func computeFailedWorkflowRunUpdate(
	policy RetryPolicy,
	attempts int,
	deadlineAt *time.Time,
	errJSON json.RawMessage,
	now time.Time,
) failedWorkflowRunUpdate {
	failed := func(finalErr json.RawMessage) failedWorkflowRunUpdate {
		finishedAt := now
		return failedWorkflowRunUpdate{
			Status:      "failed",
			AvailableAt: nil,
			FinishedAt:  &finishedAt,
			Error:       finalErr,
		}
	}

	// now >= deadlineAt
	if deadlineAt != nil && !now.Before(*deadlineAt) {
		return failed(mustJSON(serializedError{Message: "Workflow run deadline exceeded"}))
	}

	// 0 = unlimited attempts
	if policy.MaximumAttempts > 0 && attempts >= policy.MaximumAttempts {
		return failed(errJSON)
	}

	retryDelayMS := computeBackoffDelayMs(policy, attempts)
	nextRetryAt := now.Add(time.Duration(retryDelayMS * float64(time.Millisecond)))

	// nextRetryAt >= deadlineAt
	if deadlineAt != nil && !nextRetryAt.Before(*deadlineAt) {
		return failed(errJSON)
	}

	return failedWorkflowRunUpdate{
		Status:      "pending",
		AvailableAt: &nextRetryAt,
		FinishedAt:  nil,
		Error:       errJSON,
	}
}
