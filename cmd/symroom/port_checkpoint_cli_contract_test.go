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
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/room/event"
	"github.com/danieljustus/symaira-desktop/internal/room/identity"
	"github.com/danieljustus/symaira-desktop/internal/room/journal"
)

const checkpointCLIContractPath = "testdata/port/room/checkpoint-cli.json"

type checkpointCLIFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type checkpointCLICase struct {
	Name               string              `json:"name"`
	Args               []string            `json:"args"`
	Actor              string              `json:"actor,omitempty"`
	GlobalConfig       string              `json:"global_config,omitempty"`
	ProjectConfig      string              `json:"project_config,omitempty"`
	DefaultIdentityEnv string              `json:"default_identity_env,omitempty"`
	InitialJournal     []checkpointCLIFile `json:"initial_journal,omitempty"`
	ResolveArgs        []string            `json:"resolve_args,omitempty"`
	ResolverExitCode   int                 `json:"resolver_exit_code,omitempty"`
	ResolverStdout     string              `json:"resolver_stdout,omitempty"`
	ResolverStderr     string              `json:"resolver_stderr,omitempty"`
	DynamicEvent       bool                `json:"dynamic_event,omitempty"`
	DynamicCheckpoint  bool                `json:"dynamic_checkpoint,omitempty"`
	ExitCode           int                 `json:"exit_code"`
	Stdout             string              `json:"stdout"`
	Stderr             string              `json:"stderr"`
	FinalFiles         []checkpointCLIFile `json:"final_files"`
}

type checkpointCLIContract struct {
	SchemaVersion  int                 `json:"schema_version"`
	OracleRevision string              `json:"oracle_revision"`
	Normalization  string              `json:"normalization"`
	SourceHashes   map[string]string   `json:"source_hashes"`
	IdentityKey    string              `json:"identity_key"`
	AgentKey       string              `json:"agent_key"`
	Cases          []checkpointCLICase `json:"cases"`
}

type checkpointCLIVector struct {
	name          string
	args          []string
	actor         string
	globalConfig  string
	projectConfig string
	defaultEnv    string
	initial       string
	requestPair   bool
}

// TestPortCheckpointCLIContract freezes Go process output and journal writes.
// The fixture is writable only through PORT_GENERATE=1.
func TestPortCheckpointCLIContract(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := makeCheckpointCLIContract(t, root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, checkpointCLIContractPath)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // generated contract fixture is intentionally world-readable
			t.Fatal(err)
		}
		t.Logf("wrote %s", checkpointCLIContractPath)
		return
	}
	got, err := os.ReadFile(path) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
	if err != nil {
		t.Fatalf("read %s: %v (set PORT_GENERATE=1 to create it)", checkpointCLIContractPath, err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("Go checkpoint CLI fixture is stale; regenerate deliberately with PORT_GENERATE=1")
	}
}

