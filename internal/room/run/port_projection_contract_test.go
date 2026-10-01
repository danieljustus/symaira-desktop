package run

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/room/event"
	"github.com/danieljustus/symaira-desktop/internal/room/identity"
	"github.com/danieljustus/symaira-desktop/internal/room/journal"
	roomconfig "github.com/danieljustus/symaira-desktop/internal/room/room"
)

const runProjectionFixture = "testdata/port/room/run-projection.json"

type runProjectionFixtureData struct {
	SchemaVersion     int                    `json:"schema_version"`
	OracleRevision    string                 `json:"oracle_revision"`
	RootEvent         string                 `json:"root_event"`
	RootPubkey        string                 `json:"root_pubkey"`
	SourceHashes      map[string]string      `json:"source_hashes"`
	Events            []*event.Event         `json:"events"`
	Records           []string               `json:"records"`
	CheckpointRecords []string               `json:"checkpoint_records"`
	JournalQueries    runJournalQueryFixture `json:"journal_queries"`
}

type runJournalFile struct {
	Name          string `json:"name"`
	Content       string `json:"content"`
	RepeatSuffix  string `json:"repeat_suffix,omitempty"`
	RepeatCount   int    `json:"repeat_count,omitempty"`
	RepeatNewline bool   `json:"repeat_newline,omitempty"`
}

type runGetVector struct {
	RunID  string `json:"run_id"`
	Record string `json:"record"`
	Error  string `json:"error"`
}

type runJournalQueryFixture struct {
	RootEvent               string                   `json:"root_event"`
	RootPubkey              string                   `json:"root_pubkey"`
	JournalFiles            []runJournalFile         `json:"journal_files"`
	Signers                 map[string]string        `json:"signers"`
	MergedEventIDs          []string                 `json:"merged_event_ids"`
	ListAll                 []string                 `json:"list_all"`
	ListPending             []string                 `json:"list_pending"`
	Gets                    []runGetVector           `json:"gets"`
	CheckpointRecords       []string                 `json:"checkpoint_records"`
	CheckpointNormalization string                   `json:"checkpoint_array_normalization"`
	EqualCreatedAt          runEqualCreatedAtFixture `json:"equal_created_at_list"`
	ReadErrors              []runReadErrorFixture    `json:"read_errors"`
}

type runEqualCreatedAtFixture struct {
	JournalFiles       []runJournalFile  `json:"journal_files"`
	Signers            map[string]string `json:"signers"`
	Records            []string          `json:"records_as_multiset"`
	DistinctOrders     int               `json:"go_distinct_orders_observed"`
	OrderNormalization string            `json:"order_normalization"`
}

type runReadErrorFixture struct {
	Name          string           `json:"name"`
	JournalFiles  []runJournalFile `json:"journal_files"`
	JournalIsFile bool             `json:"journal_is_file,omitempty"`
	ErrorClass    string           `json:"go_error_class"`
	ErrorCompare  string           `json:"error_comparison"`
	SegmentAuthor string           `json:"segment_author,omitempty"`
	GoError       string           `json:"go_error,omitempty"`
	RunID         string           `json:"run_id,omitempty"`
	Records       []string         `json:"expected_records,omitempty"`
	Signer        string           `json:"signer_public_key,omitempty"`
}

// TestPortRunProjectionContract freezes ProjectRuns and ProjectCheckpoints.
// Set PORT_GENERATE=1 to deliberately regenerate.
func TestPortRunProjectionContract(t *testing.T) {
	fixture := makeRunProjectionFixture(t)
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join("..", "..", "..", runProjectionFixture)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // generated contract fixture is intentionally world-readable
			t.Fatal(err)
		}
		t.Logf("wrote %s", runProjectionFixture)
		return
	}
	got, err := os.ReadFile(path) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
	if err != nil {
		t.Fatalf("read %s: %v (set PORT_GENERATE=1 to create it)", runProjectionFixture, err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Go room projection fixture is stale; regenerate deliberately with PORT_GENERATE=1")
	}
}

