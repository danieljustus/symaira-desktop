package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-room/internal/identity"
	"github.com/danieljustus/symaira-room/internal/members"
	"github.com/danieljustus/symaira-room/internal/room"
)

// hermeticEnv redirects identity/config storage to a temp dir, clears the
// signing-key env var and SYMROOM_ROOM_DIR, and empties PATH so per-command
// functions can be invoked in-process without touching the developer's
// machine or shelling out to symvault/keychain fallback chains.
func hermeticEnv(t *testing.T) {
	t.Helper()
	base := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(base, "data"))
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(base, "config"))
	t.Setenv("SYMROOM_IDENTITY_KEY", "")
	t.Setenv("SYMROOM_ROOM_DIR", "")
	t.Setenv("PATH", t.TempDir())
}

// runner matches the runXxx command signatures that do not need a context.
type runner func(args []string, stdout, stderr io.Writer) int

// assertExit checks the exit code and output substrings of an in-process
// command invocation.
func assertExit(t *testing.T, name string, code, wantCode int, out, errOut string, wantOut []string) {
	t.Helper()
	if code != wantCode {
		t.Errorf("%s: exit code = %d, want %d", name, code, wantCode)
	}
	for _, w := range wantOut {
		if !strings.Contains(out+errOut, w) {
			t.Errorf("%s: output missing %q\nstdout: %s\nstderr: %s", name, w, out, errOut)
		}
	}
}

// runInProcess invokes a runXxx-style command in-process with captured output.
func runInProcess(fn runner, args []string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := fn(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

// TestRunVersionTable covers the version command's plain and --json surfaces.
func TestRunVersionTable(t *testing.T) {
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  []string
	}{
		{"plain", []string{"symroom", "version"}, 0, []string{"symroom"}},
		{"json", []string{"symroom", "version", "--json"}, 0, []string{`"tool":"symroom"`}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out, errOut := runInProcess(runVersion, tt.args)
			assertExit(t, tt.name, code, tt.wantCode, out, errOut, tt.wantOut)
		})
	}
}

// TestRunIdentityExitBranches covers the identity command's action dispatch
// and error/exit-code branches in-process (no room required).
func TestRunIdentityExitBranches(t *testing.T) {
	hermeticEnv(t)
	alice, err := identity.Generate("alice")
	if err != nil {
		t.Fatalf("generate alice: %v", err)
	}
	if err := identity.Save(alice); err != nil {
		t.Fatalf("identity.Save: %v", err)
	}

	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  []string
	}{
		{"no action", []string{"symroom", "identity"}, 0, []string{"Usage: symroom identity"}},
		{"create no name", []string{"symroom", "identity", "create"}, 2, []string{"Usage: symroom identity create"}},
		{"unknown action", []string{"symroom", "identity", "bogus"}, 2, []string{"Unknown identity action"}},
		{"export no name", []string{"symroom", "identity", "export"}, 2, []string{"Usage: symroom identity export"}},
		{"export missing identity", []string{"symroom", "identity", "export", "--public", "ghost"}, 5, []string{"Error loading identity"}},
		{"export public key", []string{"symroom", "identity", "export", "--public", "alice"}, 0, nil},
		{"export private forbidden", []string{"symroom", "identity", "export", "alice"}, 4, []string{"forbidden"}},
		{"show", []string{"symroom", "identity", "show", "alice"}, 0, []string{"alice", "Member ID"}},
		{"show missing", []string{"symroom", "identity", "show", "ghost"}, 5, []string{"Error loading identity"}},
		{"list", []string{"symroom", "identity", "list"}, 0, []string{"alice"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out, errOut := runInProcess(runIdentity, tt.args)
			assertExit(t, tt.name, code, tt.wantCode, out, errOut, tt.wantOut)
		})
	}

	// create writes a second identity and is visible via list.
	code, out, _ := runInProcess(runIdentity, []string{"symroom", "identity", "create", "bob"})
	assertExit(t, "create", code, 0, out, "", []string{"Created identity bob"})
	code, out, _ = runInProcess(runIdentity, []string{"symroom", "identity", "list"})
	assertExit(t, "list after create", code, 0, out, "", []string{"alice", "bob"})
}