func makeCheckpointCLIContract(t *testing.T, root string) (checkpointCLIContract, error) {
	t.Helper()
	ownerSeed := sha256.Sum256([]byte("symroom-checkpoint-cli-owner"))
	agentSeed := sha256.Sum256([]byte("symroom-checkpoint-cli-agent"))
	owner := checkpointCLIIdentity("owner", ownerSeed[:])
	agent := checkpointCLIIdentity("agent", agentSeed[:])
	fixture := checkpointCLIContract{
		SchemaVersion:  1,
		OracleRevision: "def48b15ff6a9392ce4de3a824a0eec530557e8a",
		Normalization:  "normalize dynamic timestamps/signatures, generated checkpoint/event ids, and prev hashes that depend on a generated request event",
		IdentityKey:    hex.EncodeToString(ownerSeed[:]),
		AgentKey:       hex.EncodeToString(agentSeed[:]),
		SourceHashes:   map[string]string{},
	}
	for _, source := range []string{
		"cmd/symroom/main.go", "cmd/symroom/cmd_checkpoint.go", "internal/room/run/checkpoint.go",
		"internal/room/config/config.go", "internal/room/identity/identity.go", "internal/room/journal/journal.go",
		"internal/room/members/members.go",
	} {
		data, err := os.ReadFile(filepath.Join(root, source)) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
		if err != nil {
			return fixture, err
		}
		sum := sha256.Sum256(data)
		fixture.SourceHashes[source] = hex.EncodeToString(sum[:])
	}
	goBinary := oracleExecutablePath(t, "symroom-go-checkpoint-oracle")
	build := exec.Command("go", "build", "-o", goBinary, "./cmd/symroom") //nolint:gosec // fixed Go build command for the test oracle
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		return fixture, fmt.Errorf("build Go symroom checkpoint oracle: %w\n%s", err, output)
	}

	vectors := []checkpointCLIVector{
		{name: "usage", args: []string{"checkpoint"}, actor: "owner"},
		{name: "unknown-action", args: []string{"checkpoint", "bogus"}, actor: "owner"},
		{name: "request-usage", args: []string{"checkpoint", "request"}, actor: "owner"},
		{name: "request-help", args: []string{"checkpoint", "request", "--help"}, actor: "owner"},
		{name: "request-unknown-flag", args: []string{"checkpoint", "request", "--bogus"}, actor: "owner"},
		{name: "request-default-missing", args: []string{"checkpoint", "request", "--run", "run_fixture", "--question", "Proceed?", "--timeout=1ms"}, actor: "owner"},
		{name: "request-timeout-explicit", args: []string{"checkpoint", "request", "--identity", "owner", "--run", "run_fixture", "--question", "Proceed?", "--timeout=1ms"}, actor: "owner"},
		{name: "request-timeout-fractional", args: []string{"checkpoint", "request", "--identity", "owner", "--run", "run_fixture", "--question", "Proceed?", "--timeout=.5ms"}, actor: "owner"},
		{name: "request-timeout-invalid", args: []string{"checkpoint", "request", "--timeout=bad"}, actor: "owner"},
		{name: "request-timeout-project-over-global", args: []string{"checkpoint", "request", "--run", "run_fixture", "--question", "Proceed?", "--timeout=1ms"}, actor: "owner", globalConfig: "default_identity = \"missing\"\n", projectConfig: "default_identity = \"owner\"\n"},
		{name: "request-timeout-env-over-global", args: []string{"checkpoint", "request", "--run", "run_fixture", "--question", "Proceed?", "--timeout=1ms"}, actor: "owner", globalConfig: "default_identity = \"missing\"\n", defaultEnv: "owner"},
		{name: "request-success-global-and-resolve", args: []string{"checkpoint", "request", "--run", "run_fixture", "--question", "Proceed?", "--timeout=3s"}, actor: "owner", globalConfig: "default_identity = \"owner\"\n", requestPair: true},
		{name: "resolve-usage", args: []string{"checkpoint", "resolve"}, actor: "owner"},
		{name: "resolve-no-answer", args: []string{"checkpoint", "resolve", "chk_existing"}, actor: "owner"},
		{name: "resolve-success-explicit", args: []string{"checkpoint", "resolve", "--identity", "owner", "--answer", "Approved", "chk_existing"}, actor: "owner", initial: "requested"},
		{name: "resolve-success-default-project", args: []string{"checkpoint", "resolve", "--answer", "Approved", "chk_existing"}, actor: "owner", initial: "requested", globalConfig: "default_identity = \"missing\"\n", projectConfig: "default_identity = \"owner\"\n"},
		{name: "resolve-not-found", args: []string{"checkpoint", "resolve", "--identity", "owner", "--answer", "No", "chk_missing"}, actor: "owner"},
		{name: "resolve-already-resolved", args: []string{"checkpoint", "resolve", "--identity", "owner", "--answer", "Again", "chk_existing"}, actor: "owner", initial: "resolved"},
		{name: "resolve-agent-forbidden", args: []string{"checkpoint", "resolve", "--identity", "agent", "--answer", "No", "chk_existing"}, actor: "agent", initial: "agent"},
	}
	for _, vector := range vectors {
		caseDir := filepath.Join(t.TempDir(), vector.name)
		roomDir := filepath.Join(caseDir, "room")
		home, dataHome, tmp := filepath.Join(caseDir, "home"), filepath.Join(caseDir, "data"), filepath.Join(caseDir, "tmp")
		for _, path := range []string{roomDir, home, dataHome, tmp} {
			if err := os.MkdirAll(path, 0o700); err != nil {
				return fixture, err
			}
		}
		var initial []checkpointCLIFile
		if vector.initial != "" {
			var err error
			initial, err = checkpointCLIInitialJournal(t, roomDir, owner, agent, vector.initial)
			if err != nil {
				return fixture, err
			}
		}
		if vector.globalConfig != "" {
			configDir := filepath.Join(home, ".config", "symroom")
			if err := os.MkdirAll(configDir, 0o700); err != nil {
				return fixture, err
			}
			if err := os.WriteFile(filepath.Join(configDir, "config.toml"), []byte(vector.globalConfig), 0o600); err != nil {
				return fixture, err
			}
		}
		if vector.projectConfig != "" {
			if err := os.WriteFile(filepath.Join(roomDir, ".symroom.toml"), []byte(vector.projectConfig), 0o600); err != nil {
				return fixture, err
			}
		}
		actorKey := fixture.IdentityKey
		if vector.actor == "agent" {
			actorKey = fixture.AgentKey
		}
		baseEnv := []string{
			"HOME=" + home, "USERPROFILE=" + home, "XDG_DATA_HOME=" + dataHome, "TMPDIR=" + tmp,
			"TZ=UTC", "LC_ALL=C", "LANG=C", "PATH=" + tmp,
			"SYMROOM_ROOM_DIR=" + roomDir, "SYMROOM_IDENTITY_KEY=" + actorKey,
		}
		if vector.defaultEnv != "" {
			baseEnv = append(baseEnv, "SYMROOM_DEFAULT_IDENTITY="+vector.defaultEnv)
		}
		args := append([]string(nil), vector.args...)
		result := checkpointCLICase{Name: vector.name, Args: args, Actor: vector.actor, GlobalConfig: vector.globalConfig, ProjectConfig: vector.projectConfig, DefaultIdentityEnv: vector.defaultEnv, InitialJournal: initial}
		var stdout, stderr bytes.Buffer
		if vector.requestPair {
			requestCmd := exec.Command(goBinary, args...) //nolint:gosec // goBinary is the test-built oracle and args are fixed vectors
			requestCmd.Dir, requestCmd.Env, requestCmd.Stdout, requestCmd.Stderr = roomDir, baseEnv, &stdout, &stderr
			if err := requestCmd.Start(); err != nil {
				return fixture, err
			}
			checkpointID, err := waitForCheckpointRequest(roomDir, 2*time.Second)
			if err != nil {
				_ = requestCmd.Process.Kill()
				_ = requestCmd.Wait()
				return fixture, err
			}
			resolveArgs := []string{"checkpoint", "resolve", "--answer", "Approved", checkpointID}
			resolver := exec.Command(goBinary, resolveArgs...) //nolint:gosec // test-only command uses a fixed helper and controlled arguments
			resolver.Dir, resolver.Env = roomDir, baseEnv
			var resolverOut, resolverErr bytes.Buffer
			resolver.Stdout, resolver.Stderr = &resolverOut, &resolverErr
			resolveErr := resolver.Run()
			result.ResolverExitCode = 0
			if resolveErr != nil {
				if exitError, ok := resolveErr.(*exec.ExitError); ok {
					result.ResolverExitCode = exitError.ExitCode()
				} else {
					return fixture, fmt.Errorf("run resolver process: %w", resolveErr)
				}
			}
			result.ResolveArgs = []string{"checkpoint", "resolve", "--answer", "Approved", "<checkpoint-id>"}
			result.ResolverStdout = normalizeCheckpointEventID(resolverOut.String())
			result.ResolverStderr = resolverErr.String()
			waitErr := requestCmd.Wait()
			result.ExitCode = processExitCode(waitErr)
			result.Stdout = stdout.String()
			result.Stderr = normalizeCheckpointID(stderr.String(), checkpointID)
			result.DynamicEvent = true
		} else {
			cmd := exec.Command(goBinary, args...) //nolint:gosec // test-only command uses a fixed helper and controlled arguments
			cmd.Dir, cmd.Env = roomDir, baseEnv
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			runErr := cmd.Run()
			result.ExitCode = processExitCode(runErr)
			result.Stdout, result.Stderr = stdout.String(), stderr.String()
			if strings.HasPrefix(vector.name, "request-timeout") {
				result.DynamicCheckpoint = true
				if id, err := latestRequestedCheckpoint(roomDir); err == nil {
					result.Stderr = normalizeCheckpointID(result.Stderr, id)
				}
			}
			if strings.HasPrefix(vector.name, "resolve-success") {
				result.DynamicEvent = true
				result.Stdout = normalizeCheckpointEventID(result.Stdout)
			}
		}
		finalFiles, err := checkpointCLIReadFiles(roomDir)
		if err != nil {
			return fixture, err
		}
		result.FinalFiles = finalFiles
		fixture.Cases = append(fixture.Cases, result)
	}
	return fixture, nil
}

