package main

import (
	"encoding/hex"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-room/internal/identity"
	"github.com/danieljustus/symaira-room/internal/members"
	"github.com/danieljustus/symaira-room/internal/room"
)

func buildBinary(t *testing.T) string {
	t.Helper()
	binPath := filepath.Join(t.TempDir(), "symroom")
	cmd := exec.Command("go", "build", "-o", binPath, ".")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("failed to build test binary: %v, output: %s", err, string(out))
	}
	return binPath
}

func TestCLIUsage(t *testing.T) {
	binPath := buildBinary(t)
	cmd := exec.Command(binPath)
	err := cmd.Run()
	if err == nil {
		t.Fatalf("expected error/exit code 2 when running without arguments, got success")
	}
	if exitErr, ok := err.(*exec.ExitError); ok {
		if exitErr.ExitCode() != 2 {
			t.Fatalf("expected exit code 2, got %d", exitErr.ExitCode())
		}
	} else {
		t.Fatalf("unexpected error type: %v", err)
	}
}

func TestCLISubcommand(t *testing.T) {
	binPath := buildBinary(t)
	subcommands := []string{
		"init", "note", "decide", "log", "verify", "index", "watch",
		"brain-profile", "doctor", "version", "mcp", "member",
	}
	for _, sub := range subcommands {
		cmd := exec.Command(binPath, sub, "--help")
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("subcommand %s failed: %v, output: %s", sub, err, string(out))
		}
	}
}

// TestCLIMemberManagement exercises the member subcommands end-to-end through
// the built binary: member add persists a member.added event, member list
// shows the added member, and a non-owner caller is rejected with nothing
// persisted.
func TestCLIMemberManagement(t *testing.T) {
	binPath := buildBinary(t)
	tempDir := t.TempDir()
	t.Setenv("XDG_DATA_HOME", filepath.Join(tempDir, "data"))

	ownerID, err := identity.Generate("owner")
	if err != nil {
		t.Fatalf("generate owner identity: %v", err)
	}
	bobID, err := identity.Generate("bob")
	if err != nil {
		t.Fatalf("generate bob identity: %v", err)
	}
	carolID, err := identity.Generate("carol")
	if err != nil {
		t.Fatalf("generate carol identity: %v", err)
	}
	eveID, err := identity.Generate("eve")
	if err != nil {
		t.Fatalf("generate eve identity: %v", err)
	}

	roomDir := filepath.Join(tempDir, "room")
	if _, err := room.Init(roomDir, "Test Room", ownerID); err != nil {
		t.Fatalf("init room: %v", err)
	}

	runCLI := func(args ...string) (string, int) {
		t.Helper()
		cmd := exec.Command(binPath, args...)
		cmd.Dir = roomDir
		out, err := cmd.CombinedOutput()
		code := 0
		if exitErr, ok := err.(*exec.ExitError); ok {
			code = exitErr.ExitCode()
		} else if err != nil {
			t.Fatalf("run %v: %v", args, err)
		}
		return string(out), code
	}

	// Owner adds bob using the flag form documented in the README.
	t.Setenv("SYMROOM_IDENTITY_KEY", hex.EncodeToString(ownerID.PrivateKey))
	out, code := runCLI("member", "add", "--identity", "owner", "--pubkey", hex.EncodeToString(bobID.PublicKey), "--name", "bob", "--role", "agent", "--kind", "agent")
	if code != 0 {
		t.Fatalf("member add as owner failed (exit %d): %s", code, out)
	}
	if !strings.Contains(out, "Member added") || !strings.Contains(out, "bob") {
		t.Errorf("unexpected member add output: %s", out)
	}

	// The event landed in the journal.
	state, err := room.ListMembers(roomDir)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	bob, ok := state.Members[bobID.MemberID]
	if !ok {
		t.Fatalf("member.added event not persisted for bob")
	}
	if bob.Name != "bob" || bob.Role != members.RoleAgent || bob.Kind != members.KindAgent {
		t.Errorf("unexpected bob state: %+v", bob)
	}

	// Positional form also works.
	out, code = runCLI("member", "add", "--identity", "owner", "carol", hex.EncodeToString(carolID.PublicKey))
	if code != 0 {
		t.Fatalf("member add positional failed (exit %d): %s", code, out)
	}

	// member list shows owner, bob and carol.
	out, code = runCLI("member", "list")
	if code != 0 {
		t.Fatalf("member list failed (exit %d): %s", code, out)
	}
	for _, want := range []string{"owner", "bob", "carol"} {
		if !strings.Contains(out, want) {
			t.Errorf("member list missing %q in output: %s", want, out)
		}
	}

	// Owner changes bob's role.
	out, code = runCLI("member", "role", "--identity", "owner", bobID.MemberID, "member")
	if code != 0 {
		t.Fatalf("member role failed (exit %d): %s", code, out)
	}
	state, err = room.ListMembers(roomDir)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if state.Members[bobID.MemberID].Role != members.RoleMember {
		t.Errorf("expected bob role %s, got %s", members.RoleMember, state.Members[bobID.MemberID].Role)
	}

	// Owner removes carol.
	out, code = runCLI("member", "remove", "--identity", "owner", carolID.MemberID)
	if code != 0 {
		t.Fatalf("member remove failed (exit %d): %s", code, out)
	}
	state, err = room.ListMembers(roomDir)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if _, ok := state.Members[carolID.MemberID]; ok {
		t.Errorf("carol still present after remove")
	}

	// A non-owner (stranger, not a member) is rejected with exit code 4
	// (forbidden) and nothing is persisted.
	t.Setenv("SYMROOM_IDENTITY_KEY", hex.EncodeToString(eveID.PrivateKey))
	out, code = runCLI("member", "add", "--identity", "eve", "--pubkey", hex.EncodeToString(eveID.PublicKey), "--name", "eve")
	if code != 4 {
		t.Fatalf("expected exit 4 for non-owner member add, got %d: %s", code, out)
	}
	if !strings.Contains(out, "only room owners") {
		t.Errorf("expected clear owner-only error, got: %s", out)
	}
	state, err = room.ListMembers(roomDir)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if _, ok := state.Members[eveID.MemberID]; ok {
		t.Errorf("non-owner member add persisted an event")
	}

	// Invalid role value is rejected without persisting.
	t.Setenv("SYMROOM_IDENTITY_KEY", hex.EncodeToString(ownerID.PrivateKey))
	out, code = runCLI("member", "add", "--identity", "owner", "--pubkey", hex.EncodeToString(eveID.PublicKey), "--name", "eve", "--role", "admin")
	if code != 2 {
		t.Fatalf("expected exit 2 for invalid role, got %d: %s", code, out)
	}
	state, err = room.ListMembers(roomDir)
	if err != nil {
		t.Fatalf("ListMembers: %v", err)
	}
	if _, ok := state.Members[eveID.MemberID]; ok {
		t.Errorf("invalid-role member add persisted an event")
	}
}
