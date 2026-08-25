package annotate

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
)

// ValidationResult holds the outcome of validating a sidecar JSONL file.
type ValidationResult struct {
	Valid    bool     `json:"valid"`
	Lines    int      `json:"lines"`
	Errors   []string `json:"errors,omitempty"`
	Warnings []string `json:"warnings,omitempty"`
}

// Validate checks that a sidecar JSONL file is well-formed and each line
// contains required fields (span, snippet, value).
func Validate(path string) (*ValidationResult, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open sidecar: %w", err)
	}
	defer f.Close()

	result := &ValidationResult{Valid: true}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 1024*1024)

	for scanner.Scan() {
		line := scanner.Text()
		result.Lines++

		if line == "" {
			continue
		}

		var e Extraction
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			result.Valid = false
			result.Errors = append(result.Errors, fmt.Sprintf("line %d: invalid JSON: %v", result.Lines, err))
			continue
		}

		if e.Value == "" {
			result.Valid = false
			result.Errors = append(result.Errors, fmt.Sprintf("line %d: missing required field 'value'", result.Lines))
		}
		if e.Field == "" {
			result.Valid = false
			result.Errors = append(result.Errors, fmt.Sprintf("line %d: missing required field 'field'", result.Lines))
		}
		if e.Span == nil {
			result.Warnings = append(result.Warnings, fmt.Sprintf("line %d: missing span (extraction not grounded)", result.Lines))
		} else {
			if e.Span.Snippet == "" {
				result.Warnings = append(result.Warnings, fmt.Sprintf("line %d: span has empty snippet", result.Lines))
			}
			if e.Span.Start < 0 || e.Span.End <= e.Span.Start {
				result.Warnings = append(result.Warnings, fmt.Sprintf("line %d: span has invalid offsets (%d, %d)", result.Lines, e.Span.Start, e.Span.End))
			}
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("read sidecar: %w", err)
	}

	return result, nil
}
