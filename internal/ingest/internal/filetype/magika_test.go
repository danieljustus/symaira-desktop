package filetype

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFakeMagika creates a fake `magika` executable in a fresh temp dir and
// prepends that dir to PATH so exec.LookPath/exec.Command pick it up. Tests
// using it are deterministic and independent of whether a real magika binary
// exists on the machine. script is appended to a "#!/bin/sh" shebang.
func writeFakeMagika(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	bin := filepath.Join(dir, "magika")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"+script), 0o755); err != nil {
		t.Fatalf("write fake magika: %v", err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
}

// collectLogf returns a logf func that appends formatted messages to a slice.
func collectLogf() (func(format string, args ...any), *[]string) {
	var msgs []string
	logf := func(format string, args ...any) {
		msgs = append(msgs, fmt.Sprintf(format, args...))
	}
	return logf, &msgs
}

func TestLabelToMIME(t *testing.T) {
	tests := []struct {
		label    string
		expected string
	}{
		{"pdf", "application/pdf"},
		{"png", "image/png"},
		{"jpeg", "image/jpeg"},
		{"tiff", "image/tiff"},
		{"webp", "image/webp"},
		{"txt", "text/plain"},
		{"text", "text/plain"},
		{"csv", "text/csv"},
		{"markdown", "text/markdown"},
		{"html", "text/html"},
		{"rtf", "application/rtf"},
		{"docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document"},
		{"xlsx", "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"},
		{"odt", "application/vnd.oasis.opendocument.text"},
		{"eml", "message/rfc822"},
		{"PDF", "application/pdf"},
		{"TXT", "text/plain"},
		{"Markdown", "text/markdown"},
		// Unmapped labels
		{"zip", ""},
		{"exe", ""},
		{"unknown", ""},
		{"", ""},
	}

	for _, tc := range tests {
		t.Run(tc.label, func(t *testing.T) {
			got := LabelToMIME(tc.label)
			if got != tc.expected {
				t.Errorf("LabelToMIME(%q) = %q, want %q", tc.label, got, tc.expected)
			}
		})
	}
}

func TestAvailable(t *testing.T) {
	// Available should always return a value without error
	_ = Available()
}

func TestDetectWithoutMagika(t *testing.T) {
	// When magika is not installed, Detect should return nil, nil
	pred, err := Detect("/nonexistent/file.pdf", nil)
	if err != nil {
		t.Fatalf("Detect returned error: %v", err)
	}
	if pred != nil {
		t.Fatalf("Detect returned non-nil prediction when magika is unavailable")
	}
}

func TestVerifyAgainstGuessWithoutMagika(t *testing.T) {
	// Should not panic or error when magika is not installed
	logged := false
	logf := func(format string, args ...any) {
		logged = true
	}
	VerifyAgainstGuess("/nonexistent/file.pdf", "application/pdf", logf)
	if logged {
		t.Fatal("unexpected log when magika is unavailable")
	}
}

func TestVerifyAgainstGuessEmptyPath(t *testing.T) {
	// Should not panic on empty paths
	VerifyAgainstGuess("", "application/pdf", nil)
}

// --- PATH-injection tests: each installs a fake `magika` on PATH, so results
// are deterministic and independent of any real magika installation. ---

func TestDetectSuccess(t *testing.T) {
	writeFakeMagika(t, `cat <<'EOF'
{"path":"/tmp/report.pdf","result":{"output":{"ct_label":"pdf","mime_type":"application/pdf","score":0.99}}}
EOF
`)
	pred, err := Detect("/tmp/report.pdf", nil)
	if err != nil {
		t.Fatalf("Detect returned error: %v", err)
	}
	if pred == nil {
		t.Fatal("Detect returned nil prediction")
	}
	if pred.CTLabel != "pdf" {
		t.Errorf("CTLabel = %q, want %q", pred.CTLabel, "pdf")
	}
	if pred.MIMEType != "application/pdf" {
		t.Errorf("MIMEType = %q, want %q", pred.MIMEType, "application/pdf")
	}
	if pred.Score != 0.99 {
		t.Errorf("Score = %v, want %v", pred.Score, 0.99)
	}
}

func TestDetectMagikaErrorLogsStderr(t *testing.T) {
	writeFakeMagika(t, `echo "boom" >&2; exit 1`)
	logf, msgs := collectLogf()
	pred, err := Detect("/tmp/report.pdf", logf)
	if err != nil {
		t.Fatalf("Detect returned error: %v", err)
	}
	if pred != nil {
		t.Fatal("Detect returned non-nil prediction on magika failure")
	}
	if len(*msgs) != 1 {
		t.Fatalf("got %d log messages, want 1: %v", len(*msgs), *msgs)
	}
	if !strings.Contains((*msgs)[0], "boom") {
		t.Errorf("log message %q does not contain stderr text %q", (*msgs)[0], "boom")
	}
}

func TestDetectMagikaErrorEmptyStderr(t *testing.T) {
	// With empty stderr the fallback is the exec error text ("exit status 1").
	writeFakeMagika(t, `exit 1`)
	logf, msgs := collectLogf()
	pred, err := Detect("/tmp/report.pdf", logf)
	if err != nil {
		t.Fatalf("Detect returned error: %v", err)
	}
	if pred != nil {
		t.Fatal("Detect returned non-nil prediction on magika failure")
	}
	if len(*msgs) != 1 {
		t.Fatalf("got %d log messages, want 1: %v", len(*msgs), *msgs)
	}
	if !strings.Contains((*msgs)[0], "exit status 1") {
		t.Errorf("log message %q does not contain exec error %q", (*msgs)[0], "exit status 1")
	}
}

func TestDetectMalformedJSON(t *testing.T) {
	writeFakeMagika(t, `echo "this is not json"`)
	logf, msgs := collectLogf()
	pred, err := Detect("/tmp/report.pdf", logf)
	if err != nil {
		t.Fatalf("Detect returned error: %v", err)
	}
	if pred != nil {
		t.Fatal("Detect returned non-nil prediction on malformed output")
	}
	if len(*msgs) != 1 {
		t.Fatalf("got %d log messages, want 1: %v", len(*msgs), *msgs)
	}
	if !strings.Contains((*msgs)[0], "failed to parse JSON output") {
		t.Errorf("log message %q does not mention JSON parse failure", (*msgs)[0])
	}
}

func TestDetectEmptyResult(t *testing.T) {
	// Both shapes of "no usable output" must degrade gracefully.
	tests := []struct {
		name   string
		output string
	}{
		{"result null", `{"path":"/tmp/report.pdf","result":null}`},
		{"output missing", `{"path":"/tmp/report.pdf","result":{}}`},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			writeFakeMagika(t, `echo '`+tc.output+`'`)
			logf, msgs := collectLogf()
			pred, err := Detect("/tmp/report.pdf", logf)
			if err != nil {
				t.Fatalf("Detect returned error: %v", err)
			}
			if pred != nil {
				t.Fatal("Detect returned non-nil prediction on empty result")
			}
			if len(*msgs) != 1 {
				t.Fatalf("got %d log messages, want 1: %v", len(*msgs), *msgs)
			}
			if !strings.Contains((*msgs)[0], "unexpected empty result") {
				t.Errorf("log message %q does not mention empty result", (*msgs)[0])
			}
		})
	}
}

