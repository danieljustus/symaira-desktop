// Package errs defines the structured error vocabulary shared by every
// symrelate service and repository. Callers (CLI, and later MCP) classify
// failures by Kind instead of parsing error strings.
package errs

import (
	"errors"
	"fmt"
)

// Kind classifies an error for callers that need to react programmatically
// (choose an exit code, decide whether to retry, map to an API status).
type Kind string

const (
	KindNotFound    Kind = "not_found"
	KindConflict    Kind = "conflict"
	KindInvalid     Kind = "invalid"
	KindPermission  Kind = "permission"
	KindUnavailable Kind = "unavailable"
	KindInternal    Kind = "internal"
)

// Error is the structured error type returned by symrelate's internal
// packages. Op identifies the failing operation (e.g. "contact.Create") for
// diagnostics; Message is safe to show to a user; Err is the wrapped cause
// and may carry information that must not be surfaced verbatim (see the
// redaction rules enforced by the security package).
type Error struct {
	Kind    Kind
	Op      string
	Message string
	Err     error
}

func (e *Error) Error() string {
	if e.Err != nil {
		return fmt.Sprintf("%s: %s: %v", e.Op, e.Message, e.Err)
	}
	return fmt.Sprintf("%s: %s", e.Op, e.Message)
}

func (e *Error) Unwrap() error { return e.Err }

// E constructs a structured Error.
func E(op string, kind Kind, message string, cause error) *Error {
	return &Error{Op: op, Kind: kind, Message: message, Err: cause}
}

// KindOf extracts the Kind of err, defaulting to KindInternal when err does
// not wrap a *Error.
func KindOf(err error) Kind {
	var e *Error
	if errors.As(err, &e) {
		return e.Kind
	}
	if err == nil {
		return ""
	}
	return KindInternal
}

func NotFound(op, message string, cause error) *Error {
	return E(op, KindNotFound, message, cause)
}

func Conflict(op, message string, cause error) *Error {
	return E(op, KindConflict, message, cause)
}

func Invalid(op, message string, cause error) *Error {
	return E(op, KindInvalid, message, cause)
}

func Internal(op, message string, cause error) *Error {
	return E(op, KindInternal, message, cause)
}
