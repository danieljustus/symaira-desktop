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
	"runtime"
	"sort"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/room/artifact"
	"github.com/danieljustus/symaira-desktop/internal/room/event"
	"github.com/danieljustus/symaira-desktop/internal/room/identity"
)

const watchCLIContractPath = "testdata/port/room/watch-cli.json"

type watchCLIFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type watchCLICase struct {
	Name               string         `json:"name"`
	Args               []string       `json:"args"`
	DefaultIdentityEnv bool           `json:"default_identity_env,omitempty"`
	PathMode           string         `json:"path_mode,omitempty"`
	Files              []watchCLIFile `json:"files,omitempty"`
	UpdatedFiles       []watchCLIFile `json:"updated_files,omitempty"`
	InitialJournal     []watchCLIFile `json:"initial_journal,omitempty"`
	ExitCode           int            `json:"exit_code"`
	Stdout             string         `json:"stdout"`
	Stderr             string         `json:"stderr"`
	FinalFiles         []watchCLIFile `json:"final_files"`
	SymdeskArgs        []string       `json:"symdesk_args,omitempty"`
}

type watchCLIContract struct {
	SchemaVersion  int               `json:"schema_version"`
	OracleRevision string            `json:"oracle_revision"`
	SourceHashes   map[string]string `json:"source_hashes"`
	IdentityKey    string            `json:"identity_key"`
	Cases          []watchCLICase    `json:"cases"`
}

