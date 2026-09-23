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
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/room/event"
	"github.com/danieljustus/symaira-desktop/internal/room/identity"
)

const memberCLIContractPath = "testdata/port/room/member-cli.json"

type memberCLIContract struct {
	SchemaVersion  int               `json:"schema_version"`
	OracleRevision string            `json:"oracle_revision"`
	SourceHashes   map[string]string `json:"source_hashes"`
	OwnerKey       string            `json:"owner_key"`
	StrangerKey    string            `json:"stranger_key"`
	IdentityFiles  []memberCLIFile   `json:"identity_files"`
	AddPublicKey   string            `json:"add_public_key"`
	RoomTOML       string            `json:"room_toml"`
	InitialFiles   []memberCLIFile   `json:"initial_files"`
	Cases          []memberCLICase   `json:"cases"`
}

type memberCLIFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type memberCLICase struct {
	Name               string          `json:"name"`
	Args               []string        `json:"args"`
	Actor              string          `json:"actor"`
	EmptyRoom          bool            `json:"empty_room,omitempty"`
	DynamicEvent       bool            `json:"dynamic_event,omitempty"`
	IdentityFile       bool            `json:"identity_file,omitempty"`
	GlobalConfig       string          `json:"global_config,omitempty"`
	ProjectConfig      string          `json:"project_config,omitempty"`
	DefaultIdentityEnv string          `json:"default_identity_env,omitempty"`
	ConfigError        bool            `json:"config_error,omitempty"`
	ExitCode           int             `json:"exit_code"`
	Stdout             string          `json:"stdout"`
	Stderr             string          `json:"stderr"`
	FinalFiles         []memberCLIFile `json:"final_files"`
}

type memberCLIVector struct {
	name               string
	args               []string
	actor              string
	emptyRoom          bool
	dynamicEvent       bool
	identityFile       bool
	globalConfig       string
	projectConfig      string
	defaultIdentityEnv string
	configError        bool
}

// TestPortMemberCLIContract records the shipped Go process result and journal
// effect for each member command. PORT_GENERATE=1 deliberately writes the
// fixture; ordinary checks are read-only.
func TestPortMemberCLIContract(t *testing.T) {
	root := memberCLIRoot(t)
	fixture, err := makeMemberCLIContract(t, root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, memberCLIContractPath)
	if os.Getenv("PORT_GENERATE") == "1" || os.Getenv("ROOM_MEMBER_CLI_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", memberCLIContractPath)
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (set PORT_GENERATE=1 to create it)", memberCLIContractPath, err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Go member CLI fixture is stale; regenerate deliberately with PORT_GENERATE=1")
	}
}

