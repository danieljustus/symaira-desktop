// Package page implements simple offset-based pagination shared by every
// list query across the domain services.
package page

const (
	DefaultLimit = 50
	MaxLimit     = 200
)

// Request is a bounds-checked pagination request.
type Request struct {
	Limit  int
	Offset int
}

// NewRequest clamps limit to [1, MaxLimit] (defaulting to DefaultLimit when
// limit <= 0) and offset to >= 0.
func NewRequest(limit, offset int) Request {
	if limit <= 0 {
		limit = DefaultLimit
	}
	if limit > MaxLimit {
		limit = MaxLimit
	}
	if offset < 0 {
		offset = 0
	}
	return Request{Limit: limit, Offset: offset}
}

// Result wraps a page of items with whether a subsequent page has more
// results, determined by fetching one extra row per query (Limit+1).
type Result[T any] struct {
	Items   []T
	HasMore bool
}

// Trim splits an over-fetched slice (Limit+1 rows requested) into the page
// of at most req.Limit items plus whether more results exist.
func Trim[T any](rows []T, req Request) Result[T] {
	if len(rows) > req.Limit {
		return Result[T]{Items: rows[:req.Limit], HasMore: true}
	}
	return Result[T]{Items: rows, HasMore: false}
}
