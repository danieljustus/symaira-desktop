package diff

import (
	"encoding/base64"
	"errors"
	"fmt"
	"unicode/utf8"
)

// Observation is the normalized, language-neutral outcome of one case: exactly
// the fields Compare would have compared between two live implementations. A
// frozen replay compares a candidate's Observation to a recorded one instead.
type Observation struct {
	ExitCode int    `json:"exit_code"`
	Signal   string `json:"signal,omitempty"`
	// StdoutMode and StderrMode echo the comparison mode the stream was
	// normalized with. A stream compared in "ignore" mode is not recorded.
	StdoutMode string `json:"stdout_mode"`
	Stdout     string `json:"stdout,omitempty"`
	// StdoutBase64 replaces Stdout when the normalized bytes are not UTF-8.
	StdoutBase64 string `json:"stdout_base64,omitempty"`
	StderrMode   string `json:"stderr_mode"`
	Stderr       string `json:"stderr,omitempty"`
	StderrBase64 string `json:"stderr_base64,omitempty"`
	// Files is the whole sandbox manifest, recorded only for compare_files cases.
	Files []ManifestEntry `json:"files,omitempty"`
	// SidecarLayout is recorded only for compare_sidecar_layout cases.
	SidecarLayout []string `json:"sidecar_layout,omitempty"`
}

// Observe reduces a Result to the Observation Compare would have compared.
func Observe(testCase Case, result Result) (Observation, error) {
	if result.TimedOut {
		return Observation{}, errors.New("timed-out case cannot be recorded")
	}
	observation := Observation{
		ExitCode:   result.ExitCode,
		Signal:     result.Signal,
		StdoutMode: testCase.stdoutComparisonMode(),
		StderrMode: testCase.stderrComparisonMode(),
	}
	var err error
	if observation.Stdout, observation.StdoutBase64, err = observeStream("stdout", observation.StdoutMode, result.Stdout, result.SandboxRoot); err != nil {
		return Observation{}, err
	}
	if observation.Stderr, observation.StderrBase64, err = observeStream("stderr", observation.StderrMode, result.Stderr, result.SandboxRoot); err != nil {
		return Observation{}, err
	}
	if testCase.CompareFiles {
		observation.Files = result.Files
	}
	if testCase.CompareSidecarLayout {
		observation.SidecarLayout = sidecarLayout(result.Files)
		if len(observation.SidecarLayout) == 0 {
			return Observation{}, errors.New("sidecar layout is empty")
		}
	}
	return observation, nil
}

func observeStream(name, mode string, value []byte, sandboxRoot string) (text, encoded string, err error) {
	if mode == comparisonModeIgnore {
		return "", "", nil
	}
	normalized, err := normalizeStream(name, mode, value, sandboxRoot)
	if err != nil {
		return "", "", fmt.Errorf("observe: %w", err)
	}
	if utf8.Valid(normalized) {
		return string(normalized), "", nil
	}
	return "", base64.StdEncoding.EncodeToString(normalized), nil
}
