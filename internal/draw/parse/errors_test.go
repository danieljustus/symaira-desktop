package parse

import (
	"errors"
	"strings"
	"testing"
)

func TestParseErrorFormatting(t *testing.T) {
	pe := &ParseError{
		Stage:   "parse",
		Message: "unsupported construct \"click\"",
		Detail:  "line 12: click A \"https://example.com\"",
		Hint:    "Interactive click bindings are not supported.",
		Line:    12,
		Err:     errors.New("root cause"),
	}

	errStr := pe.Error()
	if !strings.Contains(errStr, `unsupported construct "click"`) {
		t.Errorf("expected message in error string: %s", errStr)
	}
	if !strings.Contains(errStr, "line 12") {
		t.Errorf("expected line number in error string: %s", errStr)
	}
	if !strings.Contains(errStr, "hint: Interactive click bindings") {
		t.Errorf("expected hint in error string: %s", errStr)
	}

	if errors.Unwrap(pe) == nil {
		t.Error("expected Unwrap() to return root cause error")
	}
}

func TestHelperConstructors(t *testing.T) {
	pe1 := NewUnsupportedConstructError("style", "Use frontmatter themes", 5, "style A fill:#fff")
	if pe1.Stage != "parse" || pe1.Line != 5 || !strings.Contains(pe1.Message, "style") {
		t.Errorf("unexpected pe1: %+v", pe1)
	}

	pe2 := NewUnsupportedDiagramError("pie", "Use IR chart", 1, "pie")
	if pe2.Stage != "parse" || pe2.Line != 1 || !strings.Contains(pe2.Message, "pie") {
		t.Errorf("unexpected pe2: %+v", pe2)
	}

	pe3 := NewSyntaxError("malformed bracket", 3, "A[B", "Close bracket")
	if pe3.Stage != "parse" || pe3.Line != 3 {
		t.Errorf("unexpected pe3: %+v", pe3)
	}

	pe4 := NewSchemaError("kind", "diagram kind required", "Set kind", nil)
	if pe4.Stage != "schema" || pe4.Detail != "kind" {
		t.Errorf("unexpected pe4: %+v", pe4)
	}

	pe5 := NewContractError("nodes[0].id", "duplicate id", "Fix duplicate", nil)
	if pe5.Stage != "contract" || pe5.Detail != "nodes[0].id" {
		t.Errorf("unexpected pe5: %+v", pe5)
	}
}