func makeRunProjectionFixture(t *testing.T) runProjectionFixtureData {
	t.Helper()
	owner := runProjectionIdentity("projection-owner")
	reviewer := runProjectionIdentity("projection-reviewer")
	agent := runProjectionIdentity("projection-agent")
	observer := runProjectionIdentity("projection-observer")
	forged := runProjectionIdentity("projection-forged")
	takeover := runProjectionIdentity("projection-room-takeover")
	rotated := runProjectionIdentity("projection-reviewer-rotated-key")
	lowLamportRoot := signedProjectionEvent(t, "low-lamport-forged-root", event.KindRoomCreated, `{"name":"Low Lamport attacker","public_key":"`+hex.EncodeToString(takeover.PublicKey)+`"}`, takeover, "2026-01-02T02:00:00.000Z")
	lowLamportRoot.Lamport = 0
	if err := lowLamportRoot.Sign(takeover); err != nil {
		t.Fatal(err)
	}
	events := []*event.Event{
		lowLamportRoot,
		signedProjectionEvent(t, "projection-room-created", event.KindRoomCreated, `{"name":"Owner","public_key":"`+hex.EncodeToString(owner.PublicKey)+`"}`, owner, "2026-01-02T03:00:00.000Z"),
		// A valid signature from a different key is not a room root when the key
		// does not hash to the event author/member ID.
		signedProjectionEventAs(t, "projection-forged-room-created", event.KindRoomCreated, `{"name":"Forged owner","public_key":"`+hex.EncodeToString(forged.PublicKey)+`"}`, owner.MemberID, forged, "2026-01-02T03:00:00.500Z"),
		signedProjectionEvent(t, "projection-reviewer-added", event.KindMemberAdded, `{"id":"`+reviewer.MemberID+`","name":"Reviewer","public_key":"`+hex.EncodeToString(reviewer.PublicKey)+`","role":"member","kind":"human"}`, owner, "2026-01-02T03:00:01.000Z"),
		signedProjectionEvent(t, "projection-agent-added", event.KindMemberAdded, `{"id":"`+agent.MemberID+`","name":"Agent","public_key":"`+hex.EncodeToString(agent.PublicKey)+`","role":"agent","kind":"agent"}`, owner, "2026-01-02T03:00:02.000Z"),
		// A later room.created signed by a legitimate non-owner must not reset
		// that member's role to owner.
		signedProjectionEvent(t, "projection-agent-room-recreated", event.KindRoomCreated, `{"name":"Agent","public_key":"`+hex.EncodeToString(agent.PublicKey)+`"}`, agent, "2026-01-02T03:00:02.500Z"),
		signedProjectionEvent(t, "projection-observer-added", event.KindMemberAdded, `{"id":"`+observer.MemberID+`","name":"Observer","public_key":"`+hex.EncodeToString(observer.PublicKey)+`","role":"observer","kind":"human"}`, owner, "2026-01-02T03:00:03.000Z"),
		projectionEvent("request-cross-room-approval", event.KindRunRequested, `{"run_id":"run-cross-room-approval","title":"Cross-room approval"}`, "author", "2026-01-02T03:00:03.500Z"),
		signedProjectionEventInRoom(t, "cross-room-member-added", event.KindMemberAdded, `{"id":"`+forged.MemberID+`","name":"Foreign reviewer","public_key":"`+hex.EncodeToString(forged.PublicKey)+`","role":"member","kind":"human"}`, owner, "2026-01-02T03:00:03.750Z", "rm_foreign"),
		signedProjectionEventInRoom(t, "cross-room-approval", event.KindRunApproved, `{"run_id":"run-cross-room-approval","approval_id":"foreign-approval","scope":"room"}`, forged, "2026-01-02T03:00:03.875Z", "rm_foreign"),
		// This forged owner-signed membership claim must not make its key eligible
		// to authorize a later run.approved event.
		signedProjectionEventAs(t, "projection-forged-member-added", event.KindMemberAdded, `{"id":"`+forged.MemberID+`","name":"Forged","public_key":"`+hex.EncodeToString(forged.PublicKey)+`","role":"member","kind":"human"}`, owner.MemberID, forged, "2026-01-02T03:00:04.000Z"),
		projectionEvent("request-a", event.KindRunRequested, `{"run_id":"run-a","title":"Alpha","plan_file":"plans/a.md","adapter":"local"}`, "author-a", "2026-01-02T03:04:05.000Z"),
		signedProjectionEvent(t, "approve-a", event.KindRunApproved, `{"run_id":"run-a","approval_id":"approval-a","scope":"workspace","expires_at":"2026-01-03T00:00:00Z"}`, reviewer, "2026-01-02T03:05:00.000Z"),
		projectionEvent("start-a", event.KindRunStarted, `{"run_id":"run-a"}`, "worker-a", "2026-01-02T03:06:00.000Z"),
		projectionEvent("finish-a", event.KindRunFinished, `{"run_id":"run-a","summary":"done","artifacts":["out/a.txt","out/b.txt"]}`, "worker-a", "2026-01-02T03:07:00.000Z"),
		projectionEvent("request-b", event.KindRunRequested, `{"run_id":"run-b","title":"Beta"}`, "author-b", "2026-01-02T04:00:00.000Z"),
		projectionEvent("deny-b", event.KindRunDenied, `{"run_id":"run-b","reason":"outside scope"}`, "reviewer", "2026-01-02T04:01:00.000Z"),
		projectionEvent("request-c", event.KindRunRequested, `{"run_id":"run-c","title":"Gamma","adapter":"remote"}`, "author-c", "2026-01-02T05:00:00.000Z"),
		projectionEvent("start-c", event.KindRunStarted, `{"run_id":"run-c"}`, "worker-c", "2026-01-02T05:01:00.000Z"),
		projectionEvent("fail-c", event.KindRunFailed, `{"run_id":"run-c","error":"worker exited 7"}`, "worker-c", "2026-01-02T05:02:00.000Z"),
		projectionEvent("request-d", event.KindRunRequested, `{"run_id":"run-d","title":"Delta"}`, "author-d", "2026-01-02T06:00:00.000Z"),
		projectionEvent("cancel-d", event.KindRunCancelled, `{"run_id":"run-d","reason":"superseded"}`, "author-d", "2026-01-02T06:01:00.000Z"),
		signedProjectionEvent(t, "malformed", event.KindRunApproved, `[]`, reviewer, "2026-01-02T07:00:00.000Z"),
		projectionEvent("empty-request", event.KindRunRequested, `{"run_id":"","title":"ignored"}`, "author", "2026-01-02T07:01:00.000Z"),
		projectionEvent("unknown", "run.retried", `{"run_id":"run-a"}`, "worker-a", "2026-01-02T07:02:00.000Z"),
		projectionEvent("unmatched", event.KindRunStarted, `{"run_id":"missing-run"}`, "worker-x", "2026-01-02T07:03:00.000Z"),
		projectionEvent("bad-request", event.KindRunRequested, `[]`, "author", "2026-01-02T07:04:00.000Z"),
		projectionEvent("key-order-upper-lower", event.KindRunRequested, `{"RUN_ID":"ignored-upper","run_id":"run-key-order-one","title":"upper then lower"}`, "author", "2026-01-02T08:00:00.000Z"),
		projectionEvent("key-order-lower-upper", event.KindRunRequested, `{"run_id":"ignored-lower","RUN_ID":"run-key-order-two","title":"lower then upper"}`, "author", "2026-01-02T08:01:00.000Z"),
		projectionEvent("key-order-interleaved", event.KindRunRequested, `{"run_id":"run-a","RUN_ID":"run-b","run_id":"run-c","title":"interleaved duplicates"}`, "author", "2026-01-02T08:02:00.000Z"),
		projectionEvent("ignored-deep", event.KindRunRequested, `{"run_id":"run-ignored-deep","title":"unknown deep value","extra":`+strings.Repeat("[", 130)+`0`+strings.Repeat("]", 130)+`}`, "author", "2026-01-02T08:03:00.000Z"),
		projectionEvent("ignored-huge-number", event.KindRunRequested, `{"run_id":"run-ignored-huge","title":"unknown huge number","extra":1e100000}`, "author", "2026-01-02T08:04:00.000Z"),
		projectionEvent("checkpoint-orphan-resolve", event.KindCheckpointResolved, `{"checkpoint_id":"chk-later","answer":"too early"}`, "reviewer", "2026-01-02T09:00:00.000Z"),
		projectionEvent("checkpoint-first-request", event.KindCheckpointReq, `{"checkpoint_id":"chk-main","run_id":"run-b","question":"initial"}`, "author-first", "2026-01-02T09:01:00.000Z"),
		projectionEvent("checkpoint-first-resolve", event.KindCheckpointResolved, `{"checkpoint_id":"chk-main","answer":"discarded by request"}`, "reviewer", "2026-01-02T09:02:00.000Z"),
		projectionEvent("checkpoint-repeat-request", event.KindCheckpointReq, `{"checkpoint_id":"chk-main","run_id":"run-b","question":"replacement"}`, "author-second", "2026-01-02T09:03:00.000Z"),
		projectionEvent("checkpoint-final-resolve", event.KindCheckpointResolved, `{"checkpoint_id":"chk-main","answer":"intermediate","answer":"final answer"}`, "reviewer", "2026-01-02T09:04:00.000Z"),
		projectionEvent("checkpoint-null-request", event.KindCheckpointReq, `{"checkpoint_id":"chk-null","run_id":"run-c","question":"keep question","question":null}`, "author-null", "2026-01-02T09:05:00.000Z"),
		projectionEvent("checkpoint-null-resolve", event.KindCheckpointResolved, `{"checkpoint_id":"chk-null","answer":"keep answer","answer":null}`, "reviewer", "2026-01-02T09:06:00.000Z"),
		projectionEvent("checkpoint-unmatched-resolve", event.KindCheckpointResolved, `{"checkpoint_id":"chk-missing","answer":"ignored"}`, "reviewer", "2026-01-02T09:07:00.000Z"),
		projectionEvent("checkpoint-bad-request", event.KindCheckpointReq, `{"checkpoint_id":7,"run_id":"run-a","question":"ignored"}`, "author", "2026-01-02T09:08:00.000Z"),
		projectionEvent("checkpoint-bad-resolve", event.KindCheckpointResolved, `{"checkpoint_id":"chk-main","answer":7}`, "reviewer", "2026-01-02T09:09:00.000Z"),
		projectionEvent("checkpoint-empty-request", event.KindCheckpointReq, `{"checkpoint_id":"","run_id":"run-a","question":"ignored"}`, "author", "2026-01-02T09:10:00.000Z"),
	}
	tamperedApproval := signedProjectionEvent(t, "tampered-approval", event.KindRunApproved, `{"run_id":"run-tampered-approval","approval_id":"tampered-approval","scope":"room"}`, reviewer, "2026-01-02T10:07:00.000Z")
	tamperedApproval.Body = json.RawMessage(`{"run_id":"run-tampered-approval","approval_id":"tampered-approval","scope":"forged"}`)
	events = append(events,
		projectionEvent("request-agent-approval", event.KindRunRequested, `{"run_id":"run-agent-approval","title":"Agent approval"}`, "author", "2026-01-02T10:00:00.000Z"),
		signedProjectionEvent(t, "agent-approval", event.KindRunApproved, `{"run_id":"run-agent-approval","approval_id":"agent-approval","scope":"room"}`, agent, "2026-01-02T10:01:00.000Z"),
		projectionEvent("request-observer-approval", event.KindRunRequested, `{"run_id":"run-observer-approval","title":"Observer approval"}`, "author", "2026-01-02T10:02:00.000Z"),
		signedProjectionEvent(t, "observer-approval", event.KindRunApproved, `{"run_id":"run-observer-approval","approval_id":"observer-approval","scope":"room"}`, observer, "2026-01-02T10:03:00.000Z"),
		projectionEvent("request-forged-approval", event.KindRunRequested, `{"run_id":"run-forged-approval","title":"Forged approval"}`, "author", "2026-01-02T10:04:00.000Z"),
		signedProjectionEvent(t, "forged-approval", event.KindRunApproved, `{"run_id":"run-forged-approval","approval_id":"forged-approval","scope":"room"}`, forged, "2026-01-02T10:05:00.000Z"),
		projectionEvent("request-tampered-approval", event.KindRunRequested, `{"run_id":"run-tampered-approval","title":"Tampered approval"}`, "author", "2026-01-02T10:06:00.000Z"),
		tamperedApproval,
		signedProjectionEvent(t, "projection-reviewer-key-rotated", event.KindMemberAdded, `{"id":"`+reviewer.MemberID+`","name":"Reviewer","public_key":"`+hex.EncodeToString(rotated.PublicKey)+`","role":"member","kind":"human"}`, owner, "2026-01-02T10:08:00.000Z"),
		projectionEvent("request-old-key-approval", event.KindRunRequested, `{"run_id":"run-old-key-approval","title":"Old key approval"}`, "author", "2026-01-02T10:09:00.000Z"),
		signedProjectionEvent(t, "old-key-approval", event.KindRunApproved, `{"run_id":"run-old-key-approval","approval_id":"old-key-approval","scope":"room"}`, reviewer, "2026-01-02T10:10:00.000Z"),
		projectionEvent("request-current-key-approval", event.KindRunRequested, `{"run_id":"run-current-key-approval","title":"Current key approval"}`, "author", "2026-01-02T10:11:00.000Z"),
		signedProjectionEventAs(t, "current-key-approval", event.KindRunApproved, `{"run_id":"run-current-key-approval","approval_id":"current-key-approval","scope":"room"}`, reviewer.MemberID, rotated, "2026-01-02T10:12:00.000Z"),
		signedProjectionEvent(t, "projection-reviewer-demoted", event.KindMemberRoleChanged, `{"id":"`+reviewer.MemberID+`","role":"observer"}`, owner, "2026-01-02T10:13:00.000Z"),
		projectionEvent("request-demoted-approval", event.KindRunRequested, `{"run_id":"run-demoted-approval","title":"Demoted approval"}`, "author", "2026-01-02T10:14:00.000Z"),
		signedProjectionEventAs(t, "demoted-approval", event.KindRunApproved, `{"run_id":"run-demoted-approval","approval_id":"demoted-approval","scope":"room"}`, reviewer.MemberID, rotated, "2026-01-02T10:15:00.000Z"),
		projectionEvent("request-room-takeover", event.KindRunRequested, `{"run_id":"run-room-takeover","title":"Room takeover"}`, "author", "2026-01-02T10:16:00.000Z"),
		signedProjectionEvent(t, "owner-self-removed", event.KindMemberRemoved, `{"id":"`+owner.MemberID+`"}`, owner, "2026-01-02T10:17:00.000Z"),
		signedProjectionEvent(t, "takeover-room-created", event.KindRoomCreated, `{"name":"Takeover","public_key":"`+hex.EncodeToString(takeover.PublicKey)+`"}`, takeover, "2026-01-02T10:18:00.000Z"),
		signedProjectionEvent(t, "takeover-approval", event.KindRunApproved, `{"run_id":"run-room-takeover","approval_id":"takeover-approval","scope":"room"}`, takeover, "2026-01-02T10:19:00.000Z"),
	)
	projected := ProjectRunsInConfiguredRoom(events, &roomconfig.RoomConfig{
		ID: "room-test", RootEvent: "projection-room-created", RootPubkey: "ed25519:" + hex.EncodeToString(owner.PublicKey),
	})
	emptyAnchorProjection := ProjectRunsInConfiguredRoom(events, &roomconfig.RoomConfig{
		ID: "room-test", RootPubkey: "ed25519:" + hex.EncodeToString(takeover.PublicKey),
	})
	if got := emptyAnchorProjection["run-room-takeover"].State; got != StateRequested {
		t.Fatalf("an empty configured root anchor must reject a self-signed root and approval, got %q", got)
	}
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
	projectedCheckpoints := ProjectCheckpoints(events)
	checkpointIDs := make([]string, 0, len(projectedCheckpoints))
	for id := range projectedCheckpoints {
		checkpointIDs = append(checkpointIDs, id)
	}
	sort.Strings(checkpointIDs)
	checkpointRecords := make([]string, 0, len(checkpointIDs))
	for _, id := range checkpointIDs {
		record, err := json.Marshal(projectedCheckpoints[id])
		if err != nil {
			t.Fatal(err)
		}
		checkpointRecords = append(checkpointRecords, string(record))
	}
	return runProjectionFixtureData{
		SchemaVersion:  1,
		OracleRevision: "6f1c04e38e283e0e722661725bd5baec9f3f5fe5",
		RootEvent:      "projection-room-created",
		RootPubkey:     "ed25519:" + hex.EncodeToString(owner.PublicKey),
		SourceHashes: map[string]string{
			"internal/room/run/run.go":         fileSHA256(t, "internal/room/run/run.go"),
			"internal/room/run/checkpoint.go":  fileSHA256(t, "internal/room/run/checkpoint.go"),
			"internal/room/event/event.go":     fileSHA256(t, "internal/room/event/event.go"),
			"internal/room/journal/journal.go": fileSHA256(t, "internal/room/journal/journal.go"),
			"internal/room/journal/merge.go":   fileSHA256(t, "internal/room/journal/merge.go"),
			"internal/room/members/members.go": fileSHA256(t, "internal/room/members/members.go"),
		},
		Events:            events,
		Records:           records,
		CheckpointRecords: checkpointRecords,
		JournalQueries:    makeRunJournalQueryFixture(t),
	}
}

