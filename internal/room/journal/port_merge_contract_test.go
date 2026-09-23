package journal

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/room/event"
)

type mergeVector struct {
	ID       string                    `json:"id"`
	Segments map[string][]*event.Event `json:"segments"`
	Ordered  []string                  `json:"ordered"`
	Bodies   []string                  `json:"bodies,omitempty"`
}

type mergeContract struct {
	SchemaVersion int           `json:"schema_version"`
	SourceHash    string        `json:"source_hash"`
	Cases         []mergeVector `json:"cases"`
}

func TestPortRoomMergeContract(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("source location unavailable")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "../../.."))
	production, err := os.ReadFile(filepath.Join(root, "internal/room/journal/merge.go"))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(production)
	makeEvent := func(id, author, ts string, lamport, seq uint64) *event.Event {
		return &event.Event{V: 1, ID: id, Author: author, TS: ts, Lamport: lamport, Seq: seq, Body: json.RawMessage(`{}`)}
	}
	stamp := "2026-09-23T10:00:00.000Z"
	cases := []mergeVector{
		{ID: "empty", Segments: map[string][]*event.Event{}, Ordered: []string{}},
		{ID: "five-order-keys", Segments: map[string][]*event.Event{
			"z": {
				makeEvent("lamport-later", "z", stamp, 3, 1),
				makeEvent("id-z", "z", stamp, 2, 2),
				makeEvent("id-a", "z", stamp, 2, 2),
				makeEvent("seq-earlier", "z", stamp, 2, 1),
				makeEvent("timestamp-earlier", "z", "2026-09-23T09:00:00.000Z", 2, 1),
			},
			"a": {
				makeEvent("author-earlier", "a", stamp, 2, 1),
				makeEvent("lamport-earlier", "a", stamp, 1, 1),
			},
		}, Ordered: []string{"lamport-earlier", "timestamp-earlier", "author-earlier", "seq-earlier", "id-a", "id-z", "lamport-later"}},
		{ID: "stable-identical-keys", Segments: map[string][]*event.Event{
			"a": {makeEvent("same", "a", stamp, 1, 1), makeEvent("same", "a", stamp, 1, 1)},
		}, Ordered: []string{"same", "same"}, Bodies: []string{`{"position":"first"}`, `{"position":"second"}`}},
	}
	cases[2].Segments["a"][0].Body = json.RawMessage(cases[2].Bodies[0])
	cases[2].Segments["a"][1].Body = json.RawMessage(cases[2].Bodies[1])
	for i := range cases {
		merged := Merge(cases[i].Segments)
		if len(merged) != len(cases[i].Ordered) {
			t.Fatalf("%s: expected %d events, got %d", cases[i].ID, len(cases[i].Ordered), len(merged))
		}
		for index, value := range merged {
			if value.ID != cases[i].Ordered[index] {
				t.Fatalf("%s: index %d: expected %s, got %s", cases[i].ID, index, cases[i].Ordered[index], value.ID)
			}
			if len(cases[i].Bodies) > 0 && string(value.Body) != cases[i].Bodies[index] {
				t.Fatalf("%s: index %d: stable body order changed", cases[i].ID, index)
			}
		}
	}
	fixture := mergeContract{SchemaVersion: 1, SourceHash: hex.EncodeToString(sum[:]), Cases: cases}
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join(root, "testdata/port/room/merge.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(path, encoded, 0o644); err != nil { //nolint:gosec // fixed path under repository root
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != string(encoded) {
		t.Fatal("Go merge fixture drift: regenerate explicitly")
	}
}
