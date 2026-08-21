package mcp

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-room/internal/approval"
	"github.com/danieljustus/symaira-room/internal/artifact"
	"github.com/danieljustus/symaira-room/internal/event"
	"github.com/danieljustus/symaira-room/internal/identity"
	"github.com/danieljustus/symaira-room/internal/members"
	"github.com/danieljustus/symaira-room/internal/room"
	"github.com/danieljustus/symaira-room/internal/run"
)

// newInProcessServer builds a hermetic temp room (owner identity plus one
// added member) and returns a Server wired to it. Handlers run in-process, so
// they contribute real coverage to internal/mcp and its dependencies.
func newInProcessServer(t *testing.T) (*Server, *identity.Identity, string) {
	t.Helper()
	owner, err := identity.Generate("owner")
	if err != nil {
		t.Fatalf("generate owner identity: %v", err)
	}
	member, err := identity.Generate("bob")
	if err != nil {
		t.Fatalf("generate member identity: %v", err)
	}
	roomDir := filepath.Join(t.TempDir(), "room")
	if _, err := room.Init(roomDir, "Test Room", owner); err != nil {
		t.Fatalf("room.Init: %v", err)
	}
	if _, err := room.AddMember(roomDir, "bob", hex.EncodeToString(member.PublicKey), members.RoleMember, members.KindHuman, owner); err != nil {
		t.Fatalf("room.AddMember: %v", err)
	}
	return &Server{RoomDir: roomDir, Identity: owner, ArtifactRoot: ""}, owner, roomDir
}

func TestStatusHandlerInProcess(t *testing.T) {
	s, owner, _ := newInProcessServer(t)

	res, err := s.status(context.Background(), nil)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	m, ok := res.(map[string]any)
	if !ok {
		t.Fatalf("status result type %T, want map[string]any", res)
	}
	cfg, ok := m["room"].(*room.RoomConfig)
	if !ok {
		t.Fatalf("status room field type %T, want *room.RoomConfig", m["room"])
	}
	if cfg.ID == "" {
		t.Error("status room ID is empty")
	}
	if cfg.RootPubkey != "ed25519:"+hex.EncodeToString(owner.PublicKey) {
		t.Errorf("status root_pubkey = %q, want owner public key hex with ed25519 prefix", cfg.RootPubkey)
	}
	lamport, ok := m["max_lamport"].(uint64)
	if !ok || lamport < 2 {
		t.Errorf("status max_lamport = %v (type %T), want >= 2 (room.created + member.added)", m["max_lamport"], m["max_lamport"])
	}
}

func TestJournalTailHandlerInProcess(t *testing.T) {
	s, _, _ := newInProcessServer(t)

	// Default limit applies when no limit is given.
	res, err := s.journalTail(context.Background(), nil)
	if err != nil {
		t.Fatalf("journalTail: %v", err)
	}
	evs, ok := res.([]*event.Event)
	if !ok {
		t.Fatalf("journalTail result type %T, want []*event.Event", res)
	}
	if len(evs) != 2 {
		t.Fatalf("journalTail len = %d, want 2 (room.created + member.added)", len(evs))
	}
	if evs[0].Kind != event.KindRoomCreated || evs[1].Kind != event.KindMemberAdded {
		t.Errorf("journalTail kinds = [%s %s], want [room.created member.added]", evs[0].Kind, evs[1].Kind)
	}

	// Explicit limit truncates the tail.
	res, err = s.journalTail(context.Background(), json.RawMessage(`{"limit":1}`))
	if err != nil {
		t.Fatalf("journalTail limit: %v", err)
	}
	evs = res.([]*event.Event)
	if len(evs) != 1 || evs[0].Kind != event.KindMemberAdded {
		t.Errorf("journalTail limit=1 = %d event(s), want the member.added event", len(evs))
	}
}

