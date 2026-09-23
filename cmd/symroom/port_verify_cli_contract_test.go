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

type verifyCLIContract struct {
	SchemaVersion int               `json:"schema_version"`
	SourceHashes  map[string]string `json:"source_hashes"`
	Cases         []verifyCLICase   `json:"cases"`
}

type verifyCLICase struct {
	Name         string   `json:"name"`
	JournalCase  string   `json:"journal_case"`
	Args         []string `json:"args"`
	ExitCode     int      `json:"exit_code"`
	Stdout       string   `json:"stdout"`
	StderrPrefix string   `json:"stderr_prefix"`
}

func TestPortVerifyCLIContract(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("source location unavailable")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "../.."))
	fixture := verifyCLIContract{SchemaVersion: 1, SourceHashes: map[string]string{}}
	for _, rel := range []string{"cmd/symroom/main.go", "cmd/symroom/cmd_verify.go", "internal/room/journal/verifier.go"} {
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
	journalCases := map[string]map[string]string{}
	for _, row := range journalFixture.Cases {
		journalCases[row.Name] = row.Files
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
	for _, vector := range []verifyCLICase{
		{Name: "empty", JournalCase: "empty", Args: []string{"verify"}},
		{Name: "valid-text", JournalCase: "valid", Args: []string{"verify"}},
		{Name: "valid-json", JournalCase: "valid", Args: []string{"verify", "--json"}},
		{Name: "tampered-text", JournalCase: "tampered-signature", Args: []string{"verify"}},
		{Name: "agent-json", JournalCase: "agent-approval", Args: []string{"verify", "--json=true"}},
		{Name: "malformed", JournalCase: "malformed", Args: []string{"verify"}},
		{Name: "help", JournalCase: "valid", Args: []string{"verify", "--help"}},
		{Name: "bad-flag", JournalCase: "valid", Args: []string{"verify", "--bogus"}},
		{Name: "bad-bool", JournalCase: "valid", Args: []string{"verify", "--json=maybe"}},
		{Name: "positional-stops-flags", JournalCase: "valid", Args: []string{"verify", "extra", "--json"}},
	} {
		room := t.TempDir()
		journalDir := filepath.Join(room, "journal")
		if err := os.MkdirAll(journalDir, 0o700); err != nil {
			t.Fatal(err)
		}
		files, ok := journalCases[vector.JournalCase]
		if !ok {
			t.Fatalf("unknown journal case %s", vector.JournalCase)
		}
		for name, content := range files {
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
			const prefix = "Error verifying journal: read all segments: read segment "
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
	path := filepath.Join(root, "testdata/port/room/verify-cli.json")
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
		t.Fatal("Go Room verify CLI fixture changed; regenerate explicitly with PORT_GENERATE=1")
	}
}
