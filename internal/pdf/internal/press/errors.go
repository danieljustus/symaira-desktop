package press

import (
	"fmt"
	"strings"
)

// Issue is a single problem found while validating a document against a profile.
type Issue struct {
	Severity string `json:"severity"` // "error" | "warning"
	Field    string `json:"field"`
	Message  string `json:"message"`
}

// ContractError reports that a document violates the frontmatter contract:
// a parse failure or one or more validation issues. It is a usage/data error.
type ContractError struct {
	Reason string  `json:"reason,omitempty"`
	Detail string  `json:"detail,omitempty"`
	Issues []Issue `json:"issues,omitempty"`
}

func (e *ContractError) Error() string {
	if len(e.Issues) > 0 {
		var b strings.Builder
		fmt.Fprintf(&b, "%d frontmatter issue(s)", len(e.Issues))
		for _, is := range e.Issues {
			fmt.Fprintf(&b, "\n  • [%s] %s — %s", is.Severity, is.Field, is.Message)
		}
		return b.String()
	}
	if e.Detail != "" {
		return fmt.Sprintf("%s: %s", e.Reason, e.Detail)
	}
	return e.Reason
}

// RenderError reports a failure during rendering, tagged with the pipeline
// stage so the CLI/MCP layer can map it to the right exit code and surface a
// plain-language hint instead of a raw engine log.
type RenderError struct {
	Stage   string `json:"stage"`            // "engine" | "compile" | "write" | "contract"
	Message string `json:"message"`          // short, human-readable
	Detail  string `json:"detail,omitempty"` // cleaned engine stderr, if any
	Hint    string `json:"hint,omitempty"`   // actionable next step
	Err     error  `json:"-"`
}

func (e *RenderError) Error() string {
	msg := e.Message
	if e.Detail != "" {
		msg += "\n" + e.Detail
	}
	return msg
}

func (e *RenderError) Unwrap() error { return e.Err }

func hasErrors(issues []Issue) bool {
	for _, is := range issues {
		if is.Severity == "error" {
			return true
		}
	}
	return false
}
