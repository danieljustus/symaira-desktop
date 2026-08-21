package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-room/internal/identity"
)

// reexecEnv marks a child process that should run the real main() dispatch
// switch instead of the test suite (see TestMain).
const reexecEnv = "SYMROOM_MAIN_REEXEC"

// TestMain supports the re-exec pattern for main() coverage: main() is a
// monolithic switch that terminates every branch with os.Exit, so it cannot be
// called in-process. When SYMROOM_MAIN_REEXEC is set, this process instead
// rebuilds os.Args from the JSON-encoded argv and runs the real main() — with
// coverage enabled, os.Exit still flushes the counters (Go >= 1.20 writes
// covdata on exit), so the child processes contribute genuine coverage for
// main.go's dispatch paths.
func TestMain(m *testing.M) {
	if raw := os.Getenv(reexecEnv); raw != "" {
		var args []string
		if err := json.Unmarshal([]byte(raw), &args); err != nil {
			fmt.Fprintf(os.Stderr, "invalid %s: %v\n", reexecEnv, err)
			os.Exit(2)
		}
		os.Args = append([]string{"symroom"}, args...)
		main()
		os.Exit(0) // unreachable: main() always terminates via os.Exit.
	}
	os.Exit(m.Run())
}

// runMain spawns this test binary in re-exec mode so the real main() runs with
// args in a fresh child process, in dir, with hermetic XDG dirs, an empty PATH
// (so identity.Load never shells out to symvault or the macOS keychain) and no
// inherited signing key. Combined output and exit code are returned.
func runMain(t *testing.T, dir, xdgData, xdgConfig string, args ...string) (string, int) {
	t.Helper()
	argsJSON, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("marshal args %v: %v", args, err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Dir = dir
	cmd.Stdin = strings.NewReader("") // immediate EOF so mcp mode exits cleanly
	cmd.Env = append(filterEnv("PATH=", "SYMROOM_IDENTITY_KEY="),
		"PATH="+t.TempDir(),
		"XDG_DATA_HOME="+xdgData,
		"XDG_CONFIG_HOME="+xdgConfig,
		"SYMROOM_IDENTITY_KEY=",
		reexecEnv+"="+string(argsJSON),
	)
	var buf bytes.Buffer
	cmd.Stdout = &buf
	cmd.Stderr = &buf
	runErr := cmd.Run()
	code := 0
	if ee, ok := runErr.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if runErr != nil {
		t.Fatalf("runMain(%v): %v", args, runErr)
	}
	return buf.String(), code
}

// filterEnv returns the current environment without entries whose key matches
// one of the given "KEY=" prefixes, so tests can fully override them.
func filterEnv(prefixes ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		drop := false
		for _, p := range prefixes {
			if strings.HasPrefix(kv, p) {
				drop = true
				break
			}
		}
		if !drop {
			env = append(env, kv)
		}
	}
	return env
}

