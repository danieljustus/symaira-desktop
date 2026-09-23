package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type logCLIContract struct {
	SchemaVersion int               `json:"schema_version"`
	SourceHashes  map[string]string `json:"source_hashes"`
	Cases         []logCLICase      `json:"cases"`
}
type logCLICase struct {
	Name         string   `json:"name"`
	JournalCase  string   `json:"journal_case"`
	Args         []string `json:"args"`
	ExitCode     int      `json:"exit_code"`
	Stdout       string   `json:"stdout"`
	StderrPrefix string   `json:"stderr_prefix"`
}

func TestPortLogCLIContract(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("source location unavailable")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "../.."))
	fixture := logCLIContract{SchemaVersion: 1, SourceHashes: map[string]string{}}
	for _, rel := range []string{"cmd/symroom/main.go", "cmd/symroom/cmd_log.go", "internal/room/journal/log.go"} {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(data)
		fixture.SourceHashes[rel] = hex.EncodeToString(hash[:])
	}
	var journalFixture struct {
		Cases []struct {
			Name  string            `json:"name"`
			Files map[string]string `json:"files"`
		} `json:"cases"`
	}
	data, err := os.ReadFile(filepath.Join(root, "testdata/port/room/verify.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &journalFixture); err != nil {
		t.Fatal(err)
	}
	journals := map[string]map[string]string{}
	for _, row := range journalFixture.Cases {
		journals[row.Name] = row.Files
	}
	goBinary := filepath.Join(t.TempDir(), "symroom-go")
	if runtime.GOOS == "windows" {
		goBinary += ".exe"
	}
	build := exec.Command("go", "build", "-o", goBinary, "./cmd/symroom")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Go symroom: %v\n%s", err, output)
	}
	for _, vector := range []logCLICase{
		{Name: "empty", JournalCase: "empty", Args: []string{"log"}},
		{Name: "valid-human", JournalCase: "valid", Args: []string{"log"}},
		{Name: "valid-json-kind", JournalCase: "valid", Args: []string{"log", "--json", "--kind", "note.posted"}},
		{Name: "run-limit", JournalCase: "valid", Args: []string{"log", "--run=run_1", "--limit=1"}},
		{Name: "since-until", JournalCase: "valid", Args: []string{"log", "--since", "2026-09-23T10:00:00.000Z", "--until", "2026-09-23T10:00:00.000Z"}},
		{Name: "tampered-warning", JournalCase: "tampered-signature", Args: []string{"log"}},
		{Name: "unknown-author-kept", JournalCase: "unknown-author", Args: []string{"log", "--json"}},
		{Name: "malformed", JournalCase: "malformed", Args: []string{"log"}},
		{Name: "help", JournalCase: "valid", Args: []string{"log", "--help"}},
		{Name: "bad-flag", JournalCase: "valid", Args: []string{"log", "--bogus"}},
		{Name: "bad-limit", JournalCase: "valid", Args: []string{"log", "--limit=bad"}},
		{Name: "missing-limit", JournalCase: "valid", Args: []string{"log", "--limit"}},
		{Name: "positional-stops-flags", JournalCase: "valid", Args: []string{"log", "extra", "--kind", "note.posted"}},
	} {
		room := t.TempDir()
		journalDir := filepath.Join(room, "journal")
		if err := os.MkdirAll(journalDir, 0o700); err != nil {
			t.Fatal(err)
		}
		for name, content := range journals[vector.JournalCase] {
			if err := os.WriteFile(filepath.Join(journalDir, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		cmd := exec.Command(goBinary, vector.Args...)
		cmd.Env = append(os.Environ(), "SYMROOM_ROOM_DIR="+room)
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		err := cmd.Run()
		if err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				t.Fatal(err)
			}
		}
		vector.ExitCode = cmd.ProcessState.ExitCode()
		vector.Stdout = stdout.String()
		vector.StderrPrefix = stderr.String()
		if vector.Name == "malformed" {
			const prefix = "Error querying log: verify error: read all segments: read segment "
			if !strings.HasPrefix(vector.StderrPrefix, prefix) || !strings.Contains(vector.StderrPrefix, ": unmarshal line:") {
				t.Fatalf("unexpected malformed error: %s", vector.StderrPrefix)
			}
			vector.StderrPrefix = strings.SplitAfter(vector.StderrPrefix, ": unmarshal line:")[0]
		}
		fixture.Cases = append(fixture.Cases, vector)
	}
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join(root, "testdata/port/room/log-cli.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, want) {
		t.Fatal("Go Room log CLI fixture changed; regenerate explicitly with PORT_GENERATE=1")
	}
}
