package compose

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

type portProbeObservation struct {
	Available bool   `json:"available"`
	Version   string `json:"version"`
	Argv      string `json:"argv"`
}
type portProbeCase struct {
	ID           string                 `json:"id"`
	Output       string                 `json:"output"`
	Exit         int                    `json:"exit"`
	Missing      bool                   `json:"missing"`
	Timeout      bool                   `json:"timeout"`
	Replace      string                 `json:"replace"`
	Observations []portProbeObservation `json:"observations"`
}

// Records actual HasTool, including the absence cache and explicit ResetCache.
// All discovered tools are owned shell doubles; no installed sibling executes.
func TestPortComposeProbeContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Unix shell doubles; native Windows discovery remains open")
	}
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	commit := fixtureoracle.Current().Commit
	//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
	source, err := os.ReadFile(filepath.Join(repo, "internal/compose/compose.go"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(source)
	cases := []portProbeCase{
		{ID: "missing", Missing: true},
		{ID: "version", Output: `{"schema_version":1,"version":"1.2.3"}`},
		{ID: "schema-not-enforced", Output: `{"schema_version":99,"version":"future"}`},
		{ID: "no-schema", Output: `{"version":"dev"}`},
		{ID: "empty-version", Output: `{"version":""}`},
		{ID: "absent-version", Output: `{}`},
		{ID: "null-document", Output: `null`},
		{ID: "null-version", Output: `{"version":null}`},
		{ID: "uppercase-keys", Output: `{"SCHEMA_VERSION":1,"VeRsIoN":"case-folded"}`},
		{ID: "duplicate-version-null-keeps-value", Output: `{"version":"first","version":null}`},
		{ID: "duplicate-version-last-value", Output: `{"version":"first","Version":"last"}`},
		{ID: "wrong-schema-type", Output: `{"schema_version":"1","version":"ignored"}`},
		{ID: "wrong-version-type", Output: `{"version":123}`},
		{ID: "malformed", Output: `{broken}`},
		{ID: "empty-output"},
		{ID: "nonzero-exit", Output: `{"version":"ignored"}`, Exit: 7},
		{ID: "timeout", Timeout: true},
		{ID: "cache-success-until-reset", Output: `{"version":"old"}`, Replace: `{"version":"new"}`},
		{ID: "cache-missing-until-reset", Missing: true, Replace: `{"version":"installed"}`},
	}
	original := ResolveFunc
	defer func() { ResolveFunc = original; ResetCache() }()
	ResolveFunc = Resolve
	for i := range cases {
		c := &cases[i]
		root := t.TempDir()
		bin := filepath.Join(root, "bin")
		if err := os.MkdirAll(bin, 0o750); err != nil {
			t.Fatal(err)
		}
		transcript := filepath.Join(root, "argv")
		t.Setenv("HOME", filepath.Join(root, "home"))
		t.Setenv("USERPROFILE", filepath.Join(root, "home"))
		t.Setenv("SYMAIRA_BIN", bin)
		t.Setenv("PATH", "")
		scriptPath := filepath.Join(bin, "symprobe")
		write := func(output string, exit int, hang bool) {
			script := "#!/bin/sh\nprintf '%s\\n' \"$@\" >> '" + transcript + "'\n"
			if hang {
				script += "while :; do :; done\n"
			} else {
				script += "printf '%s' '" + output + "'\nexit " + fmt.Sprint(exit) + "\n"
			}
			//nolint:gosec // test helper must be executable; bytes are the fixed fixture script or this test executable.
			if err := os.WriteFile(scriptPath, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
		}
		if !c.Missing {
			write(c.Output, c.Exit, c.Timeout)
		}
		ResetCache()
		observe := func() {
			available, version := HasTool("symprobe")
			//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
			argv, err := os.ReadFile(transcript)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			c.Observations = append(c.Observations, portProbeObservation{available, version, string(argv)})
		}
		observe()
		if c.Replace != "" {
			write(c.Replace, 0, false)
		}
		observe() // always cached: a successful probe is not repeated either.
		if c.Replace != "" {
			ResetCache()
			observe()
		}
	}
	data, err := json.MarshalIndent(struct {
		SchemaVersion int               `json:"schema_version"`
		OracleCommit  string            `json:"oracle_commit"`
		Platform      string            `json:"platform"`
		SourceHashes  map[string]string `json:"source_hashes"`
		Cases         []portProbeCase   `json:"cases"`
	}{1, commit, "unix", map[string]string{"internal/compose/compose.go": hex.EncodeToString(sum[:])}, cases}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(repo, "testdata/port/ai/compose-probe.json")
	if override := os.Getenv("PORT_FIXTURE_PATH"); override != "" {
		path = override
	}
	if os.Getenv("PORT_GENERATE") == "1" {
		//nolint:gosec // path is the repository-local fixture output selected by this test.
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	} else {
		//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
		expected, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, expected) {
			t.Fatal("compose probe fixture drift; regenerate deliberately")
		}
	}
	t.Logf("executed %d real Go HasTool/cache cases", len(cases))
}
