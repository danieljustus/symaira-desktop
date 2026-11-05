// Command diffrecord records the normalized observations of the real Go
// symdesk binary for a diffharness case suite, so a Rust test can replay the
// suite against the Rust binary without Go (RUST-019 cutover).
//
// Without --check it writes the recording; with --check it records again and
// fails if the committed file differs. Normalization is the one Compare uses
// (internal/diff Observe), never a second implementation.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	portdiff "github.com/danieljustus/symaira-desktop/scripts/rust-port/internal/diff"
)

// Recording is the committed document.
type Recording struct {
	SchemaVersion int             `json:"schema_version"`
	Oracle        portdiff.Oracle `json:"oracle"`
	Source        Source          `json:"source"`
	Stage         string          `json:"stage"`
	Cases         []RecordedCase  `json:"cases"`
}

// Source names the exact case file and tool chain the observations came from.
type Source struct {
	CasesFile   string `json:"cases_file"`
	CasesSHA256 string `json:"cases_sha256"`
	GoVersion   string `json:"go_version"`
	// BinaryVersion is the version string the recorded Go binary reports. The
	// Rust replay binary is built without SYMDESK_VERSION and reports the same.
	BinaryVersion string `json:"binary_version"`
	Platform      string `json:"platform"`
}

// RecordedCase is one case input plus the observation Go produced for it. The
// input is embedded so the recording is self-contained: a replay needs no
// other file and a mutated input cannot silently desynchronize from the
// recorded output.
type RecordedCase struct {
	ID          string               `json:"id"`
	Case        portdiff.Case        `json:"case"`
	Observation portdiff.Observation `json:"observation"`
}

func main() {
	pkg := flag.String("package", "./cmd/symdesk", "Go main package of the oracle, built from the working directory")
	casesPath := flag.String("cases", "", "path to the case suite")
	stage := flag.String("stage", "", "record only cases assigned to this migration stage")
	output := flag.String("output", "", "recording path")
	binaryVersion := flag.String("binary-version", "devel", "value for -X main.version; the Rust replay binary reports the same string")
	check := flag.Bool("check", false, "re-record and fail when --output differs")
	flag.Parse()
	if *casesPath == "" || *stage == "" || *output == "" {
		fatal("--cases, --stage and --output are required")
	}
	workDir, err := os.MkdirTemp("", "symdesk-diffrecord-")
	if err != nil {
		fatal("create build directory: %v", err)
	}
	defer func() { _ = os.RemoveAll(workDir) }()
	binary := filepath.Join(workDir, "symdesk-go")
	build := exec.Command("go", "build", "-ldflags=-X main.version="+*binaryVersion, "-o", binary, *pkg) // #nosec G204 -- flags are developer-supplied
	build.Stdout, build.Stderr = os.Stderr, os.Stderr
	if err := build.Run(); err != nil {
		fatal("build the Go oracle %s: %v", *pkg, err)
	}

	raw, err := os.ReadFile(*casesPath)
	if err != nil {
		fatal("read cases: %v", err)
	}
	var suite portdiff.Suite
	if err := json.Unmarshal(raw, &suite); err != nil {
		fatal("decode cases: %v", err)
	}
	if suite.SchemaVersion != 1 || len(suite.Cases) == 0 {
		fatal("unsupported or empty case suite")
	}
	digest := sha256.Sum256(raw)
	recording := Recording{
		SchemaVersion: 1,
		Oracle:        suite.Oracle,
		Stage:         *stage,
		Source: Source{
			CasesFile:     filepath.ToSlash(*casesPath),
			CasesSHA256:   hex.EncodeToString(digest[:]),
			GoVersion:     runtime.Version(),
			BinaryVersion: *binaryVersion,
			Platform:      runtime.GOOS,
		},
	}
	seen := map[string]bool{}
	for _, testCase := range suite.Cases {
		if testCase.ID == "" || seen[testCase.ID] {
			fatal("case IDs must be non-empty and unique: %q", testCase.ID)
		}
		seen[testCase.ID] = true
		if testCase.Stage != *stage {
			continue
		}
		if testCase.TargetBinary() != "symdesk" {
			fatal("case %q targets %q; only symdesk is recorded here", testCase.ID, testCase.TargetBinary())
		}
		result, err := portdiff.Run(binary, testCase)
		if err != nil {
			fatal("%s: run: %v", testCase.ID, err)
		}
		observation, err := portdiff.Observe(testCase, result)
		if err != nil {
			fatal("%s: %v", testCase.ID, err)
		}
		recording.Cases = append(recording.Cases, RecordedCase{ID: testCase.ID, Case: testCase, Observation: observation})
	}
	if len(recording.Cases) == 0 {
		fatal("no cases selected for stage %q", *stage)
	}

	var encoded bytes.Buffer
	encoder := json.NewEncoder(&encoded)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(recording); err != nil {
		fatal("encode recording: %v", err)
	}
	if *check {
		committed, err := os.ReadFile(*output)
		if err != nil {
			fatal("read committed recording: %v", err)
		}
		if !bytes.Equal(committed, encoded.Bytes()) {
			fatal("%s drifted from the live Go oracle; regenerate deliberately", *output)
		}
		fmt.Printf("PASS %s matches the live Go oracle (%d cases)\n", *output, len(recording.Cases))
		return
	}
	if err := os.WriteFile(*output, encoded.Bytes(), 0o644); err != nil { // #nosec G306 -- committed fixture
		fatal("write recording: %v", err)
	}
	fmt.Printf("recorded %d cases to %s\n", len(recording.Cases), *output)
}

func fatal(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "FAIL "+format+"\n", args...)
	os.Exit(1)
}
