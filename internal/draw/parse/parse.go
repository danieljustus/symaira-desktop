package parse

import (
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/draw/ir"
)

// DialectVersion specifies the version of the supported SymDraw Mermaid subset.
const DialectVersion = "1.0"

// Source auto-detects whether the given source string is JSON IR or Mermaid
// syntax, and parses it into an ir.Diagram.
func Source(source string) (*ir.Diagram, error) {
	trimmed := strings.TrimSpace(source)
	if strings.HasPrefix(trimmed, "{") {
		return ParseJSON([]byte(source))
	}
	return ParseMermaid(source)
}

// Mermaid parses a Mermaid flowchart/graph source into an ir.Diagram.
func Mermaid(source string) (*ir.Diagram, error) {
	return ParseMermaid(source)
}

// JSON parses raw JSON data into an ir.Diagram with strict schema validation.
func JSON(data []byte) (*ir.Diagram, error) {
	return ParseJSON(data)
}
