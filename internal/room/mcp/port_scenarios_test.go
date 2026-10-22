package mcp

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/danieljustus/symaira-desktop/internal/room/event"
	"github.com/danieljustus/symaira-desktop/internal/room/identity"
	"github.com/danieljustus/symaira-desktop/internal/room/journal"
)

// Scenario fixtures for MCP-004. A scenario names a room setup (materialized
// identically by Go and Rust from the fixture itself), whether a signing
// identity is configured, and an ordered list of steps. Request steps are one
// framed request per ServeIO call; stream steps feed raw bytes through a single
// ServeIO call. All outputs are real Go output; paths and the non-deterministic
// event fields (ts, sig, prev, generated ids) are normalized only through the
// mechanisms described on normalizeEventText.

const roomTomlFixture = "schema_version = 1\nid = \"rm_fixture\"\ncreated = \"2026-09-01T00:00:00Z\"\nroot_pubkey = \"ed25519:fixture\"\nroot_event = \"ev_fixture\"\n"

type portSetup struct {
	RoomToml     *string           `json:"room_toml"`
	Files        map[string]string `json:"files"`
	JournalFile  string            `json:"journal_file"`
	JournalLines []string          `json:"journal_lines"`
}

type setupEvent struct{ ID, Kind, Body string }

func fixtureSigningIdentity() *identity.Identity {
	seed := sha256.Sum256([]byte("symroom-mcp-fixture-signing-seed"))
	privateKey := ed25519.NewKeyFromSeed(seed[:])
	publicKey := privateKey.Public().(ed25519.PublicKey)
	return &identity.Identity{Name: "fixture", MemberID: identity.ComputeMemberID(publicKey), PublicKey: publicKey, PrivateKey: privateKey}
}

func mutationSigningIdentity() *identity.Identity {
	seed := sha256.Sum256([]byte("symroom-mcp-mutation-seed"))
	privateKey := ed25519.NewKeyFromSeed(seed[:])
	publicKey := privateKey.Public().(ed25519.PublicKey)
	return &identity.Identity{Name: "fixture", MemberID: identity.ComputeMemberID(publicKey), PublicKey: publicKey, PrivateKey: privateKey}
}

func hexSHA(content string) string {
	sum := sha256.Sum256([]byte(content))
	return hex.EncodeToString(sum[:])
}

