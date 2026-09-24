package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/room/event"
	"github.com/danieljustus/symaira-desktop/internal/room/identity"
	"github.com/danieljustus/symaira-desktop/internal/room/journal"
	"github.com/danieljustus/symaira-desktop/internal/room/members"
)

const runApprovalCLIContractPath = "testdata/port/room/run-approval-cli.json"

type runApprovalCLIFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type runApprovalCLICase struct {
	Name               string               `json:"name"`
	Args               []string             `json:"args"`
	Actor              string               `json:"actor"`
	GlobalConfig       string               `json:"global_config,omitempty"`
	DefaultIdentityEnv string               `json:"default_identity_env,omitempty"`
	InitialJournal     []runApprovalCLIFile `json:"initial_journal"`
	DynamicApproval    bool                 `json:"dynamic_approval,omitempty"`
	DynamicEvent       bool                 `json:"dynamic_event,omitempty"`
	ExitCode           int                  `json:"exit_code"`
	Stdout             string               `json:"stdout"`
	Stderr             string               `json:"stderr"`
	FinalJournal       []runApprovalCLIFile `json:"final_journal"`
}

type runApprovalCLIContract struct {
	SchemaVersion  int                  `json:"schema_version"`
	OracleRevision string               `json:"oracle_revision"`
	Normalization  string               `json:"normalization"`
	SourceHashes   map[string]string    `json:"source_hashes"`
	IdentityKeys   map[string]string    `json:"identity_keys"`
	InitialJournal []runApprovalCLIFile `json:"initial_journal"`
	Cases          []runApprovalCLICase `json:"cases"`
}

type runApprovalCLIVector struct {
	name       string
	args       []string
	actor      string
	config     string
	defaultEnv string
	dynamic    bool
	approval   bool
}

// TestPortRunApprovalCLIContract freezes Go approval and denial process output
// plus signed journal effects. PORT_GENERATE=1 is the only fixture write path.
func TestPortRunApprovalCLIContract(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := makeRunApprovalCLIContract(t, root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, runApprovalCLIContractPath)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // generated contract fixture is intentionally world-readable
			t.Fatal(err)
		}
		t.Logf("wrote %s", runApprovalCLIContractPath)
		return
	}
	got, err := os.ReadFile(path) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
	if err != nil {
		t.Fatalf("read %s: %v (set PORT_GENERATE=1 to create it)", runApprovalCLIContractPath, err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Go run approval CLI fixture is stale; regenerate deliberately with PORT_GENERATE=1")
	}
}

