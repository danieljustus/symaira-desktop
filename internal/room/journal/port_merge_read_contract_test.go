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

	"github.com/danieljustus/symaira-desktop/internal/room/event"
)

const mergeReadFixturePath = "../../../testdata/port/room/merge-read.json"

type mergeReadFile struct {
	Name          string `json:"name"`
	Content       string `json:"content"`
	RepeatBytes   int    `json:"repeat_bytes,omitempty"`
	RepeatByte    string `json:"repeat_byte,omitempty"`
	RepeatNewline bool   `json:"repeat_newline,omitempty"`
}

type mergeReadCase struct {
	ID             string          `json:"id"`
	Files          []mergeReadFile `json:"files"`
	OrderedIDs     []string        `json:"ordered_ids"`
	OrderedMarkers []string        `json:"ordered_markers"`
	ErrorClass     string          `json:"error_class,omitempty"`
	ErrorAuthor    string          `json:"error_author,omitempty"`
	wantIDs        []string        `json:"-"`
	wantMarkers    []string        `json:"-"`
}

type mergeReadFixture struct {
	SchemaVersion int             `json:"schema_version"`
	SourceHash    string          `json:"source_hash"`
	Cases         []mergeReadCase `json:"cases"`
}

func TestPortRoomMergeReadContract(t *testing.T) {
	_, sourceFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("source location unavailable")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(sourceFile), "../../.."))
	mergeSource, err := os.ReadFile(filepath.Join(root, "internal/room/journal/merge.go"))
	if err != nil {
		t.Fatal(err)
	}
	readerSource, err := os.ReadFile(filepath.Join(root, "internal/room/journal/journal.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := append(append([]byte(nil), mergeSource...), readerSource...)
	sourceDigest := sha256.Sum256(source)

	stamp := "2026-09-23T10:00:00.000Z"
	makeEvent := func(id, author, ts string, lamport, seq uint64, marker string) *event.Event {
		return &event.Event{V: 1, ID: id, Author: author, TS: ts, Lamport: lamport, Seq: seq, Kind: event.KindNotePosted, Body: json.RawMessage(`{"marker":"` + marker + `"}`)}
	}
	line := func(ev *event.Event) string {
		t.Helper()
		encoded, err := ev.MarshalJSONLine()
		if err != nil {
			t.Fatal(err)
		}
		return string(encoded)
	}
	cases := []mergeReadCase{
		{ID: "disk-merge-lamport-ties", Files: []mergeReadFile{
			{Name: "a.jsonl", Content: line(makeEvent("later-lamport", "z", stamp, 3, 1, "later")) + line(makeEvent("same", "z", stamp, 2, 2, "stable-first")) + line(makeEvent("same", "z", stamp, 2, 2, "stable-second")) + line(makeEvent("id-z", "z", stamp, 2, 2, "id-z")) + line(makeEvent("seq-earlier", "z", stamp, 2, 1, "seq-earlier"))},
			{Name: "z.jsonl", Content: line(makeEvent("time-earlier", "z", "2026-09-23T09:00:00.000Z", 2, 1, "time-earlier")) + line(makeEvent("author-earlier", "a", stamp, 2, 1, "author-earlier")) + line(makeEvent("id-a", "z", stamp, 2, 2, "id-a")) + line(makeEvent("lamport-first", "z", stamp, 1, 1, "lamport-first"))},
		}, wantIDs: []string{"lamport-first", "time-earlier", "author-earlier", "seq-earlier", "id-a", "id-z", "same", "same", "later-lamport"}, wantMarkers: []string{"lamport-first", "time-earlier", "author-earlier", "seq-earlier", "id-a", "id-z", "stable-first", "stable-second", "later"}},
		{ID: "malformed-event-aborts-merge", Files: []mergeReadFile{{Name: "broken.jsonl", Content: line(makeEvent("prefix", "broken", stamp, 1, 1, "prefix")) + "{\"v\":\n"}}, OrderedIDs: []string{}, OrderedMarkers: []string{}, ErrorClass: "malformed_event", ErrorAuthor: "broken"},
		{ID: "scanner-overflow-keeps-prefix", Files: []mergeReadFile{{Name: "overflow.jsonl", Content: line(makeEvent("prefix", "overflow", stamp, 1, 1, "prefix")), RepeatBytes: 64 * 1024, RepeatByte: "x", RepeatNewline: true}}, wantIDs: []string{"prefix"}, wantMarkers: []string{"prefix"}},
	}
	doc := mergeReadFixture{SchemaVersion: 1, SourceHash: hex.EncodeToString(sourceDigest[:]), Cases: cases}
	for i := range doc.Cases {
		roomDir := t.TempDir()
		journalDir := filepath.Join(roomDir, "journal")
		if err := os.MkdirAll(journalDir, 0o700); err != nil {
			t.Fatal(err)
		}
		for _, file := range doc.Cases[i].Files {
			content := []byte(file.Content)
			if file.RepeatBytes > 0 {
				content = append(content, bytes.Repeat([]byte(file.RepeatByte), file.RepeatBytes)...)
				if file.RepeatNewline {
					content = append(content, '\n')
				}
			}
			if err := os.WriteFile(filepath.Join(journalDir, file.Name), content, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		merged, err := New(journalDir).MergeAll()
		caseRow := &doc.Cases[i]
		if caseRow.ErrorClass != "" {
			if err == nil {
				t.Fatalf("%s: expected %s error", caseRow.ID, caseRow.ErrorClass)
			}
			if !strings.Contains(err.Error(), "read segment "+caseRow.ErrorAuthor+": unmarshal line:") {
				t.Fatalf("%s: unexpected Go error: %v", caseRow.ID, err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("%s: MergeAll: %v", caseRow.ID, err)
		}
		for _, ev := range merged {
			caseRow.OrderedIDs = append(caseRow.OrderedIDs, ev.ID)
			var body struct {
				Marker string `json:"marker"`
			}
			if err := json.Unmarshal(ev.Body, &body); err != nil {
				t.Fatal(err)
			}
			caseRow.OrderedMarkers = append(caseRow.OrderedMarkers, body.Marker)
		}
		if !equalStrings(caseRow.OrderedIDs, caseRow.wantIDs) {
			t.Fatalf("%s: Go merge order %v, expected %v", caseRow.ID, caseRow.OrderedIDs, caseRow.wantIDs)
		}
		if !equalStrings(caseRow.OrderedMarkers, caseRow.wantMarkers) {
			t.Fatalf("%s: Go stable tie order %v, expected %v", caseRow.ID, caseRow.OrderedMarkers, caseRow.wantMarkers)
		}
	}
	encoded, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join(root, "testdata/port/room/merge-read.json")
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
		t.Fatal("Go MergeAll fixture changed; regenerate explicitly with PORT_GENERATE=1")
	}
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return false
		}
	}
	return true
}
