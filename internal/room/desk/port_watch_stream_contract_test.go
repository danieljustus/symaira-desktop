package desk

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

type watchStreamCase struct {
	Name        string            `json:"name"`
	Input       string            `json:"input"`
	InputHex    string            `json:"input_hex,omitempty"`
	RepeatBytes int               `json:"repeat_bytes,omitempty"`
	StopAfter   int               `json:"stop_after,omitempty"`
	Cancel      bool              `json:"cancel,omitempty"`
	Events      []EventStreamItem `json:"events"`
	Error       string            `json:"error,omitempty"`
}

type watchStreamFixture struct {
	SchemaVersion int               `json:"schema_version"`
	SourceHash    string            `json:"source_hash"`
	Cases         []watchStreamCase `json:"cases"`
}

func TestPortWatchStreamContract(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("source path unavailable")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "../../.."))
	goSource, err := os.ReadFile(filepath.Join(filepath.Dir(source), "watch.go"))
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(goSource)
	fixture := watchStreamFixture{SchemaVersion: 1, SourceHash: hex.EncodeToString(hash[:]), Cases: []watchStreamCase{
		{Name: "mixed-lines", Input: "\n{\"event\":\"file_changed\",\"path\":\"docs/a.md\"}\nnot-json\n{\"event\":\"file_removed\",\"path\":\"\"}\n{\"event\":\"file_added\",\"path\":\"ß.md\",\"extra\":1}\r\n"},
		{Name: "scanner-overflow-keeps-prefix", Input: "{\"event\":\"first\",\"path\":\"a\"}\n", RepeatBytes: 65536},
		{Name: "handler-error-stops", Input: "{\"event\":\"first\",\"path\":\"a\"}\n{\"event\":\"second\",\"path\":\"b\"}\n", StopAfter: 1},
		{Name: "cancel-before-first", Input: "{\"event\":\"first\",\"path\":\"a\"}\n", Cancel: true},
		{Name: "invalid-utf8-in-path", InputHex: hex.EncodeToString([]byte("{\"event\":\"file_added\",\"path\":\"bad\xff.md\"}\n"))},
		{Name: "unpaired-surrogate-in-path", Input: "{\"event\":\"file_added\",\"path\":\"bad\\ud800.md\"}\n"},
		{Name: "paired-surrogates-in-path", Input: "{\"event\":\"file_added\",\"path\":\"astral-\\ud83d\\ude00.md\"}\n"},
		{Name: "escaped-surrogate-text-in-path", Input: "{\"event\":\"file_added\",\"path\":\"literal\\\\uD800.md\"}\n"},
	}}
	for i := range fixture.Cases {
		row := &fixture.Cases[i]
		input := []byte(row.Input)
		if row.InputHex != "" {
			input, err = hex.DecodeString(row.InputHex)
			if err != nil {
				t.Fatal(err)
			}
		}
		input = append(input, bytes.Repeat([]byte("x"), row.RepeatBytes)...)
		if row.RepeatBytes > 0 {
			input = append(input, '\n')
		}
		ctx, cancel := context.WithCancel(context.Background())
		if row.Cancel {
			cancel()
		}
		row.Events = []EventStreamItem{}
		err := WatchStream(ctx, bytes.NewReader(input), func(item *EventStreamItem) error {
			row.Events = append(row.Events, *item)
			if row.StopAfter > 0 && len(row.Events) == row.StopAfter {
				return errors.New("handler stopped")
			}
			return nil
		})
		cancel()
		if err != nil {
			row.Error = err.Error()
		}
	}
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join(root, "testdata/port/room/watch-stream.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded) {
		t.Fatal("Go watch stream fixture changed; regenerate explicitly with PORT_GENERATE=1")
	}
}
