// Package errors provides structured error types for all interfaces.
package errors

import "fmt"

// DatabaseError is returned when a database operation fails.
type DatabaseError struct {
	Op  string
	Err error
}

func (e *DatabaseError) Error() string {
	return fmt.Sprintf("database %s: %v", e.Op, e.Err)
}

func (e *DatabaseError) Unwrap() error {
	return e.Err
}

// FileNotFoundError is returned when a file does not exist.
type FileNotFoundError struct {
	Path string
}

func (e *FileNotFoundError) Error() string {
	return fmt.Sprintf("file not found: %s", e.Path)
}

// SearchError is returned when a search operation fails.
type SearchError struct {
	Query string
	Err   error
}

func (e *SearchError) Error() string {
	return fmt.Sprintf("search failed for query %q: %v", e.Query, e.Err)
}

func (e *SearchError) Unwrap() error {
	return e.Err
}

// IndexError is returned when an indexing operation fails.
type IndexError struct {
	Path string
	Err  error
}

func (e *IndexError) Error() string {
	return fmt.Sprintf("indexing failed for %s: %v", e.Path, e.Err)
}

func (e *IndexError) Unwrap() error {
	return e.Err
}

// ValidationError is returned when input validation fails.
type ValidationError struct {
	Field   string
	Message string
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("validation error for field %s: %s", e.Field, e.Message)
}