func TestNotePostHandlerInProcess(t *testing.T) {
	s, owner, _ := newInProcessServer(t)

	res, err := s.notePost(context.Background(), json.RawMessage(`{"text":"hello from mcp"}`))
	if err != nil {
		t.Fatalf("notePost: %v", err)
	}
	ev, ok := res.(*event.Event)
	if !ok {
		t.Fatalf("notePost result type %T, want *event.Event", res)
	}
	if ev.Kind != event.KindNotePosted || ev.Author != owner.MemberID {
		t.Errorf("notePost event = kind %q author %q, want note.posted by %s", ev.Kind, ev.Author, owner.MemberID)
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(ev.Body, &body); err != nil {
		t.Fatalf("unmarshal note body: %v", err)
	}
	if body.Text != "hello from mcp" {
		t.Errorf("notePost body text = %q, want %q", body.Text, "hello from mcp")
	}

	// The event is persisted and visible via journalTail.
	res, err = s.journalTail(context.Background(), nil)
	if err != nil {
		t.Fatalf("journalTail: %v", err)
	}
	evs := res.([]*event.Event)
	if last := evs[len(evs)-1]; last.Kind != event.KindNotePosted {
		t.Errorf("journalTail last kind = %q, want note.posted", last.Kind)
	}

	// Validation: empty text is rejected.
	if _, err := s.notePost(context.Background(), json.RawMessage(`{"text":""}`)); err == nil || !strings.Contains(err.Error(), "text is required") {
		t.Errorf("notePost empty text err = %v, want %q", err, "text is required")
	}
}

func TestArtifactHandlersInProcess(t *testing.T) {
	s, _, roomDir := newInProcessServer(t)

	// Empty room starts with no artifacts.
	res, err := s.artifactList(context.Background(), nil)
	if err != nil {
		t.Fatalf("artifactList: %v", err)
	}
	refs, ok := res.([]*artifact.ArtifactRef)
	if !ok {
		t.Fatalf("artifactList result type %T, want []*artifact.ArtifactRef", res)
	}
	if len(refs) != 0 {
		t.Errorf("artifactList len = %d, want 0", len(refs))
	}

	// Link a file inside the room dir.
	doc := filepath.Join(roomDir, "doc.md")
	if err := os.WriteFile(doc, []byte("# doc\n"), 0o644); err != nil {
		t.Fatalf("write artifact file: %v", err)
	}
	res, err = s.artifactLink(context.Background(), json.RawMessage(`{"path":"`+doc+`","title":"My Doc"}`))
	if err != nil {
		t.Fatalf("artifactLink: %v", err)
	}
	ev, ok := res.(*event.Event)
	if !ok {
		t.Fatalf("artifactLink result type %T, want *event.Event", res)
	}
	if ev.Kind != event.KindArtifactLinked {
		t.Errorf("artifactLink event kind = %q, want artifact.linked", ev.Kind)
	}

	// The link is now listed with its title.
	res, err = s.artifactList(context.Background(), nil)
	if err != nil {
		t.Fatalf("artifactList after link: %v", err)
	}
	refs = res.([]*artifact.ArtifactRef)
	if len(refs) != 1 {
		t.Fatalf("artifactList after link len = %d, want 1", len(refs))
	}
	if refs[0].Title != "My Doc" || refs[0].Path != "doc.md" {
		t.Errorf("artifactList ref = %+v, want title %q path %q", refs[0], "My Doc", "doc.md")
	}

	// Validation errors.
	if _, err := s.artifactLink(context.Background(), json.RawMessage(`{"path":""}`)); err == nil || !strings.Contains(err.Error(), "path is required") {
		t.Errorf("artifactLink empty path err = %v, want %q", err, "path is required")
	}
	outside := filepath.Join(t.TempDir(), "outside.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatalf("write outside file: %v", err)
	}
	if _, err := s.artifactLink(context.Background(), json.RawMessage(`{"path":"`+outside+`"}`)); err == nil {
		t.Error("artifactLink outside root: expected error")
	}
}

func TestRunRequestHandlerInProcess(t *testing.T) {
	s, owner, _ := newInProcessServer(t)

	res, err := s.runRequest(context.Background(), json.RawMessage(`{"title":"Deploy","plan_file":"plan.md","adapter":"openai"}`))
	if err != nil {
		t.Fatalf("runRequest: %v", err)
	}
	ev, ok := res.(*event.Event)
	if !ok {
		t.Fatalf("runRequest result type %T, want *event.Event", res)
	}
	if ev.Kind != event.KindRunRequested || ev.Author != owner.MemberID {
		t.Errorf("runRequest event = kind %q author %q, want run.requested by %s", ev.Kind, ev.Author, owner.MemberID)
	}
	var body struct {
		RunID    string `json:"run_id"`
		PlanFile string `json:"plan_file"`
		Adapter  string `json:"adapter"`
	}
	if err := json.Unmarshal(ev.Body, &body); err != nil {
		t.Fatalf("unmarshal run body: %v", err)
	}
	if !strings.HasPrefix(body.RunID, "run_") || body.PlanFile != "plan.md" || body.Adapter != "openai" {
		t.Errorf("runRequest body = %+v, want run_id run_* with plan.md/openai", body)
	}

	if _, err := s.runRequest(context.Background(), json.RawMessage(`{"title":""}`)); err == nil || !strings.Contains(err.Error(), "title is required") {
		t.Errorf("runRequest empty title err = %v, want %q", err, "title is required")
	}
}

func TestRunWaitHandlerInProcess(t *testing.T) {
	s, owner, roomDir := newInProcessServer(t)

	// Unknown run: the handler polls and times out.
	_, err := s.runWait(context.Background(), json.RawMessage(`{"run_id":"run_missing","timeout_seconds":0.2}`))
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("runWait unknown run err = %v, want wait timeout", err)
	}

	// Approved run: the handler returns the approved run.
	res, err := s.runRequest(context.Background(), json.RawMessage(`{"title":"Approve me"}`))
	if err != nil {
		t.Fatalf("runRequest: %v", err)
	}
	var body struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(res.(*event.Event).Body, &body); err != nil {
		t.Fatalf("unmarshal run body: %v", err)
	}
	if _, err := approval.Approve(roomDir, body.RunID, "local", time.Hour, owner); err != nil {
		t.Fatalf("approval.Approve: %v", err)
	}
	res, err = s.runWait(context.Background(), json.RawMessage(`{"run_id":"`+body.RunID+`","timeout_seconds":10}`))
	if err != nil {
		t.Fatalf("runWait approved run: %v", err)
	}
	r, ok := res.(*run.Run)
	if !ok {
		t.Fatalf("runWait result type %T, want *run.Run", res)
	}
	if r.ID != body.RunID || r.State != run.StateApproved {
		t.Errorf("runWait result = %s/%s, want %s approved", r.ID, r.State, body.RunID)
	}

	if _, err := s.runWait(context.Background(), json.RawMessage(`{}`)); err == nil || !strings.Contains(err.Error(), "run_id is required") {
		t.Errorf("runWait missing run_id err = %v, want %q", err, "run_id is required")
	}
}