// TestRunMemberUsageBranches covers member action dispatch and usage/error
// branches that terminate before any identity or room access.
func TestRunMemberUsageBranches(t *testing.T) {
	hermeticEnv(t)
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  []string
	}{
		{"no action", []string{"symroom", "member"}, 0, []string{"Usage: symroom member"}},
		{"help flag", []string{"symroom", "member", "--help"}, 0, []string{"Usage: symroom member"}},
		{"unknown action", []string{"symroom", "member", "bogus"}, 2, []string{"Unknown member action"}},
		{"add no name or pubkey", []string{"symroom", "member", "add"}, 2, []string{"Usage: symroom member add"}},
		{"remove no member id", []string{"symroom", "member", "remove"}, 2, []string{"Usage: symroom member remove"}},
		{"role missing args", []string{"symroom", "member", "role"}, 2, []string{"Usage: symroom member role"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out, errOut := runInProcess(runMember, tt.args)
			assertExit(t, tt.name, code, tt.wantCode, out, errOut, tt.wantOut)
		})
	}
}

// TestRunNoteDecideUsage covers the usage branches of note and decide, which
// return exit 0 with a usage hint before any identity resolution.
func TestRunNoteDecideUsage(t *testing.T) {
	hermeticEnv(t)
	tests := []struct {
		name     string
		args     []string
		run      runner
		wantCode int
		wantOut  []string
	}{
		{"note no message", []string{"symroom", "note"}, runNote, 0, []string{"Usage: symroom note"}},
		{"decide no decision", []string{"symroom", "decide"}, runDecide, 0, []string{"Usage: symroom decide"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out, errOut := runInProcess(tt.run, tt.args)
			assertExit(t, tt.name, code, tt.wantCode, out, errOut, tt.wantOut)
		})
	}
}

// TestRunIndexUsage covers the index subcommand guard.
func TestRunIndexUsage(t *testing.T) {
	hermeticEnv(t)
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  []string
	}{
		{"no subcommand", []string{"symroom", "index"}, 0, []string{"Usage: symroom index rebuild"}},
		{"unknown subcommand", []string{"symroom", "index", "bogus"}, 0, []string{"Usage: symroom index rebuild"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out, errOut := runInProcess(runIndex, tt.args)
			assertExit(t, tt.name, code, tt.wantCode, out, errOut, tt.wantOut)
		})
	}
}

// TestRunArtifactUsageBranches covers artifact action dispatch and usage
// errors that terminate before identity resolution.
func TestRunArtifactUsageBranches(t *testing.T) {
	hermeticEnv(t)
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  []string
	}{
		{"no action", []string{"symroom", "artifact"}, 0, []string{"Usage: symroom artifact"}},
		{"unknown action", []string{"symroom", "artifact", "bogus"}, 2, []string{"Unknown artifact action"}},
		{"link no path", []string{"symroom", "artifact", "link"}, 2, []string{"Usage: symroom artifact link"}},
		{"unlink no id", []string{"symroom", "artifact", "unlink"}, 2, []string{"Usage: symroom artifact unlink"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out, errOut := runInProcess(runArtifact, tt.args)
			assertExit(t, tt.name, code, tt.wantCode, out, errOut, tt.wantOut)
		})
	}
}

// TestRunRunUsageBranches covers run action dispatch and usage errors that
// terminate before identity resolution or journal access.
func TestRunRunUsageBranches(t *testing.T) {
	hermeticEnv(t)
	ctx := context.Background()
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  []string
	}{
		{"no action", []string{"symroom", "run"}, 0, []string{"Usage: symroom run"}},
		{"unknown action", []string{"symroom", "run", "bogus"}, 2, []string{"Unknown run action"}},
		{"request no title", []string{"symroom", "run", "request"}, 2, []string{"--title is required"}},
		{"show no run id", []string{"symroom", "run", "show"}, 2, []string{"Usage: symroom run show"}},
		{"start no run id", []string{"symroom", "run", "start"}, 2, []string{"Usage: symroom run start"}},
		{"cancel no run id", []string{"symroom", "run", "cancel"}, 2, []string{"Usage: symroom run cancel"}},
		{"wait no run id", []string{"symroom", "run", "wait"}, 2, []string{"Usage: symroom run wait"}},
		{"approve no run id", []string{"symroom", "run", "approve"}, 2, []string{"Usage: symroom run approve"}},
		{"deny no run id", []string{"symroom", "run", "deny"}, 2, []string{"Usage: symroom run deny"}},
		{"deny no reason", []string{"symroom", "run", "deny", "run_1"}, 2, []string{"Usage: symroom run deny"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out, errOut := runInProcess(func(args []string, stdout, stderr io.Writer) int {
				return runRun(ctx, args, stdout, stderr)
			}, tt.args)
			assertExit(t, tt.name, code, tt.wantCode, out, errOut, tt.wantOut)
		})
	}
}