func projectionEvent(id, kind, body, author, ts string) *event.Event {
	return &event.Event{V: event.CurrentVersion, ID: id, Room: "room-test", Author: author, TS: ts, Kind: kind, Body: json.RawMessage(body)}
}

func signedProjectionEvent(t *testing.T, id, kind, body string, signer *identity.Identity, ts string) *event.Event {
	t.Helper()
	return signedProjectionEventAs(t, id, kind, body, signer.MemberID, signer, ts)
}

func signedProjectionEventAs(t *testing.T, id, kind, body, author string, signer *identity.Identity, ts string) *event.Event {
	t.Helper()
	ev := projectionEvent(id, kind, body, author, ts)
	ev.Seq = 1
	ev.Prev = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	ev.Lamport = 1
	if err := ev.Sign(signer); err != nil {
		t.Fatalf("sign projection event %s: %v", id, err)
	}
	return ev
}

func signedProjectionEventInRoom(t *testing.T, id, kind, body string, signer *identity.Identity, ts, roomID string) *event.Event {
	t.Helper()
	ev := projectionEvent(id, kind, body, signer.MemberID, ts)
	ev.Room = roomID
	ev.Seq = 1
	ev.Prev = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	ev.Lamport = 1
	if err := ev.Sign(signer); err != nil {
		t.Fatalf("sign cross-room projection event %s: %v", id, err)
	}
	return ev
}

