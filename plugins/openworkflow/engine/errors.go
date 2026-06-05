package engine

import (
	"encoding/json"
	"errors"
	"fmt"
)

// Sentinel errors used by engine methods so the HTTP layer can map them to
// status codes. They mirror the error semantics of the reference SQLite
// backend (a guarded mutation that matches no row is "not found", and a cancel
// against a terminal run is a "conflict").
var (
	ErrNotFound   = errors.New("not found")
	ErrConflict   = errors.New("conflict")
	ErrBadRequest = errors.New("bad request")
)

// serializedError is the JSON shape OpenWorkflow uses for persisted errors.
// Only used for engine-generated messages; client-provided errors are stored
// as their raw JSON to preserve any additional fields.
type serializedError struct {
	Name    string `json:"name,omitempty"`
	Message string `json:"message"`
	Stack   string `json:"stack,omitempty"`
}

// mustJSON marshals a static value to json.RawMessage, panicking on failure
// (only used for engine-generated literals that cannot fail to marshal).
func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("openworkflow: failed to marshal json: %v", err))
	}
	return b
}

// requireRow returns ErrNotFound when a guarded mutation matched no row.
func requireRow(found bool, operation string) error {
	if !found {
		return fmt.Errorf("failed to %s: %w", operation, ErrNotFound)
	}
	return nil
}