func checkpointCLIIdentity(name string, seed []byte) *identity.Identity {
	private := ed25519.NewKeyFromSeed(seed)
	return &identity.Identity{Name: name, MemberID: identity.ComputeMemberID(private.Public().(ed25519.PublicKey)), PublicKey: private.Public().(ed25519.PublicKey), PrivateKey: private}
}

func checkpointCLIInitialJournal(t *testing.T, roomDir string, owner, agent *identity.Identity, mode string) ([]checkpointCLIFile, error) {
	t.Helper()
	dir := filepath.Join(roomDir, "journal")
	j := journal.New(dir)
	appendFixtureEvent := func(id, author, kind string, body any, signer *identity.Identity) error {
		encoded, err := json.Marshal(body)
		if err != nil {
			return err
		}
		ev := &event.Event{V: event.CurrentVersion, ID: id, Room: "rm_test", Author: author, TS: "2026-09-23T12:00:00.000Z", Kind: kind, Body: encoded}
		if err := j.PrepareEvent(ev); err != nil {
			return err
		}
		if err := ev.Sign(signer); err != nil {
			return err
		}
		return j.Append(ev)
	}
	if mode == "agent" {
		if err := appendFixtureEvent("event-room-created", owner.MemberID, event.KindRoomCreated, map[string]string{"name": "Fixture", "public_key": hex.EncodeToString(owner.PublicKey)}, owner); err != nil {
			return nil, err
		}
		if err := appendFixtureEvent("event-agent-added", owner.MemberID, event.KindMemberAdded, map[string]string{
			"id": agent.MemberID, "name": "Agent", "public_key": hex.EncodeToString(agent.PublicKey), "role": "agent", "kind": "agent",
		}, owner); err != nil {
			return nil, err
		}
	}
	if err := appendFixtureEvent("event-checkpoint-requested", owner.MemberID, event.KindCheckpointReq, map[string]string{
		"checkpoint_id": "chk_existing", "run_id": "run_fixture", "question": "Proceed?",
	}, owner); err != nil {
		return nil, err
	}
	if mode == "resolved" {
		if err := appendFixtureEvent("event-checkpoint-resolved", owner.MemberID, event.KindCheckpointResolved, map[string]string{
			"checkpoint_id": "chk_existing", "answer": "Already done",
		}, owner); err != nil {
			return nil, err
		}
	}
	return checkpointCLIReadFilesRaw(dir)
}