func makeRunJournalQueryFixture(t *testing.T) runJournalQueryFixture {
	t.Helper()
	alpha := runProjectionIdentity("alpha")
	beta := runProjectionIdentity("beta")
	takeover := runProjectionIdentity("query-takeover")
	events := []*event.Event{
		projectionEvent("query-forged-root", event.KindRoomCreated, `{"name":"Attacker root","public_key":"`+hex.EncodeToString(takeover.PublicKey)+`"}`, takeover.MemberID, "2026-02-01T09:59:57.000Z"),
		projectionEvent("query-room-created", event.KindRoomCreated, `{"name":"Alpha","public_key":"`+hex.EncodeToString(alpha.PublicKey)+`"}`, alpha.MemberID, "2026-02-01T09:59:58.000Z"),
		projectionEvent("query-member-added", event.KindMemberAdded, `{"id":"`+beta.MemberID+`","name":"Beta","public_key":"`+hex.EncodeToString(beta.PublicKey)+`","role":"member","kind":"human"}`, alpha.MemberID, "2026-02-01T09:59:59.000Z"),
		projectionEvent("query-request-a", event.KindRunRequested, `{"run_id":"query-a","title":"Query Alpha"}`, alpha.MemberID, "2026-02-01T10:00:00.000Z"),
		projectionEvent("query-request-b", event.KindRunRequested, `{"run_id":"query-b","title":"Query Beta"}`, beta.MemberID, "2026-02-01T10:01:00.000Z"),
		projectionEvent("query-approve-b", event.KindRunApproved, `{"run_id":"query-b","approval_id":"approval-qb","scope":"room"}`, alpha.MemberID, "2026-02-01T10:02:00.000Z"),
		projectionEvent("query-request-c", event.KindRunRequested, `{"run_id":"query-c","title":"Query takeover"}`, beta.MemberID, "2026-02-01T10:02:30.000Z"),
		projectionEvent("query-approve-c", event.KindRunApproved, `{"run_id":"query-c","approval_id":"approval-qc","scope":"room"}`, takeover.MemberID, "2026-02-01T10:02:45.000Z"),
		projectionEvent("query-checkpoint-a1", event.KindCheckpointReq, `{"checkpoint_id":"query-chk-a1","run_id":"query-a","question":"first question"}`, beta.MemberID, "2026-02-01T10:03:00.000Z"),
		projectionEvent("query-finish-a", event.KindRunFinished, `{"run_id":"query-a","summary":"finished"}`, alpha.MemberID, "2026-02-01T10:04:00.000Z"),
		projectionEvent("query-resolve-a1", event.KindCheckpointResolved, `{"checkpoint_id":"query-chk-a1","answer":"first answer"}`, alpha.MemberID, "2026-02-01T10:05:00.000Z"),
		projectionEvent("query-checkpoint-a2", event.KindCheckpointReq, `{"checkpoint_id":"query-chk-a2","run_id":"query-a","question":"second question"}`, beta.MemberID, "2026-02-01T10:06:00.000Z"),
		projectionEvent("query-resolve-a2", event.KindCheckpointResolved, `{"checkpoint_id":"query-chk-a2","answer":"second answer"}`, alpha.MemberID, "2026-02-01T10:07:00.000Z"),
	}

	journalDir := filepath.Join(t.TempDir(), "journal")
	if err := os.MkdirAll(journalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	identityByAuthor := map[string]*identity.Identity{alpha.MemberID: alpha, beta.MemberID: beta, takeover.MemberID: takeover}
	seqByAuthor := make(map[string]uint64)
	prevByAuthor := make(map[string]string)
	contentsByAuthor := make(map[string][]byte)
	signers := map[string]string{
		alpha.MemberID:    hex.EncodeToString(alpha.PublicKey),
		beta.MemberID:     hex.EncodeToString(beta.PublicKey),
		takeover.MemberID: hex.EncodeToString(takeover.PublicKey),
	}
	for index, ev := range events {
		ev.Seq = seqByAuthor[ev.Author] + 1
		ev.Prev = prevByAuthor[ev.Author]
		if ev.Prev == "" {
			ev.Prev = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
		}
		ev.Lamport = uint64(index + 1)
		if ev.ID == "query-forged-root" {
			ev.Lamport = 0
		}
		signer := identityByAuthor[ev.Author]
		if err := ev.Sign(signer); err != nil {
			t.Fatal(err)
		}
		if err := ev.VerifySignature(signer.PublicKey); err != nil {
			t.Fatalf("verify signed fixture event %s: %v", ev.ID, err)
		}
		line, err := ev.MarshalJSONLine()
		if err != nil {
			t.Fatal(err)
		}
		contentsByAuthor[ev.Author] = append(contentsByAuthor[ev.Author], line...)
		prevByAuthor[ev.Author] = journal.ComputeLineHash(bytes.TrimSuffix(line, []byte{'\n'}))
		seqByAuthor[ev.Author] = ev.Seq
	}

	authors := make([]string, 0, len(contentsByAuthor))
	for author := range contentsByAuthor {
		authors = append(authors, author)
	}
	sort.Strings(authors)
	files := make([]runJournalFile, 0, len(authors))
	for _, author := range authors {
		name := author + ".jsonl"
		content := contentsByAuthor[author]
		if err := os.WriteFile(filepath.Join(journalDir, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
		files = append(files, runJournalFile{Name: name, Content: string(content)})
	}

	roomDir := filepath.Dir(journalDir)
	roomConfig := "id = \"room-test\"\nroot_event = \"query-room-created\"\nroot_pubkey = \"ed25519:" + hex.EncodeToString(alpha.PublicKey) + "\"\n"
	if err := os.WriteFile(filepath.Join(roomDir, "room.toml"), []byte(roomConfig), 0o600); err != nil {
		t.Fatal(err)
	}
	merged, err := journal.New(journalDir).MergeAll()
	if err != nil {
		t.Fatal(err)
	}
	mergedIDs := make([]string, 0, len(merged))
	for _, ev := range merged {
		mergedIDs = append(mergedIDs, ev.ID)
	}
	all, err := List(roomDir, false)
	if err != nil {
		t.Fatal(err)
	}
	pending, err := List(roomDir, true)
	if err != nil {
		t.Fatal(err)
	}
	getCases := make([]runGetVector, 0, 3)
	for _, runID := range []string{"query-a", "query-b", "query-c", "missing-run"} {
		got, err := Get(roomDir, runID)
		if err != nil {
			getCases = append(getCases, runGetVector{RunID: runID, Error: err.Error()})
			continue
		}
		getCases = append(getCases, runGetVector{RunID: runID, Record: runProjectionRecord(t, got)})
	}
	checkpoints := ProjectCheckpoints(merged)
	checkpointIDs := make([]string, 0, len(checkpoints))
	for id := range checkpoints {
		checkpointIDs = append(checkpointIDs, id)
	}
	sort.Strings(checkpointIDs)
	checkpointRecords := make([]string, 0, len(checkpointIDs))
	for _, id := range checkpointIDs {
		data, err := json.Marshal(checkpoints[id])
		if err != nil {
			t.Fatal(err)
		}
		checkpointRecords = append(checkpointRecords, string(data))
	}
	return runJournalQueryFixture{
		RootEvent: "query-room-created", RootPubkey: "ed25519:" + hex.EncodeToString(alpha.PublicKey),
		JournalFiles: files, Signers: signers, MergedEventIDs: mergedIDs,
		ListAll: runProjectionRecords(t, all), ListPending: runProjectionRecords(t, pending),
		Gets: getCases, CheckpointRecords: checkpointRecords,
		CheckpointNormalization: "sort only each run.checkpoints array by checkpoint id; Go ProjectRuns ranges a map",
		EqualCreatedAt:          makeEqualCreatedAtListFixture(t),
		ReadErrors:              makeRunReadErrorFixtures(t),
	}
}

func makeEqualCreatedAtListFixture(t *testing.T) runEqualCreatedAtFixture {
	t.Helper()
	alpha := runProjectionIdentity("equal-alpha")
	beta := runProjectionIdentity("equal-beta")
	identities := map[string]*identity.Identity{alpha.MemberID: alpha, beta.MemberID: beta}
	events := []*event.Event{
		projectionEvent("equal-request-a", event.KindRunRequested, `{"run_id":"equal-a","title":"Equal A"}`, alpha.MemberID, "2026-02-02T10:00:00.000Z"),
		projectionEvent("equal-request-b", event.KindRunRequested, `{"run_id":"equal-b","title":"Equal B"}`, beta.MemberID, "2026-02-02T10:00:00.000Z"),
	}
	contents := make(map[string][]byte, len(events))
	for index, ev := range events {
		ev.Seq = 1
		ev.Prev = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
		ev.Lamport = uint64(index + 1)
		if err := ev.Sign(identities[ev.Author]); err != nil {
			t.Fatal(err)
		}
		line, err := ev.MarshalJSONLine()
		if err != nil {
			t.Fatal(err)
		}
		contents[ev.Author] = line
	}

	journalDir := filepath.Join(t.TempDir(), "journal")
	if err := os.MkdirAll(journalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	authors := make([]string, 0, len(contents))
	for author := range contents {
		authors = append(authors, author)
	}
	sort.Strings(authors)
	files := make([]runJournalFile, 0, len(authors))
	for _, author := range authors {
		name := author + ".jsonl"
		content := contents[author]
		if err := os.WriteFile(filepath.Join(journalDir, name), content, 0o600); err != nil {
			t.Fatal(err)
		}
		files = append(files, runJournalFile{Name: name, Content: string(content)})
	}

	roomDir := filepath.Dir(journalDir)
	if err := os.WriteFile(filepath.Join(roomDir, "room.toml"), []byte("id = \"room-test\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	orders := make(map[string]struct{})
	var multiset []string
	for range 64 {
		list, err := List(roomDir, false)
		if err != nil {
			t.Fatal(err)
		}
		records := runProjectionRecords(t, list)
		order, err := json.Marshal(records)
		if err != nil {
			t.Fatal(err)
		}
		orders[string(order)] = struct{}{}
		canonical := append([]string(nil), records...)
		sort.Strings(canonical)
		if multiset == nil {
			multiset = canonical
		} else if !equalStrings(multiset, canonical) {
			t.Fatalf("Go List changed the equal-created-at record multiset: %v vs %v", multiset, canonical)
		}
	}
	if len(orders) < 2 {
		t.Fatalf("Go List did not demonstrate equal-CreatedAt map-order nondeterminism in 64 calls")
	}
	return runEqualCreatedAtFixture{
		JournalFiles: files, Signers: map[string]string{
			alpha.MemberID: hex.EncodeToString(alpha.PublicKey),
			beta.MemberID:  hex.EncodeToString(beta.PublicKey),
		},
		Records: multiset, DistinctOrders: len(orders),
		OrderNormalization: "sort only the List record array as a semantic multiset; Go map iteration plus equal CreatedAt does not define order",
	}
}

func makeRunReadErrorFixtures(t *testing.T) []runReadErrorFixture {
	t.Helper()
	const malformedAuthor = "malformed-segment"
	malformedRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(malformedRoot, "room.toml"), []byte("id = \"room-test\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	malformedDir := filepath.Join(malformedRoot, "journal")
	if err := os.MkdirAll(malformedDir, 0o700); err != nil {
		t.Fatal(err)
	}
	malformedContent := "not-json\n"
	if err := os.WriteFile(filepath.Join(malformedDir, malformedAuthor+".jsonl"), []byte(malformedContent), 0o600); err != nil {
		t.Fatal(err)
	}
	_, malformedErr := List(malformedRoot, false)
	if malformedErr == nil {
		t.Fatal("Go List accepted malformed journal event")
	}
	var syntaxErr *json.SyntaxError
	if !errors.As(malformedErr, &syntaxErr) {
		t.Fatalf("expected Go malformed-event syntax error, got %T: %v", malformedErr, malformedErr)
	}
	if !strings.Contains(malformedErr.Error(), "read segment "+malformedAuthor+": unmarshal line:") {
		t.Fatalf("malformed event error lost segment context: %v", malformedErr)
	}
	if _, getErr := Get(malformedRoot, "missing-run"); getErr == nil {
		t.Fatal("Go Get accepted malformed journal event")
	} else if !errors.As(getErr, &syntaxErr) {
		t.Fatalf("expected Go Get malformed-event syntax error, got %T: %v", getErr, getErr)
	}

	notDirectoryRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(notDirectoryRoot, "room.toml"), []byte("id = \"room-test\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(notDirectoryRoot, "journal"), []byte("not a directory"), 0o600); err != nil {
		t.Fatal(err)
	}
	list, notDirectoryErr := List(notDirectoryRoot, false)
	_, getErr := Get(notDirectoryRoot, "missing-run")
	if runtime.GOOS == "windows" {
		// Windows classifies ReadDir(file) as missing; Go's journal reader ignores it.
		if notDirectoryErr != nil || len(list) != 0 || !errors.Is(getErr, ErrRunNotFound) {
			t.Fatalf("expected Windows Go missing-journal behavior, got list=%v listErr=%v getErr=%v", list, notDirectoryErr, getErr)
		}
	} else if !errors.Is(notDirectoryErr, syscall.ENOTDIR) || !errors.Is(getErr, syscall.ENOTDIR) {
		t.Fatalf("expected Go not-a-directory read errors, got listErr=%v getErr=%v", notDirectoryErr, getErr)
	}
	return []runReadErrorFixture{
		{
			Name: "malformed-event-line", JournalFiles: []runJournalFile{{Name: malformedAuthor + ".jsonl", Content: malformedContent}},
			ErrorClass: "malformed_event", ErrorCompare: "Go/Rust error class and segment author; parser wording is implementation-specific",
			SegmentAuthor: malformedAuthor, GoError: malformedErr.Error(),
		},
		{
			Name: "journal-path-is-file", JournalFiles: []runJournalFile{}, JournalIsFile: true, ErrorClass: "not_a_directory",
			ErrorCompare: "Go syscall error and Rust I/O error kind; message contains a temporary path",
		},
		makeScannerOverflowFixture(t),
	}
}

func makeScannerOverflowFixture(t *testing.T) runReadErrorFixture {
	t.Helper()
	owner := runProjectionIdentity("scanner-overflow")
	ev := projectionEvent("scanner-prefix-event", event.KindRunRequested, `{"run_id":"scanner-prefix","title":"Scanner prefix"}`, owner.MemberID, "2026-02-03T10:00:00.000Z")
	ev.Seq = 1
	ev.Prev = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	ev.Lamport = 1
	if err := ev.Sign(owner); err != nil {
		t.Fatal(err)
	}
	line, err := ev.MarshalJSONLine()
	if err != nil {
		t.Fatal(err)
	}
	file := runJournalFile{
		Name: owner.MemberID + ".jsonl", Content: string(line),
		RepeatSuffix: "x", RepeatCount: 70 * 1024, RepeatNewline: true,
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "room.toml"), []byte("id = \"room-test\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	journalDir := filepath.Join(root, "journal")
	if err := os.MkdirAll(journalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(journalDir, file.Name), file.bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	list, err := List(root, false)
	if err != nil {
		t.Fatalf("Go Scanner should preserve prefix before an oversized token: %v", err)
	}
	got, err := Get(root, "scanner-prefix")
	if err != nil {
		t.Fatalf("Go Get should preserve prefix before an oversized token: %v", err)
	}
	if len(list) != 1 || list[0].ID != got.ID {
		t.Fatalf("unexpected scanner-prefix projection: list=%+v get=%+v", list, got)
	}
	return runReadErrorFixture{
		Name: "scanner-token-too-long", JournalFiles: []runJournalFile{file},
		ErrorClass:    "scanner_stops_silently",
		ErrorCompare:  "surviving prefix records are byte-exact; only the scanner error is suppressed by Go",
		SegmentAuthor: owner.MemberID,
		RunID:         "scanner-prefix", Records: []string{runProjectionRecord(t, got)},
		Signer: hex.EncodeToString(owner.PublicKey),
	}
}

func (file runJournalFile) bytes() []byte {
	content := []byte(file.Content)
	content = append(content, bytes.Repeat([]byte(file.RepeatSuffix), file.RepeatCount)...)
	if file.RepeatNewline {
		content = append(content, '\n')
	}
	return content
}

func equalStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func runProjectionIdentity(name string) *identity.Identity {
	seed := sha256.Sum256([]byte("symroom-run-query-fixture/" + name))
	private := ed25519.NewKeyFromSeed(seed[:])
	public := private.Public().(ed25519.PublicKey)
	return &identity.Identity{
		Name: name, MemberID: identity.ComputeMemberID(public), PublicKey: public, PrivateKey: private,
	}
}

func runProjectionRecords(t *testing.T, records []*Run) []string {
	t.Helper()
	encoded := make([]string, 0, len(records))
	for _, record := range records {
		encoded = append(encoded, runProjectionRecord(t, record))
	}
	return encoded
}

func runProjectionRecord(t *testing.T, record *Run) string {
	t.Helper()
	// List/Get may attach checkpoints in Go map order. Normalize only this field.
	sort.Slice(record.Checkpoints, func(i, j int) bool {
		return record.Checkpoints[i].ID < record.Checkpoints[j].ID
	})
	data, err := json.Marshal(record)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("..", "..", "..", path)) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
