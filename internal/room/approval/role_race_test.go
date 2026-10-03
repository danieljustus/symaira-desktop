package approval

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/room/authorization"
	"github.com/danieljustus/symaira-desktop/internal/room/event"
	"github.com/danieljustus/symaira-desktop/internal/room/identity"
	"github.com/danieljustus/symaira-desktop/internal/room/journal"
	"github.com/danieljustus/symaira-desktop/internal/room/members"
	"github.com/danieljustus/symaira-desktop/internal/room/room"
	"github.com/danieljustus/symaira-desktop/internal/room/run"
)

// The subprocess probe must observe real kernel contention before the parent
// releases its approval barrier. This makes the test independent of timing or
// whether the competing writer happened to get scheduled yet.
func TestApproveSerializesIndependentRoleChanges(t *testing.T) {
	for _, action := range []string{"remove", "observer", "agent"} {
		t.Run(action, func(t *testing.T) {
			roomDir := t.TempDir()
			t.Setenv("XDG_DATA_HOME", t.TempDir())
			owner, err := identity.Generate("owner")
			if err != nil {
				t.Fatal(err)
			}
			if err := identity.Save(owner); err != nil {
				t.Fatal(err)
			}
			actor, err := identity.Generate("reviewer")
			if err != nil {
				t.Fatal(err)
			}
			runID := requestRun(t, roomDir, owner)
			addMember(t, roomDir, owner, actor, members.RoleMember, members.KindHuman)
			checked := make(chan struct{})
			resume := make(chan struct{})
			approvalDone := make(chan error, 1)
			var approved *event.Event
			go func() {
				var err error
				approved, err = approve(roomDir, runID, "all", time.Hour, actor, func() { close(checked); <-resume })
				approvalDone <- err
			}()
			select {
			case <-checked:
			case err := <-approvalDone:
				t.Fatalf("approval did not reach barrier: %v", err)
			case <-time.After(30 * time.Second):
				t.Fatal("approval barrier timeout")
			}
			// Always release the goroutine if setup or a negative control fails.
			released := false
			defer func() {
				if !released {
					close(resume)
					<-approvalDone
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			testBinary, err := os.Executable()
			if err != nil {
				t.Fatal(err)
			}
			// Execute this running test image, resolved by the OS, with fixed arguments.
			cmd := exec.CommandContext(ctx, testBinary, "-test.run=^TestApprovalRoleChangeChild$", "-test.v") // #nosec G204 -- os.Executable identifies this test image; neither command nor arguments accept input.
			cmd.Env = append(os.Environ(), "SYMDESK_APPROVAL_ROLE_CHILD=1", "SYMDESK_APPROVAL_ROOM="+roomDir, "SYMDESK_APPROVAL_MEMBER="+actor.MemberID, "SYMDESK_APPROVAL_ACTION="+action)
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			var stderr strings.Builder
			cmd.Stderr = &stderr
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			scanner := bufio.NewScanner(stdout)
			contended := false
			for scanner.Scan() {
				if scanner.Text() == "AUTHORIZATION_LOCK_BUSY" {
					contended = true
					break
				}
			}
			if !contended {
				_ = cmd.Wait()
				t.Fatalf("independent writer did not see held kernel lock: %v; %s", scanner.Err(), stderr.String())
			}
			select {
			case err := <-approvalDone:
				t.Fatalf("approval left paused barrier: %v", err)
			default:
			}
			close(resume)
			released = true
			if err := <-approvalDone; err != nil {
				t.Fatalf("serialized approval: %v", err)
			}
			for scanner.Scan() {
			}
			if err := scanner.Err(); err != nil {
				t.Fatal(err)
			}
			if err := cmd.Wait(); err != nil {
				t.Fatalf("role writer: %v; %s", err, stderr.String())
			}
			events, err := journal.New(filepath.Join(roomDir, "journal")).MergeAll()
			if err != nil {
				t.Fatal(err)
			}
			approvalIndex, changeIndex := -1, -1
			for i, ev := range events {
				if ev.ID == approved.ID {
					approvalIndex = i
				}
				if ev.Kind == event.KindMemberRemoved || ev.Kind == event.KindMemberRoleChanged {
					changeIndex = i
				}
			}
			if approvalIndex < 0 || changeIndex <= approvalIndex {
				t.Fatalf("role change overtook locked authorization: approval=%d role=%d", approvalIndex, changeIndex)
			}
			projected, err := run.Get(roomDir, runID)
			if err != nil || projected.State != run.StateApproved {
				t.Fatalf("authorized ordered approval: %+v %v", projected, err)
			}
			report, err := journal.New(filepath.Join(roomDir, "journal")).Verify()
			if err != nil || !report.Valid {
				t.Fatalf("signed journal integrity changed: %+v %v", report, err)
			}
			// A later attempt observes the changed role and preserves existing denial.
			want := members.ErrMemberNotFound
			if action == "observer" {
				want = members.ErrObserverForbidden
			}
			if action == "agent" {
				want = ErrAgentApprovalForbidden
			}
			if _, err := Approve(roomDir, runID, "all", time.Hour, actor); err != want {
				t.Fatalf("post-change error=%v, want exact %v", err, want)
			}
		})
	}
}

func TestApprovalRoleChangeChild(t *testing.T) {
	if os.Getenv("SYMDESK_APPROVAL_ROLE_CHILD") != "1" {
		t.Skip("independent writer helper")
	}
	roomDir := os.Getenv("SYMDESK_APPROVAL_ROOM")
	release, err := authorization.TryAcquire(roomDir)
	if err == nil {
		_ = release()
		t.Fatal("approval released its transaction before append")
	}
	if !errors.Is(err, authorization.ErrBusy) {
		t.Fatalf("contention probe: %v", err)
	}
	fmt.Println("AUTHORIZATION_LOCK_BUSY")
	owner, err := identity.Load("owner")
	if err != nil {
		t.Fatal(err)
	}
	member := os.Getenv("SYMDESK_APPROVAL_MEMBER")
	switch action := os.Getenv("SYMDESK_APPROVAL_ACTION"); action {
	case "remove":
		_, err = room.RemoveMember(roomDir, member, owner)
	case "observer", "agent":
		_, err = room.SetMemberRole(roomDir, member, members.Role(action), owner)
	default:
		t.Fatalf("unknown action %q", action)
	}
	if err != nil {
		t.Fatal(err)
	}
}

// Raw/offline writers need not cooperate with the local transaction. Their
// invalid signed events remain in the audit journal but never approve a run.
func TestInvalidApprovalAfterRoleChangeRemainsAuditOnly(t *testing.T) {
	for _, action := range []string{"remove", "observer", "agent"} {
		t.Run(action, func(t *testing.T) {
			dir := t.TempDir()
			owner, err := identity.Generate("owner")
			if err != nil {
				t.Fatal(err)
			}
			actor, err := identity.Generate("reviewer")
			if err != nil {
				t.Fatal(err)
			}
			runID := requestRun(t, dir, owner)
			addMember(t, dir, owner, actor, members.RoleMember, members.KindHuman)
			if action == "remove" {
				_, err = room.RemoveMember(dir, actor.MemberID, owner)
			} else {
				_, err = room.SetMemberRole(dir, actor.MemberID, members.Role(action), owner)
			}
			if err != nil {
				t.Fatal(err)
			}
			cfg, err := room.ReadRoomConfig(dir)
			if err != nil {
				t.Fatal(err)
			}
			body, _ := json.Marshal(map[string]string{"run_id": runID, "approval_id": "app_offline_invalid", "scope": "all"})
			ev := &event.Event{V: event.CurrentVersion, ID: "ev_offline_invalid", Room: cfg.ID, Author: actor.MemberID, Kind: event.KindRunApproved, Body: body}
			j := journal.New(filepath.Join(dir, "journal"))
			if err := j.PrepareEvent(ev); err != nil {
				t.Fatal(err)
			}
			if err := ev.Sign(actor); err != nil {
				t.Fatal(err)
			}
			if err := j.Append(ev); err != nil {
				t.Fatal(err)
			}
			if err := j.VerifyChain(actor.MemberID); err != nil {
				t.Fatalf("audit hash chain: %v", err)
			}
			projected, err := run.Get(dir, runID)
			if err != nil || projected.State != run.StateRequested || projected.ApprovalID != "" {
				t.Fatalf("invalid approval became effective: %+v %v", projected, err)
			}
			report, err := j.Verify()
			if err != nil || report.Valid {
				t.Fatalf("invalid event was not reported: %+v %v", report, err)
			}
			found := false
			for _, finding := range report.Findings {
				if finding.EventID == ev.ID && finding.Code != journal.CodeSignatureInvalid && finding.Code != journal.CodeChainBroken {
					found = true
				}
			}
			if !found {
				t.Fatalf("validly signed invalid approval lacks authorization finding: %+v", report)
			}
			if _, err := run.Start(dir, runID, owner); !errors.Is(err, run.ErrInvalidTransition) {
				t.Fatalf("invalid approval allowed Start: %v", err)
			}
			for _, finding := range report.Findings {
				if finding.Code == journal.CodeSignatureInvalid || finding.Code == journal.CodeChainBroken || finding.Code == journal.CodeSeqMismatch {
					t.Fatalf("authorization test corrupted integrity: %+v", finding)
				}
			}
		})
	}
}
