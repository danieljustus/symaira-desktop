package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-print/internal/config"
	"github.com/danieljustus/symaira-print/internal/press"
)

func TestMapPressError_ContractError(t *testing.T) {
	err := &press.ContractError{
		Reason: "validation failed",
		Issues: []press.Issue{
			{Severity: "error", Field: "title", Message: "required"},
		},
	}
	cliErr := mapPressError(err)
	var ce *exitcodes.CLIError
	if !errors.As(cliErr, &ce) {
		t.Fatalf("expected *exitcodes.CLIError, got %T", cliErr)
	}
	if ce.Code != exitcodes.ExitData {
		t.Errorf("exit code = %v, want %v", ce.Code, exitcodes.ExitData)
	}
	if ce.Kind != exitcodes.KindValidation {
		t.Errorf("kind = %v, want %v", ce.Kind, exitcodes.KindValidation)
	}
}

func TestMapPressError_RenderError(t *testing.T) {
	tests := []struct {
		stage    string
		wantCode exitcodes.ExitCode
		wantKind exitcodes.ErrorKind
	}{
		{"engine", exitcodes.ExitGeneric, exitcodes.KindUnavailable},
		{"compile", exitcodes.ExitData, exitcodes.KindValidation},
		{"write", exitcodes.ExitGeneric, exitcodes.KindInternal},
		{"contract", exitcodes.ExitData, exitcodes.KindValidation},
		{"unknown", exitcodes.ExitGeneric, exitcodes.KindInternal},
	}
	for _, tt := range tests {
		t.Run(tt.stage, func(t *testing.T) {
			err := &press.RenderError{
				Stage:   tt.stage,
				Message: "render failed",
				Hint:    "check your input",
			}
			cliErr := mapPressError(err)
			var ce *exitcodes.CLIError
			if !errors.As(cliErr, &ce) {
				t.Fatalf("expected *exitcodes.CLIError, got %T", cliErr)
			}
			if ce.Code != tt.wantCode {
				t.Errorf("stage %q: code = %v, want %v", tt.stage, ce.Code, tt.wantCode)
			}
			if ce.Kind != tt.wantKind {
				t.Errorf("stage %q: kind = %v, want %v", tt.stage, ce.Kind, tt.wantKind)
			}
			if ce.Hint != "check your input" {
				t.Errorf("stage %q: hint = %q, want %q", tt.stage, ce.Hint, "check your input")
			}
		})
	}
}

func TestMapPressError_GenericError(t *testing.T) {
	err := errors.New("something unexpected")
	cliErr := mapPressError(err)
	var ce *exitcodes.CLIError
	if !errors.As(cliErr, &ce) {
		t.Fatalf("expected *exitcodes.CLIError, got %T", cliErr)
	}
	if ce.Code != exitcodes.ExitGeneric {
		t.Errorf("exit code = %v, want %v", ce.Code, exitcodes.ExitGeneric)
	}
	if ce.Kind != exitcodes.KindInternal {
		t.Errorf("kind = %v, want %v", ce.Kind, exitcodes.KindInternal)
	}
}

func TestNewRenderCmd(t *testing.T) {
	cmd := newRenderCmd()
	if cmd.Use != "render <input.md>" {
		t.Errorf("Use = %q, want %q", cmd.Use, "render <input.md>")
	}
	if cmd.Short == "" {
		t.Error("Short should not be empty")
	}
	// Verify flags exist
	flags := []string{"output", "profile", "pdf-standard", "reproducible"}
	for _, name := range flags {
		if cmd.Flags().Lookup(name) == nil {
			t.Errorf("missing flag %q", name)
		}
	}
}

func TestNewValidateCmd(t *testing.T) {
	cmd := newValidateCmd()
	if cmd.Use != "validate <input.md>" {
		t.Errorf("Use = %q, want %q", cmd.Use, "validate <input.md>")
	}
	if cmd.Short == "" {
		t.Error("Short should not be empty")
	}
	// Verify profile flag exists
	if cmd.Flags().Lookup("profile") == nil {
		t.Error("missing flag profile")
	}
}

