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
	"regexp"
	"runtime"
	"sort"
	"testing"

	"github.com/BurntSushi/toml"
	"github.com/danieljustus/symaira-desktop/internal/room/event"
	"github.com/danieljustus/symaira-desktop/internal/room/identity"
	"github.com/danieljustus/symaira-desktop/internal/room/room"
)

const initCLIFixturePath = "testdata/port/room/init-cli.json"
const initCLIOracleRevision = "8e11384470ea86b15d1d60f21442a3e0c7287d53"
const initCLINormalizedRoomID = "rm_0123456789abcdef"
const initCLINormalizedEventID = "ev_0123456789abcdef0123"
const initCLINormalizedTime = "2026-01-02T03:04:05.006Z"

type initCLIContract struct {
	SchemaVersion       int               `json:"schema_version"`
	OracleRevision      string            `json:"oracle_revision"`
	SourceHashes        map[string]string `json:"source_hashes"`
	IdentityKey         string            `json:"identity_key"`
	IdentityMember      string            `json:"identity_member"`
	IdentityFileName    string            `json:"identity_file_name"`
	IdentityFileContent string            `json:"identity_file_content"`
	Cases               []initCLICase     `json:"cases"`
}

type initCLIFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type initCLICase struct {
	Name               string            `json:"name"`
	Args               []string          `json:"args"`
	Identity           string            `json:"identity,omitempty"`
	DefaultIdentityEnv bool              `json:"default_identity_env,omitempty"`
	GlobalConfig       string            `json:"global_config,omitempty"`
	RoomDirEnv         bool              `json:"room_dir_env,omitempty"`
	Nonempty           bool              `json:"nonempty,omitempty"`
	ExitCode           int               `json:"exit_code"`
	Stdout             string            `json:"stdout"`
	Stderr             string            `json:"stderr"`
	Files              []initCLIFile     `json:"files"`
	Modes              map[string]string `json:"modes"`
	Preserved          string            `json:"preserved,omitempty"`
}

