package diff

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"regexp"
	"strconv"
	"strings"
)

// Compare checks the observable contract selected by testCase.
func Compare(testCase Case, left, right Result) error {
	if left.TimedOut != right.TimedOut {
		return fmt.Errorf("timeout mismatch: left=%t right=%t", left.TimedOut, right.TimedOut)
	}
	if left.TimedOut {
		// A case whose expected behaviour is completion must never pass by
		// hanging: two matching kills are not parity evidence.
		return errors.New("both sides timed out: a timed-out case is not a pass")
	}
	if left.ExitCode != right.ExitCode {
		return fmt.Errorf("exit mismatch: left=%d right=%d", left.ExitCode, right.ExitCode)
	}
	if left.Signal != right.Signal {
		return fmt.Errorf("signal mismatch: left=%q right=%q", left.Signal, right.Signal)
	}
	if err := compareStream("stdout", testCase.stdoutComparisonMode(), left.Stdout, right.Stdout, left.SandboxRoot, right.SandboxRoot); err != nil {
		return err
	}
	if err := compareStream("stderr", testCase.stderrComparisonMode(), left.Stderr, right.Stderr, left.SandboxRoot, right.SandboxRoot); err != nil {
		return err
	}
	if testCase.CompareFiles && !reflect.DeepEqual(left.Files, right.Files) {
		return fmt.Errorf("filesystem manifest mismatch: left=%s right=%s", digestValue(left.Files), digestValue(right.Files))
	}
	if testCase.CompareSidecarLayout {
		leftLayout, rightLayout := sidecarLayout(left.Files), sidecarLayout(right.Files)
		if len(leftLayout) == 0 || !reflect.DeepEqual(leftLayout, rightLayout) {
			return fmt.Errorf("sidecar layout mismatch: left=%s right=%s", digestValue(leftLayout), digestValue(rightLayout))
		}
	}
	return nil
}

func sidecarLayout(entries []ManifestEntry) []string {
	layout := make([]string, 0, 1)
	for _, entry := range entries {
		// A per-vault sidecar directory holds the database, the write-ahead
		// log and shared-memory files SQLite leaves while the connection is
		// open, and the metadata record Go writes on open (issue #1006). All
		// of them belong to the layout; their contents carry a timestamp and
		// SQLite state and can therefore never be compared byte-for-byte
		// across two processes.
		if entry.Type != "file" ||
			(!strings.HasSuffix(entry.Path, "/sidecar.db") &&
				!strings.HasSuffix(entry.Path, "/sidecar.db-wal") &&
				!strings.HasSuffix(entry.Path, "/sidecar.db-shm") &&
				!strings.HasSuffix(entry.Path, "/metadata.json")) {
			continue
		}
		parts := strings.Split(entry.Path, "/")
		valid := false
		for index := 0; index+2 < len(parts); index++ {
			if parts[index] == "vaults" && isLowerHex16(parts[index+1]) {
				parts[index+1] = "<vault-hash>"
				valid = true
				break
			}
		}
		if valid {
			layout = append(layout, strings.Join(parts, "/"))
		}
	}
	return layout
}

func isLowerHex16(value string) bool {
	if len(value) != 16 {
		return false
	}
	for _, char := range value {
		if (char < '0' || char > '9') && (char < 'a' || char > 'f') {
			return false
		}
	}
	return true
}

func compareStream(name, mode string, left, right []byte, leftRoot, rightRoot string) error {
	switch mode {
	case comparisonModeIgnore:
		return nil
	case comparisonModeBytes:
	case comparisonModeConsoleText:
		left = normalizeConsole(left, leftRoot)
		right = normalizeConsole(right, rightRoot)
	case comparisonModeJSONRunID:
		var err error
		left, err = normalizeJSONRunID(left)
		if err != nil {
			return fmt.Errorf("left %s: %w", name, err)
		}
		right, err = normalizeJSONRunID(right)
		if err != nil {
			return fmt.Errorf("right %s: %w", name, err)
		}
	case comparisonModeTextRunID:
		var err error
		left, err = normalizeTextRunID(left)
		if err != nil {
			return fmt.Errorf("left %s: %w", name, err)
		}
		right, err = normalizeTextRunID(right)
		if err != nil {
			return fmt.Errorf("right %s: %w", name, err)
		}
	default:
		return fmt.Errorf("unsupported %s comparison mode %q", name, mode)
	}
	if !bytes.Equal(left, right) {
		return fmt.Errorf("%s mismatch: left_bytes=%d left_sha256=%s right_bytes=%d right_sha256=%s",
			name, len(left), digestBytes(left), len(right), digestBytes(right))
	}
	return nil
}

func normalizeJSONRunID(value []byte) ([]byte, error) {
	var output struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(value, &output); err != nil {
		return nil, errors.New("invalid JSON output")
	}
	seconds, ok := strings.CutPrefix(output.RunID, "ret-")
	if !ok || seconds == "" {
		return nil, errors.New("missing retention run ID")
	}
	if _, err := strconv.ParseInt(seconds, 10, 64); err != nil {
		return nil, errors.New("invalid retention run ID")
	}
	needle := []byte(`"run_id":"` + output.RunID + `"`)
	if bytes.Count(value, needle) != 1 {
		return nil, errors.New("ambiguous retention run ID field")
	}
	return bytes.Replace(value, needle, []byte(`"run_id":"ret-<clock>"`), 1), nil
}

var textRunID = regexp.MustCompile(`run_id:ret-[0-9]+`)

func normalizeTextRunID(value []byte) ([]byte, error) {
	if len(textRunID.FindAll(value, 2)) != 1 {
		return nil, errors.New("missing or ambiguous retention run ID")
	}
	return textRunID.ReplaceAll(value, []byte("run_id:ret-<clock>")), nil
}

func normalizeConsole(value []byte, sandboxRoot string) []byte {
	value = bytes.ReplaceAll(value, []byte("\r\n"), []byte("\n"))
	if sandboxRoot != "" {
		value = bytes.ReplaceAll(value, []byte(sandboxRoot), []byte("<SANDBOX>"))
	}
	return value
}

func digestBytes(value []byte) string {
	digest := sha256.Sum256(value)
	return hex.EncodeToString(digest[:])
}

func digestValue(value any) string {
	return digestBytes([]byte(fmt.Sprintf("%#v", value)))
}