func makeRunApprovalCLIContract(t *testing.T, root string) (runApprovalCLIContract, error) {
	t.Helper()
	keys := map[string]*identity.Identity{
		"owner":    runApprovalCLIIdentity("approval-owner"),
		"reviewer": runApprovalCLIIdentity("approval-reviewer"),
		"member":   runApprovalCLIIdentity("approval-member"),
		"agent":    runApprovalCLIIdentity("approval-agent"),
		"observer": runApprovalCLIIdentity("approval-observer"),
		"outsider": runApprovalCLIIdentity("approval-outsider"),
	}
	fixture := runApprovalCLIContract{
		SchemaVersion:  1,
		OracleRevision: "558cac10528b2a03e344640190327a662d5a60e8",
		Normalization:  "dynamic appended event ts and sig; approval ID, event ID, and expires_at for run.approved; no other event fields normalized",
		IdentityKeys:   make(map[string]string, len(keys)),
		SourceHashes:   make(map[string]string),
	}
	for name, signer := range keys {
		fixture.IdentityKeys[name] = hex.EncodeToString(signer.PrivateKey[:ed25519.SeedSize])
	}
	for _, source := range []string{
		"cmd/symroom/main.go", "cmd/symroom/cmd_run.go", "internal/room/approval/approval.go",
		"internal/room/config/config.go", "internal/room/event/event.go", "internal/room/identity/identity.go",
		"internal/room/journal/journal.go", "internal/room/members/members.go", "internal/room/run/run.go",
	} {
		data, err := os.ReadFile(filepath.Join(root, source)) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
		if err != nil {
			return fixture, err
		}
		sum := sha256.Sum256(data)
		fixture.SourceHashes[source] = hex.EncodeToString(sum[:])
	}
	initial, err := runApprovalCLIInitialJournal(keys)
	if err != nil {
		return fixture, err
	}
	fixture.InitialJournal = initial

	vectors := []runApprovalCLIVector{
		{name: "approve-usage", args: []string{"run", "approve"}, actor: "owner"},
		{name: "approve-invalid-ttl", args: []string{"run", "approve", "--identity", "owner", "--ttl=bogus", "approval-pending"}, actor: "owner"},
		{name: "approve-explicit-scope-and-ttl", args: []string{"run", "approve", "--identity", "reviewer", "--scope=deploy:stage", "--ttl=45m", "approval-pending"}, actor: "reviewer", dynamic: true, approval: true},
		{name: "approve-default-scope-and-ttl", args: []string{"run", "approve", "--identity", "owner", "approval-pending"}, actor: "owner", dynamic: true, approval: true},
		{name: "approve-config-identity-and-ttl", args: []string{"run", "approve", "--ttl=0s", "approval-pending"}, actor: "reviewer", config: "default_identity = \"reviewer\"\n\n[approval]\ndefault_ttl = \"45m\"\n", dynamic: true, approval: true},
		{name: "approve-env-identity", args: []string{"run", "approve", "approval-pending"}, actor: "reviewer", defaultEnv: "reviewer", dynamic: true, approval: true},
		{name: "approve-agent-forbidden", args: []string{"run", "approve", "--identity", "agent", "approval-pending"}, actor: "agent"},
		{name: "approve-observer-forbidden", args: []string{"run", "approve", "--identity", "observer", "approval-pending"}, actor: "observer"},
		{name: "approve-not-member", args: []string{"run", "approve", "--identity", "outsider", "approval-pending"}, actor: "outsider"},
		{name: "approve-run-not-found", args: []string{"run", "approve", "--identity", "owner", "approval-missing"}, actor: "owner"},
		{name: "approve-already-approved", args: []string{"run", "approve", "--identity", "owner", "approval-approved"}, actor: "owner"},
		{name: "approve-already-denied", args: []string{"run", "approve", "--identity", "owner", "approval-denied"}, actor: "owner"},
		{name: "deny-usage", args: []string{"run", "deny"}, actor: "owner"},
		{name: "deny-reason-required", args: []string{"run", "deny", "approval-pending"}, actor: "owner"},
		{name: "deny-explicit-reason", args: []string{"run", "deny", "--identity", "reviewer", "--reason=operator <declined>& hold", "approval-pending"}, actor: "reviewer", dynamic: true},
		{name: "deny-config-identity", args: []string{"run", "deny", "--reason", "policy", "approval-pending"}, actor: "reviewer", config: "default_identity = \"reviewer\"\n", dynamic: true},
		{name: "deny-agent-currently-allowed", args: []string{"run", "deny", "--identity", "agent", "--reason", "legacy behavior", "approval-pending"}, actor: "agent", dynamic: true},
		{name: "deny-run-not-found", args: []string{"run", "deny", "--identity", "owner", "--reason", "no", "approval-missing"}, actor: "owner"},
		{name: "deny-already-approved", args: []string{"run", "deny", "--identity", "owner", "--reason", "late", "approval-approved"}, actor: "owner"},
		{name: "deny-already-denied", args: []string{"run", "deny", "--identity", "owner", "--reason", "again", "approval-denied"}, actor: "owner"},
	}

	executable := oracleExecutablePath(t, "symroom-go-approval-oracle")
	build := exec.Command("go", "build", "-o", executable, "./cmd/symroom") //nolint:gosec // test-only command uses a fixed helper and controlled arguments
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		return fixture, fmt.Errorf("build Go symroom approval oracle: %w\n%s", err, output)
	}
	for _, vector := range vectors {
		caseDir := filepath.Join(t.TempDir(), vector.name)
		roomDir := filepath.Join(caseDir, "room")
		journalDir := filepath.Join(roomDir, "journal")
		if err := os.MkdirAll(journalDir, 0o700); err != nil {
			return fixture, err
		}
		for _, file := range initial {
			if err := os.WriteFile(filepath.Join(journalDir, file.Name), []byte(file.Content), 0o600); err != nil {
				return fixture, err
			}
		}
		home := filepath.Join(caseDir, "home")
		dataHome := filepath.Join(caseDir, "data")
		tempDir := filepath.Join(caseDir, "tmp")
		for _, dir := range []string{home, dataHome, tempDir} {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return fixture, err
			}
		}
		if vector.config != "" {
			configPath := filepath.Join(home, ".config", "symroom", "config.toml")
			if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
				return fixture, err
			}
			if err := os.WriteFile(configPath, []byte(vector.config), 0o600); err != nil {
				return fixture, err
			}
		}
		cmd := exec.Command(executable, vector.args...) //nolint:gosec // test-only command uses a fixed helper and controlled arguments
		cmd.Env = []string{
			"HOME=" + home, "USERPROFILE=" + home, "XDG_DATA_HOME=" + dataHome, "TMPDIR=" + tempDir,
			"TZ=UTC", "LC_ALL=C", "LANG=C", "SYMROOM_ROOM_DIR=" + roomDir,
			"SYMROOM_IDENTITY_KEY=" + fixture.IdentityKeys[vector.actor],
			"SYMROOM_DEFAULT_IDENTITY=" + vector.defaultEnv,
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		stdout, err := cmd.Output()
		code := 0
		if err != nil {
			if exitError, ok := err.(*exec.ExitError); ok {
				code = exitError.ExitCode()
			} else {
				return fixture, fmt.Errorf("run Go approval case %s: %w", vector.name, err)
			}
		}
		finalJournal, err := readRunApprovalCLIJournal(journalDir, vector.actor, vector.dynamic, vector.approval)
		if err != nil {
			return fixture, err
		}
		if vector.dynamic && code == 0 {
			stdout = []byte("<dynamic-event-id>\n")
		}
		fixture.Cases = append(fixture.Cases, runApprovalCLICase{
			Name: vector.name, Args: vector.args, Actor: vector.actor, GlobalConfig: vector.config,
			DefaultIdentityEnv: vector.defaultEnv, InitialJournal: initial,
			DynamicEvent: vector.dynamic, DynamicApproval: vector.approval,
			ExitCode: code, Stdout: string(stdout), Stderr: stderr.String(), FinalJournal: finalJournal,
		})
	}
	return fixture, nil
}