// TestRunCheckpointUsageBranches covers checkpoint action dispatch and usage
// errors that terminate before identity resolution.
func TestRunCheckpointUsageBranches(t *testing.T) {
	hermeticEnv(t)
	ctx := context.Background()
	tests := []struct {
		name     string
		args     []string
		wantCode int
		wantOut  []string
	}{
		{"no action", []string{"symroom", "checkpoint"}, 0, []string{"Usage: symroom checkpoint"}},
		{"unknown action", []string{"symroom", "checkpoint", "bogus"}, 2, []string{"Unknown checkpoint action"}},
		{"request no flags", []string{"symroom", "checkpoint", "request"}, 2, []string{"Usage: symroom checkpoint request"}},
		{"resolve no id", []string{"symroom", "checkpoint", "resolve"}, 2, []string{"Usage: symroom checkpoint resolve"}},
		{"resolve no answer", []string{"symroom", "checkpoint", "resolve", "chk_1"}, 2, []string{"Usage: symroom checkpoint resolve"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code, out, errOut := runInProcess(func(args []string, stdout, stderr io.Writer) int {
				return runCheckpoint(ctx, args, stdout, stderr)
			}, tt.args)
			assertExit(t, tt.name, code, tt.wantCode, out, errOut, tt.wantOut)
		})
	}
}

// TestRunBrainProfileAndWatchUsage covers the remaining usage guards.
func TestRunBrainProfileAndWatchUsage(t *testing.T) {
	hermeticEnv(t)
	ctx := context.Background()
	code, out, errOut := runInProcess(runBrainProfile, []string{"symroom", "brain-profile"})
	assertExit(t, "brain-profile no member", code, 2, out, errOut, []string{"Usage: symroom brain-profile"})
	code, out, errOut = runInProcess(func(args []string, stdout, stderr io.Writer) int {
		return runWatch(ctx, args, stdout, stderr)
	}, []string{"symroom", "watch"})
	assertExit(t, "watch no desk", code, 0, out, errOut, []string{"Usage: symroom watch"})
}

// setupRoom creates a hermetic room with an owner identity and returns the
// room directory. The room path is also exported via SYMROOM_ROOM_DIR so
// commands operate on it regardless of the test process working directory.
func setupRoom(t *testing.T) (string, *identity.Identity) {
	t.Helper()
	hermeticEnv(t)
	base := t.TempDir()
	roomDirPath := filepath.Join(base, "room")
	owner, err := identity.Generate("owner")
	if err != nil {
		t.Fatalf("generate owner: %v", err)
	}
	if err := identity.Save(owner); err != nil {
		t.Fatalf("identity.Save: %v", err)
	}
	if _, err := room.Init(roomDirPath, "Test Room", owner); err != nil {
		t.Fatalf("room.Init: %v", err)
	}
	t.Setenv("SYMROOM_ROOM_DIR", roomDirPath)
	return roomDirPath, owner
}

