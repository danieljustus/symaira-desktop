// Package filetype provides optional content-type verification via the magika
// CLI (google/magika). When magika is not installed, all operations degrade
// gracefully with no error.
package filetype

import (
	"bytes"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
)

// MagikaPrediction holds the detected content-type information from magika.
type MagikaPrediction struct {
	CTLabel  string  // content-type label, e.g. "pdf", "docx"
	MIMEType string  // MIME type string, e.g. "application/pdf"
	Score    float64 // confidence score [0,1]
}

// magikaResult maps the JSON output of "magika --json <path>".
type magikaResult struct {
	Path   string          `json:"path"`
	Result *magikaOutput   `json:"result"`
	Error  json.RawMessage `json:"error,omitempty"`
}

type magikaOutput struct {
	DL     *magikaPrediction `json:"dl,omitempty"`
	Output *magikaPrediction `json:"output,omitempty"`
}

type magikaPrediction struct {
	CTLabel  string  `json:"ct_label"`
	Group    string  `json:"group,omitempty"`
	MIMEType string  `json:"mime_type,omitempty"`
	Score    float64 `json:"score,omitempty"`
}

// Available returns true if the magika binary is found on PATH.
func Available() bool {
	_, err := exec.LookPath("magika")
	return err == nil
}

// Detect shells out to "magika --json <path>" and returns the detected
// content-type prediction. It returns nil, nil when magika is not installed
// or when the call fails (warnings are logged via the optional logf parameter;
// pass nil to discard them).
func Detect(path string, logf func(format string, args ...any)) (*MagikaPrediction, error) {
	if !Available() {
		return nil, nil
	}
	return detectUncached(path, logf)
}

func detectUncached(path string, logf func(format string, args ...any)) (*MagikaPrediction, error) {
	magikaPath, err := exec.LookPath("magika")
	if err != nil {
		return nil, nil
	}

	cmd := exec.Command(magikaPath, "--json", path)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		if logf != nil {
			logf("magika: %s", msg)
		}
		return nil, nil // degrade gracefully
	}

	var result magikaResult
	if err := json.Unmarshal(stdout.Bytes(), &result); err != nil {
		if logf != nil {
			logf("magika: failed to parse JSON output: %v", err)
		}
		return nil, nil
	}

	if result.Result == nil || result.Result.Output == nil {
		if logf != nil {
			logf("magika: unexpected empty result for %s", path)
		}
		return nil, nil
	}

	return &MagikaPrediction{
		CTLabel:  result.Result.Output.CTLabel,
		MIMEType: result.Result.Output.MIMEType,
		Score:    result.Result.Output.Score,
	}, nil
}

// LabelToMIME maps a magika ct_label to a MIME-type string.
// Returns an empty string for unmapped labels.
func LabelToMIME(label string) string {
	switch strings.ToLower(label) {
	case "pdf":
		return "application/pdf"
	case "png":
		return "image/png"
	case "jpeg":
		return "image/jpeg"
	case "tiff":
		return "image/tiff"
	case "webp":
		return "image/webp"
	case "txt", "text":
		return "text/plain"
	case "csv":
		return "text/csv"
	case "markdown":
		return "text/markdown"
	case "html":
		return "text/html"
	case "rtf":
		return "application/rtf"
	case "docx":
		return "application/vnd.openxmlformats-officedocument.wordprocessingml.document"
	case "xlsx":
		return "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet"
	case "odt":
		return "application/vnd.oasis.opendocument.text"
	case "eml":
		return "message/rfc822"
	default:
		return ""
	}
}

// VerifyAgainstGuess runs magika detection on path and compares its result
// against the MIME type derived from the file extension. If a mismatch is
// found, a warning is logged via the provided logf function (pass nil to
// discard). This is purely advisory — the detected type is never overridden.
//
// path is the file to check.
// extMIME is the MIME type inferred from the file extension (e.g. "application/pdf").
// logf receives the warning message; pass nil to suppress logging.
func VerifyAgainstGuess(path string, extMIME string, logf func(format string, args ...any)) {
	pred, err := Detect(path, logf)
	if err != nil || pred == nil {
		return // magika unavailable or errored — fall through silently
	}

	magikaMIME := LabelToMIME(pred.CTLabel)
	if magikaMIME == "" {
		return // unmapped label — not our concern
	}

	if magikaMIME != extMIME {
		if logf != nil {
			logf("Warning: %s extension suggests %q but magika detected %q (label=%q, score=%.3f); proceeding with extension-based guess",
				filepath.Base(path), extMIME, magikaMIME, pred.CTLabel, pred.Score)
		}
	}
}