// TestPortWatchCLIContract captures the Go watch process and its signed journal effect.
func TestPortWatchCLIContract(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := makeWatchCLIContract(t, root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, watchCLIContractPath)
	if os.Getenv("PORT_GENERATE") == "1" {
		if runtime.GOOS == "windows" {
			t.Skip("Unix cancellation case is generated on Unix")
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // generated contract fixture is intentionally world-readable
			t.Fatal(err)
		}
		t.Logf("wrote %s", watchCLIContractPath)
		return
	}
	got, err := os.ReadFile(path) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
	if err != nil {
		t.Fatalf("read %s: %v (set PORT_GENERATE=1 to create it)", watchCLIContractPath, err)
	}
	if runtime.GOOS == "windows" {
		var frozen watchCLIContract
		if err := json.Unmarshal(got, &frozen); err != nil {
			t.Fatal(err)
		}
		portable := frozen.Cases[:0]
		for _, c := range frozen.Cases {
			if c.Name != "watch-cancel" {
				portable = append(portable, c)
			}
		}
		frozen.Cases = portable
		portableBytes, err := json.MarshalIndent(frozen, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		portableBytes = append(portableBytes, '\n')
		if !bytes.Equal(data, portableBytes) {
			t.Fatal("Go watch portable fixture is stale")
		}
		return
	}
	if !bytes.Equal(got, data) {
		t.Fatal("Go watch CLI fixture is stale; regenerate deliberately with PORT_GENERATE=1")
	}
}

func makeWatchCLIContract(t *testing.T, root string) (watchCLIContract, error) {
	t.Helper()
	seed := sha256.Sum256([]byte("symroom-watch-cli-owner"))
	private := ed25519.NewKeyFromSeed(seed[:])
	owner := &identity.Identity{
		Name: "owner", MemberID: identity.ComputeMemberID(private.Public().(ed25519.PublicKey)),
		PublicKey: private.Public().(ed25519.PublicKey), PrivateKey: private,
	}
	fixture := watchCLIContract{
		SchemaVersion:  1,
		OracleRevision: "138746d3ac4df97dd230ecd0fd67f762dd55f499",
		IdentityKey:    hex.EncodeToString(seed[:]),
		SourceHashes:   map[string]string{},
	}
	for _, rel := range []string{
		"cmd/symroom/main.go", "cmd/symroom/cmd_watch.go", "internal/room/desk/watch.go",
		"internal/room/artifact/artifact.go", "internal/room/event/event.go",
	} {
		data, err := os.ReadFile(filepath.Join(root, rel)) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
		if err != nil {
			return fixture, err
		}
		sum := sha256.Sum256(data)
		fixture.SourceHashes[rel] = hex.EncodeToString(sum[:])
	}
	goBinary := filepath.Join(t.TempDir(), "symroom-go-watch-oracle")
	if runtime.GOOS == "windows" {
		goBinary += ".exe"
	}
	build := exec.Command("go", "build", "-o", goBinary, "./cmd/symroom") //nolint:gosec // test-only command uses a fixed helper and controlled arguments
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		return fixture, fmt.Errorf("build Go symroom watch oracle: %w\n%s", err, output)
	}
	for _, vector := range []struct {
		name               string
		args               []string
		pathMode           string
		withArtifact       bool
		defaultIdentityEnv bool
		updates            []watchCLIFile
	}{
		{name: "usage", args: []string{"watch"}},
		{name: "unknown-flag", args: []string{"watch", "--bogus"}},
		{name: "flag-help", args: []string{"watch", "--help"}},
		{name: "missing-desk", args: []string{"watch", "--identity", "owner"}},
		{name: "missing-identity", args: []string{"watch", "--desk", "fixture-vault"}},
		{name: "symdesk-not-found", args: []string{"watch", "--desk", "fixture-vault", "--identity", "owner"}, pathMode: "empty"},
		{name: "default-identity-env", args: []string{"watch", "--desk", "fixture-vault"}, pathMode: "empty", defaultIdentityEnv: true},
		{name: "watch-cancel", args: []string{"watch", "--desk", "fixture-vault", "--identity", "owner"}, pathMode: "fake", withArtifact: true, updates: []watchCLIFile{{Name: "report.md", Content: "after\n"}}},
	} {
		if runtime.GOOS == "windows" && vector.name == "watch-cancel" {
			continue
		}
		caseDir := filepath.Join(t.TempDir(), vector.name)
		roomDir := filepath.Join(caseDir, "room")
		journalDir := filepath.Join(roomDir, "journal")
		if err := os.MkdirAll(roomDir, 0o700); err != nil {
			return fixture, err
		}
		files := []watchCLIFile{}
		var initialJournal []watchCLIFile
		if vector.withArtifact {
			before := []byte("before\n")
			docPath := filepath.Join(roomDir, "report.md")
			if err := os.WriteFile(docPath, before, 0o600); err != nil {
				return fixture, err
			}
			if _, err := artifact.Link(roomDir, roomDir, docPath, "Watch fixture", owner); err != nil {
				return fixture, err
			}
			if err := freezeWatchInitialEvent(filepath.Join(journalDir, owner.MemberID+".jsonl"), owner); err != nil {
				return fixture, err
			}
			readJournal, readErr := watchCLIReadFiles(journalDir, false)
			if readErr != nil {
				return fixture, readErr
			}
			initialJournal = readJournal
			files = []watchCLIFile{{Name: "report.md", Content: "before\n"}}
			for _, update := range vector.updates {
				if err := os.WriteFile(filepath.Join(roomDir, update.Name), []byte(update.Content), 0o600); err != nil {
					return fixture, err
				}
			}
		}
		home, tmp := filepath.Join(caseDir, "home"), filepath.Join(caseDir, "tmp")
		for _, path := range []string{home, tmp} {
			if err := os.MkdirAll(path, 0o700); err != nil {
				return fixture, err
			}
		}
		pathDir := filepath.Join(caseDir, "path")
		if err := os.MkdirAll(pathDir, 0o700); err != nil {
			return fixture, err
		}
		argsFile := filepath.Join(caseDir, "symdesk-args.txt")
		if vector.pathMode == "fake" {
			script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$WATCH_ARGS_FILE\"\nprintf '{\"event\":\"file_changed\",\"path\":\"%s\"}\\n' \"$WATCH_EVENT_PATH\"\nexec /bin/sleep 60\n"
			if err := os.WriteFile(filepath.Join(pathDir, "symdesk"), []byte(script), 0o755); err != nil { //nolint:gosec // test helper must be executable
				return fixture, err
			}
		}
		cmd := exec.Command(goBinary, vector.args...) //nolint:gosec // test-only command uses a fixed helper and controlled arguments
		cmd.Dir = roomDir
		cmd.Env = []string{
			"HOME=" + home, "USERPROFILE=" + home, "TMPDIR=" + tmp, "TZ=UTC", "LC_ALL=C", "LANG=C",
			"PATH=" + pathDir, "SYMROOM_ROOM_DIR=" + roomDir, "SYMROOM_IDENTITY_KEY=" + fixture.IdentityKey,
			"WATCH_ARGS_FILE=" + argsFile, "WATCH_EVENT_PATH=report.md",
		}
		if vector.defaultIdentityEnv {
			cmd.Env = append(cmd.Env, "SYMROOM_DEFAULT_IDENTITY=owner")
		}
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		if err := cmd.Start(); err != nil {
			return fixture, fmt.Errorf("start Go watch case %s: %w", vector.name, err)
		}
		if vector.name == "watch-cancel" {
			if !waitForAppendedJournal(t, filepath.Join(journalDir, owner.MemberID+".jsonl"), 2, 3*time.Second) {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				return fixture, fmt.Errorf("watch case did not append its signed artifact event")
			}
			if err := cmd.Process.Signal(syscall.SIGTERM); err != nil {
				_ = cmd.Process.Kill()
				_ = cmd.Wait()
				return fixture, fmt.Errorf("signal Go watch process: %w", err)
			}
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		var runErr error
		select {
		case runErr = <-done:
		case <-time.After(3 * time.Second):
			_ = cmd.Process.Kill()
			<-done
			return fixture, fmt.Errorf("Go watch case %s exceeded termination bound", vector.name)
		}
		code := 0
		if runErr != nil {
			if exitError, ok := runErr.(*exec.ExitError); ok {
				code = exitError.ExitCode()
			} else {
				return fixture, fmt.Errorf("run Go watch case %s: %w", vector.name, runErr)
			}
		}
		finalFiles, err := watchCLIReadRoom(roomDir)
		if err != nil {
			return fixture, err
		}
		result := watchCLICase{
			Name: vector.name, Args: vector.args, PathMode: vector.pathMode, DefaultIdentityEnv: vector.defaultIdentityEnv,
			Files: files, UpdatedFiles: vector.updates, InitialJournal: initialJournal, ExitCode: code,
			Stdout: stdout.String(), Stderr: stderr.String(), FinalFiles: finalFiles,
		}
		if vector.pathMode == "fake" {
			data, err := os.ReadFile(argsFile) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
			if err != nil {
				return fixture, err
			}
			result.SymdeskArgs = strings.Fields(string(data))
		}
		fixture.Cases = append(fixture.Cases, result)
	}
	return fixture, nil
}

func waitForAppendedJournal(t *testing.T, path string, lines int, timeout time.Duration) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(path) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
		if err == nil && bytes.Count(data, []byte{'\n'}) >= lines {
			return true
		}
		time.Sleep(10 * time.Millisecond)
	}
	return false
}