// TestRunRoomCommandsInProcess exercises room-backed commands in-process:
// journal reads/writes, member listing, artifact linking, run lookups, brain
// profiles and doctor reporting — all without spawning child processes.
func TestRunRoomCommandsInProcess(t *testing.T) {
	roomDirPath, owner := setupRoom(t)
	bob, err := identity.Generate("bob")
	if err != nil {
		t.Fatalf("generate bob: %v", err)
	}
	if _, err := room.AddMember(roomDirPath, "bob", hex.EncodeToString(bob.PublicKey), members.RoleAgent, members.KindAgent, owner); err != nil {
		t.Fatalf("AddMember bob: %v", err)
	}

	// member list (human + json surfaces).
	code, out, errOut := runInProcess(runMember, []string{"symroom", "member", "list"})
	assertExit(t, "member list", code, 0, out, errOut, []string{"owner", "bob"})
	code, out, errOut = runInProcess(runMember, []string{"symroom", "member", "list", "--json"})
	assertExit(t, "member list --json", code, 0, out, errOut, []string{`"id"`, `"name"`})
	var membersJSON []map[string]any
	if err := json.Unmarshal([]byte(out), &membersJSON); err != nil {
		t.Errorf("member list --json not valid JSON: %v (%q)", err, out)
	}
	if len(membersJSON) != 2 {
		t.Errorf("member list --json: got %d members, want 2", len(membersJSON))
	}

	// note + decide happy paths (resolve identity from the hermetic store).
	code, out, errOut = runInProcess(runNote, []string{"symroom", "note", "--identity", "owner", "hello world"})
	assertExit(t, "note", code, 0, out, errOut, nil)
	if !strings.HasPrefix(out, "ev_") {
		t.Errorf("note output %q, want event ID", out)
	}
	noteID := strings.TrimSpace(out)
	code, out, errOut = runInProcess(runNote, []string{"symroom", "note", "--identity", "owner", "--json", "hello json"})
	assertExit(t, "note --json", code, 0, out, errOut, []string{"note.posted"})

	code, out, errOut = runInProcess(runDecide, []string{"symroom", "decide", "--identity", "owner", "--refs", noteID, "ship it"})
	assertExit(t, "decide", code, 0, out, errOut, nil)

	// log (human + ndjson surfaces).
	code, out, errOut = runInProcess(runLog, []string{"symroom", "log"})
	assertExit(t, "log", code, 0, out, errOut, []string{"hello world", "note.posted"})
	code, out, errOut = runInProcess(runLog, []string{"symroom", "log", "--json", "--limit", "10"})
	assertExit(t, "log --json", code, 0, out, errOut, []string{`"kind":"note.posted"`})

	// verify (human + json).
	code, out, errOut = runInProcess(runVerify, []string{"symroom", "verify"})
	assertExit(t, "verify", code, 0, out, errOut, []string{"PASSED"})
	code, out, errOut = runInProcess(runVerify, []string{"symroom", "verify", "--json"})
	assertExit(t, "verify --json", code, 0, out, errOut, []string{`"valid": true`})

	// artifact link + list (human + json).
	docPath := filepath.Join(roomDirPath, "doc.md")
	if err := os.WriteFile(docPath, []byte("# doc\n"), 0o644); err != nil {
		t.Fatalf("write doc.md: %v", err)
	}
	code, out, errOut = runInProcess(runArtifact, []string{"symroom", "artifact", "link", "--identity", "owner", docPath})
	assertExit(t, "artifact link", code, 0, out, errOut, nil)
	if !strings.HasPrefix(out, "ev_") {
		t.Errorf("artifact link output %q, want event ID", out)
	}
	artID := "art_" + strings.TrimPrefix(strings.TrimSpace(out), "ev_")
	code, out, errOut = runInProcess(runArtifact, []string{"symroom", "artifact", "list"})
	assertExit(t, "artifact list", code, 0, out, errOut, []string{"doc.md"})
	code, out, errOut = runInProcess(runArtifact, []string{"symroom", "artifact", "list", "--json"})
	assertExit(t, "artifact list --json", code, 0, out, errOut, []string{"doc.md"})

	// run show: not found -> exit 5; list -> empty ok.
	code, out, errOut = runInProcess(func(args []string, stdout, stderr io.Writer) int {
		return runRun(context.Background(), args, stdout, stderr)
	}, []string{"symroom", "run", "show", "run_ghost"})
	assertExit(t, "run show ghost", code, 5, out, errOut, []string{"not found"})
	code, out, errOut = runInProcess(func(args []string, stdout, stderr io.Writer) int {
		return runRun(context.Background(), args, stdout, stderr)
	}, []string{"symroom", "run", "list", "--json"})
	assertExit(t, "run list --json", code, 0, out, errOut, nil)
	if !strings.Contains(out, "null") {
		t.Errorf("run list --json output %q, want null (no runs)", out)
	}

	// brain-profile: existing member -> 0; unknown member -> generic error.
	code, out, errOut = runInProcess(runBrainProfile, []string{"symroom", "brain-profile", "--member", bob.MemberID})
	assertExit(t, "brain-profile", code, 0, out, errOut, []string{"# To install run:"})
	code, out, errOut = runInProcess(runBrainProfile, []string{"symroom", "brain-profile", "--member", "member_ghost"})
	assertExit(t, "brain-profile ghost", code, 1, out, errOut, []string{"Error generating brain profile"})

	// doctor: healthy room + default identity -> exit 0 (human and json).
	t.Setenv("SYMROOM_DEFAULT_IDENTITY", "owner")
	code, out, errOut = runInProcess(runDoctor, []string{"symroom", "doctor"})
	assertExit(t, "doctor", code, 0, out, errOut, []string{"[OK] room_manifest"})
	code, out, errOut = runInProcess(runDoctor, []string{"symroom", "doctor", "--json"})
	assertExit(t, "doctor --json", code, 0, out, errOut, []string{`"failed": false`})

	// index rebuild on the live room. The derived index path is relative to
	// the process working directory, so chdir to a scratch dir to avoid
	// writing .symroom/ into the source tree.
	t.Chdir(t.TempDir())
	code, out, errOut = runInProcess(runIndex, []string{"symroom", "index", "rebuild"})
	assertExit(t, "index rebuild", code, 0, out, errOut, []string{"Rebuilt derived index"})

	// artifact unlink round-trip.
	code, out, errOut = runInProcess(runArtifact, []string{"symroom", "artifact", "unlink", "--identity", "owner", artID})
	assertExit(t, "artifact unlink", code, 0, out, errOut, nil)
}