func TestVerifyAgainstGuessMismatchLogsWarning(t *testing.T) {
	// magika says docx, extension says pdf -> exactly one advisory warning,
	// and the guess is NOT overridden (nothing is returned; only logged).
	writeFakeMagika(t, `cat <<'EOF'
{"path":"/tmp/report.pdf","result":{"output":{"ct_label":"docx","mime_type":"application/vnd.openxmlformats-officedocument.wordprocessingml.document","score":0.9}}}
EOF
`)
	logf, msgs := collectLogf()
	VerifyAgainstGuess("/tmp/report.pdf", "application/pdf", logf)
	if len(*msgs) != 1 {
		t.Fatalf("got %d log messages, want exactly 1 warning: %v", len(*msgs), *msgs)
	}
	msg := (*msgs)[0]
	for _, want := range []string{
		"Warning:",
		"report.pdf",
		"application/pdf",
		"application/vnd.openxmlformats-officedocument.wordprocessingml.document",
		`label="docx"`,
		"score=0.900",
		"proceeding with extension-based guess",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("warning %q does not contain %q", msg, want)
		}
	}
}

func TestVerifyAgainstGuessAgreeingNoWarning(t *testing.T) {
	// magika and the extension agree -> no warning at all.
	writeFakeMagika(t, `cat <<'EOF'
{"path":"/tmp/report.pdf","result":{"output":{"ct_label":"pdf","mime_type":"application/pdf","score":1.0}}}
EOF
`)
	logf, msgs := collectLogf()
	VerifyAgainstGuess("/tmp/report.pdf", "application/pdf", logf)
	if len(*msgs) != 0 {
		t.Fatalf("got %d log messages, want none: %v", len(*msgs), *msgs)
	}
}

func TestVerifyAgainstGuessUnmappedLabelNoWarning(t *testing.T) {
	// Unmapped magika label (zip) -> no warning, no panic.
	writeFakeMagika(t, `cat <<'EOF'
{"path":"/tmp/archive.bin","result":{"output":{"ct_label":"zip","mime_type":"application/zip","score":0.8}}}
EOF
`)
	logf, msgs := collectLogf()
	VerifyAgainstGuess("/tmp/archive.bin", "application/octet-stream", logf)
	if len(*msgs) != 0 {
		t.Fatalf("got %d log messages, want none: %v", len(*msgs), *msgs)
	}
}
