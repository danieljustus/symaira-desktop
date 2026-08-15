package main

import (
	"encoding/json"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-desktop/internal/config"
)

// When a health check fails, `doctor --json` printed the full report and then
// a second `{"error": ...}` document, both on stdout. Any strict JSON decoder
// — including the macOS app's — fails on the trailing document and throws the
// diagnosis away (issue #438). The report already carries "overall":"error",
// so the envelope is redundant on the JSON path.

// decodeOneJSONDocument decodes the first JSON value from out and returns it
// along with whatever non-whitespace text follows it.
func decodeOneJSONDocument(t *testing.T, out string) (map[string]interface{}, string) {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(out))
	var first map[string]interface{}
	if err := dec.Decode(&first); err != nil {
		t.Fatalf("stdout did not start with a JSON object: %v\nstdout was:\n%s", err, out)
	}
	rest, err := io.ReadAll(dec.Buffered())
	if err != nil {
		t.Fatalf("reading buffered remainder: %v", err)
	}
	return first, strings.TrimSpace(string(rest))
}

// useMissingVault points the config at a path that does not exist, which is
// the failing-check case that produced the second document.
func useMissingVault(t *testing.T) {
	t.Helper()
	origCfg := cfg
	cfg = &config.Config{
		Vault:           filepath.Join(t.TempDir(), "definitely-not-here"),
		ReviewThreshold: 85,
		LLMProvider:     "ollama",
	}
	t.Cleanup(func() { cfg = origCfg })
	t.Setenv("PATH", "/usr/bin:/bin")
}

func TestDoctorJSONEmitsExactlyOneDocumentOnFailure(t *testing.T) {
	useMissingVault(t)
	jsonFlag = true
	defer func() { jsonFlag = false }()

	out, runErr := runCommand(t, newDoctorCmd(), nil)

	report, trailing := decodeOneJSONDocument(t, out)
	if trailing != "" {
		t.Errorf("doctor --json wrote more than one JSON document to stdout; trailing bytes: %q", trailing)
	}
	if report["overall"] != "error" {
		t.Errorf("expected the report to mark overall=error, got %v", report["overall"])
	}

	// The failure must still be signalled so the process exit code stays
	// non-zero — that behaviour was added deliberately and must not regress.
	if runErr == nil {
		t.Fatal("expected doctor to return an error so the exit code is non-zero")
	}
	if got := exitcodes.ExitCodeFromError(runErr); got == exitcodes.ExitOK {
		t.Errorf("expected a non-zero exit code, got %v", got)
	}

	// ...but it must be marked as already reported, so main() does not print
	// a second JSON document on top of the report.
	var reported jsonReportedError
	if !errors.As(runErr, &reported) {
		t.Errorf("expected the error to be marked as already reported in JSON, got %T: %v", runErr, runErr)
	}
}

func TestDoctorTextModeStillReturnsAPlainError(t *testing.T) {
	useMissingVault(t)
	jsonFlag = false

	_, runErr := runCommand(t, newDoctorCmd(), nil)
	if runErr == nil {
		t.Fatal("expected doctor to return an error in text mode too")
	}

	// Nothing was printed as JSON here, so main() must still produce the
	// human-readable error output.
	var reported jsonReportedError
	if errors.As(runErr, &reported) {
		t.Error("text mode must not suppress the error output")
	}
}