func waitForCheckpointRequest(roomDir string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if id, err := latestRequestedCheckpoint(roomDir); err == nil {
			return id, nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return "", fmt.Errorf("checkpoint request process did not append an event before timeout")
}

func latestRequestedCheckpoint(roomDir string) (string, error) {
	files, err := filepath.Glob(filepath.Join(roomDir, "journal", "*.jsonl"))
	if err != nil {
		return "", err
	}
	for _, path := range files {
		data, err := os.ReadFile(path) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			var parsed struct {
				Kind string `json:"kind"`
				Body struct {
					CheckpointID string `json:"checkpoint_id"`
				} `json:"body"`
			}
			if json.Unmarshal([]byte(line), &parsed) == nil && parsed.Kind == event.KindCheckpointReq && parsed.Body.CheckpointID != "" {
				return parsed.Body.CheckpointID, nil
			}
		}
	}
	return "", fmt.Errorf("checkpoint request event not found")
}

func processExitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitError, ok := err.(*exec.ExitError); ok {
		return exitError.ExitCode()
	}
	return 1
}

func normalizeCheckpointEventID(value string) string {
	if strings.HasPrefix(value, "ev_") && len(value) >= 20 {
		return "<event-id>" + value[19:]
	}
	return value
}

func normalizeCheckpointID(value, checkpointID string) string {
	return strings.ReplaceAll(value, checkpointID, "<checkpoint-id>")
}