func watchCLIReadRoom(room string) ([]watchCLIFile, error) {
	files, err := watchCLIReadFiles(room, true)
	if os.IsNotExist(err) {
		return []watchCLIFile{}, nil
	}
	return files, err
}

func watchCLIReadFiles(root string, normalizeJournal bool) ([]watchCLIFile, error) {
	files := []watchCLIFile{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
		if err != nil {
			return err
		}
		if normalizeJournal && strings.HasSuffix(path, ".jsonl") {
			data, err = normalizeWatchJournal(data)
			if err != nil {
				return err
			}
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		files = append(files, watchCLIFile{Name: filepath.ToSlash(name), Content: string(data)})
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}

func normalizeWatchJournal(data []byte) ([]byte, error) {
	lines := bytes.Split(data, []byte{'\n'})
	for i, line := range lines {
		if len(line) == 0 {
			continue
		}
		if i == 0 {
			continue
		}
		var value map[string]any
		if err := json.Unmarshal(line, &value); err != nil {
			return nil, err
		}
		value["ts"], value["sig"] = "<dynamic-clock>", "<signature>"
		lines[i], _ = json.Marshal(value)
	}
	return bytes.Join(lines, []byte{'\n'}), nil
}

func freezeWatchInitialEvent(path string, owner *identity.Identity) error {
	data, err := os.ReadFile(path) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
	if err != nil {
		return err
	}
	ev, err := event.UnmarshalJSONLine(bytes.TrimSpace(data))
	if err != nil {
		return err
	}
	ev.TS = "2026-09-23T00:00:00.000Z"
	ev.Sig = ""
	if err := ev.Sign(owner); err != nil {
		return err
	}
	line, err := ev.MarshalJSONLine()
	if err != nil {
		return err
	}
	return os.WriteFile(path, line, 0o600) //nolint:gosec // path is a test fixture beneath the temporary room root
}
