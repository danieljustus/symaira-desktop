// Package parse translates diagram source text (documented Mermaid subset or
// SymDraw IR JSON) into the input-independent diagram model (ir.Diagram).
package parse

import (
	"fmt"
	"strings"
)

// ParseError reports a failure encountered during diagram parsing, contract
// checking, or schema validation. It follows the established error shape
// (Stage, Message, Detail, Hint), naming the unsupported construct or syntax
// failure and providing an actionable hint.
type ParseError struct {
	Stage   string `json:"stage"`            // "parse" | "schema" | "contract"
	Message string `json:"message"`          // short human-readable summary naming the issue/construct
	Detail  string `json:"detail,omitempty"` // line number, offending snippet, or JSON property path
	Hint    string `json:"hint,omitempty"`   // actionable next step / recommendation
	Line    int    `json:"line,omitempty"`   // 1-based source line number, if applicable
	Err     error  `json:"-"`
}

func (e *ParseError) Error() string {
	var b strings.Builder
	b.WriteString(e.Message)
	if e.Detail != "" {
		b.WriteString(": ")
		b.WriteString(e.Detail)
	}
	if e.Hint != "" {
		b.WriteString(" (hint: ")
		b.WriteString(e.Hint)
		b.WriteString(")")
	}
	return b.String()
}

func (e *ParseError) Unwrap() error {
	return e.Err
}

// NewUnsupportedConstructError creates a typed ParseError naming an unsupported
// syntax construct or directive with an actionable hint.
func NewUnsupportedConstructError(construct, hint string, line int, rawLine string) *ParseError {
	detail := ""
	if line > 0 && rawLine != "" {
		detail = fmt.Sprintf("line %d: %s", line, strings.TrimSpace(rawLine))
	} else if line > 0 {
		detail = fmt.Sprintf("line %d", line)
	}
	return &ParseError{
		Stage:   "parse",
		Message: fmt.Sprintf("unsupported construct %q", construct),
		Detail:  detail,
		Hint:    hint,
		Line:    line,
	}
}

// NewUnsupportedDiagramError creates a typed ParseError for unsupported
// Mermaid diagram families (e.g. sequenceDiagram, pie, classDiagram).
func NewUnsupportedDiagramError(diagramKind, hint string, line int, rawLine string) *ParseError {
	detail := ""
	if line > 0 && rawLine != "" {
		detail = fmt.Sprintf("line %d: %s", line, strings.TrimSpace(rawLine))
	} else if line > 0 {
		detail = fmt.Sprintf("line %d", line)
	}
	return &ParseError{
		Stage:   "parse",
		Message: fmt.Sprintf("unsupported diagram kind %q", diagramKind),
		Detail:  detail,
		Hint:    hint,
		Line:    line,
	}
}

// NewSyntaxError creates a typed ParseError for malformed syntax.
func NewSyntaxError(message string, line int, rawLine, hint string) *ParseError {
	detail := ""
	if line > 0 && rawLine != "" {
		detail = fmt.Sprintf("line %d: %s", line, strings.TrimSpace(rawLine))
	} else if line > 0 {
		detail = fmt.Sprintf("line %d", line)
	}
	return &ParseError{
		Stage:   "parse",
		Message: message,
		Detail:  detail,
		Hint:    hint,
		Line:    line,
	}
}

// NewSchemaError creates a typed ParseError for IR schema validation failures.
func NewSchemaError(field, message, hint string, err error) *ParseError {
	return &ParseError{
		Stage:   "schema",
		Message: message,
		Detail:  field,
		Hint:    hint,
		Err:     err,
	}
}

// NewContractError creates a typed ParseError for semantic contract failures.
func NewContractError(field, message, hint string, err error) *ParseError {
	return &ParseError{
		Stage:   "contract",
		Message: message,
		Detail:  field,
		Hint:    hint,
		Err:     err,
	}
}
