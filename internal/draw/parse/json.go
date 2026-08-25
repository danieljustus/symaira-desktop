package parse

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/draw/ir"
)

// ParseJSON parses a JSON diagram payload into an ir.Diagram, performing strict
// schema validation and semantic checks. Any violation produces a typed ParseError.
func ParseJSON(data []byte) (*ir.Diagram, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, &ParseError{
			Stage:   "parse",
			Message: "empty JSON diagram input",
			Hint:    "Provide a valid JSON payload adhering to the SymDraw IR schema.",
		}
	}

	var diag ir.Diagram
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()

	if err := dec.Decode(&diag); err != nil {
		errStr := err.Error()
		if strings.Contains(errStr, "unknown field") {
			return nil, &ParseError{
				Stage:   "schema",
				Message: "unknown field in diagram JSON",
				Detail:  errStr,
				Hint:    "Check property names against the SymDraw IR JSON schema (internal/draw/ir/schema.go).",
				Err:     err,
			}
		}
		return nil, &ParseError{
			Stage:   "parse",
			Message: "malformed JSON diagram input",
			Detail:  errStr,
			Hint:    "Verify JSON syntax, commas, and string quotes.",
			Err:     err,
		}
	}

	if err := ir.Validate(&diag); err != nil {
		if valErr, ok := err.(*ir.ValidationError); ok {
			return nil, &ParseError{
				Stage:   "contract",
				Message: valErr.Message,
				Detail:  valErr.Field,
				Hint:    irValidationHint(valErr.Field),
				Err:     err,
			}
		}
		return nil, &ParseError{
			Stage:   "contract",
			Message: err.Error(),
			Hint:    "Ensure diagram entities and references satisfy SymDraw IR constraints.",
			Err:     err,
		}
	}

	return &diag, nil
}

func irValidationHint(field string) string {
	switch {
	case field == "kind":
		return "Diagram kind must be one of: 'graph', 'sequence', 'timeline', 'tree', 'chart', 'custom'."
	case field == "direction":
		return "Diagram flow direction must be one of: 'TD', 'TB', 'BT', 'LR', 'RL'."
	case strings.Contains(field, "shape"):
		return "Supported shapes are: 'rect', 'round', 'circle', 'cylinder', 'diamond', 'pill', 'stadium', 'subroutine', 'hexagon', 'asymmetric'."
	case strings.Contains(field, "style"):
		return "Supported edge styles are: 'solid', 'dashed', 'dotted', 'thick'."
	case strings.Contains(field, "arrow"):
		return "Supported arrow types are: 'none', 'single', 'double', 'cross', 'circle'."
	case strings.HasPrefix(field, "edges"):
		return "Ensure edge 'from' and 'to' fields reference valid node IDs defined in 'nodes'."
	case strings.HasPrefix(field, "groups"):
		return "Ensure group 'members' list contains valid node IDs defined in 'nodes'."
	case strings.HasPrefix(field, "chart"):
		return "Chart requires a valid 'type' ('bar', 'line', 'pie', 'scatter', 'area', 'donut') and at least one series."
	default:
		return fmt.Sprintf("Refer to SymDraw IR schema documentation for valid %q values.", field)
	}
}
