package engine

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
)

// Cursors encode {createdAt, id} as base64 JSON. createdAt is stored and emitted
// in the same canonical ISO form, so a row's stored string round-trips and can be
// bound verbatim in the row-value comparison.

const (
	defaultPageSize = 100
	// maxPageSize caps a single list page so one request can't load an unbounded
	// number of rows into memory (the API is superuser-only, but the limit query
	// param is otherwise unbounded). Pagination cursors still walk the full set.
	maxPageSize = 1000
)

// clampLimit applies the default page size for a non-positive limit and caps the
// upper bound at maxPageSize.
func clampLimit(limit int) int {
	if limit <= 0 {
		return defaultPageSize
	}
	if limit > maxPageSize {
		return maxPageSize
	}
	return limit
}

// listCursor is a decoded pagination cursor.
type listCursor struct {
	CreatedAt string
	ID        string
}

func encodeCursor(createdAt, id string) string {
	payload, _ := json.Marshal(struct {
		CreatedAt string `json:"createdAt"`
		ID        string `json:"id"`
	}{CreatedAt: createdAt, ID: id})
	return base64.StdEncoding.EncodeToString(payload)
}

func decodeCursor(encoded string) (*listCursor, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("invalid cursor: %w", ErrBadRequest)
	}
	var p struct {
		CreatedAt string `json:"createdAt"`
		ID        string `json:"id"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("invalid cursor: %w", ErrBadRequest)
	}
	return &listCursor{CreatedAt: p.CreatedAt, ID: p.ID}, nil
}

// decodeListCursor rejects specifying both after+before, otherwise decodes
// whichever side is set.
func decodeListCursor(after, before string) (*listCursor, error) {
	if after != "" && before != "" {
		return nil, fmt.Errorf("cannot specify both 'after' and 'before' cursors: %w", ErrBadRequest)
	}
	if after != "" {
		return decodeCursor(after)
	}
	if before != "" {
		return decodeCursor(before)
	}
	return nil, nil
}

// paginationOrder returns the effective ORDER BY direction and the cursor
// comparison operator for a table's natural order.
func paginationOrder(naturalOrder string, hasBefore bool) (effectiveOrder, cursorOp string) {
	reversed := "DESC"
	if naturalOrder == "DESC" {
		reversed = "ASC"
	}
	effectiveOrder = naturalOrder
	if hasBefore {
		effectiveOrder = reversed
	}
	cursorOp = "<"
	if effectiveOrder == "ASC" {
		cursorOp = ">"
	}
	return effectiveOrder, cursorOp
}

// paginationMeta is the {next, prev} envelope.
type paginationMeta struct {
	Next *string `json:"next"`
	Prev *string `json:"prev"`
}

// buildPaginatedResponse trims/reverses an over-fetched (limit+1) batch and
// computes the next/prev cursors. cursorOf returns the (createdAt, id) used to
// encode a row's cursor.
func buildPaginatedResponse[T any](rows []T, limit int, hasAfter, hasBefore bool, cursorOf func(T) (string, string)) ([]T, paginationMeta) {
	overflow := len(rows) > limit

	ordered := rows
	if hasBefore {
		ordered = reversed(rows)
	}
	data := trimOverflow(ordered, overflow, hasBefore)

	hasNext := hasBefore || overflow
	hasPrev := hasAfter
	if hasBefore {
		hasPrev = overflow
	}

	meta := paginationMeta{}
	if hasNext && len(data) > 0 {
		c := encodeCursor(cursorOf(data[len(data)-1]))
		meta.Next = &c
	}
	if hasPrev && len(data) > 0 {
		c := encodeCursor(cursorOf(data[0]))
		meta.Prev = &c
	}
	return data, meta
}

func trimOverflow[T any](rows []T, overflow, hasBefore bool) []T {
	if !overflow {
		return rows
	}
	if hasBefore {
		return rows[1:]
	}
	return rows[:len(rows)-1]
}

func reversed[T any](in []T) []T {
	out := make([]T, len(in))
	for i, v := range in {
		out[len(in)-1-i] = v
	}
	return out
}