func runApprovalCLIIdentity(label string) *identity.Identity {
	seed := sha256.Sum256([]byte("symroom-run-approval-cli:" + label))
	private := ed25519.NewKeyFromSeed(seed[:])
	public := private.Public().(ed25519.PublicKey)
	return &identity.Identity{Name: label, MemberID: identity.ComputeMemberID(public), PublicKey: public, PrivateKey: private}
}

func runApprovalCLIInitialJournal(keys map[string]*identity.Identity) ([]runApprovalCLIFile, error) {
	owner := keys["owner"]
	var events []*event.Event
	roomBody, _ := json.Marshal(struct {
		Name      string `json:"name"`
		PublicKey string `json:"public_key"`
	}{"Approval fixture", hex.EncodeToString(owner.PublicKey)})
	events = append(events, &event.Event{V: event.CurrentVersion, ID: "approval-room-created", Room: "rm_test", Author: owner.MemberID, Kind: event.KindRoomCreated, Body: roomBody, TS: "2026-09-01T00:00:00.000Z"})
	for _, entry := range []struct {
		name string
		role members.Role
		kind members.MemberKind
	}{
		{name: "reviewer", role: members.RoleMember, kind: members.KindHuman},
		{name: "member", role: members.RoleMember, kind: members.KindHuman},
		{name: "agent", role: members.RoleAgent, kind: members.KindAgent},
		{name: "observer", role: members.RoleObserver, kind: members.KindHuman},
	} {
		identity := keys[entry.name]
		body, _ := json.Marshal(struct {
			ID        string             `json:"id"`
			Name      string             `json:"name"`
			PublicKey string             `json:"public_key"`
			Role      members.Role       `json:"role"`
			Kind      members.MemberKind `json:"kind"`
		}{identity.MemberID, entry.name, hex.EncodeToString(identity.PublicKey), entry.role, entry.kind})
		events = append(events, &event.Event{V: event.CurrentVersion, ID: "approval-add-" + entry.name, Room: "rm_test", Author: owner.MemberID, Kind: event.KindMemberAdded, Body: body, TS: fmt.Sprintf("2026-09-01T00:00:%02d.000Z", len(events))})
	}
	for _, runID := range []string{"approval-pending", "approval-approved", "approval-denied"} {
		body, _ := json.Marshal(map[string]string{"adapter": "", "plan_file": "", "run_id": runID, "title": runID})
		events = append(events, &event.Event{V: event.CurrentVersion, ID: "approval-request-" + runID, Room: "rm_test", Author: owner.MemberID, Kind: event.KindRunRequested, Body: body, TS: fmt.Sprintf("2026-09-01T00:01:%02d.000Z", len(events))})
	}
	approvedBody, _ := json.Marshal(struct {
		RunID      string `json:"run_id"`
		ApprovalID string `json:"approval_id"`
		Scope      string `json:"scope"`
		ExpiresAt  string `json:"expires_at"`
	}{"approval-approved", "app_seeded", "all", "2099-01-01T00:00:00Z"})
	events = append(events, &event.Event{V: event.CurrentVersion, ID: "approval-approved-event", Room: "rm_test", Author: owner.MemberID, Kind: event.KindRunApproved, Body: approvedBody, TS: "2026-09-01T00:02:00.000Z"})
	deniedBody, _ := json.Marshal(struct {
		RunID  string `json:"run_id"`
		Reason string `json:"reason"`
	}{"approval-denied", "seeded"})
	events = append(events, &event.Event{V: event.CurrentVersion, ID: "approval-denied-event", Room: "rm_test", Author: owner.MemberID, Kind: event.KindRunDenied, Body: deniedBody, TS: "2026-09-01T00:03:00.000Z"})
	var content []byte
	prev := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	for index, ev := range events {
		ev.Seq = uint64(index + 1)
		ev.Prev = prev
		ev.Lamport = uint64(index + 1)
		if err := ev.Sign(owner); err != nil {
			return nil, err
		}
		line, err := ev.MarshalJSONLine()
		if err != nil {
			return nil, err
		}
		content = append(content, line...)
		prev = journal.ComputeLineHash(bytes.TrimSuffix(line, []byte{'\n'}))
	}
	return []runApprovalCLIFile{{Name: owner.MemberID + ".jsonl", Content: string(content)}}, nil
}

