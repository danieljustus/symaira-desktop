package journal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

type logContract struct {
	SchemaVersion int               `json:"schema_version"`
	SourceHashes  map[string]string `json:"source_hashes"`
	Cases         []logCase         `json:"cases"`
}

type logCase struct {
	Name         string    `json:"name"`
	JournalCase  string    `json:"journal_case"`
	Filter       LogFilter `json:"filter"`
	Events       []string  `json:"events"`
	Human        []string  `json:"human"`
	InvalidCount int       `json:"invalid_count"`
	ErrorPrefix  string    `json:"error_prefix,omitempty"`
}

func TestPortRoomLogContract(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("source location unavailable")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "../../.."))
	fixture := logContract{SchemaVersion: 1, SourceHashes: map[string]string{}}
	for _, rel := range []string{"internal/room/journal/log.go", "internal/room/journal/verifier.go", "internal/room/journal/merge.go"} {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(data)
		fixture.SourceHashes[rel] = hex.EncodeToString(hash[:])
	}
	var journalFixture verifyContract
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
	owner := verifyFixtureIdentity("owner")
	for _, vector := range []logCase{
		{Name: "empty", JournalCase: "empty"},
		{Name: "valid-all", JournalCase: "valid"},
		{Name: "kind", JournalCase: "valid", Filter: LogFilter{Kind: "note.posted"}},
		{Name: "author-uppercase", JournalCase: "valid", Filter: LogFilter{Author: strings.ToUpper(owner.MemberID)}},
		{Name: "since", JournalCase: "valid", Filter: LogFilter{Since: "2026-09-23T10:00:00.001Z"}},
		{Name: "until", JournalCase: "valid", Filter: LogFilter{Until: "2026-09-23T10:00:00.000Z"}},
		{Name: "limit", JournalCase: "valid", Filter: LogFilter{Limit: 1}},
		{Name: "negative-limit", JournalCase: "valid", Filter: LogFilter{Limit: -1}},
		{Name: "run-match", JournalCase: "valid", Filter: LogFilter{Run: "run_1"}},
		{Name: "run-missing", JournalCase: "valid", Filter: LogFilter{Run: "other"}},
		{Name: "tampered-omitted", JournalCase: "tampered-signature"},
		{Name: "unknown-author-kept", JournalCase: "unknown-author"},
		{Name: "malformed", JournalCase: "malformed"},
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
		res, err := New(journalDir).QueryLog(vector.Filter)
		if err != nil {
			const prefix = "verify error: read all segments: read segment "
			if !strings.HasPrefix(err.Error(), prefix) || !strings.Contains(err.Error(), ": unmarshal line:") {
				t.Fatalf("unexpected query error: %v", err)
			}
			vector.ErrorPrefix = strings.SplitAfter(err.Error(), ": unmarshal line:")[0]
		} else {
			vector.InvalidCount = res.InvalidCount
			for _, e := range res.Events {
				line, err := e.MarshalJSONLine()
				if err != nil {
					t.Fatal(err)
				}
				vector.Events = append(vector.Events, string(line))
				vector.Human = append(vector.Human, FormatEventHuman(e))
			}
		}
		fixture.Cases = append(fixture.Cases, vector)
	}
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join(root, "testdata/port/room/log.json")
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
		t.Fatal("Go Room log fixture changed; regenerate explicitly with PORT_GENERATE=1")
	}
}