func TestPortInitCLIContract(t *testing.T) {
	root := noteCLIRoot(t)
	fixture, err := makeInitCLIContract(t, root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, initCLIFixturePath)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // generated contract fixture is intentionally world-readable
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(path) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
	if err != nil {
		t.Fatalf("read %s: %v (set PORT_GENERATE=1 to create it)", initCLIFixturePath, err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("Go init CLI fixture is stale; regenerate deliberately with PORT_GENERATE=1")
	}
}

func makeInitCLIContract(t *testing.T, root string) (initCLIContract, error) {
	t.Helper()
	seed := sha256.Sum256([]byte("symroom-init-cli-fixture-identity"))
	private := ed25519.NewKeyFromSeed(seed[:])
	public := private.Public().(ed25519.PublicKey)
	owner := &identity.Identity{
		Name: "oracle", MemberID: identity.ComputeMemberID(public),
		PublicKey: public, PrivateKey: private,
	}
	fixture := initCLIContract{
		SchemaVersion:    1,
		OracleRevision:   initCLIOracleRevision,
		IdentityKey:      hex.EncodeToString(seed[:]),
		IdentityMember:   owner.MemberID,
		IdentityFileName: "oracle.json",
		SourceHashes: map[string]string{
			"cmd/symroom/main.go":                noteCLIFileHash(t, root, "cmd/symroom/main.go"),
			"cmd/symroom/cmd_init.go":            noteCLIFileHash(t, root, "cmd/symroom/cmd_init.go"),
			"internal/room/room/room.go":         noteCLIFileHash(t, root, "internal/room/room/room.go"),
			"internal/room/identity/identity.go": noteCLIFileHash(t, root, "internal/room/identity/identity.go"),
		},
	}
	goBinary := buildNoteCLIOracle(t, root)
	temp := t.TempDir()
	fileHome := filepath.Join(temp, "identity-file-data")
	if err := saveInitCLIIdentityFile(fileHome, owner); err != nil {
		return initCLIContract{}, err
	}
	fileData, err := os.ReadFile(filepath.Join(fileHome, "symroom", "identities", fixture.IdentityFileName)) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
	if err != nil {
		return initCLIContract{}, err
	}
	fixture.IdentityFileContent = string(fileData)

	for _, vector := range []struct {
		name               string
		args               []string
		identity           string
		roomDirEnv         bool
		nonempty           bool
		defaultIdentityEnv bool
		globalConfig       string
	}{
		{"init-env-separated-flags", []string{"init", "--identity", "oracle", "--name", "Env Room", "room"}, "env", false, false, false, ""},
		{"init-file-equals-flags", []string{"init", "--identity=oracle", "--name=File Room", "room"}, "file", false, false, false, ""},
		{"init-env-current-dir", []string{"init", "--identity", "oracle"}, "env", true, false, false, ""},
		{"init-default-identity-env", []string{"init", "room"}, "file", false, false, true, ""},
		{"init-default-identity-toml", []string{"init", "room"}, "file", false, false, false, "default_identity = \"oracle\"\n"},
		{"init-usage", []string{"init"}, "", false, false, false, ""},
		{"init-help", []string{"init", "--help"}, "", false, false, false, ""},
		{"init-unknown-flag", []string{"init", "--bogus"}, "", false, false, false, ""},
		{"init-missing-flag-value", []string{"init", "--identity"}, "", false, false, false, ""},
		{"init-missing-identity", []string{"init", "room"}, "", false, false, false, ""},
		{"init-nonempty-preserves-file", []string{"init", "--identity", "oracle", "room"}, "env", false, true, false, ""},
	} {
		work := filepath.Join(temp, vector.name)
		if err := os.MkdirAll(work, 0o700); err != nil {
			return initCLIContract{}, err
		}
		roomPath := filepath.Join(work, "room")
		if vector.nonempty {
			if err := os.MkdirAll(roomPath, 0o700); err != nil {
				return initCLIContract{}, err
			}
			if err := os.WriteFile(filepath.Join(roomPath, "keep"), []byte("preserve me"), 0o600); err != nil {
				return initCLIContract{}, err
			}
		}
		home := filepath.Join(work, "home")
		dataHome := fileHome
		if vector.identity != "file" {
			dataHome = filepath.Join(work, "data")
		}
		configHome, tempDir := filepath.Join(work, "config"), filepath.Join(work, "tmp")
		for _, dir := range []string{home, dataHome, configHome, tempDir} {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return initCLIContract{}, err
			}
		}
		if vector.globalConfig != "" {
			configPath := filepath.Join(home, ".config", "symroom", "config.toml")
			if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
				return initCLIContract{}, err
			}
			if err := os.WriteFile(configPath, []byte(vector.globalConfig), 0o600); err != nil {
				return initCLIContract{}, err
			}
		}
		cmd := exec.Command(goBinary, vector.args...) //nolint:gosec // test-only command uses a fixed helper and controlled arguments
		cmd.Dir = work
		cmd.Env = []string{
			"HOME=" + home, "USERPROFILE=" + home, "XDG_DATA_HOME=" + dataHome,
			"XDG_CONFIG_HOME=" + configHome, "TMPDIR=" + tempDir,
			"TZ=UTC", "LC_ALL=C", "LANG=C", "PATH=" + tempDir,
		}
		if vector.identity == "env" {
			cmd.Env = append(cmd.Env, "SYMROOM_IDENTITY_KEY="+fixture.IdentityKey)
		}
		if vector.defaultIdentityEnv {
			cmd.Env = append(cmd.Env, "SYMROOM_DEFAULT_IDENTITY=oracle")
		}
		if vector.roomDirEnv {
			cmd.Env = append(cmd.Env, "SYMROOM_ROOM_DIR=room")
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		stdout, runErr := cmd.Output()
		code := 0
		if runErr != nil {
			if exitErr, ok := runErr.(*exec.ExitError); ok {
				code = exitErr.ExitCode()
			} else {
				return initCLIContract{}, fmt.Errorf("run Go init case %s: %w", vector.name, runErr)
			}
		}
		result := initCLICase{
			Name: vector.name, Args: vector.args, Identity: vector.identity,
			DefaultIdentityEnv: vector.defaultIdentityEnv, GlobalConfig: vector.globalConfig,
			RoomDirEnv: vector.roomDirEnv, Nonempty: vector.nonempty,
			ExitCode: code, Stdout: string(normalizeInitCLIOutput(stdout)), Stderr: stderr.String(),
			Files: []initCLIFile{}, Modes: map[string]string{},
		}
		if vector.nonempty {
			preserved, err := os.ReadFile(filepath.Join(roomPath, "keep")) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
			if err != nil {
				return initCLIContract{}, err
			}
			result.Preserved = string(preserved)
		}
		if code == 0 && len(stdout) > 0 && bytes.Contains(stdout, []byte("Initialized room ")) {
			if err := normalizeGoInitRoom(t, roomPath, owner); err != nil {
				return initCLIContract{}, err
			}
			result.Files, result.Modes, err = readInitCLIFiles(roomPath)
			if err != nil {
				return initCLIContract{}, err
			}
		}
		fixture.Cases = append(fixture.Cases, result)
	}
	return fixture, nil
}

