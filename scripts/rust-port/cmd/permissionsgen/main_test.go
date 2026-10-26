package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func committedFixture(t *testing.T) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "..", defaultOutput))
	if err != nil {
		t.Fatalf("read committed fixture: %v", err)
	}
	return data
}

// windowsCapture mimics what a Windows host records: no UnixOnly scenarios and
// Go's synthetic 0666/0777 modes instead of the POSIX bits.
func windowsCapture(t *testing.T, committed []byte, mutate func(*document)) []byte {
	t.Helper()
	doc, err := decodeDocument(committed)
	if err != nil {
		t.Fatalf("decode committed fixture: %v", err)
	}
	var kept []scenario
	for _, s := range doc.Scenarios {
		if s.UnixOnly {
			continue
		}
		files := map[string]file{}
		for name, f := range s.Final.Files {
			f.Mode = 0o666
			files[name] = f
		}
		s.Final.Files = files
		if s.Final.ConfigDirMode != 0 {
			s.Final.ConfigDirMode = 0o777
		}
		kept = append(kept, s)
	}
	doc.Scenarios = kept
	if mutate != nil {
		mutate(&doc)
	}
	out, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return append(out, '\n')
}

func TestWindowsCheckIgnoresOnlyPermissionBitsAndUnixOnlyScenarios(t *testing.T) {
	committed := committedFixture(t)
	if err := compareFixtureForPlatform(committed, windowsCapture(t, committed, nil), "windows"); err != nil {
		t.Fatalf("windows-shaped capture must match the committed fixture: %v", err)
	}
}

func TestWindowsCheckStillDetectsRealDrift(t *testing.T) {
	committed := committedFixture(t)
	mutations := map[string]func(*document){
		"final bytes": func(d *document) {
			for name, f := range d.Scenarios[0].Final.Files {
				f.Content += "x"
				d.Scenarios[0].Final.Files[name] = f
				return
			}
			d.Scenarios[0].Final.Files["extra.json"] = file{Content: "x"}
		},
		"operation result": func(d *document) {
			for _, s := range d.Scenarios {
				for _, op := range s.Ops {
					if result, ok := op["result"].(map[string]any); ok {
						result["negative_control"] = true
						return
					}
				}
			}
		},
		"dropped scenario": func(d *document) { d.Scenarios = d.Scenarios[1:] },
		"oracle commit":    func(d *document) { d.Oracle.Commit = strings.Repeat("0", 40) },
	}
	for name, mutate := range mutations {
		if err := compareFixtureForPlatform(committed, windowsCapture(t, committed, mutate), "windows"); err == nil {
			t.Errorf("%s: windows comparison accepted a real drift", name)
		}
	}
}

func TestPosixCheckRemainsByteExact(t *testing.T) {
	committed := committedFixture(t)
	if err := compareFixtureForPlatform(committed, committed, "linux"); err != nil {
		t.Fatalf("identical bytes must pass: %v", err)
	}
	if err := compareFixtureForPlatform(committed, windowsCapture(t, committed, nil), "darwin"); err == nil {
		t.Fatal("a posix host must not accept a mode-stripped capture")
	}
}