func makeMemberCLIContract(t *testing.T, root string) (memberCLIContract, error) {
	t.Helper()
	ownerSeed := sha256.Sum256([]byte("symroom-member-cli-owner"))
	strangerSeed := sha256.Sum256([]byte("symroom-member-cli-stranger"))
	bobSeed := sha256.Sum256([]byte("symroom-member-cli-bob"))
	addSeed := sha256.Sum256([]byte("symroom-member-cli-add-candidate"))
	ownerPrivate := ed25519.NewKeyFromSeed(ownerSeed[:])
	bobPrivate := ed25519.NewKeyFromSeed(bobSeed[:])
	addPrivate := ed25519.NewKeyFromSeed(addSeed[:])
	owner := &identity.Identity{
		Name: "owner", MemberID: identity.ComputeMemberID(ownerPrivate.Public().(ed25519.PublicKey)),
		PublicKey: ownerPrivate.Public().(ed25519.PublicKey), PrivateKey: ownerPrivate,
	}
	strangerPrivate := ed25519.NewKeyFromSeed(strangerSeed[:])
	stranger := &identity.Identity{
		Name: "stranger", MemberID: identity.ComputeMemberID(strangerPrivate.Public().(ed25519.PublicKey)),
		PublicKey: strangerPrivate.Public().(ed25519.PublicKey), PrivateKey: strangerPrivate,
	}
	ownerIdentityFile, err := memberCLIIdentityFile(owner)
	if err != nil {
		return memberCLIContract{}, err
	}
	strangerIdentityFile, err := memberCLIIdentityFile(stranger)
	if err != nil {
		return memberCLIContract{}, err
	}
	fixture := memberCLIContract{
		SchemaVersion:  1,
		OracleRevision: "b96219bcd39ce85e017626feade557979aefd7c6",
		OwnerKey:       hex.EncodeToString(ownerSeed[:]),
		StrangerKey:    hex.EncodeToString(strangerSeed[:]),
		IdentityFiles:  []memberCLIFile{{Name: "owner.json", Content: ownerIdentityFile}, {Name: "stranger.json", Content: strangerIdentityFile}},
		AddPublicKey:   hex.EncodeToString(addPrivate.Public().(ed25519.PublicKey)),
		RoomTOML:       "schema_version = 1\nid = \"rm_member_fixture\"\ncreated = \"2026-09-23T10:00:00.000Z\"\n",
		SourceHashes: map[string]string{
			"cmd/symroom/main.go":              memberCLIFileHash(t, root, "cmd/symroom/main.go"),
			"cmd/symroom/cmd_member.go":        memberCLIFileHash(t, root, "cmd/symroom/cmd_member.go"),
			"internal/room/room/members.go":    memberCLIFileHash(t, root, "internal/room/room/members.go"),
			"internal/room/members/members.go": memberCLIFileHash(t, root, "internal/room/members/members.go"),
			"internal/room/config/config.go":   memberCLIFileHash(t, root, "internal/room/config/config.go"),
		},
	}
	fixture.InitialFiles = makeMemberJournal(t, owner, bobPrivate.Public().(ed25519.PublicKey))
	goBinary := buildMemberCLIOracle(t, root)
	temp := t.TempDir()
	ownerKey := fixture.OwnerKey
	strangerKey := fixture.StrangerKey
	addKey := fixture.AddPublicKey
	bobID := identity.ComputeMemberID(bobPrivate.Public().(ed25519.PublicKey))
	vectors := []memberCLIVector{
		{name: "member-usage", args: []string{"member"}, actor: "owner"},
		{name: "member-help", args: []string{"member", "--help"}, actor: "owner"},
		{name: "member-unknown-show", args: []string{"member", "show", "bob"}, actor: "owner"},
		{name: "add-flag-form", args: []string{"member", "add", "--identity", "owner", "--pubkey", addKey, "--name", "added", "--role", "agent", "--kind", "agent"}, actor: "owner", dynamicEvent: true},
		{name: "add-positional-form", args: []string{"member", "add", "--identity=owner", "positional", addKey}, actor: "owner", dynamicEvent: true},
		{name: "add-positional-overrides-flags", args: []string{"member", "add", "--identity", "owner", "--name", "flag-name", "--pubkey", addKey, "positional", addKey, "--role", "agent"}, actor: "owner", dynamicEvent: true},
		{name: "add-default-identity-env", args: []string{"member", "add", "--pubkey", addKey, "--name", "default-env"}, actor: "owner", dynamicEvent: true, identityFile: true, defaultIdentityEnv: "owner"},
		{name: "add-default-identity-toml", args: []string{"member", "add", "--pubkey", addKey, "--name", "default-toml"}, actor: "owner", dynamicEvent: true, identityFile: true, globalConfig: "default_identity = \"owner\"\n"},
		{name: "add-default-identity-project-over-global", args: []string{"member", "add", "--pubkey", addKey, "--name", "project-default"}, actor: "owner", dynamicEvent: true, identityFile: true, globalConfig: "default_identity = \"stranger\"\n", projectConfig: "default_identity = \"owner\"\n"},
		{name: "add-default-identity-env-over-toml", args: []string{"member", "add", "--pubkey", addKey, "--name", "env-overrides"}, actor: "owner", dynamicEvent: true, identityFile: true, globalConfig: "default_identity = \"stranger\"\n", defaultIdentityEnv: "owner"},
		{name: "add-config-adapters-rejected", args: []string{"member", "add", "--pubkey", addKey, "--name", "blocked"}, actor: "owner", globalConfig: "default_identity = \"owner\"\n[adapters.deploy]\ncommand = [\"echo\"]\n", defaultIdentityEnv: "owner", configError: true},
		{name: "add-default-identity-missing", args: []string{"member", "add", "--pubkey", addKey, "--name", "no-default"}, actor: "owner"},
		{name: "add-usage", args: []string{"member", "add"}, actor: "owner"},
		{name: "add-invalid-hex", args: []string{"member", "add", "--identity", "owner", "--pubkey", "zz", "--name", "bad"}, actor: "owner"},
		{name: "add-invalid-key-length", args: []string{"member", "add", "--identity", "owner", "--pubkey", "abcd", "--name", "bad"}, actor: "owner"},
		{name: "add-invalid-role", args: []string{"member", "add", "--identity", "owner", "--pubkey", addKey, "--name", "bad", "--role", "admin"}, actor: "owner"},
		{name: "add-invalid-kind", args: []string{"member", "add", "--identity", "owner", "--pubkey", addKey, "--name", "bad", "--kind", "cyborg"}, actor: "owner"},
		{name: "add-unauthorized", args: []string{"member", "add", "--identity", "stranger", "--pubkey", addKey, "--name", "bad"}, actor: "stranger"},
		{name: "add-help", args: []string{"member", "add", "--help"}, actor: "owner"},
		{name: "list-human", args: []string{"member", "list"}, actor: "owner"},
		{name: "list-json", args: []string{"member", "list", "--json=TRUE"}, actor: "owner"},
		{name: "list-empty", args: []string{"member", "list", "--json"}, actor: "owner", emptyRoom: true},
		{name: "list-invalid-bool", args: []string{"member", "list", "--json=maybe"}, actor: "owner"},
		{name: "list-help", args: []string{"member", "list", "--help"}, actor: "owner"},
		{name: "remove-success", args: []string{"member", "remove", "--identity", "owner", bobID}, actor: "owner", dynamicEvent: true},
		{name: "remove-usage", args: []string{"member", "remove"}, actor: "owner"},
		{name: "remove-missing", args: []string{"member", "remove", "--identity", "owner", "mem_missing"}, actor: "owner"},
		{name: "remove-unauthorized", args: []string{"member", "remove", "--identity", "stranger", bobID}, actor: "stranger"},
		{name: "role-success", args: []string{"member", "role", "--identity", "owner", bobID, "observer"}, actor: "owner", dynamicEvent: true},
		{name: "role-usage", args: []string{"member", "role"}, actor: "owner"},
		{name: "role-invalid", args: []string{"member", "role", "--identity", "owner", bobID, "admin"}, actor: "owner"},
		{name: "role-missing-member", args: []string{"member", "role", "--identity", "owner", "mem_missing", "member"}, actor: "owner"},
		{name: "role-unauthorized", args: []string{"member", "role", "--identity", "stranger", bobID, "member"}, actor: "stranger"},
	}
	_ = ownerKey
	_ = strangerKey
	for _, vector := range vectors {
		caseDir := filepath.Join(temp, vector.name)
		roomDir := filepath.Join(caseDir, "room")
		journalDir := filepath.Join(roomDir, "journal")
		if err := os.MkdirAll(journalDir, 0o700); err != nil {
			return memberCLIContract{}, err
		}
		if err := os.WriteFile(filepath.Join(roomDir, "room.toml"), []byte(fixture.RoomTOML), 0o600); err != nil {
			return memberCLIContract{}, err
		}
		if !vector.emptyRoom {
			for _, file := range fixture.InitialFiles {
				if err := os.WriteFile(filepath.Join(journalDir, file.Name), []byte(file.Content), 0o600); err != nil {
					return memberCLIContract{}, err
				}
			}
		}
		home := filepath.Join(caseDir, "home")
		dataHome := filepath.Join(caseDir, "data")
		tmp := filepath.Join(caseDir, "tmp")
		for _, path := range []string{home, dataHome, tmp} {
			if err := os.MkdirAll(path, 0o700); err != nil {
				return memberCLIContract{}, err
			}
		}
		if vector.globalConfig != "" {
			configDir := filepath.Join(home, ".config", "symroom")
			if err := os.MkdirAll(configDir, 0o700); err != nil {
				return memberCLIContract{}, err
			}
			if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(vector.globalConfig), 0o600); err != nil {
				return memberCLIContract{}, err
			}
		}
		if vector.projectConfig != "" {
			if err := os.WriteFile(filepath.Join(roomDir, ".symroom.toml"), []byte(vector.projectConfig), 0o600); err != nil {
				return memberCLIContract{}, err
			}
		}
		if vector.identityFile {
			identityDir := filepath.Join(dataHome, "symroom", "identities")
			if err := os.MkdirAll(identityDir, 0o700); err != nil {
				return memberCLIContract{}, err
			}
			for _, file := range fixture.IdentityFiles {
				if err := os.WriteFile(filepath.Join(identityDir, file.Name), []byte(file.Content), 0o600); err != nil {
					return memberCLIContract{}, err
				}
			}
		}
		key := fixture.OwnerKey
		if vector.actor == "stranger" {
			key = fixture.StrangerKey
		}
		cmd := exec.Command(goBinary, vector.args...)
		cmd.Dir = roomDir
		cmd.Env = []string{
			"HOME=" + home, "USERPROFILE=" + home, "XDG_DATA_HOME=" + dataHome, "TMPDIR=" + tmp,
			"TZ=UTC", "LC_ALL=C", "LANG=C", "SYMROOM_ROOM_DIR=" + roomDir, "PATH=" + tmp,
		}
		if !vector.identityFile {
			cmd.Env = append(cmd.Env, "SYMROOM_IDENTITY_KEY="+key)
		}
		if vector.defaultIdentityEnv != "" {
			cmd.Env = append(cmd.Env, "SYMROOM_DEFAULT_IDENTITY="+vector.defaultIdentityEnv)
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		stdout, runErr := cmd.Output()
		code := 0
		if runErr != nil {
			if exitError, ok := runErr.(*exec.ExitError); ok {
				code = exitError.ExitCode()
			} else {
				return memberCLIContract{}, fmt.Errorf("run Go member case %s: %w", vector.name, runErr)
			}
		}
		finalFiles, err := readMemberCLIFiles(journalDir, vector.dynamicEvent)
		if err != nil {
			return memberCLIContract{}, err
		}
		output := string(stdout)
		if vector.dynamicEvent {
			output = normalizeMemberEventID(output)
		}
		stderrText := stderr.String()
		if vector.configError {
			stderrText = strings.ReplaceAll(stderrText, filepath.Join(home, ".config", "symroom", "config.toml"), "<config-path>")
		}
		fixture.Cases = append(fixture.Cases, memberCLICase{
			Name: vector.name, Args: vector.args, Actor: vector.actor, EmptyRoom: vector.emptyRoom,
			DynamicEvent: vector.dynamicEvent, IdentityFile: vector.identityFile,
			GlobalConfig: vector.globalConfig, ProjectConfig: vector.projectConfig, DefaultIdentityEnv: vector.defaultIdentityEnv, ConfigError: vector.configError,
			ExitCode: code, Stdout: output, Stderr: stderrText, FinalFiles: finalFiles,
		})
	}
	return fixture, nil
}