func TestCheckpointRequestHandlerInProcess(t *testing.T) {
	s, owner, _ := newInProcessServer(t)

	res, err := s.checkpointRequest(context.Background(), json.RawMessage(`{"run_id":"run_abc","question":"Proceed?"}`))
	if err != nil {
		t.Fatalf("checkpointRequest: %v", err)
	}
	ev, ok := res.(*event.Event)
	if !ok {
		t.Fatalf("checkpointRequest result type %T, want *event.Event", res)
	}
	if ev.Kind != event.KindCheckpointReq || ev.Author != owner.MemberID {
		t.Errorf("checkpointRequest event = kind %q author %q, want checkpoint.requested by %s", ev.Kind, ev.Author, owner.MemberID)
	}
	var body struct {
		CheckpointID string `json:"checkpoint_id"`
		Question     string `json:"question"`
	}
	if err := json.Unmarshal(ev.Body, &body); err != nil {
		t.Fatalf("unmarshal checkpoint body: %v", err)
	}
	if !strings.HasPrefix(body.CheckpointID, "chk_") || body.Question != "Proceed?" {
		t.Errorf("checkpointRequest body = %+v, want chk_* with question", body)
	}

	if _, err := s.checkpointRequest(context.Background(), json.RawMessage(`{"run_id":"run_abc"}`)); err == nil || !strings.Contains(err.Error(), "run_id and question are required") {
		t.Errorf("checkpointRequest missing fields err = %v, want %q", err, "run_id and question are required")
	}
}

