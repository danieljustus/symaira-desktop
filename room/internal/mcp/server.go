package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"time"

	"github.com/danieljustus/symaira-corekit/mcpserver"
	"github.com/danieljustus/symaira-room/internal/artifact"
	"github.com/danieljustus/symaira-room/internal/identity"
	"github.com/danieljustus/symaira-room/internal/journal"
	"github.com/danieljustus/symaira-room/internal/room"
	"github.com/danieljustus/symaira-room/internal/run"
)

type Server struct {
	RoomDir, ArtifactRoot string
	Identity              *identity.Identity
}

func NewServer(roomDir string, id *identity.Identity, artifactRoot string) *mcpserver.Server {
	s := &Server{RoomDir: roomDir, Identity: id, ArtifactRoot: artifactRoot}
	out := mcpserver.New("symroom", "0.1.0")
	out.SetInstructions("Use room_* tools to inspect and record the signed room work record. There is no approval-granting tool in this server.")
	reg := func(name, desc string, schema string, h func(context.Context, json.RawMessage) (any, error)) {
		out.RegisterTool(&mcpserver.Tool{Name: name, Description: desc, InputSchema: json.RawMessage(schema), Handler: h})
	}
	reg("room_status", "Return room metadata and journal statistics", `{ "type":"object", "properties":{} }`, s.status)
	reg("room_journal_tail", "Return the merged signed journal tail", `{ "type":"object", "properties":{"limit":{"type":"integer"}} }`, s.journalTail)
	reg("room_note_post", "Post a signed note to the room journal", `{ "type":"object", "required":["text"], "properties":{"text":{"type":"string"}} }`, s.notePost)
	reg("room_artifact_list", "List linked room artifacts", `{ "type":"object", "properties":{} }`, s.artifactList)
	reg("room_artifact_link", "Link and hash an artifact, recording a signed event", `{ "type":"object", "required":["path"], "properties":{"path":{"type":"string"},"title":{"type":"string"}} }`, s.artifactLink)
	reg("room_run_request", "Request a run with a signed journal event", `{ "type":"object", "required":["title"], "properties":{"title":{"type":"string"},"plan_file":{"type":"string"},"adapter":{"type":"string"}} }`, s.runRequest)
	reg("room_run_wait", "Wait for a run approval decision", `{ "type":"object", "required":["run_id"], "properties":{"run_id":{"type":"string"},"timeout_seconds":{"type":"number"}} }`, s.runWait)
	reg("room_checkpoint_request", "Request a signed human checkpoint", `{ "type":"object", "required":["run_id","question"], "properties":{"run_id":{"type":"string"},"question":{"type":"string"}} }`, s.checkpointRequest)
	return out
}
func (s *Server) status(_ context.Context, _ json.RawMessage) (any, error) {
	c, e := room.ReadRoomConfig(s.RoomDir)
	if e != nil {
		return nil, e
	}
	st, e := room.ReadJournalStats(s.RoomDir)
	if e != nil {
		return nil, e
	}
	return map[string]any{"room": c, "max_lamport": st.MaxLamport}, nil
}
func (s *Server) journalTail(_ context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Limit int `json:"limit"`
	}
	_ = json.Unmarshal(raw, &a)
	if a.Limit <= 0 {
		a.Limit = 20
	}
	evs, e := journal.New(filepath.Join(s.RoomDir, "journal")).MergeAll()
	if e != nil {
		return nil, e
	}
	if len(evs) > a.Limit {
		evs = evs[len(evs)-a.Limit:]
	}
	return evs, nil
}
func (s *Server) notePost(_ context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Text string `json:"text"`
	}
	if e := json.Unmarshal(raw, &a); e != nil || a.Text == "" {
		return nil, fmt.Errorf("text is required")
	}
	return room.PostNote(s.RoomDir, a.Text, s.Identity)
}
func (s *Server) artifactList(_ context.Context, _ json.RawMessage) (any, error) {
	return artifact.List(s.RoomDir, s.ArtifactRoot)
}
func (s *Server) artifactLink(_ context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Path  string `json:"path"`
		Title string `json:"title"`
	}
	if e := json.Unmarshal(raw, &a); e != nil || a.Path == "" {
		return nil, fmt.Errorf("path is required")
	}
	return artifact.Link(s.RoomDir, s.ArtifactRoot, a.Path, a.Title, s.Identity)
}
func (s *Server) runRequest(_ context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		Title    string `json:"title"`
		PlanFile string `json:"plan_file"`
		Adapter  string `json:"adapter"`
	}
	if e := json.Unmarshal(raw, &a); e != nil || a.Title == "" {
		return nil, fmt.Errorf("title is required")
	}
	return run.Request(s.RoomDir, a.Title, a.PlanFile, a.Adapter, s.Identity)
}
func (s *Server) runWait(ctx context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		RunID   string  `json:"run_id"`
		Timeout float64 `json:"timeout_seconds"`
	}
	if e := json.Unmarshal(raw, &a); e != nil || a.RunID == "" {
		return nil, fmt.Errorf("run_id is required")
	}
	if a.Timeout <= 0 {
		a.Timeout = 30
	}
	c, cancel := context.WithTimeout(ctx, time.Duration(a.Timeout*float64(time.Second)))
	defer cancel()
	return run.Wait(c, s.RoomDir, a.RunID, 100*time.Millisecond)
}
func (s *Server) checkpointRequest(_ context.Context, raw json.RawMessage) (any, error) {
	var a struct {
		RunID    string `json:"run_id"`
		Question string `json:"question"`
	}
	if e := json.Unmarshal(raw, &a); e != nil || a.RunID == "" || a.Question == "" {
		return nil, fmt.Errorf("run_id and question are required")
	}
	return run.RequestCheckpoint(s.RoomDir, a.RunID, a.Question, s.Identity)
}
