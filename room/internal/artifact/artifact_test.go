package artifact

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-room/internal/desk"
	"github.com/danieljustus/symaira-room/internal/event"
	"github.com/danieljustus/symaira-room/internal/identity"
	"github.com/danieljustus/symaira-room/internal/journal"
)

func TestArtifactLinkUnlinkList(t *testing.T) {
	tempDir := t.TempDir()
	ownerID, err := identity.Generate("owner")
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}

	docPath := filepath.Join(tempDir, "doc.txt")
	if err := os.WriteFile(docPath, []byte("hello artifact"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	// 1. Link artifact inside root
	evLink, err := Link(tempDir, tempDir, docPath, "Document One", ownerID)
	if err != nil {
		t.Fatalf("Link artifact failed: %v", err)
	}
	if evLink == nil {
		t.Fatalf("expected non-nil event")
	}

	// 2. List artifacts -> status ok
	list, err := List(tempDir, tempDir)
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 artifact, got %d", len(list))
	}
	if list[0].Status != "ok" {
		t.Errorf("expected status ok, got %s", list[0].Status)
	}

	// 3. Modify artifact file -> status modified
	if err := os.WriteFile(docPath, []byte("modified content"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	listMod, _ := List(tempDir, tempDir)
	if len(listMod) != 1 || listMod[0].Status != "modified" {
		t.Errorf("expected status modified, got %s", listMod[0].Status)
	}

	// 4. Unlink artifact
	artID := list[0].ID
	_, err = Unlink(tempDir, artID, ownerID)
	if err != nil {
		t.Fatalf("Unlink failed: %v", err)
	}

	listAfter, _ := List(tempDir, tempDir)
	if len(listAfter) != 0 {
		t.Errorf("expected 0 artifacts after unlink, got %d", len(listAfter))
	}
}

func TestArtifactOutsideRootRefused(t *testing.T) {
	tempDir := t.TempDir()
	ownerID, _ := identity.Generate("owner")

	outsidePath := filepath.Join(os.TempDir(), "outside.txt")
	if err := os.WriteFile(outsidePath, []byte("outside"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}
	defer func() { _ = os.Remove(outsidePath) }()

	_, err := Link(tempDir, tempDir, outsidePath, "Outside Doc", ownerID)
	if err == nil {
		t.Fatalf("expected error for path outside root, got nil")
	}
	if err != ErrOutsideRoot {
		t.Errorf("expected ErrOutsideRoot, got %v", err)
	}
}

func TestHandleDeskEventRecordsChanges(t *testing.T) {
	tempDir := t.TempDir()
	ownerID, err := identity.Generate("owner")
	if err != nil {
		t.Fatalf("generate identity: %v", err)
	}
	docPath := filepath.Join(tempDir, "doc.txt")
	if err := os.WriteFile(docPath, []byte("before"), 0o644); err != nil {
		t.Fatalf("write document: %v", err)
	}
	if _, err := Link(tempDir, tempDir, docPath, "Document", ownerID); err != nil {
		t.Fatalf("Link: %v", err)
	}

	if err := HandleDeskEvent(tempDir, tempDir, &desk.EventStreamItem{Event: "file_changed", Path: docPath}, ownerID); err != nil {
		t.Fatalf("HandleDeskEvent changed: %v", err)
	}
	if err := os.Remove(docPath); err != nil {
		t.Fatalf("remove document: %v", err)
	}
	if err := HandleDeskEvent(tempDir, tempDir, &desk.EventStreamItem{Event: "file_removed", Path: docPath}, ownerID); err != nil {
		t.Fatalf("HandleDeskEvent removed: %v", err)
	}

	merged, err := journal.New(filepath.Join(tempDir, "journal")).MergeAll()
	if err != nil {
		t.Fatalf("MergeAll: %v", err)
	}
	var changed []*event.Event
	for _, ev := range merged {
		if ev.Kind == event.KindArtifactChanged {
			changed = append(changed, ev)
		}
	}
	if len(changed) != 2 {
		t.Fatalf("artifact.changed events = %d, want 2", len(changed))
	}
	var body struct {
		EventType string `json:"event_type"`
		SHA256    string `json:"sha256"`
	}
	if err := json.Unmarshal(changed[0].Body, &body); err != nil {
		t.Fatalf("unmarshal changed body: %v", err)
	}
	if body.EventType != "file_changed" || body.SHA256 == "" {
		t.Errorf("changed body = %+v, want file_changed with hash", body)
	}
	if err := json.Unmarshal(changed[1].Body, &body); err != nil {
		t.Fatalf("unmarshal removed body: %v", err)
	}
	if body.EventType != "file_removed" || body.SHA256 != "" {
		t.Errorf("removed body = %+v, want file_removed with empty hash", body)
	}
}