func checkpointCLIReadFiles(root string) ([]checkpointCLIFile, error) {
	files := []checkpointCLIFile{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
		if err != nil {
			return err
		}
		if strings.HasSuffix(path, ".jsonl") {
			data, err = normalizeCheckpointJournal(data)
			if err != nil {
				return err
			}
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, checkpointCLIFile{Name: filepath.ToSlash(relative), Content: string(data)})
		return nil
	})
	if os.IsNotExist(err) {
		return files, nil
	}
	if err != nil {
		return nil, err
	}
	return files, nil
}

func checkpointCLIReadFilesRaw(root string) ([]checkpointCLIFile, error) {
	files := []checkpointCLIFile{}
	entries, err := os.ReadDir(root)
	if os.IsNotExist(err) {
		return files, nil
	}
	if err != nil {
		return nil, err
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, entry.Name())) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
		if err != nil {
			return nil, err
		}
		files = append(files, checkpointCLIFile{Name: entry.Name(), Content: string(data)})
	}
	return files, nil
}

func normalizeCheckpointJournal(data []byte) ([]byte, error) {
	lines := bytes.Split(data, []byte{'\n'})
	previousDynamic := false
	for index, line := range lines {
		if len(line) == 0 {
			continue
		}
		var value map[string]any
		if err := json.Unmarshal(line, &value); err != nil {
			return nil, err
		}
		value["ts"], value["sig"] = "<dynamic-clock>", "<signature>"
		if index > 0 && previousDynamic {
			value["prev"] = "<dynamic-prev>"
		}
		previousDynamic = false
		if checkpointID, ok := value["id"].(string); ok && strings.HasPrefix(checkpointID, "ev_") && len(checkpointID) == 19 {
			value["id"] = "ev_<event-id>"
			previousDynamic = true
		}
		body, ok := value["body"].(map[string]any)
		if ok {
			if id, ok := body["checkpoint_id"].(string); ok && strings.HasPrefix(id, "chk_") && len(id) == 20 && id != "chk_existing" {
				body["checkpoint_id"] = "<checkpoint-id>"
				previousDynamic = true
			}
		}
		lines[index], _ = json.Marshal(value)
	}
	return bytes.Join(lines, []byte{'\n'}), nil
}
