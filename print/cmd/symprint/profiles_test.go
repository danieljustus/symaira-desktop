package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-print/internal/press"
)

func TestNewProfilesCmd_ListText(t *testing.T) {
	jsonOut = false
	cmd := newProfilesCmd()
	out := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
	})
	if !strings.Contains(out, "PROFILE") {
		t.Errorf("output missing table header, got: %q", out)
	}
	if !strings.Contains(out, "brief") || !strings.Contains(out, "behoerde") {
		t.Errorf("output missing known profiles, got: %q", out)
	}
}

func TestNewProfilesCmd_ListJSON(t *testing.T) {
	jsonOut = true
	defer func() { jsonOut = false }()
	cmd := newProfilesCmd()
	out := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
	})
	var profiles []press.Profile
	if err := json.Unmarshal([]byte(out), &profiles); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out)
	}
	if len(profiles) == 0 {
		t.Error("expected at least one profile")
	}
}

func TestNewProfilesCmd_ShowText(t *testing.T) {
	jsonOut = false
	cmd := newProfilesCmd()
	cmd.SetArgs([]string{"behoerde"})
	out := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
	})
	if !strings.Contains(out, "behoerde") {
		t.Errorf("output missing profile name, got: %q", out)
	}
	if !strings.Contains(out, "PDF/A") || !strings.Contains(out, "PDF/UA") {
		t.Errorf("output missing behoerde guarantees, got: %q", out)
	}
}

func TestNewProfilesCmd_ShowJSON(t *testing.T) {
	jsonOut = true
	defer func() { jsonOut = false }()
	cmd := newProfilesCmd()
	cmd.SetArgs([]string{"brief"})
	out := captureStdout(t, func() {
		if err := cmd.Execute(); err != nil {
			t.Fatalf("Execute() error = %v", err)
		}
	})
	var result struct {
		press.Profile
		Capability press.Capability `json:"capability"`
	}
	if err := json.Unmarshal([]byte(out), &result); err != nil {
		t.Fatalf("output is not valid JSON: %v\noutput: %s", err, out)
	}
	if result.Name != "brief" {
		t.Errorf("name = %q, want %q", result.Name, "brief")
	}
}

func TestNewProfilesCmd_UnknownProfile(t *testing.T) {
	jsonOut = false
	cmd := newProfilesCmd()
	cmd.SetArgs([]string{"does-not-exist"})
	cmd.SilenceErrors = true
	cmd.SilenceUsage = true
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected an error for an unknown profile")
	}
	if !strings.Contains(err.Error(), "does-not-exist") {
		t.Errorf("error %q should mention the unknown profile name", err.Error())
	}
}

func TestShowProfile(t *testing.T) {
	p, ok := press.Lookup("behoerde")
	if !ok {
		t.Fatal("expected the behoerde profile to exist")
	}
	out := captureStdout(t, func() {
		showProfile(p)
	})
	for _, want := range []string{"behoerde", p.Title, p.Description, "din 5008 form A", "a-2a, ua-1", "recipient, title, lang"} {
		if !strings.Contains(out, want) {
			t.Errorf("showProfile() output missing %q, got: %q", want, out)
		}
	}
}

func TestCapLabel(t *testing.T) {
	tests := []struct {
		name string
		cap  press.Capability
		want string
	}{
		{"none", press.Capability{}, "tagged PDF"},
		{"pdfa only", press.Capability{PDFA: true}, "PDF/A"},
		{"all", press.Capability{PDFA: true, PDFUA: true, DINWindow: true, Reproducible: true}, "PDF/A, PDF/UA, DIN-window, reproducible"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := capLabel(tt.cap); got != tt.want {
				t.Errorf("capLabel() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTruncate(t *testing.T) {
	tests := []struct {
		input string
		n     int
		want  string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is a long string", 10, "this is a…"},
		{"abc", 1, "a"},
	}
	for _, tt := range tests {
		got := truncate(tt.input, tt.n)
		if got != tt.want {
			t.Errorf("truncate(%q, %d) = %q, want %q", tt.input, tt.n, got, tt.want)
		}
	}
}

func TestReadmeDriftGuard(t *testing.T) {
	content, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatalf("failed to read README.md: %v", err)
	}
	readme := string(content)

	for _, p := range press.All() {
		pattern := "`" + p.Name + "`"
		if !strings.Contains(readme, pattern) {
			t.Errorf("profile %q is not documented in README.md (missing %s)", p.Name, pattern)
		}
	}
}
