package run

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/room/event"
)

const runProjectionFixture = "testdata/port/room/run-projection.json"

type runProjectionFixtureData struct {
	SchemaVersion  int               `json:"schema_version"`
	OracleRevision string            `json:"oracle_revision"`
	SourceHashes   map[string]string `json:"source_hashes"`
	Events         []*event.Event    `json:"events"`
	Records        []string          `json:"records"`
}

// TestPortRunProjectionContract freezes the pure ProjectRuns output.
// Set ROOM_PROJECTION_GENERATE=1 only when deliberately regenerating the Go oracle.
func TestPortRunProjectionContract(t *testing.T) {
	fixture := makeRunProjectionFixture(t)
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join("..", "..", "..", runProjectionFixture)
	if os.Getenv("ROOM_PROJECTION_GENERATE") == "1" || os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", runProjectionFixture)
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (set ROOM_PROJECTION_GENERATE=1 to create it)", runProjectionFixture, err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Go run projection fixture is stale; regenerate deliberately with ROOM_PROJECTION_GENERATE=1")
	}
}

func makeRunProjectionFixture(t *testing.T) runProjectionFixtureData {
	t.Helper()
	events := []*event.Event{
		projectionEvent("request-a", event.KindRunRequested, `{"run_id":"run-a","title":"Alpha","plan_file":"plans/a.md","adapter":"local"}`, "author-a", "2026-01-02T03:04:05.000Z"),
		projectionEvent("approve-a", event.KindRunApproved, `{"run_id":"run-a","approval_id":"approval-a","scope":"workspace","expires_at":"2026-01-03T00:00:00Z"}`, "reviewer", "2026-01-02T03:05:00.000Z"),
		projectionEvent("start-a", event.KindRunStarted, `{"run_id":"run-a"}`, "worker-a", "2026-01-02T03:06:00.000Z"),
		projectionEvent("finish-a", event.KindRunFinished, `{"run_id":"run-a","summary":"done","artifacts":["out/a.txt","out/b.txt"]}`, "worker-a", "2026-01-02T03:07:00.000Z"),
		projectionEvent("request-b", event.KindRunRequested, `{"run_id":"run-b","title":"Beta"}`, "author-b", "2026-01-02T04:00:00.000Z"),
		projectionEvent("deny-b", event.KindRunDenied, `{"run_id":"run-b","reason":"outside scope"}`, "reviewer", "2026-01-02T04:01:00.000Z"),
		projectionEvent("request-c", event.KindRunRequested, `{"run_id":"run-c","title":"Gamma","adapter":"remote"}`, "author-c", "2026-01-02T05:00:00.000Z"),
		projectionEvent("start-c", event.KindRunStarted, `{"run_id":"run-c"}`, "worker-c", "2026-01-02T05:01:00.000Z"),
		projectionEvent("fail-c", event.KindRunFailed, `{"run_id":"run-c","error":"worker exited 7"}`, "worker-c", "2026-01-02T05:02:00.000Z"),
		projectionEvent("request-d", event.KindRunRequested, `{"run_id":"run-d","title":"Delta"}`, "author-d", "2026-01-02T06:00:00.000Z"),
		projectionEvent("cancel-d", event.KindRunCancelled, `{"run_id":"run-d","reason":"superseded"}`, "author-d", "2026-01-02T06:01:00.000Z"),
		projectionEvent("malformed", event.KindRunApproved, `[]`, "reviewer", "2026-01-02T07:00:00.000Z"),
		projectionEvent("empty-request", event.KindRunRequested, `{"run_id":"","title":"ignored"}`, "author", "2026-01-02T07:01:00.000Z"),
		projectionEvent("unknown", "run.retried", `{"run_id":"run-a"}`, "worker-a", "2026-01-02T07:02:00.000Z"),
		projectionEvent("unmatched", event.KindRunStarted, `{"run_id":"missing-run"}`, "worker-x", "2026-01-02T07:03:00.000Z"),
		projectionEvent("bad-request", event.KindRunRequested, `[]`, "author", "2026-01-02T07:04:00.000Z"),
		projectionEvent("key-order-upper-lower", event.KindRunRequested, `{"RUN_ID":"ignored-upper","run_id":"run-key-order-one","title":"upper then lower"}`, "author", "2026-01-02T08:00:00.000Z"),
		projectionEvent("key-order-lower-upper", event.KindRunRequested, `{"run_id":"ignored-lower","RUN_ID":"run-key-order-two","title":"lower then upper"}`, "author", "2026-01-02T08:01:00.000Z"),
		projectionEvent("key-order-interleaved", event.KindRunRequested, `{"run_id":"run-a","RUN_ID":"run-b","run_id":"run-c","title":"interleaved duplicates"}`, "author", "2026-01-02T08:02:00.000Z"),
		projectionEvent("ignored-deep", event.KindRunRequested, `{"run_id":"run-ignored-deep","title":"unknown deep value","extra":`+strings.Repeat("[", 130)+`0`+strings.Repeat("]", 130)+`}`, "author", "2026-01-02T08:03:00.000Z"),
		projectionEvent("ignored-huge-number", event.KindRunRequested, `{"run_id":"run-ignored-huge","title":"unknown huge number","extra":1e100000}`, "author", "2026-01-02T08:04:00.000Z"),
	}
	projected := ProjectRuns(events)
	ids := make([]string, 0, len(projected))
	for id := range projected {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	records := make([]string, 0, len(ids))
	for _, id := range ids {
		record, err := json.Marshal(projected[id])
		if err != nil {
			t.Fatal(err)
		}
		records = append(records, string(record))
	}
	return runProjectionFixtureData{
		SchemaVersion:  1,
		OracleRevision: "a80da93e3ec02801c73aa5b2318dc06de3efd3fa",
		SourceHashes: map[string]string{
			"internal/room/run/run.go":     fileSHA256(t, "internal/room/run/run.go"),
			"internal/room/event/event.go": fileSHA256(t, "internal/room/event/event.go"),
		},
		Events:  events,
		Records: records,
	}
}

func projectionEvent(id, kind, body, author, ts string) *event.Event {
	return &event.Event{V: event.CurrentVersion, ID: id, Room: "room-test", Author: author, TS: ts, Kind: kind, Body: json.RawMessage(body)}
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", path))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