// TestMainDispatchCoversSubcommands exercises the real main() dispatch switch
// through the TestMain re-exec pattern. State (identities on disk, the room
// journal) is shared across children via one hermetic XDG_DATA_HOME and room
// directory, mirroring how a user would drive the CLI.
func TestMainDispatchCoversSubcommands(t *testing.T) {
	// EvalSymlinks normalizes /var -> /private/var on macOS so that
	// filepath.Rel comparisons between the room dir and its children match.
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks temp dir: %v", err)
	}
	xdgData := filepath.Join(base, "xdg-data")
	xdgConfig := filepath.Join(base, "xdg-config")
	roomDir := filepath.Join(base, "room")

	assertRun := func(dir string, wantCode int, wantOut []string, args ...string) string {
		t.Helper()
		out, code := runMain(t, dir, xdgData, xdgConfig, args...)
		if code != wantCode {
			t.Errorf("args %v: exit code = %d, want %d\noutput: %s", args, code, wantCode, out)
		}
		for _, w := range wantOut {
			if !strings.Contains(out, w) {
				t.Errorf("args %v: output missing %q:\n%s", args, w, out)
			}
		}
		return strings.TrimSpace(out)
	}

	// Generated in-process so the tests never touch a real keychain or disk.
	bob, err := identity.Generate("bob")
	if err != nil {
		t.Fatalf("generate bob: %v", err)
	}
	eve, err := identity.Generate("eve")
	if err != nil {
		t.Fatalf("generate eve: %v", err)
	}
	bobID := identity.ComputeMemberID(bob.PublicKey)

	// ---- top-level dispatch -------------------------------------------------
	assertRun(base, 2, []string{"Available Subcommands"}) // no args -> usage + exit 2
	assertRun(base, 0, []string{"Available Subcommands"}, "--help")
	assertRun(base, 2, []string{"Unknown subcommand"}, "bogus")

	// ---- version -------------------------------------------------------------
	assertRun(base, 0, []string{"symroom"}, "version")
	versionJSON := assertRun(base, 0, []string{`"tool":"symroom"`}, "version", "--json")
	var vi map[string]any
	if err := json.Unmarshal([]byte(versionJSON), &vi); err != nil {
		t.Errorf("version --json output is not valid JSON: %v (%q)", err, versionJSON)
	}

	// ---- identity ------------------------------------------------------------
	assertRun(base, 0, []string{"Usage: symroom identity"}, "identity")
	assertRun(base, 2, []string{"Usage: symroom identity create"}, "identity", "create")
	assertRun(base, 0, []string{"Created identity alice"}, "identity", "create", "alice")
	assertRun(base, 0, []string{"alice"}, "identity", "list")
	assertRun(base, 0, []string{"alice", "Member ID"}, "identity", "show", "alice")
	assertRun(base, 5, []string{"Error loading identity"}, "identity", "show", "ghost")
	pubOut := assertRun(base, 0, nil, "identity", "export", "--public", "alice")
	if _, err := hex.DecodeString(pubOut); err != nil {
		t.Errorf("identity export --public output is not hex: %q", pubOut)
	}
	assertRun(base, 4, []string{"forbidden"}, "identity", "export", "alice")
	assertRun(base, 2, []string{"Unknown identity action"}, "identity", "bogus")

	// ---- room setup ----------------------------------------------------------
	assertRun(base, 0, []string{"Initialized room"}, "init", "--identity", "alice", "--name", "Test Room", roomDir)

	// ---- member --------------------------------------------------------------
	assertRun(roomDir, 0, []string{"Member added"},
		"member", "add", "--identity", "alice", "--pubkey", hex.EncodeToString(bob.PublicKey), "--name", "bob", "--role", "agent", "--kind", "agent")
	assertRun(roomDir, 0, []string{"Test Room", "bob"}, "member", "list")
	membersJSON := assertRun(roomDir, 0, []string{`"id"`, `"name"`, `"role"`, `"kind"`}, "member", "list", "--json")
	var members []map[string]any
	if err := json.Unmarshal([]byte(membersJSON), &members); err != nil {
		t.Errorf("member list --json output is not a JSON array: %v (%q)", err, membersJSON)
	}
	if len(members) != 2 {
		t.Errorf("member list --json: got %d members, want 2 (%q)", len(members), membersJSON)
	}
	assertRun(roomDir, 0, []string{"Updated role for"}, "member", "role", "--identity", "alice", bobID, "member")
	assertRun(roomDir, 0, []string{"# To install run:"}, "brain-profile", "--member", bobID)
	assertRun(base, 0, []string{"Created identity eve"}, "identity", "create", "eve")
	assertRun(roomDir, 4, []string{"only room owners"},
		"member", "add", "--identity", "eve", "--pubkey", hex.EncodeToString(eve.PublicKey), "--name", "eve")
	assertRun(roomDir, 2, []string{"invalid member role"},
		"member", "add", "--identity", "alice", "--pubkey", hex.EncodeToString(eve.PublicKey), "--name", "eve", "--role", "admin")
	assertRun(roomDir, 2, []string{"invalid member kind"},
		"member", "add", "--identity", "alice", "--pubkey", hex.EncodeToString(eve.PublicKey), "--name", "eve", "--kind", "cyborg")
	assertRun(roomDir, 5, []string{"member not found"}, "member", "remove", "--identity", "alice", "member_ghost")
	assertRun(roomDir, 0, []string{"Member removed"}, "member", "remove", "--identity", "alice", bobID)

	// ---- note / decide / log / verify / index --------------------------------
	noteID := assertRun(roomDir, 0, nil, "note", "--identity", "alice", "hello world")
	if !strings.HasPrefix(noteID, "ev_") {
		t.Errorf("note output %q, want event ID", noteID)
	}
	assertRun(roomDir, 0, []string{"note.posted"}, "note", "--identity", "alice", "--json", "hello json")
	assertRun(roomDir, 2, []string{"--identity is required"}, "note", "no identity")
	assertRun(roomDir, 0, nil, "decide", "--identity", "alice", "--refs", noteID, "ship it")
	assertRun(roomDir, 0, []string{"hello world", "note.posted"}, "log")
	assertRun(roomDir, 0, []string{`"kind":"note.posted"`}, "log", "--json", "--limit", "10")
	assertRun(roomDir, 0, []string{"PASSED"}, "verify")
	assertRun(roomDir, 0, []string{`"valid": true`}, "verify", "--json")
	assertRun(roomDir, 0, []string{"Rebuilt derived index"}, "index", "rebuild")

	// ---- artifact ------------------------------------------------------------
	docPath := filepath.Join(roomDir, "doc.md")
	if err := os.WriteFile(docPath, []byte("# doc\n"), 0o644); err != nil {
		t.Fatalf("write doc.md: %v", err)
	}
	assertRun(roomDir, 0, nil, "artifact", "link", "--identity", "alice", docPath)
	assertRun(roomDir, 0, []string{"doc.md"}, "artifact", "list")
	assertRun(roomDir, 0, []string{"doc.md"}, "artifact", "list", "--json")

	// ---- run -----------------------------------------------------------------
	runEvID := assertRun(roomDir, 0, nil, "run", "request", "--identity", "alice", "--title", "Deploy", "--plan-file", "plan.md", "--adapter", "openai")
	runID := "run_" + strings.TrimPrefix(runEvID, "ev_")
	if !strings.HasPrefix(runID, "run_") {
		t.Fatalf("run request output %q, want event ID", runEvID)
	}
	assertRun(roomDir, 0, []string{"Deploy"}, "run", "list")
	assertRun(roomDir, 0, []string{"Deploy"}, "run", "show", runID)
	assertRun(roomDir, 10, []string{"wait timed out for run"}, "run", "wait", "--timeout", "200ms", runID)
	assertRun(roomDir, 0, []string{"ev_"}, "run", "approve", "--identity", "alice", "--scope", "local", "--ttl", "1h", runID)
	assertRun(roomDir, 0, []string{"approved"}, "run", "wait", "--timeout", "5s", runID)

	// run start requires the approved state; a second start is an invalid
	// transition (exit 2).
	assertRun(roomDir, 0, []string{"ev_"}, "run", "start", "--identity", "alice", runID)
	assertRun(roomDir, 2, []string{"invalid run state transition"}, "run", "start", "--identity", "alice", runID)

	// ---- checkpoint ----------------------------------------------------------
	assertRun(roomDir, 10, []string{"wait timed out for checkpoint"},
		"checkpoint", "request", "--identity", "alice", "--run", runID, "--question", "Continue?", "--timeout", "300ms")

	// ---- mcp / misc ----------------------------------------------------------
	assertRun(roomDir, 0, nil, "mcp", "--room", roomDir, "--identity", "alice") // stdin EOF -> clean exit
	assertRun(roomDir, 2, []string{"--identity is required"}, "mcp")
	assertRun(roomDir, 2, []string{"Usage: symroom brain-profile"}, "brain-profile")
	assertRun(roomDir, 0, []string{"Usage: symroom watch"}, "watch")

	// doctor reports failed checks (no default identity configured) with the
	// stable generic exit code 1, in both human and JSON surfaces.
	assertRun(roomDir, 1, []string{"no default identity is configured"}, "doctor")
	assertRun(roomDir, 1, []string{`"failed": true`}, "doctor", "--json")
}

