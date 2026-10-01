package run

import (
	"encoding/json"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/room/event"
	"github.com/danieljustus/symaira-desktop/internal/room/identity"
	"github.com/danieljustus/symaira-desktop/internal/room/journal"
	"github.com/danieljustus/symaira-desktop/internal/room/room"
)

func TestRunLifecycleAndStateTransitions(t *testing.T) {
	tempDir := t.TempDir()
	ownerID, err := identity.Generate("owner")
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	initRunTestRoom(t, tempDir, ownerID)

	// 1. Request run
	evReq, err := Request(tempDir, "Test Task", "plan.md", "shell", ownerID)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}

	var b struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(evReq.Body, &b); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}
	runID := b.RunID
	roomCfg, err := room.ReadRoomConfig(tempDir)
	if err != nil {
		t.Fatalf("read room config: %v", err)
	}

	r, err := Get(tempDir, runID)
	if err != nil {
		t.Fatalf("Get run failed: %v", err)
	}
	if r.State != StateRequested {
		t.Fatalf("expected state requested, got %s", r.State)
	}

	// 2. Start before approved should fail
	_, err = Start(tempDir, runID, ownerID)
	if err == nil {
		t.Fatalf("expected error starting unapproved run, got nil")
	}

	// 3. Approve run (manually append run.approved event)
	j := journal.New(filepath.Join(tempDir, "journal"))
	appBody, _ := json.Marshal(map[string]string{"run_id": runID, "approval_id": "app_123"})
	evApp := &event.Event{
		V:      event.CurrentVersion,
		ID:     "ev_app1",
		Room:   roomCfg.ID,
		Author: ownerID.MemberID,
		Kind:   event.KindRunApproved,
		Body:   json.RawMessage(appBody),
	}
	if err := j.PrepareEvent(evApp); err != nil {
		t.Fatalf("PrepareEvent failed: %v", err)
	}
	if err := evApp.Sign(ownerID); err != nil {
		t.Fatalf("Sign failed: %v", err)
	}
	if err := j.Append(evApp); err != nil {
		t.Fatalf("Append failed: %v", err)
	}

	rApproved, _ := Get(tempDir, runID)
	if rApproved.State != StateApproved {
		t.Fatalf("expected state approved, got %s", rApproved.State)
	}

	// 4. Start run
	_, err = Start(tempDir, runID, ownerID)
	if err != nil {
		t.Fatalf("Start failed: %v", err)
	}
	rStarted, _ := Get(tempDir, runID)
	if rStarted.State != StateStarted {
		t.Fatalf("expected state started, got %s", rStarted.State)
	}

	// 5. Finish run
	_, err = Finish(tempDir, runID, "Task completed successfully", []string{"art_1"}, ownerID)
	if err != nil {
		t.Fatalf("Finish failed: %v", err)
	}
	rFinished, _ := Get(tempDir, runID)
	if rFinished.State != StateFinished {
		t.Fatalf("expected state finished, got %s", rFinished.State)
	}
	if rFinished.Summary != "Task completed successfully" {
		t.Errorf("unexpected summary: %s", rFinished.Summary)
	}
}

func initRunTestRoom(t *testing.T, dir string, owner *identity.Identity) {
	t.Helper()
	if _, err := room.Init(dir, "Run Test Room", owner); err != nil {
		t.Fatalf("initialize room: %v", err)
	}
}

func TestPendingRunsList(t *testing.T) {
	tempDir := t.TempDir()
	ownerID, _ := identity.Generate("owner")
	initRunTestRoom(t, tempDir, ownerID)

	ev1, _ := Request(tempDir, "Pending 1", "", "", ownerID)
	var b1 struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(ev1.Body, &b1); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}

	listPending, err := List(tempDir, true)
	if err != nil {
		t.Fatalf("List pending failed: %v", err)
	}
	if len(listPending) != 1 {
		t.Fatalf("expected 1 pending run, got %d", len(listPending))
	}
}

func TestCancelRun(t *testing.T) {
	tempDir := t.TempDir()
	ownerID, err := identity.Generate("owner")
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	initRunTestRoom(t, tempDir, ownerID)

	ev, err := Request(tempDir, "Cancel me", "", "", ownerID)
	if err != nil {
		t.Fatalf("Request failed: %v", err)
	}
	var body struct {
		RunID string `json:"run_id"`
	}
	if err := json.Unmarshal(ev.Body, &body); err != nil {
		t.Fatalf("unmarshal request body: %v", err)
	}

	if _, err := Cancel(tempDir, body.RunID, "no longer needed", ownerID); err != nil {
		t.Fatalf("Cancel failed: %v", err)
	}
	r, err := Get(tempDir, body.RunID)
	if err != nil {
		t.Fatalf("Get cancelled run: %v", err)
	}
	if r.State != StateCancelled || r.Error != "no longer needed" {
		t.Errorf("cancelled run = state %q, error %q; want cancelled/no longer needed", r.State, r.Error)
	}

	if _, err := Cancel(tempDir, body.RunID, "again", ownerID); err == nil {
		t.Fatal("expected terminal run cancellation to fail")
	}
}