func memberCLIIdentityFile(id *identity.Identity) (string, error) {
	stored := identity.StoredIdentity{
		Name: id.Name, MemberID: id.MemberID,
		PublicKey: hex.EncodeToString(id.PublicKey), PrivateKey: hex.EncodeToString(id.PrivateKey),
	}
	data, err := json.MarshalIndent(stored, "", "  ")
	return string(data), err
}

func makeMemberJournal(t *testing.T, owner *identity.Identity, memberPublic ed25519.PublicKey) []memberCLIFile {
	t.Helper()
	ownerEvent := &event.Event{
		V: event.CurrentVersion, ID: "member-fixture-room-created", Room: "rm_member_fixture",
		Author: owner.MemberID, Seq: 1,
		Prev:    "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		Lamport: 1, TS: "2026-09-23T10:00:00.000Z", Kind: event.KindRoomCreated,
		Body: json.RawMessage(fmt.Sprintf(`{"name":"Member Fixture","public_key":"%s"}`, hex.EncodeToString(owner.PublicKey))),
	}
	if err := ownerEvent.Sign(owner); err != nil {
		t.Fatal(err)
	}
	ownerLine, err := ownerEvent.MarshalJSONLine()
	if err != nil {
		t.Fatal(err)
	}
	memberBody, err := json.Marshal(map[string]string{
		"id": identity.ComputeMemberID(memberPublic), "name": "bob",
		"public_key": hex.EncodeToString(memberPublic), "role": "member", "kind": "human",
	})
	if err != nil {
		t.Fatal(err)
	}
	previous := sha256.Sum256(bytes.TrimSuffix(ownerLine, []byte{'\n'}))
	memberEvent := &event.Event{
		V: event.CurrentVersion, ID: "member-fixture-bob-added", Room: "rm_member_fixture",
		Author: owner.MemberID, Seq: 2, Prev: "sha256:" + hex.EncodeToString(previous[:]),
		Lamport: 2, TS: "2026-09-23T10:01:00.000Z", Kind: event.KindMemberAdded,
		Body: json.RawMessage(memberBody),
	}
	if err := memberEvent.Sign(owner); err != nil {
		t.Fatal(err)
	}
	memberLine, err := memberEvent.MarshalJSONLine()
	if err != nil {
		t.Fatal(err)
	}
	return []memberCLIFile{{Name: owner.MemberID + ".jsonl", Content: string(append(ownerLine, memberLine...))}}
}