// TestResolveIdentityInProcess covers the happy path of resolveIdentity
// (the identity resolution helper) directly in-process. Its error branches
// terminate via os.Exit and are covered by the re-exec scenarios above.
func TestResolveIdentityInProcess(t *testing.T) {
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("SYMROOM_IDENTITY_KEY", "")
	t.Setenv("PATH", t.TempDir()) // skip symvault/keychain fallback chains in identity.Load

	alice, err := identity.Generate("alice")
	if err != nil {
		t.Fatalf("generate alice: %v", err)
	}
	if err := identity.Save(alice); err != nil {
		t.Fatalf("identity.Save: %v", err)
	}
	got := resolveIdentity("alice")
	if got.MemberID != alice.MemberID || got.Name != "alice" {
		t.Errorf("resolveIdentity(alice) = %s/%s, want %s/alice", got.Name, got.MemberID, alice.MemberID)
	}
}

// TestUsageTextListsSubcommands guards the help text against command drift.
func TestUsageTextListsSubcommands(t *testing.T) {
	for _, sub := range []string{
		"init", "identity", "member", "note", "decide", "artifact", "log",
		"verify", "index", "run", "checkpoint", "watch", "brain-profile",
		"doctor", "version", "mcp",
	} {
		if !strings.Contains(usageText, sub) {
			t.Errorf("usageText missing subcommand %q", sub)
		}
	}
}