// signedSetup signs events with the fixed fixture identity and captures the
// resulting journal lines, so Rust replays Go-signed bytes.
func signedSetup(t *testing.T, roomToml *string, files map[string]string, events []setupEvent) portSetup {
	t.Helper()
	setup := portSetup{RoomToml: roomToml, Files: files}
	if events == nil {
		return setup
	}
	dir := t.TempDir()
	journalDir := filepath.Join(dir, "journal")
	if err := os.MkdirAll(journalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	id := fixtureSigningIdentity()
	j := journal.New(journalDir)
	for _, spec := range events {
		ev := &event.Event{V: event.CurrentVersion, ID: spec.ID, Room: "rm_fixture", Author: id.MemberID, TS: "2026-09-01T00:00:00.000Z", Kind: spec.Kind, Body: json.RawMessage(spec.Body)}
		if err := j.PrepareEvent(ev); err != nil {
			t.Fatal(err)
		}
		if err := ev.Sign(id); err != nil {
			t.Fatal(err)
		}
		if err := j.Append(ev); err != nil {
			t.Fatal(err)
		}
	}
	setup.JournalFile = id.MemberID + ".jsonl"
	setup.JournalLines = []string{}
	if len(events) > 0 {
		content, err := os.ReadFile(filepath.Join(journalDir, setup.JournalFile)) //nolint:gosec // deterministic identity under t.TempDir
		if err != nil {
			t.Fatal(err)
		}
		setup.JournalLines = strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	}
	return setup
}

func fixtureEvents() []setupEvent {
	return []setupEvent{
		{"ev_fixture", event.KindArtifactLinked, fmt.Sprintf(`{"artifact_id":"art_fixture","path":"known.txt","sha256":"%s","title":"Known"}`, hexSHA("artifact-content"))},
		{"ev_run_request", event.KindRunRequested, `{"run_id":"run_fixture","title":"Fixture"}`},
		{"ev_run_denied", event.KindRunDenied, `{"run_id":"run_fixture","reason":"outside policy"}`},
	}
}

func buildPortSetups(t *testing.T) map[string]portSetup {
	t.Helper()
	toml := roomTomlFixture
	setups := map[string]portSetup{
		"fixture":        signedSetup(t, &toml, map[string]string{"known.txt": "artifact-content"}, fixtureEvents()),
		"empty_dir":      signedSetup(t, nil, nil, nil),
		"room_toml_only": signedSetup(t, &toml, nil, nil),
		"journal_only":   signedSetup(t, nil, nil, fixtureEvents()),
	}
	var notes []setupEvent
	for i := 0; i < 30; i++ {
		notes = append(notes, setupEvent{fmt.Sprintf("ev_note_%02d", i), event.KindNotePosted, fmt.Sprintf(`{"text":"note %02d"}`, i)})
	}
	setups["html_notes"] = signedSetup(t, &toml, nil, []setupEvent{
		{"ev_html_1", event.KindNotePosted, `{"text":"<b>bold</b> & \"q\" \u00e9 \u2028 \u0008\u000c\u0001"}`},
		{"ev_html_2", event.KindNotePosted, `{"text":"plain"}`},
	})
	ownerHex := hex.EncodeToString(fixtureSigningIdentity().PublicKey)
	ownerToml := fmt.Sprintf("schema_version = 1\nid = \"rm_fixture\"\ncreated = \"2026-09-01T00:00:00Z\"\nroot_pubkey = \"ed25519:%s\"\nroot_event = \"ev_root\"\n", ownerHex)
	rootEvent := setupEvent{"ev_root", event.KindRoomCreated, fmt.Sprintf(`{"name":"Owner Room","public_key":"%s"}`, ownerHex)}
	runRequest := func(id, title string) setupEvent {
		return setupEvent{"ev_req_" + id, event.KindRunRequested, fmt.Sprintf(`{"run_id":"run_%s","title":"%s","plan_file":"plan.md","adapter":"local"}`, id, title)}
	}
	runEvent := func(kind, id, body string) setupEvent {
		return setupEvent{fmt.Sprintf("ev_%s_%s", strings.ReplaceAll(kind, ".", "_"), id), kind, body}
	}
	setups["owner_room"] = signedSetup(t, &ownerToml, nil, []setupEvent{
		rootEvent,
		runRequest("ok", "Approved run"),
		runEvent(event.KindRunApproved, "ok", `{"run_id":"run_ok","approval_id":"apr_1","scope":"run","expires_at":"2099-01-01T00:00:00Z"}`),
		runRequest("cancel", "Cancelled run"),
		runEvent(event.KindRunCancelled, "cancel", `{"run_id":"run_cancel","reason":"changed mind"}`),
		runRequest("deny", "Denied run"),
		runEvent(event.KindRunDenied, "deny", `{"run_id":"run_deny","reason":"not allowed <here> & now"}`),
		runRequest("pending", "Pending run"),
		runRequest("started", "Started run"),
		runEvent(event.KindRunApproved, "started", `{"run_id":"run_started","approval_id":"apr_2","scope":"run"}`),
		runEvent(event.KindRunStarted, "started", `{"run_id":"run_started"}`),
		runRequest("done", "Finished run"),
		runEvent(event.KindRunApproved, "done", `{"run_id":"run_done","approval_id":"apr_3","scope":"project"}`),
		runEvent(event.KindRunStarted, "done", `{"run_id":"run_done"}`),
		runEvent(event.KindRunFinished, "done", `{"run_id":"run_done","summary":"All <done>","artifacts":["a.txt","b.txt"]}`),
		runRequest("failed", "Failed run"),
		runEvent(event.KindRunApproved, "failed", `{"run_id":"run_failed","approval_id":"apr_4","scope":"run"}`),
		runEvent(event.KindRunStarted, "failed", `{"run_id":"run_failed"}`),
		runEvent(event.KindRunFailed, "failed", `{"run_id":"run_failed","error":"boom"}`),
		runRequest("minimal", ""),
		runEvent(event.KindRunApproved, "minimal", `{"run_id":"run_minimal"}`),
	})
	mutationHex := hex.EncodeToString(mutationSigningIdentity().PublicKey)
	setups["observer_room"] = signedSetup(t, &ownerToml, nil, []setupEvent{
		rootEvent,
		{"ev_obs", event.KindMemberAdded, fmt.Sprintf(`{"id":"%s","name":"obs","public_key":"%s","role":"observer","kind":"human"}`, mutationSigningIdentity().MemberID, mutationHex)},
	})
	setups["member_room"] = signedSetup(t, &ownerToml, nil, []setupEvent{
		rootEvent,
		{"ev_mem", event.KindMemberAdded, fmt.Sprintf(`{"id":"%s","name":"mem","public_key":"%s","role":"member","kind":"agent"}`, mutationSigningIdentity().MemberID, mutationHex)},
	})
	setups["dotdot_file"] = signedSetup(t, &toml, map[string]string{"..name": "dotdot-content", ".name": "dot-content"}, nil)
	setups["tail_30"] = signedSetup(t, &toml, nil, notes)
	setups["artifacts"] = signedSetup(t, &toml, map[string]string{
		"ok.txt":       "ok-content",
		"modified.txt": "changed content",
		"unlinked.txt": "u",
	}, []setupEvent{
		{"ev_a_ok", event.KindArtifactLinked, fmt.Sprintf(`{"artifact_id":"art_ok","path":"ok.txt","sha256":"%s","title":"OK"}`, hexSHA("ok-content"))},
		{"ev_a_mod", event.KindArtifactLinked, fmt.Sprintf(`{"artifact_id":"art_mod","path":"modified.txt","sha256":"%s"}`, hexSHA("original content"))},
		{"ev_a_gone", event.KindArtifactLinked, fmt.Sprintf(`{"artifact_id":"art_gone","path":"gone.txt","sha256":"%s","title":"Gone"}`, hexSHA("gone"))},
		{"ev_a_unl", event.KindArtifactLinked, fmt.Sprintf(`{"artifact_id":"art_unl","path":"unlinked.txt","sha256":"%s","title":"Unlinked"}`, hexSHA("u"))},
		{"ev_a_unl2", event.KindArtifactUnlinked, `{"artifact_id":"art_unl"}`},
		{"ev_a_sd", event.KindArtifactLinked, fmt.Sprintf(`{"artifact_id":"art_sd","path":"ok.txt","sha256":"%s","title":"Desk","symdesk_id":"sd_1"}`, hexSHA("ok-content"))},
		{"ev_a_bad", event.KindArtifactLinked, `{"artifact_id":"art_bad","path":"ok.txt"}`},
		{"ev_a_abs", event.KindArtifactLinked, `{"artifact_id":"art_abs","path":"/abs-missing.txt","sha256":"00","title":"Absolute"}`},
		{"ev_a_typeerr", event.KindArtifactLinked, `{"artifact_id":"art_typeerr","path":5,"sha256":"00"}`},
		{"ev_a_noid", event.KindArtifactLinked, `{"path":"ok.txt","sha256":"00"}`},
		{"ev_a_unl_unknown", event.KindArtifactUnlinked, `{"artifact_id":"art_never"}`},
		{"ev_a_ok2", event.KindArtifactLinked, fmt.Sprintf(`{"artifact_id":"art_ok","path":"ok.txt","sha256":"%s","title":"OK renamed"}`, hexSHA("ok-content"))},
	})
	return setups
}

func materializePortSetup(t *testing.T, setup portSetup) string {
	t.Helper()
	roomDir := t.TempDir()
	if setup.RoomToml != nil {
		if err := os.WriteFile(filepath.Join(roomDir, "room.toml"), []byte(*setup.RoomToml), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, content := range setup.Files {
		if err := os.WriteFile(filepath.Join(roomDir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if setup.JournalLines != nil {
		journalDir := filepath.Join(roomDir, "journal")
		if err := os.MkdirAll(journalDir, 0o700); err != nil {
			t.Fatal(err)
		}
		if len(setup.JournalLines) > 0 {
			if err := os.WriteFile(filepath.Join(journalDir, setup.JournalFile), []byte(strings.Join(setup.JournalLines, "\n")+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	return roomDir
}

type portStep struct {
	Name    string          `json:"name,omitempty"`
	Request json.RawMessage `json:"request,omitempty"`
	// Response is the single response frame, or nil when the request produced
	// no output (notification).
	Response any     `json:"response"`
	Input    *string `json:"input,omitempty"`
	Output   *string `json:"output,omitempty"`
	Error    *string `json:"error,omitempty"`
}

type portScenario struct {
	Name           string              `json:"name"`
	Setup          string              `json:"setup"`
	Identity       bool                `json:"identity"`
	ArtifactRoot   string              `json:"artifact_root"`
	NormalizeEvent bool                `json:"normalize_events"`
	SymdeskScript  string              `json:"symdesk_script,omitempty"`
	SortArtifacts  bool                `json:"sort_artifact_lists"`
	Steps          []portStep          `json:"steps"`
	Journal        map[string][]string `json:"journal,omitempty"`

	// fixtureRaw is set only for a scenario this platform cannot run (see
	// skipFakeSymdesk): it carries the committed fixture scenario verbatim.
	fixtureRaw json.RawMessage
}

// MarshalJSON emits a skipped scenario's committed fixture bytes unchanged
// (MarshalIndent re-indents them identically) and any other scenario normally.
func (s portScenario) MarshalJSON() ([]byte, error) {
	if s.fixtureRaw != nil {
		return s.fixtureRaw, nil
	}
	type plainScenario portScenario
	return json.Marshal(plainScenario(s))
}

// skipFakeSymdesk is true where a `#!/bin/sh` fake `symdesk` cannot run: on
// Windows exec.LookPath needs a PATHEXT extension and cannot execute a shell
// script. Scenarios that install such a script are not executed there; check
// mode takes them verbatim from the committed fixture (so the byte comparison
// still covers every other scenario) and the Rust replay skips the same
// scenarios. Scenarios without a script still run on Windows: PATH is confined
// to an empty directory, so `symdesk` is not found and the document id is
// empty, exactly as in the Rust replay.
var skipFakeSymdesk = runtime.GOOS == "windows"

func skipsScenario(spec scenarioSpec) bool {
	return skipFakeSymdesk && spec.symdeskScript != ""
}

// committedScenario returns the fixture's recorded bytes for a scenario that
// this platform skips, after checking that the recorded fake script is the one
// the spec declares.
func committedScenario(t *testing.T, fixturePath string, spec scenarioSpec) portScenario {
	t.Helper()
	if os.Getenv("PORT_GENERATE") == "1" {
		t.Fatalf("scenario %s needs a fake symdesk script that cannot run on %s; generate fixtures on a Unix host", spec.name, runtime.GOOS)
	}
	content, err := os.ReadFile(fixturePath) //nolint:gosec // fixed repository fixture path
	if err != nil {
		t.Fatalf("read oracle fixture %s: %v", fixturePath, err)
	}
	var doc struct {
		Scenarios []json.RawMessage `json:"scenarios"`
	}
	if err := json.Unmarshal(content, &doc); err != nil {
		t.Fatalf("parse oracle fixture %s: %v", fixturePath, err)
	}
	for _, raw := range doc.Scenarios {
		var probe struct {
			Name          string `json:"name"`
			SymdeskScript string `json:"symdesk_script"`
		}
		if err := json.Unmarshal(raw, &probe); err != nil {
			t.Fatalf("parse scenario in %s: %v", fixturePath, err)
		}
		if probe.Name != spec.name {
			continue
		}
		if probe.SymdeskScript != spec.symdeskScript {
			t.Fatalf("scenario %s: fixture symdesk_script differs from the generator spec", spec.name)
		}
		return portScenario{Name: spec.name, Setup: spec.setup, fixtureRaw: raw}
	}
	t.Fatalf("scenario %s: missing from %s", spec.name, fixturePath)
	return portScenario{}
}

// scenarioSpec is the generator-side description of a scenario.
type scenarioSpec struct {
	name, setup   string
	identity      bool
	artifactRoot  string // "room" (default) or "testdata"
	normalize     bool
	sortArtifacts bool
	symdeskScript string // body of a fake `symdesk` on PATH; empty means no symdesk on PATH
	requests      []namedRequest
	streams       []namedStream
	recordJournal bool
}

type namedRequest struct {
	name string
	req  any
}

type namedStream struct{ name, input string }

func call(id int, tool string, args any) any {
	params := map[string]any{"name": tool}
	if args != nil {
		params["arguments"] = args
	}
	return map[string]any{"jsonrpc": "2.0", "id": id, "method": "tools/call", "params": params}
}

func frame(body string) string {
	return fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(body), body)
}

// frameExpanded frames a body that carries {{repeat:..}} / {{hex:..}} tokens:
// Content-Length is the length of the body after both expansions, while the
// fixture stores the compact token form.
func frameExpanded(body string) string {
	return fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(expandHexBytes(expandRepeat(body))), body)
}

func testdataRoomDir() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Clean(filepath.Join(filepath.Dir(file), "../../..", "testdata/port/room"))
}

func substitutePlaceholders(v any, replacements map[string]string) any {
	switch value := v.(type) {
	case string:
		for from, to := range replacements {
			value = strings.ReplaceAll(value, from, to)
		}
		return value
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = substitutePlaceholders(item, replacements)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(value))
		for k, item := range value {
			out[k] = substitutePlaceholders(item, replacements)
		}
		return out
	default:
		return v
	}
}

// maskJSONStringValue replaces the value of every `"key":"..."` pair in a
// compact JSON text, preserving all other bytes (and therefore key order).
func maskJSONStringValue(text, key, replacement string) string {
	needle := `"` + key + `":"`
	var out strings.Builder
	for {
		i := strings.Index(text, needle)
		if i < 0 {
			out.WriteString(text)
			return out.String()
		}
		start := i + len(needle)
		end := strings.IndexByte(text[start:], '"')
		if end < 0 {
			out.WriteString(text)
			return out.String()
		}
		out.WriteString(text[:start])
		out.WriteString(replacement)
		text = text[start+end:]
	}
}

// maskGeneratedTimestamps masks `ts` only for note.posted events, the one kind
// whose Go writer stamps a wall-clock time (room.PostNote). Go leaves `ts` empty
// for run.requested, checkpoint.requested and artifact.linked, so those events
// keep their literal `"ts":""` (or the deterministic setup timestamp) in the
// fixture: a Rust implementation that stamped a wall-clock time would diverge.
// The text is an event, an array of events, or a journal line; element bytes are
// preserved.
func maskGeneratedTimestamps(text string) string {
	trimmed := strings.TrimSpace(text)
	if strings.HasPrefix(trimmed, "[") {
		var items []json.RawMessage
		if err := json.Unmarshal([]byte(trimmed), &items); err == nil {
			parts := make([]string, len(items))
			for i, item := range items {
				parts[i] = maskEventTimestamp(string(item))
			}
			return "[" + strings.Join(parts, ",") + "]"
		}
	}
	return maskEventTimestamp(text)
}

func maskEventTimestamp(text string) string {
	var probe struct {
		Kind string `json:"kind"`
	}
	if err := json.Unmarshal([]byte(text), &probe); err != nil || probe.Kind != event.KindNotePosted {
		return text
	}
	return maskJSONStringValue(text, "ts", "<ts>")
}

// normalizeEventText masks the fields that differ per run (ts, sig, prev, and
// the generated ids of note and checkpoint events) without re-marshalling, so
// the byte order of the event JSON stays pinned.
func normalizeEventText(text string) string {
	if !strings.Contains(text, `"kind":"`) {
		return text
	}
	text = maskGeneratedTimestamps(text)
	text = maskJSONStringValue(text, "sig", "<sig>")
	text = maskJSONStringValue(text, "prev", "<prev>")
	if strings.Contains(text, `"kind":"note.posted"`) || strings.Contains(text, `"kind":"checkpoint.requested"`) {
		text = maskJSONStringValue(text, "id", "ev_<generated>")
		text = maskJSONStringValue(text, "checkpoint_id", "chk_<generated>")
	}
	return text
}

// sortArtifactRefs orders a room_artifact_list result by id. Go builds the list
// from a map, so its order is nondeterministic; the fixture and the Rust test
// compare the list sorted by id. Element bytes (and their key order) are kept.
func sortArtifactRefs(text string) string {
	var items []json.RawMessage
	if err := json.Unmarshal([]byte(text), &items); err != nil || len(items) == 0 {
		return text
	}
	type keyed struct {
		id  string
		raw json.RawMessage
	}
	keyedItems := make([]keyed, 0, len(items))
	for _, item := range items {
		var probe struct {
			ID     *string `json:"id"`
			Status *string `json:"status"`
		}
		if err := json.Unmarshal(item, &probe); err != nil || probe.ID == nil || probe.Status == nil {
			return text
		}
		keyedItems = append(keyedItems, keyed{*probe.ID, item})
	}
	sort.SliceStable(keyedItems, func(i, j int) bool { return keyedItems[i].id < keyedItems[j].id })
	parts := make([]string, len(keyedItems))
	for i, item := range keyedItems {
		parts[i] = string(item.raw)
	}
	return "[" + strings.Join(parts, ",") + "]"
}

func normalizeOutput(s string, scenario *scenarioSpec, roomDir, artifactRoot string) string {
	if scenario.sortArtifacts {
		s = sortArtifactRefs(s)
	}
	if scenario.normalize {
		s = normalizeEventText(s)
	}
	if artifactRoot != roomDir {
		s = strings.ReplaceAll(s, artifactRoot, "${ARTIFACT_ROOT}")
	}
	s = strings.ReplaceAll(s, roomDir, "${ROOM}")
	return unixRecordedOSErrors(s, runtime.GOOS == "windows")
}

// windowsOSErrorSuffixes pairs the unix wording the fixtures were recorded with
// against what Go's own Windows build prints for the same failure
// (syscall.Errno.Error()): ERROR_FILE_NOT_FOUND for a missing file and
// ERROR_INVALID_FUNCTION for reading a directory handle.
var windowsOSErrorSuffixes = [...]struct{ unix, windows string }{
	{": no such file or directory", ": The system cannot find the file specified."},
	{": is a directory", ": Incorrect function."},
}

// unixRecordedOSErrors maps Go's Windows OS-error wording, and the `\`
// separators of the `${...}` placeholder path in front of it, back to the unix
// form the committed fixtures record, so the byte comparison stays meaningful
// on Windows. Only the two known error suffixes that directly follow a
// placeholder path are rewritten; all other text is untouched. The Rust replay
// applies the inverse translation to the recorded fixture
// (windows_go_error_text in crates/symroom-cli/tests/mcp.rs).
func unixRecordedOSErrors(s string, windows bool) string {
	if !windows {
		return s
	}
	for _, suffix := range windowsOSErrorSuffixes {
		var out strings.Builder
		rest := s
		for {
			index := strings.Index(rest, suffix.windows)
			if index < 0 {
				break
			}
			head := rest[:index]
			start := 0
			if delimiter := strings.LastIndexFunc(head, func(r rune) bool { return !isPlaceholderPathRune(r) }); delimiter >= 0 {
				_, width := utf8.DecodeRuneInString(head[delimiter:])
				start = delimiter + width
			}
			out.WriteString(head[:start])
			token := head[start:]
			if strings.HasPrefix(token, "${") {
				out.WriteString(strings.ReplaceAll(token, `\`, "/"))
				out.WriteString(suffix.unix)
			} else {
				out.WriteString(token)
				out.WriteString(suffix.windows)
			}
			rest = rest[index+len(suffix.windows):]
		}
		out.WriteString(rest)
		s = out.String()
	}
	return s
}

func isPlaceholderPathRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune(`._-/\${}`, r)
}

func normalizeResponseTree(v any, scenario *scenarioSpec, roomDir, artifactRoot string) any {
	switch value := v.(type) {
	case string:
		return normalizeOutput(value, scenario, roomDir, artifactRoot)
	case []any:
		out := make([]any, len(value))
		for i, item := range value {
			out[i] = normalizeResponseTree(item, scenario, roomDir, artifactRoot)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(value))
		for k, item := range value {
			out[k] = normalizeResponseTree(item, scenario, roomDir, artifactRoot)
		}
		return out
	default:
		return v
	}
}

func serveRaw(roomDir string, id *identity.Identity, artifactRoot string, input []byte) ([]byte, string) {
	var output bytes.Buffer
	err := NewServer(roomDir, id, artifactRoot).ServeIO(context.Background(), bytes.NewReader(input), &output)
	if err != nil {
		return output.Bytes(), err.Error()
	}
	return output.Bytes(), ""
}

func runPortScenario(t *testing.T, spec scenarioSpec, setups map[string]portSetup) portScenario {
	t.Helper()
	setup, ok := setups[spec.setup]
	if !ok {
		t.Fatalf("scenario %s: unknown setup %s", spec.name, spec.setup)
	}
	roomDir := materializePortSetup(t, setup)
	artifactRoot := roomDir
	artifactRootName := "room"
	if spec.artifactRoot == "testdata" {
		artifactRoot = testdataRoomDir()
		artifactRootName = "testdata"
	}
	var id *identity.Identity
	if spec.identity {
		id = mutationSigningIdentity()
	}
	replacements := map[string]string{
		"${ROOM}":          roomDir,
		"${ARTIFACT_ROOT}": artifactRoot,
		"${ARTIFACT}":      filepath.Join(testdataRoomDir(), "mcp-artifact.txt"),
	}
	out := portScenario{Name: spec.name, Setup: spec.setup, Identity: spec.identity, ArtifactRoot: artifactRootName, NormalizeEvent: spec.normalize, SortArtifacts: spec.sortArtifacts, SymdeskScript: spec.symdeskScript}
	useScenarioPath(t, spec.symdeskScript)
	for _, step := range spec.requests {
		body, err := json.Marshal(substitutePlaceholders(step.req, replacements))
		if err != nil {
			t.Fatal(err)
		}
		output, serveErr := serveRaw(roomDir, id, artifactRoot, []byte(frame(string(body))))
		if serveErr != "" {
			t.Fatalf("scenario %s step %s: ServeIO: %s", spec.name, step.name, serveErr)
		}
		frames, err := decodeFrames(output)
		if err != nil {
			t.Fatalf("scenario %s step %s: %v", spec.name, step.name, err)
		}
		if len(frames) > 1 {
			t.Fatalf("scenario %s step %s: %d frames", spec.name, step.name, len(frames))
		}
		var response any
		if len(frames) == 1 {
			response = normalizeResponseTree(frames[0], &spec, roomDir, artifactRoot)
		}
		requestJSON, err := json.Marshal(step.req)
		if err != nil {
			t.Fatal(err)
		}
		out.Steps = append(out.Steps, portStep{Name: step.name, Request: requestJSON, Response: response})
	}
	for _, step := range spec.streams {
		output, serveErr := serveRaw(roomDir, id, artifactRoot, expandHexBytes(substitutePlaceholdersString(step.input, replacements)))
		input := step.input
		outputText := normalizeOutput(string(output), &spec, roomDir, artifactRoot)
		errorText := normalizeOutput(serveErr, &spec, roomDir, artifactRoot)
		out.Steps = append(out.Steps, portStep{Name: step.name, Input: &input, Output: &outputText, Error: &errorText})
	}
	if spec.recordJournal {
		out.Journal = map[string][]string{}
		entries, err := os.ReadDir(filepath.Join(roomDir, "journal"))
		if err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
		sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
		for _, entry := range entries {
			content, err := os.ReadFile(filepath.Join(roomDir, "journal", entry.Name())) //nolint:gosec // t.TempDir scenario room
			if err != nil {
				t.Fatal(err)
			}
			var lines []string
			for _, line := range strings.Split(strings.TrimSuffix(string(content), "\n"), "\n") {
				lines = append(lines, normalizeOutput(line, &spec, roomDir, artifactRoot))
			}
			out.Journal[entry.Name()] = lines
		}
	}
	return out
}

// useScenarioPath confines PATH to a scenario-owned directory, so an installed
// `symdesk` can never leak into a fixture. A non-empty script is installed as an
// executable fake `symdesk`; the Rust replay recreates the identical script.
func useScenarioPath(t *testing.T, script string) {
	t.Helper()
	dir := t.TempDir()
	if script != "" {
		body := "#!/bin/sh\n" + script + "\n"
		if err := os.WriteFile(filepath.Join(dir, "symdesk"), []byte(body), 0o700); err != nil { //nolint:gosec // executable fake symdesk in a test-owned directory
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
}

func substitutePlaceholdersString(s string, replacements map[string]string) string {
	for from, to := range replacements {
		s = strings.ReplaceAll(s, from, to)
	}
	return expandRepeat(s)
}

// expandHexBytes expands {{hex:AABB}} tokens into raw bytes, so a stream input
// can carry invalid UTF-8 that a JSON fixture string cannot store.
func expandHexBytes(s string) []byte {
	const open = "{{hex:"
	var out []byte
	for {
		i := strings.Index(s, open)
		if i < 0 {
			return append(out, s...)
		}
		out = append(out, s[:i]...)
		rest := s[i+len(open):]
		end := strings.Index(rest, "}}")
		raw, err := hex.DecodeString(rest[:end])
		if err != nil {
			panic(err)
		}
		out = append(out, raw...)
		s = rest[end+2:]
	}
}

// expandRepeat expands {{repeat:C:N}} tokens (single character C, N times) so
// large inputs do not have to be stored verbatim in the fixture.
func expandRepeat(s string) string {
	const open = "{{repeat:"
	for {
		i := strings.Index(s, open)
		if i < 0 {
			return s
		}
		rest := s[i+len(open):]
		end := strings.Index(rest, "}}")
		var char string
		var count int
		if _, err := fmt.Sscanf(strings.Replace(rest[:end], ":", " ", 1), "%s %d", &char, &count); err != nil {
			panic(err)
		}
		s = s[:i] + strings.Repeat(char, count) + rest[end+2:]
	}
}

func runPortScenarios(t *testing.T, fixturePath string, specs []scenarioSpec, setups map[string]portSetup) []portScenario {
	t.Helper()
	out := make([]portScenario, 0, len(specs))
	for _, spec := range specs {
		if skipsScenario(spec) {
			out = append(out, committedScenario(t, fixturePath, spec))
			continue
		}
		out = append(out, runPortScenario(t, spec, setups))
	}
	return out
}

// usedSetups returns only the setups referenced by scenarios, keeping the
// fixture small and deterministic.
func usedSetups(scenarios []portScenario, setups map[string]portSetup) map[string]portSetup {
	used := map[string]portSetup{}
	for _, scenario := range scenarios {
		used[scenario.Setup] = setups[scenario.Setup]
	}
	return used
}

func parityScenarioSpecs() []scenarioSpec {
	ping := `{"jsonrpc":"2.0","id":9,"method":"ping"}`
	list := `{"jsonrpc":"2.0","id":4,"method":"tools/list"}`
	tool := func(id int, name string, args any) namedRequest {
		return namedRequest{fmt.Sprintf("%s#%d", name, id), call(id, name, args)}
	}
	var protocolRequests []namedRequest
	rawRequests := []struct {
		name string
		req  any
	}{
		{"batch_empty", []any{}},
		{"batch_one_ping", []any{map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping"}}},
		{"empty_object", map[string]any{}},
		{"string_value", "ping"},
		{"number_value", 7},
		{"null_value", nil},
		{"bool_value", true},
		{"missing_jsonrpc", map[string]any{"id": 1, "method": "ping"}},
		{"jsonrpc_1_0", map[string]any{"jsonrpc": "1.0", "id": 1, "method": "ping"}},
		{"jsonrpc_number", map[string]any{"jsonrpc": 2.0, "id": 1, "method": "ping"}},
		{"missing_method", map[string]any{"jsonrpc": "2.0", "id": 1}},
		{"empty_method", map[string]any{"jsonrpc": "2.0", "id": 1, "method": ""}},
		{"method_number", map[string]any{"jsonrpc": "2.0", "id": 1, "method": 5}},
		{"id_object", map[string]any{"jsonrpc": "2.0", "id": map[string]any{"a": 1}, "method": "ping"}},
		{"id_bool", map[string]any{"jsonrpc": "2.0", "id": true, "method": "ping"}},
		{"id_array", map[string]any{"jsonrpc": "2.0", "id": []any{1}, "method": "ping"}},
		{"id_null", map[string]any{"jsonrpc": "2.0", "id": nil, "method": "ping"}},
		{"id_string", map[string]any{"jsonrpc": "2.0", "id": "abc", "method": "ping"}},
		{"id_float", map[string]any{"jsonrpc": "2.0", "id": 1.5, "method": "ping"}},
		{"params_string", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping", "params": "x"}},
		{"params_number", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping", "params": 3}},
		{"params_null", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping", "params": nil}},
		{"params_array_ping", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "ping", "params": []any{}}},
		{"uppercase_keys", map[string]any{"JSONRPC": "2.0", "METHOD": "ping", "ID": 1}},
		{"mixed_case_keys", map[string]any{"jsonrpc": "2.0", "Method": "ping", "id": 12}},
		{"initialize_plain", map[string]any{"jsonrpc": "2.0", "id": 1, "method": "initialize"}},
		{"initialize_params", map[string]any{"jsonrpc": "2.0", "id": 2, "method": "initialize", "params": map[string]any{"protocolVersion": "2025-03-26", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "t", "version": "1"}}}},
		{"ping_params", map[string]any{"jsonrpc": "2.0", "id": 3, "method": "ping", "params": map[string]any{}}},
		{"unknown_method", map[string]any{"jsonrpc": "2.0", "id": 4, "method": "resources/list"}},
		{"unknown_method_slash", map[string]any{"jsonrpc": "2.0", "id": 5, "method": "tools/unknown"}},
		{"prompts_list", map[string]any{"jsonrpc": "2.0", "id": 6, "method": "prompts/list"}},
		{"method_case", map[string]any{"jsonrpc": "2.0", "id": 7, "method": "Tools/List"}},
		{"notification_initialized", map[string]any{"jsonrpc": "2.0", "method": "notifications/initialized"}},
		{"notification_unknown", map[string]any{"jsonrpc": "2.0", "method": "no/such"}},
		{"notification_tools_list", map[string]any{"jsonrpc": "2.0", "method": "tools/list"}},
		{"notification_ping_params", map[string]any{"jsonrpc": "2.0", "method": "ping", "params": map[string]any{"x": 1}}},
		{"notification_invalid_params", map[string]any{"jsonrpc": "2.0", "method": "ping", "params": 5}},
		{"notification_status_call", map[string]any{"jsonrpc": "2.0", "method": "tools/call", "params": map[string]any{"name": "room_status"}}},
		{"tools_list_params", map[string]any{"jsonrpc": "2.0", "id": 8, "method": "tools/list", "params": map[string]any{"cursor": "x"}}},
		{"call_no_params", map[string]any{"jsonrpc": "2.0", "id": 10, "method": "tools/call"}},
		{"call_params_array", map[string]any{"jsonrpc": "2.0", "id": 11, "method": "tools/call", "params": []any{}}},
		{"call_params_array_nonempty", map[string]any{"jsonrpc": "2.0", "id": 12, "method": "tools/call", "params": []any{"room_status"}}},
		{"call_params_empty_object", map[string]any{"jsonrpc": "2.0", "id": 13, "method": "tools/call", "params": map[string]any{}}},
		{"call_name_number", map[string]any{"jsonrpc": "2.0", "id": 14, "method": "tools/call", "params": map[string]any{"name": 5}}},
		{"call_name_null", map[string]any{"jsonrpc": "2.0", "id": 15, "method": "tools/call", "params": map[string]any{"name": nil}}},
		{"call_name_empty", map[string]any{"jsonrpc": "2.0", "id": 16, "method": "tools/call", "params": map[string]any{"name": ""}}},
		{"call_name_object", map[string]any{"jsonrpc": "2.0", "id": 17, "method": "tools/call", "params": map[string]any{"name": map[string]any{}}}},
		{"call_meta_string", map[string]any{"jsonrpc": "2.0", "id": 18, "method": "tools/call", "params": map[string]any{"name": "room_status", "_meta": "x"}}},
		{"call_meta_object", map[string]any{"jsonrpc": "2.0", "id": 19, "method": "tools/call", "params": map[string]any{"name": "room_status", "_meta": map[string]any{"progressToken": 1}}}},
		{"call_name_case", map[string]any{"jsonrpc": "2.0", "id": 20, "method": "tools/call", "params": map[string]any{"name": "ROOM_STATUS"}}},
		{"call_name_upper_key", map[string]any{"jsonrpc": "2.0", "id": 21, "method": "tools/call", "params": map[string]any{"Name": "room_status"}}},
	}
	for _, raw := range rawRequests {
		protocolRequests = append(protocolRequests, namedRequest{raw.name, raw.req})
	}
	for i, name := range []string{"room_approve", "room_run_approve", "room_approval_grant", "room_approval_approve", "room_checkpoint_resolve", "room_run_deny", "room_run_start", "room_member_add", "room_note_delete", "room_decide"} {
		protocolRequests = append(protocolRequests, namedRequest{"no_approval_tool_" + name, call(100+i, name, map[string]any{"run_id": "run_fixture"})})
	}

	parseBodies := []string{
		`{bad}`, `{"a":1,}`, `{"a" 1}`, `{"a":1 "b":2}`, `[1,2`, `tru`, `"str`, `{"a":"\q"}`, `{"a":"\u00zz"}`, `01`, `1.`, `1e`, `-`, `nulx`, `{`, `[`, `{"jsonrpc":"2.0","id":1,"method":"ping"} x`, `{"jsonrpc":"2.0",`, `}`, `]`, `,`, `:`, `'a'`, `{"a":tru}`, `{"a":1.e2}`, "{\"a\":\"\x01\"}", "{\"a\":\"line\nbreak\"}", `{"a":1}}`, `{"a":[1,]}`, `{,}`,
	}
	var parseFramed, parseLine strings.Builder
	for _, body := range parseBodies {
		parseFramed.WriteString(frame(body))
		parseLine.WriteString(body + "\n")
	}
	parseFramed.WriteString(frame(ping))
	parseLine.WriteString(ping + "\n")

	idsStream := frame(`{"jsonrpc":"2.0","id":1e3,"method":"ping"}`) + frame(`{"jsonrpc":"2.0","id":12345678901234567890,"method":"ping"}`) + frame(`{"jsonrpc":"2.0","id":-0,"method":"ping"}`) + frame(`{"jsonrpc":"2.0","id":1.0,"method":"ping"}`) + frame(`{"jsonrpc":"2.0","id":"a\u003cb","method":"ping"}`) + frame(`{"jsonrpc":"2.0","id":1E+2,"method":"ping"}`)

	streams := []namedStream{
		{"initialize_ping_list_framed", frame(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"t","version":"1"}}}`) + frame(`{"jsonrpc":"2.0","method":"notifications/initialized"}`) + frame(ping) + frame(list)},
		{"notifications_are_silent", frame(`{"jsonrpc":"2.0","method":"notifications/initialized"}`) + frame(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1}}`) + frame(`{"jsonrpc":"2.0","method":"no/such"}`) + frame(`{"jsonrpc":"2.0","method":"tools/list"}`) + frame(ping)},
		{"line_mode", ping + "\n" + list + "\n" + `{"jsonrpc":"2.0","id":1,"method":"initialize"}` + "\n"},
		{"line_mode_crlf_and_blank_lines", "\n\r\n" + ping + "\r\n\n" + list + "\r\n"},
		{"line_mode_then_framed_then_line", ping + "\n" + frame(list) + ping + "\n"},
		{"line_mode_notification", `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" + ping + "\n"},
		{"line_mode_non_object_starters", `[]` + "\n" + `123` + "\n" + `"s"` + "\n" + `true` + "\n" + `null` + "\n" + ping + "\n"},
		{"framed_extra_headers", "Content-Type: application/json\r\nContent-Length: " + fmt.Sprint(len(ping)) + "\r\nX-Other: 1\r\n\r\n" + ping},
		{"framed_lf_only_headers", "Content-Length: " + fmt.Sprint(len(ping)) + "\n\n" + ping},
		{"framed_last_content_length_wins", "Content-Length: 3\r\nContent-Length: " + fmt.Sprint(len(ping)) + "\r\n\r\n" + ping},
		{"framed_length_with_spaces", "Content-Length:   " + fmt.Sprint(len(ping)) + "  \r\n\r\n" + ping},
		{"framed_plus_length", "Content-Length: +" + fmt.Sprint(len(ping)) + "\r\n\r\n" + ping},
		{"framed_leading_blank_lines", "\r\n\r\n" + frame(ping)},
		{"framed_lowercase_header", "content-length: " + fmt.Sprint(len(ping)) + "\r\n\r\n" + ping},
		{"parse_errors_framed", parseFramed.String()},
		{"parse_errors_line_mode", parseLine.String()},
		{"request_ids_preserved", idsStream},
		{"unicode_and_escapes", frame(`{"jsonrpc":"2.0","id":"\u00e9\ud83d\ude00<>&","method":"ping"}`) + frame(`{"jsonrpc":"2.0","id":2,"method":"x\u00e9<>&"}`)},
		{"error_missing_content_length", "X-Header: 1\r\n\r\n" + ping},
		{"error_missing_content_length_then_eof", "X-Header: 1\r\n\r\n"},
		{"error_invalid_content_length", "Content-Length: abc\r\n\r\n" + ping},
		{"error_negative_content_length", "Content-Length: -5\r\n\r\n" + ping},
		{"error_zero_content_length", "Content-Length: 0\r\n\r\n"},
		{"error_oversized_content_length", "Content-Length: 1048577\r\n\r\n"},
		{"error_invalid_content_length_in_later_header", "X-A: 1\r\nContent-Length: nope\r\n\r\n" + ping},
		{"error_truncated_body", "Content-Length: 100\r\n\r\n" + ping},
		{"error_truncated_body_after_valid_frame", frame(ping) + "Content-Length: 100\r\n\r\n{"},
		{"clean_eof_inside_headers", frame(ping) + "Content-Length: 5\r\n"},
		{"clean_eof_after_header_line", frame(ping) + "Content-Length: 5"},
		{"clean_eof_garbage_line", frame(ping) + "hello"},
		{"clean_eof_empty", ""},
		{"clean_eof_blank_lines_only", "\r\n\r\n\n"},
		{"error_too_many_headers", func() string {
			var b strings.Builder
			for i := 0; i < 101; i++ {
				fmt.Fprintf(&b, "X-%d: v\r\n", i)
			}
			return b.String() + "Content-Length: 1\r\n\r\n{"
		}()},
		{"error_header_bytes_limit", "X-Big: {{repeat:a:66560}}\r\nContent-Length: 1\r\n\r\n{"},
		{"error_line_too_long", "{{repeat:a:1048586}}"},
		{"recoverable_error_then_valid", frame(`{}`) + frame(`[]`) + frame(`{bad}`) + frame(ping)},
		{"error_empty_body_eof", "Content-Length: 5\r\n\r\n"},
		{"sync_response_not_blocked_by_inflight_tool", frame(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"room_run_wait","arguments":{"run_id":"run_missing","timeout_seconds":0.3}}}`) + frame(ping)},
		{"parse_error_waits_for_inflight_tool", frame(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"room_run_wait","arguments":{"run_id":"run_missing","timeout_seconds":0.3}}}`) + frame(`{bad}`) + frame(ping)},
		{"invalid_request_waits_for_inflight_tool", frame(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"room_run_wait","arguments":{"run_id":"run_missing","timeout_seconds":0.3}}}`) + frame(`[]`) + frame(ping)},
		{"fatal_read_error_cancels_inflight_wait", frame(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"room_run_wait","arguments":{"run_id":"run_missing","timeout_seconds":30}}}`) + "Content-Length: abc\r\n\r\n"},
		{"eof_lets_inflight_wait_finish", frame(`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"room_run_wait","arguments":{"run_id":"run_missing","timeout_seconds":0.3}}}`)},
		{"ping_after_non_ascii_header_garbage", "X-Y: \xc3\xa9\r\nContent-Length: " + fmt.Sprint(len(ping)) + "\r\n\r\n" + ping},
	}

	toolsReadOnly := []namedRequest{
		tool(1, "room_status", map[string]any{}),
		tool(2, "room_status", nil),
		tool(3, "room_status", map[string]any{"ignored": true}),
		tool(4, "room_status", 5),
		tool(5, "room_journal_tail", nil),
		tool(6, "room_journal_tail", map[string]any{}),
		tool(7, "room_journal_tail", map[string]any{"limit": 1}),
		tool(8, "room_journal_tail", map[string]any{"limit": 0}),
		tool(9, "room_journal_tail", map[string]any{"limit": -3}),
		tool(10, "room_journal_tail", map[string]any{"limit": "2"}),
		tool(11, "room_journal_tail", map[string]any{"limit": 2.5}),
		tool(12, "room_journal_tail", map[string]any{"limit": 100}),
		tool(13, "room_journal_tail", map[string]any{"LIMIT": 1}),
		tool(14, "room_journal_tail", []any{1}),
		tool(15, "room_journal_tail", "x"),
		tool(16, "room_journal_tail", map[string]any{"limit": nil}),
		tool(17, "room_journal_tail", map[string]any{"limit": 1e1}),
		tool(18, "room_journal_tail", map[string]any{"limit": true}),
		tool(19, "room_artifact_list", nil),
		tool(20, "room_artifact_list", map[string]any{"x": 1}),
		tool(21, "room_run_wait", nil),
		tool(22, "room_run_wait", map[string]any{}),
		tool(23, "room_run_wait", map[string]any{"run_id": ""}),
		tool(24, "room_run_wait", map[string]any{"run_id": 5}),
		tool(25, "room_run_wait", map[string]any{"run_id": "run_fixture"}),
		tool(26, "room_run_wait", map[string]any{"run_id": "run_fixture", "timeout_seconds": 0}),
		tool(27, "room_run_wait", map[string]any{"run_id": "run_fixture", "timeout_seconds": "5"}),
		tool(28, "room_run_wait", map[string]any{"run_id": "run_missing", "timeout_seconds": 0.02}),
		tool(29, "room_run_wait", map[string]any{"run_id": "run_missing", "timeout_seconds": 1e-9}),
		tool(30, "room_run_wait", map[string]any{"run_id": "run_missing", "timeout_seconds": []any{}}),
		tool(31, "room_run_wait", map[string]any{"RUN_ID": "run_fixture"}),
		tool(32, "room_run_wait", "run_fixture"),
		tool(33, "room_run_wait", map[string]any{"run_id": "run_fixture", "timeout_seconds": nil}),
	}
	pick := func(indexes ...int) []namedRequest {
		var out []namedRequest
		for _, i := range indexes {
			out = append(out, toolsReadOnly[i])
		}
		return out
	}
	errorsOnly := pick(0, 4, 18, 20, 27)
	return []scenarioSpec{
		{name: "protocol_requests", setup: "fixture", requests: protocolRequests},
		{name: "protocol_streams", setup: "fixture", streams: streams},
		{name: "read_tools_fixture_room", setup: "fixture", requests: toolsReadOnly},
		{name: "read_tools_empty_dir", setup: "empty_dir", requests: errorsOnly},
		{name: "read_tools_room_toml_only", setup: "room_toml_only", requests: pick(0, 4, 5, 6, 18, 20, 27)},
		{name: "read_tools_journal_only", setup: "journal_only", requests: pick(0, 4, 5, 6, 18, 20, 27)},
		{name: "run_wait_projected_states", setup: "owner_room", requests: []namedRequest{
			tool(1, "room_run_wait", map[string]any{"run_id": "run_ok", "timeout_seconds": 0.05}),
			tool(2, "room_run_wait", map[string]any{"run_id": "run_cancel", "timeout_seconds": 0.05}),
			tool(3, "room_run_wait", map[string]any{"run_id": "run_deny", "timeout_seconds": 0.05}),
			tool(4, "room_run_wait", map[string]any{"run_id": "run_pending", "timeout_seconds": 0.05}),
			tool(5, "room_run_wait", map[string]any{"run_id": "run_started", "timeout_seconds": 0.05}),
			tool(6, "room_run_wait", map[string]any{"run_id": "run_done", "timeout_seconds": 0.05}),
			tool(7, "room_run_wait", map[string]any{"run_id": "run_failed", "timeout_seconds": 0.05}),
			tool(8, "room_run_wait", map[string]any{"run_id": "run_minimal", "timeout_seconds": 0.05}),
			tool(9, "room_run_wait", map[string]any{"run_id": "run_unknown", "timeout_seconds": 0.05}),
			tool(10, "room_run_wait", map[string]any{"run_id": "run_ok"}),
			tool(11, "room_status", nil),
		}},
		{name: "html_escaping_in_signed_journal", setup: "html_notes", requests: []namedRequest{
			tool(1, "room_journal_tail", nil),
			tool(2, "room_journal_tail", map[string]any{"limit": 1}),
		}},
		{name: "journal_tail_30_notes", setup: "tail_30", requests: []namedRequest{
			tool(1, "room_status", nil),
			tool(2, "room_journal_tail", nil),
			tool(3, "room_journal_tail", map[string]any{"limit": 5}),
			tool(4, "room_journal_tail", map[string]any{"limit": 30}),
			tool(5, "room_journal_tail", map[string]any{"limit": 31}),
			tool(6, "room_journal_tail", map[string]any{"limit": 20}),
			tool(7, "room_journal_tail", map[string]any{"limit": 21}),
		}},
		{name: "artifact_list_states", setup: "artifacts", sortArtifacts: true, requests: []namedRequest{
			tool(1, "room_artifact_list", nil),
			tool(2, "room_status", nil),
		}},
		{name: "json_layer_streams", setup: "fixture", streams: jsonLayerStreams()},
	}
}

// jsonLayerStreams pins the encoding/json behaviour that sits under the JSON-RPC
// layer: invalid UTF-8, lone surrogate escapes, the 10000 container nesting
// limit (and valid input deeper than serde_json's recursion limit), and
// requests exactly at the 1 MiB limits. Inputs use {{hex:..}}/{{repeat:..}}
// tokens so the fixture can store them.
func jsonLayerStreams() []namedStream {
	ping := `{"jsonrpc":"2.0","id":9,"method":"ping"}`
	deep := func(n int) string { return fmt.Sprintf("{{repeat:[:%d}}{{repeat:]:%d}}", n, n) }
	const prefix = `{"jsonrpc":"2.0","id":1,"method":"ping","pad":"`
	const suffix = `"}`
	// A request of exactly n bytes: pad the fixed members with 'a'.
	exact := func(n int) string { return prefix + fmt.Sprintf("{{repeat:a:%d}}", n-len(prefix)-len(suffix)) + suffix }
	toolCall := func(id int, name, args string) string {
		return fmt.Sprintf(`{"jsonrpc":"2.0","id":%d,"method":"tools/call","params":{"name":"%s","arguments":%s}}`, id, name, args)
	}
	return []namedStream{
		{"invalid_utf8_method_framed", frameExpanded(`{"jsonrpc":"2.0","id":1,"method":"x{{hex:ff}}y"}`) + frame(ping)},
		{"invalid_utf8_truncated_sequence_method", frameExpanded(`{"jsonrpc":"2.0","id":1,"method":"x{{hex:e282}}y"}`)},
		{"invalid_utf8_id_string", frameExpanded(`{"jsonrpc":"2.0","id":"a{{hex:80}}b","method":"ping"}`)},
		{"invalid_utf8_method_line_mode", `{"jsonrpc":"2.0","id":2,"method":"m{{hex:ff}}"}` + "\n" + ping + "\n"},
		{"invalid_utf8_tool_name", frameExpanded(toolCall(3, "room_{{hex:c0}}x", `{}`))},
		{"invalid_utf8_tool_argument_ignored", frameExpanded(toolCall(4, "room_journal_tail", `{"limit":1,"extra":"a{{hex:ff}}b"}`))},
		{"invalid_utf8_outside_string", frameExpanded(`{"jsonrpc":"2.0","id":1,"method":"ping"}{{hex:ff}}`)},
		{"lone_high_surrogate_id", frame(`{"jsonrpc":"2.0","id":"\ud800","method":"ping"}`)},
		{"lone_low_surrogate_id", frame(`{"jsonrpc":"2.0","id":"\udc00x","method":"ping"}`)},
		{"high_surrogate_then_non_low", frame(`{"jsonrpc":"2.0","id":"\ud800\u0041","method":"ping"}`)},
		{"high_surrogate_then_high_surrogate", frame(`{"jsonrpc":"2.0","id":"\ud800\ud800\ude00","method":"ping"}`)},
		{"valid_surrogate_pair_id", frame(`{"jsonrpc":"2.0","id":"\ud83d\ude00","method":"ping"}`)},
		{"lone_surrogate_method", frame(`{"jsonrpc":"2.0","id":1,"method":"m\ud800"}`)},
		{"lone_surrogate_tool_name", frame(toolCall(5, `room_\udfff`, `{}`))},
		{"nesting_exactly_at_limit_10000", frameExpanded(`{"jsonrpc":"2.0","id":1,"method":"ping","params":` + deep(9999) + `}`)},
		{"nesting_over_limit_10001", frameExpanded(`{"jsonrpc":"2.0","id":1,"method":"ping","params":` + deep(10000) + `}`)},
		{"nesting_over_limit_line_mode", `{"jsonrpc":"2.0","id":1,"method":"ping","params":` + deep(10000) + `}` + "\n"},
		{"nesting_200_in_tool_arguments", frameExpanded(toolCall(6, "room_status", `{"extra":`+deep(200)+`}`))},
		{"nesting_200_in_unused_member", frameExpanded(`{"jsonrpc":"2.0","id":1,"method":"ping","extra":` + deep(200) + `}`)},
		{"nesting_200_in_meta", frameExpanded(`{"jsonrpc":"2.0","id":7,"method":"tools/call","params":{"name":"room_status","_meta":{"a":` + deep(200) + `}}}`)},
		{"nesting_200_in_typed_argument", frameExpanded(toolCall(8, "room_journal_tail", `{"limit":`+deep(200)+`}`))},
		{"nesting_200_in_id", frameExpanded(`{"jsonrpc":"2.0","id":` + deep(200) + `,"method":"ping"}`)},
		{"timeout_seconds_overflowing_float", frame(toolCall(10, "room_run_wait", `{"run_id":"run_missing","timeout_seconds":1e400}`))},
		{"limit_overflowing_float", frame(toolCall(11, "room_journal_tail", `{"limit":1e400}`))},
		{"error_invalid_content_length_invalid_utf8_first_header", "Content-Length: 1{{hex:ff}}2\r\n\r\n"},
		{"error_invalid_content_length_invalid_utf8_later_header", "X-A: 1\r\nContent-Length: {{hex:e282}}\r\n\r\n" + ping},
		{"frame_exactly_1mib", frameExpanded(exact(1<<20)) + frame(ping)},
		{"line_exactly_1mib", exact(1<<20-1) + "\n" + ping + "\n"},
		{"line_one_byte_over_1mib", exact(1<<20) + "\n"},
		{"line_exactly_1mib_at_eof_without_newline", exact(1 << 20)},
	}
}

// symdeskScenario links the fixture artifact twice (the second time through a
// lexically different spelling of the same path), then reads the result back,
// with `script` installed as the only `symdesk` on PATH.
func symdeskScenario(name, script string, spelledTwice bool) scenarioSpec {
	tool := func(id int, tool string, args any) namedRequest {
		return namedRequest{fmt.Sprintf("%s#%d", tool, id), call(id, tool, args)}
	}
	requests := []namedRequest{tool(1, "room_artifact_link", map[string]any{"path": "${ARTIFACT}", "title": "Desk linked"})}
	if spelledTwice {
		requests = append(requests, tool(2, "room_artifact_link", map[string]any{"path": "${ARTIFACT_ROOT}/sub/../mcp-artifact.txt"}))
	}
	requests = append(requests, tool(3, "room_artifact_list", nil), tool(4, "room_journal_tail", map[string]any{"limit": 3}))
	return scenarioSpec{name: name, setup: "room_toml_only", identity: true, artifactRoot: "testdata", normalize: true, sortArtifacts: true, requests: requests, recordJournal: true, symdeskScript: script}
}

func mutationScenarioSpecs() []scenarioSpec {
	tool := func(id int, name string, args any) namedRequest {
		return namedRequest{fmt.Sprintf("%s#%d", name, id), call(id, name, args)}
	}
	ping := `{"jsonrpc":"2.0","id":9,"method":"ping"}`
	validation := []namedRequest{
		tool(1, "room_note_post", nil),
		tool(2, "room_note_post", map[string]any{}),
		tool(3, "room_note_post", map[string]any{"text": ""}),
		tool(4, "room_note_post", map[string]any{"text": 5}),
		tool(5, "room_note_post", map[string]any{"text": nil}),
		tool(6, "room_note_post", "text"),
		tool(7, "room_note_post", []any{"x"}),
		tool(8, "room_note_post", map[string]any{"TEXT": "case-insensitive key"}),
		tool(9, "room_artifact_link", nil),
		tool(10, "room_artifact_link", map[string]any{}),
		tool(11, "room_artifact_link", map[string]any{"path": ""}),
		tool(12, "room_artifact_link", map[string]any{"path": 5}),
		tool(13, "room_artifact_link", map[string]any{"path": "${ARTIFACT}", "title": 5}),
		tool(14, "room_artifact_link", map[string]any{"path": "/etc/hosts"}),
		tool(15, "room_artifact_link", map[string]any{"path": "${ARTIFACT_ROOT}/../mcp-nonexistent.txt"}),
		tool(16, "room_artifact_link", map[string]any{"path": "${ARTIFACT_ROOT}/nonexistent.txt"}),
		tool(17, "room_artifact_link", map[string]any{"path": "${ARTIFACT_ROOT}"}),
		tool(18, "room_artifact_link", map[string]any{"path": "${ARTIFACT_ROOT}/sub/../mcp-artifact.txt"}),
		tool(19, "room_artifact_link", map[string]any{"path": "mcp-artifact.txt"}),
		tool(20, "room_artifact_link", map[string]any{"path": "${ARTIFACT_ROOT}/mcp-artifact.txt", "title": ""}),
		tool(21, "room_artifact_link", map[string]any{"path": "${ARTIFACT_ROOT}//mcp-artifact.txt", "title": "Double slash"}),
		tool(22, "room_run_request", nil),
		tool(23, "room_run_request", map[string]any{}),
		tool(24, "room_run_request", map[string]any{"title": ""}),
		tool(25, "room_run_request", map[string]any{"title": 5}),
		tool(26, "room_run_request", map[string]any{"title": "ok", "plan_file": 5}),
		tool(27, "room_run_request", map[string]any{"title": "ok", "adapter": []any{}}),
		tool(28, "room_run_request", "title"),
		tool(29, "room_checkpoint_request", nil),
		tool(30, "room_checkpoint_request", map[string]any{}),
		tool(31, "room_checkpoint_request", map[string]any{"run_id": "run_x"}),
		tool(32, "room_checkpoint_request", map[string]any{"question": "q"}),
		tool(33, "room_checkpoint_request", map[string]any{"run_id": "", "question": "q"}),
		tool(34, "room_checkpoint_request", map[string]any{"run_id": "run_x", "question": ""}),
		tool(35, "room_checkpoint_request", map[string]any{"run_id": 5, "question": "q"}),
		tool(36, "room_checkpoint_request", map[string]any{"run_id": "run_x", "question": 5}),
	}
	success := []namedRequest{
		tool(1, "room_note_post", map[string]any{"text": "plain note"}),
		tool(2, "room_note_post", map[string]any{"text": "html <b>bold</b> & \"quotes\" 'single' \u00e9 \U0001f600 \u2028 line\nbreak\ttab \\ slash \b\f\x01\x7f \u2029"}),
		tool(3, "room_note_post", map[string]any{"text": "extra args ignored", "extra": 1}),
		tool(4, "room_note_post", map[string]any{"TEXT": "case-insensitive key"}),
		tool(5, "room_artifact_link", map[string]any{"path": "${ARTIFACT}"}),
		tool(6, "room_artifact_link", map[string]any{"path": "${ARTIFACT}", "title": "Titled"}),
		tool(7, "room_artifact_link", map[string]any{"path": "${ARTIFACT_ROOT}/sub/../mcp-artifact.txt", "title": ""}),
		tool(8, "room_run_request", map[string]any{"title": "Title only"}),
		tool(9, "room_run_request", map[string]any{"title": "All fields", "plan_file": "plan.md", "adapter": "local"}),
		tool(10, "room_run_request", map[string]any{"title": "Title only"}),
		tool(11, "room_run_request", map[string]any{"title": "Unicode \u00e9 <tag>", "adapter": "a&b"}),
		tool(12, "room_checkpoint_request", map[string]any{"run_id": "run_unknown", "question": "Proceed? <yes> & \"no\""}),
		tool(13, "room_checkpoint_request", map[string]any{"run_id": "run_unknown", "question": "Proceed?"}),
		tool(14, "room_note_post", map[string]any{"text": "after everything"}),
		tool(15, "room_journal_tail", map[string]any{"limit": 3}),
		tool(16, "room_status", nil),
		tool(17, "room_artifact_list", nil),
	}
	return []scenarioSpec{
		{name: "mutation_validation_errors", setup: "room_toml_only", identity: true, artifactRoot: "testdata", normalize: true, requests: validation, recordJournal: true},
		{name: "mutation_success_sequence", setup: "room_toml_only", identity: true, artifactRoot: "testdata", normalize: true, requests: success, recordJournal: true},
		{name: "mutation_observer_role", setup: "observer_room", identity: true, artifactRoot: "testdata", normalize: true, requests: []namedRequest{
			tool(1, "room_note_post", map[string]any{"text": "observer note"}),
			tool(2, "room_artifact_link", map[string]any{"path": "${ARTIFACT}"}),
			tool(3, "room_run_request", map[string]any{"title": "observer run"}),
			tool(4, "room_checkpoint_request", map[string]any{"run_id": "run_x", "question": "observer?"}),
			tool(5, "room_status", nil),
		}, recordJournal: true},
		{name: "mutation_member_role", setup: "member_room", identity: true, artifactRoot: "testdata", normalize: true, requests: []namedRequest{
			tool(1, "room_note_post", map[string]any{"text": "member note"}),
			tool(2, "room_run_request", map[string]any{"title": "member run"}),
			tool(3, "room_status", nil),
		}, recordJournal: true},
		{name: "mutation_after_existing_journal", setup: "fixture", identity: true, artifactRoot: "testdata", normalize: true, requests: []namedRequest{
			tool(1, "room_note_post", map[string]any{"text": "joins existing room"}),
			tool(2, "room_run_request", map[string]any{"title": "Second author run"}),
			tool(3, "room_checkpoint_request", map[string]any{"run_id": "run_fixture", "question": "Continue?"}),
			tool(4, "room_journal_tail", map[string]any{"limit": 3}),
			tool(5, "room_status", nil),
		}, recordJournal: true},
		{name: "mutation_no_identity", setup: "room_toml_only", identity: false, artifactRoot: "testdata", normalize: true, requests: []namedRequest{
			tool(1, "room_note_post", map[string]any{"text": "no signer"}),
			tool(2, "room_artifact_link", map[string]any{"path": "${ARTIFACT}"}),
			tool(3, "room_run_request", map[string]any{"title": "no signer"}),
			tool(4, "room_checkpoint_request", map[string]any{"run_id": "run_x", "question": "q"}),
			tool(5, "room_note_post", map[string]any{}),
			tool(6, "room_artifact_link", map[string]any{"path": "${ARTIFACT_ROOT}/nonexistent.txt"}),
			tool(7, "room_artifact_link", map[string]any{"path": "/etc/hosts"}),
			tool(8, "room_run_request", map[string]any{}),
			tool(9, "room_checkpoint_request", map[string]any{"run_id": "run_x"}),
			tool(10, "room_status", nil),
		}, recordJournal: true},
		{name: "mutation_no_identity_no_room", setup: "empty_dir", identity: false, artifactRoot: "testdata", normalize: true, requests: []namedRequest{
			tool(1, "room_note_post", map[string]any{"text": "no signer"}),
			tool(2, "room_artifact_link", map[string]any{"path": "${ARTIFACT}"}),
			tool(3, "room_run_request", map[string]any{"title": "no signer"}),
			tool(4, "room_checkpoint_request", map[string]any{"run_id": "run_x", "question": "q"}),
		}, recordJournal: true},
		{name: "mutation_no_room", setup: "empty_dir", identity: true, artifactRoot: "testdata", normalize: true, requests: []namedRequest{
			tool(1, "room_note_post", map[string]any{"text": "no room"}),
			tool(2, "room_run_request", map[string]any{"title": "no room"}),
			tool(3, "room_checkpoint_request", map[string]any{"run_id": "run_x", "question": "q"}),
			tool(4, "room_artifact_link", map[string]any{"path": "${ARTIFACT}", "title": "Linked without room.toml"}),
			tool(5, "room_status", nil),
			tool(6, "room_journal_tail", nil),
		}, recordJournal: true},
		{name: "mutation_artifact_root_defaults_to_room", setup: "fixture", identity: true, artifactRoot: "room", normalize: true, sortArtifacts: true, requests: []namedRequest{
			tool(1, "room_artifact_link", map[string]any{"path": "${ROOM}/known.txt"}),
			tool(2, "room_artifact_link", map[string]any{"path": "${ARTIFACT}"}),
			tool(3, "room_artifact_link", map[string]any{"path": "${ROOM}/known.txt", "title": "Known again"}),
			tool(4, "room_artifact_link", map[string]any{"path": "${ROOM}/room.toml"}),
			tool(5, "room_artifact_list", nil),
		}, recordJournal: true},
		// Notifications (no id) execute without a response, so the signed journal
		// is the only record of the generated events; the trailing ping proves
		// the stream stayed healthy.
		{name: "mutation_json_layer_streams", setup: "room_toml_only", identity: true, artifactRoot: "testdata", normalize: true, streams: []namedStream{
			{"note_invalid_utf8_text", frameExpanded(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"room_note_post","arguments":{"text":"bad{{hex:ff}}utf8{{hex:e282}}!"}}}`) + frame(ping)},
			{"note_lone_surrogate_text", frame(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"room_note_post","arguments":{"text":"lone \ud800 high, \udc00 low, \ud800\u0041 mismatch, \ud83d\ude00 pair"}}}`) + frame(ping)},
			{"note_with_nesting_200_in_unused_argument", frameExpanded(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"room_note_post","arguments":{"extra":{{repeat:[:200}}{{repeat:]:200}},"text":"deep extra ignored"}}}`) + frame(ping)},
			{"run_request_with_nesting_200_in_unused_argument", frameExpanded(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"room_run_request","arguments":{"title":"deep","extra":{{repeat:[:200}}{{repeat:]:200}}}}}`) + frame(ping)},
			{"checkpoint_invalid_utf8_question", frameExpanded(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"room_checkpoint_request","arguments":{"run_id":"run_x","question":"q{{hex:ff}}?"}}}`) + frame(ping)},
			{"checkpoint_lone_surrogate_run_id", frame(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"room_checkpoint_request","arguments":{"run_id":"run_\udfff","question":"q?"}}}`) + frame(ping)},
		}, recordJournal: true},
		symdeskScenario("mutation_artifact_symdesk_document_id", `case "$1:$3" in
inspect:--json) printf '{"document_id":"doc|%s|%s|%s","vault_name":"vault","valid":true}\n' "$1" "$2" "$3" ;;
*) exit 2 ;;
esac`, true),
		symdeskScenario("mutation_artifact_symdesk_exit_failure", `printf '{"document_id":"doc_ignored"}'; exit 3`, false),
		symdeskScenario("mutation_artifact_symdesk_invalid_json", `printf 'not json'`, false),
		symdeskScenario("mutation_artifact_symdesk_missing_document_id", `printf '{"vault_name":"v","valid":true}'`, false),
		symdeskScenario("mutation_artifact_symdesk_wrong_field_type_discards_result", `printf '{"document_id":"doc_typeerr","valid":"yes"}'`, false),
		symdeskScenario("mutation_artifact_symdesk_case_insensitive_key", `printf '{"DOCUMENT_ID":"doc_upper"}'`, false),
		symdeskScenario("mutation_artifact_symdesk_duplicate_key_last_wins", `printf '{"document_id":"first","document_id":"last"}'`, false),
		symdeskScenario("mutation_artifact_symdesk_trailing_garbage", `printf '{"document_id":"doc_trailing"} x'`, false),
		symdeskScenario("mutation_artifact_symdesk_null_document", `printf 'null'`, false),
		symdeskScenario("mutation_artifact_symdesk_null_document_id", `printf '{"document_id":null}'`, false),
		// A deadline-less runner would wait out the 3 s sleep and then exit 0, so
		// it would record "late"; the 1 s deadline kills the child instead and
		// discards the output (an absolute /bin/sleep: PATH holds only the fake).
		symdeskScenario("mutation_artifact_symdesk_timeout_is_one_second", `printf '{"document_id":"late"}'; exec /bin/sleep 3`, false),
		// Go runs the child with a nil Stdin (the null device). A child that
		// inherits the server's stdin would read the client stream instead.
		symdeskScenario("mutation_artifact_symdesk_stdin_is_null_device", `IFS= read -r line; printf '{"document_id":"stdin:%s"}' "$line"`, false),
		{name: "mutation_artifact_dot_dot_prefixed_file_name", setup: "dotdot_file", identity: true, artifactRoot: "room", normalize: true, requests: []namedRequest{
			tool(1, "room_artifact_link", map[string]any{"path": "${ROOM}/..name"}),
			tool(2, "room_artifact_link", map[string]any{"path": "${ROOM}/.name"}),
			tool(3, "room_artifact_list", nil),
		}, recordJournal: true},
		{name: "notification_tool_call_executes", setup: "room_toml_only", identity: true, artifactRoot: "testdata", normalize: true, streams: []namedStream{
			{"note_notification_then_ping", frame(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"room_note_post","arguments":{"text":"sent as a notification"}}}`) + frame(ping)},
		}, recordJournal: true},
	}
}