func readRunApprovalCLIJournal(dir, actor string, dynamic, approval bool) ([]runApprovalCLIFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := make([]runApprovalCLIFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		content, err := os.ReadFile(filepath.Join(dir, entry.Name())) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
		if err != nil {
			return nil, err
		}
		if dynamic && entry.Name() == actorIdentityMember(actor)+".jsonl" {
			content, err = normalizeRunApprovalCLIEvent(content, approval)
			if err != nil {
				return nil, err
			}
		}
		files = append(files, runApprovalCLIFile{Name: entry.Name(), Content: string(content)})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}

func actorIdentityMember(actor string) string {
	return runApprovalCLIIdentity("approval-" + actor).MemberID
}

func normalizeRunApprovalCLIEvent(content []byte, approval bool) ([]byte, error) {
	lines := bytes.SplitAfter(content, []byte{'\n'})
	last := -1
	for index := len(lines) - 1; index >= 0; index-- {
		if len(bytes.TrimSpace(lines[index])) != 0 {
			last = index
			break
		}
	}
	if last < 0 {
		return content, nil
	}
	{
		index := last
		line := bytes.TrimSuffix(lines[index], []byte{'\n'})
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(line, &fields); err != nil {
			return nil, err
		}
		var kind string
		if err := json.Unmarshal(fields["kind"], &kind); err != nil {
			return nil, err
		}
		if kind != event.KindRunApproved && kind != event.KindRunDenied {
			return content, nil
		}
		fields["ts"] = json.RawMessage(`"<dynamic-clock>"`)
		fields["sig"] = json.RawMessage(`"<signature-of-dynamic-clock>"`)
		if kind == event.KindRunApproved && approval {
			fields["id"] = json.RawMessage(`"<dynamic-event-id>"`)
			var body map[string]json.RawMessage
			if err := json.Unmarshal(fields["body"], &body); err != nil {
				return nil, err
			}
			body["approval_id"] = json.RawMessage(`"<dynamic-approval-id>"`)
			body["expires_at"] = json.RawMessage(`"<dynamic-expiry>"`)
			fields["body"], _ = json.Marshal(body)
		}
		canonical, err := json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		lines[index] = append(canonical, '\n')
	}
	return bytes.Join(lines, nil), nil
}