// TestRoomDirEnvOverridesCWD verifies that SYMROOM_ROOM_DIR lets commands
// target a room without changing the caller's working directory — the
// mechanism the macOS hub module uses to render a selected room.
func TestRoomDirEnvOverridesCWD(t *testing.T) {
	base, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("EvalSymlinks temp dir: %v", err)
	}
	xdgData := filepath.Join(base, "xdg-data")
	xdgConfig := filepath.Join(base, "xdg-config")
	roomDir := filepath.Join(base, "room")
	// The caller's CWD is deliberately NOT the room: commands must work when
	// run from base with SYMROOM_ROOM_DIR pointing at the room.
	t.Setenv("SYMROOM_ROOM_DIR", roomDir)

	assertRun := func(dir string, wantCode int, wantOut []string, args ...string) string {
		t.Helper()
		out, code := runMain(t, dir, xdgData, xdgConfig, args...)
		if code != wantCode {
			t.Errorf("args %v: exit code = %d, want %d\noutput: %s", args, code, wantCode, out)
		}
		for _, w := range wantOut {
			if !strings.Contains(out, w) {
				t.Errorf("args %v: output missing %q:\n%s", args, w, out)
			}
		}
		return strings.TrimSpace(out)
	}

	// Set up the room with identities.
	assertRun(base, 0, []string{"Created identity alice"}, "identity", "create", "alice")
	assertRun(base, 0, []string{"Initialized room"}, "init", "--identity", "alice", "--name", "Env Room", roomDir)

	// Member add/list must operate on the room even though CWD is base.
	bob, err := identity.Generate("bob")
	if err != nil {
		t.Fatalf("generate bob: %v", err)
	}
	assertRun(base, 0, []string{"Member added"},
		"member", "add", "--identity", "alice", "--pubkey", hex.EncodeToString(bob.PublicKey), "--name", "bob", "--role", "agent", "--kind", "agent")
	membersJSON := assertRun(base, 0, nil, "member", "list", "--json")
	if !strings.Contains(membersJSON, `"name": "bob"`) {
		t.Errorf("member list --json via SYMROOM_ROOM_DIR missing bob: %q", membersJSON)
	}

	// Journal and runs follow the same resolution.
	assertRun(base, 0, nil, "note", "--identity", "alice", "env note")
	assertRun(base, 0, []string{"env note"}, "log")

	// Without the env var, the same commands operate on CWD (base), which is
	// not the room: the member list is empty instead of showing the room's.
	t.Setenv("SYMROOM_ROOM_DIR", "")
	baseJSON := assertRun(base, 0, nil, "member", "list", "--json")
	if baseJSON != "[]" {
		t.Errorf("member list --json without SYMROOM_ROOM_DIR: got %q, want []", baseJSON)
	}
}