// ---- Full protocol path (ServeIO) -----------------------------------------

// toolResult mirrors the tools/call response envelope produced by mcpserver.
// Since corekit v0.8.0 the text field is a proper JSON string (the MCP spec
// requires TextContent.text to be a string), so text() decodes it.
type toolResult struct {
	Content []struct {
		Type string          `json:"type"`
		Text json.RawMessage `json:"text"`
	} `json:"content"`
	IsError bool `json:"isError"`
}

// text returns the handler-result payload of the first content item: the
// decoded text string, or the raw field bytes when it is not a JSON string.
func (tr toolResult) text() string {
	if len(tr.Content) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(tr.Content[0].Text, &s); err == nil {
		return s
	}
	return string(tr.Content[0].Text)
}

// extractToolResult unwraps the tools/call envelope from a response frame.
func extractToolResult(t *testing.T, frame []byte) toolResult {
	t.Helper()
	var resp jsonRPCResponse
	if err := json.Unmarshal(frame, &resp); err != nil {
		t.Fatalf("unmarshal response frame: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("unexpected JSON-RPC error: %s", string(resp.Error))
	}
	var tr toolResult
	if err := json.Unmarshal(resp.Result, &tr); err != nil {
		t.Fatalf("unmarshal tool result: %v", err)
	}
	return tr
}

// TestServeIOEndToEnd drives the registered mux through the framed JSON-RPC
// protocol in-process and asserts real response content for every handler.
func TestServeIOEndToEnd(t *testing.T) {
	_, owner, roomDir := newInProcessServer(t)
	srv := NewServer(roomDir, owner, "") // *mcpserver.Server with all tools registered

	doc := filepath.Join(roomDir, "e2e.txt")
	if err := os.WriteFile(doc, []byte("e2e"), 0o644); err != nil {
		t.Fatalf("write e2e artifact: %v", err)
	}

	var in bytes.Buffer
	reqs := []struct {
		id     int
		method string
		params map[string]any
	}{
		{1, "initialize", nil},
		{2, "tools/list", nil},
		{3, "tools/call", map[string]any{"name": "room_status", "arguments": map[string]any{}}},
		{4, "tools/call", map[string]any{"name": "room_journal_tail", "arguments": map[string]any{"limit": 5}}},
		{5, "tools/call", map[string]any{"name": "room_note_post", "arguments": map[string]any{"text": "e2e note"}}},
		{6, "tools/call", map[string]any{"name": "room_artifact_link", "arguments": map[string]any{"path": doc, "title": "E2E Doc"}}},
		{7, "tools/call", map[string]any{"name": "room_artifact_list", "arguments": map[string]any{}}},
		{8, "tools/call", map[string]any{"name": "room_run_request", "arguments": map[string]any{"title": "E2E run"}}},
		{9, "tools/call", map[string]any{"name": "room_run_wait", "arguments": map[string]any{"run_id": "run_missing", "timeout_seconds": 0.2}}},
		{10, "tools/call", map[string]any{"name": "room_checkpoint_request", "arguments": map[string]any{"run_id": "run_missing", "question": "ok?"}}},
		{11, "tools/call", map[string]any{"name": "no_such_tool", "arguments": map[string]any{}}},
	}
	for _, r := range reqs {
		in.Write(framedRequest(t, r.id, r.method, r.params))
	}

	var out bytes.Buffer
	if err := srv.ServeIO(context.Background(), &in, &out); err != nil {
		t.Fatalf("ServeIO: %v", err)
	}

	frames := splitFrames(out.Bytes())
	if len(frames) != len(reqs) {
		t.Fatalf("got %d response frames, want %d", len(frames), len(reqs))
	}

	// 1: initialize.
	var initResp jsonRPCResponse
	if err := json.Unmarshal([]byte(frames[0]), &initResp); err != nil {
		t.Fatalf("initialize frame: %v", err)
	}
	var initResult map[string]any
	if err := json.Unmarshal(initResp.Result, &initResult); err != nil {
		t.Fatalf("initialize result: %v", err)
	}
	if initResult["protocolVersion"] != "2024-11-05" {
		t.Errorf("protocolVersion = %v, want 2024-11-05", initResult["protocolVersion"])
	}
	serverInfo, _ := initResult["serverInfo"].(map[string]any)
	if serverInfo == nil || serverInfo["name"] != "symroom" {
		t.Errorf("serverInfo = %v, want name symroom", initResult["serverInfo"])
	}
	if instr, _ := initResult["instructions"].(string); !strings.Contains(instr, "room_") {
		t.Error("initialize instructions missing room_ tool guidance")
	}

	// 2: tools/list exposes all eight room tools.
	var listResp jsonRPCResponse
	if err := json.Unmarshal([]byte(frames[1]), &listResp); err != nil {
		t.Fatalf("tools/list frame: %v", err)
	}
	var listResult struct {
		Tools []struct {
			Name string `json:"name"`
		} `json:"tools"`
	}
	if err := json.Unmarshal(listResp.Result, &listResult); err != nil {
		t.Fatalf("tools/list result: %v", err)
	}
	got := make(map[string]bool)
	for _, tl := range listResult.Tools {
		got[tl.Name] = true
	}
	for _, want := range []string{"room_status", "room_journal_tail", "room_note_post", "room_artifact_list", "room_artifact_link", "room_run_request", "room_run_wait", "room_checkpoint_request"} {
		if !got[want] {
			t.Errorf("tools/list missing tool %q", want)
		}
	}

	// 3: room_status.
	tr := extractToolResult(t, []byte(frames[2]))
	if tr.IsError || !strings.Contains(tr.text(), `"id":"rm_`) {
		t.Errorf("room_status = %+v, want room config JSON", tr)
	}

	// 4: room_journal_tail contains the room.created and member.added events.
	tr = extractToolResult(t, []byte(frames[3]))
	if tr.IsError || !strings.Contains(tr.text(), `"kind":"room.created"`) || !strings.Contains(tr.text(), `"kind":"member.added"`) {
		t.Errorf("room_journal_tail = %+v, want merged events", tr)
	}

	// 5: room_note_post persisted a signed note.
	tr = extractToolResult(t, []byte(frames[4]))
	if tr.IsError || !strings.Contains(tr.text(), `"kind":"note.posted"`) || !strings.Contains(tr.text(), "e2e note") {
		t.Errorf("room_note_post = %+v, want note.posted event", tr)
	}

	// 6: room_artifact_link.
	tr = extractToolResult(t, []byte(frames[5]))
	if tr.IsError || !strings.Contains(tr.text(), `"kind":"artifact.linked"`) {
		t.Errorf("room_artifact_link = %+v, want artifact.linked event", tr)
	}

	// 7: room_artifact_list shows the linked artifact.
	tr = extractToolResult(t, []byte(frames[6]))
	if tr.IsError || !strings.Contains(tr.text(), "E2E Doc") {
		t.Errorf("room_artifact_list = %+v, want linked artifact", tr)
	}

	// 8: room_run_request.
	tr = extractToolResult(t, []byte(frames[7]))
	if tr.IsError || !strings.Contains(tr.text(), `"kind":"run.requested"`) {
		t.Errorf("room_run_request = %+v, want run.requested event", tr)
	}

	// 9: room_run_wait times out for an unknown run (tool error, not JSON-RPC error).
	tr = extractToolResult(t, []byte(frames[8]))
	if !tr.IsError || !strings.Contains(tr.text(), "timed out") {
		t.Errorf("room_run_wait = %+v, want timeout tool error", tr)
	}

	// 10: room_checkpoint_request.
	tr = extractToolResult(t, []byte(frames[9]))
	if tr.IsError || !strings.Contains(tr.text(), `"kind":"checkpoint.requested"`) {
		t.Errorf("room_checkpoint_request = %+v, want checkpoint.requested event", tr)
	}

	// 11: unknown tool yields a JSON-RPC method-not-found error.
	var errResp jsonRPCResponse
	if err := json.Unmarshal([]byte(frames[10]), &errResp); err != nil {
		t.Fatalf("unknown tool frame: %v", err)
	}
	if errResp.Error == nil || !strings.Contains(string(errResp.Error), "-32601") {
		t.Errorf("unknown tool error = %s, want code -32601", string(errResp.Error))
	}
}