func saveInitCLIIdentityFile(dataHome string, owner *identity.Identity) (result error) {
	oldDataHome, hadDataHome := os.LookupEnv("XDG_DATA_HOME")
	if err := os.Setenv("XDG_DATA_HOME", dataHome); err != nil {
		return err
	}
	defer func() {
		var restoreErr error
		if hadDataHome {
			restoreErr = os.Setenv("XDG_DATA_HOME", oldDataHome)
		} else {
			restoreErr = os.Unsetenv("XDG_DATA_HOME")
		}
		if result == nil {
			result = restoreErr
		}
	}()
	return identity.Save(owner)
}

func normalizeInitCLIOutput(output []byte) []byte {
	return regexp.MustCompile(`rm_[0-9a-f]{16}`).ReplaceAll(output, []byte("<room-id>"))
}

func normalizeGoInitRoom(t *testing.T, dir string, owner *identity.Identity) error {
	t.Helper()
	journalPath := filepath.Join(dir, "journal", owner.MemberID+".jsonl")
	line, err := os.ReadFile(journalPath) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
	if err != nil {
		return err
	}
	ev, err := event.UnmarshalJSONLine(line)
	if err != nil {
		return err
	}
	ev.ID, ev.Room, ev.TS = initCLINormalizedEventID, initCLINormalizedRoomID, initCLINormalizedTime
	if err := ev.Sign(owner); err != nil {
		return err
	}
	line, err = ev.MarshalJSONLine()
	if err != nil {
		return err
	}
	if err := os.WriteFile(journalPath, line, 0o600); err != nil { //nolint:gosec // journalPath is a test fixture beneath the temporary room root
		return err
	}
	roomPath := filepath.Join(dir, "room.toml")
	var config room.RoomConfig
	if _, err := toml.DecodeFile(roomPath, &config); err != nil {
		return err
	}
	config.ID, config.Created, config.RootEvent = initCLINormalizedRoomID, initCLINormalizedTime, initCLINormalizedEventID
	var encoded bytes.Buffer
	if err := toml.NewEncoder(&encoded).Encode(&config); err != nil {
		return err
	}
	return os.WriteFile(roomPath, encoded.Bytes(), 0o600)
}

func readInitCLIFiles(dir string) ([]initCLIFile, map[string]string, error) {
	files := []initCLIFile{}
	modes := map[string]string{}
	for _, rel := range []string{".gitignore", ".symroom/local.toml", "journal"} {
		path := filepath.Join(dir, rel)
		info, err := os.Stat(path)
		if err != nil {
			return nil, nil, err
		}
		modes[rel] = initCLIMode(info.Mode())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, err
	}
	var paths []string
	for _, entry := range entries {
		if entry.IsDir() {
			children, err := os.ReadDir(filepath.Join(dir, entry.Name()))
			if err != nil {
				return nil, nil, err
			}
			for _, child := range children {
				paths = append(paths, filepath.Join(entry.Name(), child.Name()))
			}
		} else {
			paths = append(paths, entry.Name())
		}
	}
	sort.Strings(paths)
	for _, rel := range paths {
		data, err := os.ReadFile(filepath.Join(dir, rel)) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
		if err != nil {
			return nil, nil, err
		}
		info, err := os.Stat(filepath.Join(dir, rel))
		if err != nil {
			return nil, nil, err
		}
		files = append(files, initCLIFile{Name: filepath.ToSlash(rel), Content: string(data)})
		modes[filepath.ToSlash(rel)] = initCLIMode(info.Mode())
	}
	for _, rel := range []string{".symroom", "journal"} {
		info, err := os.Stat(filepath.Join(dir, rel))
		if err != nil {
			return nil, nil, err
		}
		modes[rel] = initCLIMode(info.Mode())
	}
	return files, modes, nil
}

func initCLIMode(mode os.FileMode) string {
	if runtime.GOOS == "windows" {
		return "platform"
	}
	return fmt.Sprintf("%04o", mode.Perm())
}