func TestSplitCSV(t *testing.T) {
	tests := []struct {
		input string
		want  []string
	}{
		{"a,b,c", []string{"a", "b", "c"}},
		{"a, b, c", []string{"a", "b", "c"}},
		{"a,,b", []string{"a", "b"}},
		{"", []string{}},
		{"single", []string{"single"}},
	}
	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			got := splitCSV(tt.input)
			if len(got) != len(tt.want) {
				t.Errorf("splitCSV(%q) = %v (len %d), want %v (len %d)", tt.input, got, len(got), tt.want, len(tt.want))
				return
			}
			for i := range got {
				if got[i] != tt.want[i] {
					t.Errorf("splitCSV(%q)[%d] = %q, want %q", tt.input, i, got[i], tt.want[i])
				}
			}
		})
	}
}

func TestStandardLabel(t *testing.T) {
	tests := []struct {
		input []string
		want  string
	}{
		{nil, "PDF (tagged)"},
		{[]string{}, "PDF (tagged)"},
		{[]string{"a-2a"}, "PDF/A-2A"},
		{[]string{"a-2a", "ua-1"}, "PDF/A-2A+UA-1"},
	}
	for _, tt := range tests {
		got := standardLabel(tt.input)
		if got != tt.want {
			t.Errorf("standardLabel(%v) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

func TestAnyError(t *testing.T) {
	tests := []struct {
		name   string
		issues []press.Issue
		want   bool
	}{
		{"empty", nil, false},
		{"warnings only", []press.Issue{{Severity: "warning", Field: "x"}}, false},
		{"has error", []press.Issue{{Severity: "error", Field: "x"}}, true},
		{"mixed", []press.Issue{{Severity: "warning"}, {Severity: "error"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := anyError(tt.issues); got != tt.want {
				t.Errorf("anyError() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestSevGlyph(t *testing.T) {
	if got := sevGlyph("error"); got != "✗" {
		t.Errorf("sevGlyph(error) = %q, want ✗", got)
	}
	if got := sevGlyph("warning"); got != "!" {
		t.Errorf("sevGlyph(warning) = %q, want !", got)
	}
}

func TestEngineFromConfig(t *testing.T) {
	cfg := &config.Config{
		Engine: config.Engine{
			Typst:             "/usr/local/bin/typst",
			FontPaths:         []string{"/fonts/a", "/fonts/b"},
			IgnoreSystemFonts: true,
			TimeoutSeconds:    30,
		},
	}
	got := engineFromConfig(cfg)
	if got.TypstBin != cfg.Engine.Typst {
		t.Errorf("TypstBin = %q, want %q", got.TypstBin, cfg.Engine.Typst)
	}
	if len(got.FontPaths) != 2 || got.FontPaths[0] != "/fonts/a" || got.FontPaths[1] != "/fonts/b" {
		t.Errorf("FontPaths = %v, want %v", got.FontPaths, cfg.Engine.FontPaths)
	}
	if !got.IgnoreSystemFonts {
		t.Error("IgnoreSystemFonts should be true")
	}
	if got.Timeout != 30*time.Second {
		t.Errorf("Timeout = %v, want %v", got.Timeout, 30*time.Second)
	}
}

func TestPrintJSON(t *testing.T) {
	out := captureStdout(t, func() {
		if err := printJSON(map[string]any{"a": 1, "b": "two"}); err != nil {
			t.Fatalf("printJSON() error = %v", err)
		}
	})
	var got map[string]any
	if err := json.Unmarshal([]byte(out), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out)
	}
	if got["b"] != "two" {
		t.Errorf("b = %v, want %q", got["b"], "two")
	}
	if !strings.Contains(out, "  ") {
		t.Error("expected indented JSON output")
	}
}

func TestPrintJSON_MarshalError(t *testing.T) {
	// Channels are not JSON-marshalable, so this exercises the error branch.
	if err := printJSON(map[string]any{"bad": make(chan int)}); err == nil {
		t.Fatal("expected an error for an unmarshalable value")
	}
}

func TestNewRootCmd(t *testing.T) {
	cmd := newRootCmd()
	if cmd.Use != "symprint" {
		t.Errorf("Use = %q, want %q", cmd.Use, "symprint")
	}
	// Verify subcommands are registered
	expected := []string{"render", "validate", "profiles", "doctor", "config", "mcp", "version"}
	for _, name := range expected {
		found := false
		for _, sub := range cmd.Commands() {
			if sub.Name() == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("missing subcommand %q", name)
		}
	}
}
