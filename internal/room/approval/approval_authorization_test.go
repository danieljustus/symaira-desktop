package approval

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/room/identity"
	"github.com/danieljustus/symaira-desktop/internal/room/members"
	"github.com/danieljustus/symaira-desktop/internal/room/run"
)

func TestApproveRejectsNonApproversWithoutAppending(t *testing.T) {
	for _, tt := range []struct {
		name string
		add  bool
		want error
	}{
		{name: "observer", add: true, want: members.ErrObserverForbidden},
		{name: "not-a-member", want: members.ErrMemberNotFound},
	} {
		t.Run(tt.name, func(t *testing.T) {
			roomDir := t.TempDir()
			owner, err := identity.Generate("owner")
			if err != nil {
				t.Fatal(err)
			}
			actor, err := identity.Generate(tt.name)
			if err != nil {
				t.Fatal(err)
			}
			runID := requestRun(t, roomDir, owner)
			if tt.add {
				addMember(t, roomDir, owner, actor, members.RoleObserver, members.KindHuman)
			}
			segment := filepath.Join(roomDir, "journal", actor.MemberID+".jsonl")
			if _, err := os.Stat(segment); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unexpected pre-existing actor segment: %v", err)
			}
			if _, err := Approve(roomDir, runID, "all", 10*time.Minute, actor); !errors.Is(err, tt.want) {
				t.Fatalf("Approve error = %v, want %v", err, tt.want)
			}
			if _, err := os.Stat(segment); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("denied approval wrote a journal segment: %v", err)
			}
			r, err := run.Get(roomDir, runID)
			if err != nil {
				t.Fatal(err)
			}
			if r.State != run.StateRequested {
				t.Fatalf("denied approval changed run state: %s", r.State)
			}
		})
	}
}
