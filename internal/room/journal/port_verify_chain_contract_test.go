package journal

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/room/event"
)

type verifyChainCase struct {
	ID      string `json:"id"`
	Author  string `json:"author"`
	File    bool   `json:"file"`
	Content string `json:"content"`
	Code    string `json:"code"`
	Error   string `json:"error"`
}

type verifyChainFixture struct {
	SchemaVersion int               `json:"schema_version"`
	SourceHashes  map[string]string `json:"source_hashes"`
	Cases         []verifyChainCase `json:"cases"`
}

func TestPortRoomVerifyChainContract(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("source location unavailable")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "../../.."))
	fixture := verifyChainFixture{SchemaVersion: 1, SourceHashes: make(map[string]string)}
	for _, rel := range []string{
		"internal/room/journal/journal.go",
		"internal/room/journal/verifier.go",
		"internal/room/journal/port_verify_chain_contract_test.go",
	} {
		//nolint:gosec // rel is one of the fixed Go source paths above, not fixture input.
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		fixture.SourceHashes[rel] = hex.EncodeToString(sum[:])
	}
	line := func(id string, seq uint64, prev string) string {
		t.Helper()
		encoded, err := (&event.Event{
			V: 1, ID: id, Room: "r", Author: "alice", Seq: seq,
			Prev: prev, Lamport: seq, TS: "2026-09-23T10:00:00.000Z",
			Kind: event.KindNotePosted, Body: json.RawMessage(`{"text":"signed bytes are not checked here"}`),
		}).MarshalJSONLine()
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
	first := line("first", 1, zeroHash)
	firstHash := ComputeLineHash(bytes.TrimSuffix([]byte(first), []byte("\n")))
	second := line("second", 2, firstHash)
	cases := []verifyChainCase{
		{ID: "missing", Author: "alice", Code: "ok"},
		{ID: "empty", Author: "alice", File: true, Code: "ok"},
		{ID: "blank-only", Author: "alice", File: true, Content: "\r\n \t\n", Code: "ok"},
		{ID: "valid-two", Author: "alice", File: true, Content: first + second, Code: "ok"},
		{ID: "valid-crlf-and-blank", Author: "alice", File: true, Content: strings.TrimSuffix(first, "\n") + "\r\n \n" + strings.TrimSuffix(second, "\n"), Code: "ok"},
		{ID: "wrong-seq", Author: "alice", File: true, Content: line("wrong-seq", 2, zeroHash), Code: "seq_mismatch"},
		{ID: "wrong-prev", Author: "alice", File: true, Content: line("wrong-prev", 1, "sha256:wrong"), Code: "chain_broken"},
		{ID: "seq-before-prev", Author: "alice", File: true, Content: line("both", 2, "sha256:wrong"), Code: "seq_mismatch"},
	}
	for i := range cases {
		item := &cases[i]
		journalDir := filepath.Join(t.TempDir(), "journal")
		if item.File {
			if err := os.MkdirAll(journalDir, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(journalDir, item.Author+".jsonl"), []byte(item.Content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		err := New(journalDir).VerifyChain(item.Author)
		observed := "ok"
		switch {
		case errors.Is(err, ErrSeqMismatch):
			observed = "seq_mismatch"
		case errors.Is(err, ErrChainBroken):
			observed = "chain_broken"
		case err != nil:
			t.Fatalf("%s: unexpected oracle error: %v", item.ID, err)
		}
		if observed != item.Code {
			t.Fatalf("%s: expected %s, got %s: %v", item.ID, item.Code, observed, err)
		}
		if err != nil {
			item.Error = err.Error()
		}
	}
	fixture.Cases = cases
	if len(fixture.Cases) != 8 {
		t.Fatal("verify-chain case inventory changed")
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, "testdata/port/room/verify-chain.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, data) {
		t.Fatal("Go verify-chain fixture drift: regenerate explicitly")
	}
}