func readMemberCLIFiles(dir string, normalizeLast bool) ([]memberCLIFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := make([]memberCLIFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".jsonl") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		if normalizeLast {
			lines := bytes.Split(data, []byte{'\n'})
			lastIndex := len(lines) - 2
			if lastIndex < 0 {
				return nil, fmt.Errorf("journal %s has no appended event", entry.Name())
			}
			var value map[string]any
			if err := json.Unmarshal(lines[lastIndex], &value); err != nil {
				return nil, err
			}
			value["id"], value["ts"], value["sig"] = "<event-id>", "<dynamic-clock>", "<signature>"
			lines[lastIndex], err = json.Marshal(value)
			if err != nil {
				return nil, err
			}
			data = bytes.Join(lines, []byte{'\n'})
		}
		files = append(files, memberCLIFile{Name: entry.Name(), Content: string(data)})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}

func normalizeMemberEventID(output string) string {
	index := strings.Index(output, "ev_")
	if index >= 0 && len(output) >= index+23 {
		return output[:index] + "<event-id>" + output[index+23:]
	}
	return output
}

func buildMemberCLIOracle(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "symroom-go-member-oracle")
	cmd := exec.Command("go", "build", "-o", path, "./cmd/symroom")
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build Go symroom member oracle: %v\n%s", err, output)
	}
	return path
}

func memberCLIRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func memberCLIFileHash(t *testing.T, root, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
