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
	"time"

	"github.com/danieljustus/symaira-desktop/internal/room/event"
	"github.com/danieljustus/symaira-desktop/internal/room/identity"
)

const doctorCLIFixturePath = "testdata/port/room/doctor-cli.json"
const doctorCLIOracleRevision = "7d9d60bab2742e10f238ead245f967cce1adc4ea"

type doctorCLIContract struct {
	SchemaVersion  int               `json:"schema_version"`
	OracleRevision string            `json:"oracle_revision"`
	SourceHashes   map[string]string `json:"source_hashes"`
	IdentityKey    string            `json:"identity_key"`
	IdentityMember string            `json:"identity_member"`
	Cases          []doctorCLICase   `json:"cases"`
}

type doctorCLIIdentityFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
	Mode    string `json:"mode"`
}

type doctorCLICase struct {
	Name          string                  `json:"name"`
	Args          []string                `json:"args"`
	Room          string                  `json:"room"`
	Config        string                  `json:"config,omitempty"`
	DefaultEnv    string                  `json:"default_env,omitempty"`
	IdentityKey   bool                    `json:"identity_key,omitempty"`
	IdentityFiles []doctorCLIIdentityFile `json:"identity_files,omitempty"`
	Tools         bool                    `json:"tools,omitempty"`
	Index         string                  `json:"index,omitempty"`
	ExitCode      int                     `json:"exit_code"`
	Stdout        string                  `json:"stdout"`
	Stderr        string                  `json:"stderr"`
	ToolCalls     string                  `json:"tool_calls"`
	RoomFiles     []string                `json:"room_files"`
}

func TestPortDoctorCLIContract(t *testing.T) {
	root := noteCLIRoot(t)
	fixture, err := makeDoctorCLIContract(t, root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, doctorCLIFixturePath)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (set PORT_GENERATE=1 to create it)", doctorCLIFixturePath, err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("Go doctor CLI fixture is stale; regenerate deliberately with PORT_GENERATE=1")
	}
}

func makeDoctorCLIContract(t *testing.T, root string) (doctorCLIContract, error) {
	t.Helper()
	seed := sha256.Sum256([]byte("symroom-doctor-cli-fixture-identity"))
	private := ed25519.NewKeyFromSeed(seed[:])
	public := private.Public().(ed25519.PublicKey)
	owner := &identity.Identity{
		Name: "oracle", MemberID: identity.ComputeMemberID(public),
		PublicKey: public, PrivateKey: private,
	}
	fixture := doctorCLIContract{
		SchemaVersion: 1, OracleRevision: doctorCLIOracleRevision,
		IdentityKey: hex.EncodeToString(seed[:]), IdentityMember: owner.MemberID,
		SourceHashes: map[string]string{
			"cmd/symroom/cmd_doctor.go":          noteCLIFileHash(t, root, "cmd/symroom/cmd_doctor.go"),
			"internal/room/doctor/doctor.go":     noteCLIFileHash(t, root, "internal/room/doctor/doctor.go"),
			"internal/room/config/config.go":     noteCLIFileHash(t, root, "internal/room/config/config.go"),
			"internal/room/journal/verifier.go":  noteCLIFileHash(t, root, "internal/room/journal/verifier.go"),
			"internal/room/identity/identity.go": noteCLIFileHash(t, root, "internal/room/identity/identity.go"),
		},
	}
	goBinary := buildNoteCLIOracle(t, root)
	temp := t.TempDir()
	for _, vector := range []struct {
		name        string
		args        []string
		room        string
		config      string
		defaultEnv  string
		identityKey bool
		identity    string
		identityBad bool
		tools       bool
		index       string
	}{
		{name: "healthy-json-tools", args: []string{"doctor", "--json"}, room: "valid", config: "default_identity = \"oracle\"\n", identity: "oracle", tools: true, index: "current"},
		{name: "healthy-human", args: []string{"doctor"}, room: "valid", config: "default_identity = \"oracle\"\n", identity: "oracle", tools: true, index: "current"},
		{name: "missing-default-empty-room", args: []string{"doctor", "--json"}, room: "empty"},
		{name: "malformed-config-type", args: []string{"doctor", "--json"}, room: "empty", config: "default_identity = 7\n"},
		{name: "external-identity-provider", args: []string{"doctor", "--json"}, room: "valid", defaultEnv: "oracle", identityKey: true},
		{name: "bad-key-mode-stale-index", args: []string{"doctor", "--json"}, room: "valid", config: "default_identity = \"oracle\"\n", identity: "oracle", identityBad: true, index: "stale"},
		{name: "doctor-help", args: []string{"doctor", "--help"}, room: "empty"},
		{name: "doctor-unknown-flag", args: []string{"doctor", "--bogus"}, room: "empty"},
	} {
		work := filepath.Join(temp, vector.name)
		home := filepath.Join(work, "home")
		dataHome := filepath.Join(work, "data")
		toolsDir := filepath.Join(work, "tools")
		tempDir := filepath.Join(work, "tmp")
		for _, dir := range []string{home, dataHome, toolsDir, tempDir} {
			if err := os.MkdirAll(dir, 0o700); err != nil {
				return doctorCLIContract{}, err
			}
		}
		if vector.config != "" {
			configPath := filepath.Join(home, ".config", "symroom", "config.toml")
			if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
				return doctorCLIContract{}, err
			}
			if err := os.WriteFile(configPath, []byte(vector.config), 0o600); err != nil {
				return doctorCLIContract{}, err
			}
		}
		var identityFiles []doctorCLIIdentityFile
		if vector.identity != "" {
			if err := doctorCLISaveIdentity(dataHome, owner); err != nil {
				return doctorCLIContract{}, err
			}
			identityPath := filepath.Join(dataHome, "symroom", "identities", "oracle.json")
			if vector.identityBad {
				if err := os.Chmod(identityPath, 0o644); err != nil {
					return doctorCLIContract{}, err
				}
			}
			file, err := doctorCLIReadIdentity(identityPath)
			if err != nil {
				return doctorCLIContract{}, err
			}
			identityFiles = append(identityFiles, file)
		}
		roomDir := filepath.Join(work, "room")
		if vector.room == "valid" {
			if err := makeDoctorRoom(roomDir, owner, vector.index); err != nil {
				return doctorCLIContract{}, err
			}
		} else if err := os.MkdirAll(roomDir, 0o700); err != nil {
			return doctorCLIContract{}, err
		}
		if vector.tools {
			if err := installDoctorTools(toolsDir, fixture.IdentityKey); err != nil {
				return doctorCLIContract{}, err
			}
		}
		logPath := filepath.Join(work, "tool-calls")
		cmd := exec.Command(goBinary, vector.args...)
		cmd.Dir = work
		cmd.Env = []string{
			"HOME=" + home, "USERPROFILE=" + home,
			"XDG_DATA_HOME=" + dataHome, "XDG_CONFIG_HOME=" + filepath.Join(work, "xdg-config"),
			"TMPDIR=" + tempDir, "PATH=" + toolsDir,
			"TZ=UTC", "LC_ALL=C", "LANG=C",
			"SYMROOM_ROOM_DIR=" + roomDir, "DOCTOR_TOOL_LOG=" + logPath,
		}
		if vector.defaultEnv != "" {
			cmd.Env = append(cmd.Env, "SYMROOM_DEFAULT_IDENTITY="+vector.defaultEnv)
		}
		if vector.identityKey {
			cmd.Env = append(cmd.Env, "SYMROOM_IDENTITY_KEY="+fixture.IdentityKey)
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		stdout, runErr := cmd.Output()
		code := 0
		if runErr != nil {
			if exitErr, ok := runErr.(*exec.ExitError); ok {
				code = exitErr.ExitCode()
			} else {
				return doctorCLIContract{}, fmt.Errorf("run Go doctor case %s: %w", vector.name, runErr)
			}
		}
		calls, _ := os.ReadFile(logPath)
		result := doctorCLICase{
			Name: vector.name, Args: vector.args, Room: vector.room,
			Config: vector.config, DefaultEnv: vector.defaultEnv, IdentityKey: vector.identityKey,
			IdentityFiles: identityFiles, Tools: vector.tools, Index: vector.index,
			ExitCode:  code,
			Stdout:    normalizeDoctorCLIOutput(stdout, home, dataHome, toolsDir),
			Stderr:    normalizeDoctorCLIOutput(stderr.Bytes(), home, dataHome, toolsDir),
			ToolCalls: string(calls), RoomFiles: doctorCLIRoomFiles(roomDir),
		}
		fixture.Cases = append(fixture.Cases, result)
	}
	return fixture, nil
}

func doctorCLISaveIdentity(dataHome string, owner *identity.Identity) (result error) {
	old, had := os.LookupEnv("XDG_DATA_HOME")
	if err := os.Setenv("XDG_DATA_HOME", dataHome); err != nil {
		return err
	}
	defer func() {
		var restore error
		if had {
			restore = os.Setenv("XDG_DATA_HOME", old)
		} else {
			restore = os.Unsetenv("XDG_DATA_HOME")
		}
		if result == nil {
			result = restore
		}
	}()
	return identity.Save(owner)
}

func doctorCLIReadIdentity(path string) (doctorCLIIdentityFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return doctorCLIIdentityFile{}, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return doctorCLIIdentityFile{}, err
	}
	return doctorCLIIdentityFile{
		Name: filepath.Base(path), Content: string(data), Mode: fmt.Sprintf("%04o", info.Mode().Perm()),
	}, nil
}

func makeDoctorRoom(dir string, owner *identity.Identity, index string) error {
	if err := os.MkdirAll(filepath.Join(dir, ".symroom"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "journal"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "room.toml"), []byte("id = \"rm_doctor_fixture\"\n"), 0o644); err != nil {
		return err
	}
	ev := &event.Event{
		V: event.CurrentVersion, ID: "ev_doctor_fixture", Room: "rm_doctor_fixture", Author: owner.MemberID,
		Seq: 1, Prev: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		Lamport: 1, TS: "2026-01-02T03:04:05.006Z", Kind: event.KindRoomCreated,
		Body: json.RawMessage(fmt.Sprintf(`{"name":"Doctor Room","public_key":"%s"}`, hex.EncodeToString(owner.PublicKey))),
	}
	if err := ev.Sign(owner); err != nil {
		return err
	}
	line, err := ev.MarshalJSONLine()
	if err != nil {
		return err
	}
	journalPath := filepath.Join(dir, "journal", owner.MemberID+".jsonl")
	if err := os.WriteFile(journalPath, line, 0o644); err != nil {
		return err
	}
	if index == "" {
		return nil
	}
	indexPath := filepath.Join(dir, ".symroom", "index.sqlite")
	if err := os.WriteFile(indexPath, []byte("derived index fixture"), 0o600); err != nil {
		return err
	}
	stamp := time.Date(2035, 1, 1, 0, 0, 0, 0, time.UTC)
	if index == "stale" {
		stamp = time.Date(2000, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	return os.Chtimes(indexPath, stamp, stamp)
}

func installDoctorTools(dir, identityKey string) error {
	for _, tool := range []string{"symdesk", "symbrain", "symvault"} {
		script := fmt.Sprintf("#!/bin/sh\ncase \"$1\" in\n  get) printf '%%s %%s\\n' '%s' \"$*\" >> \"$DOCTOR_TOOL_LOG\"; printf '%%s\\n' '%s' ;;\n  version) printf '%%s %%s\\n' '%s' \"$*\" >> \"$DOCTOR_TOOL_LOG\"; printf '{\\\"version\\\":\\\"%s-1.2.3\\\"}\\n' ;;\nesac\n", tool, identityKey, tool, tool)
		path := filepath.Join(dir, tool)
		if err := os.WriteFile(path, []byte(script), 0o755); err != nil {
			return err
		}
	}
	return nil
}

func doctorCLIRoomFiles(dir string) []string {
	files := []string{}
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err == nil && info != nil && !info.IsDir() {
			rel, relErr := filepath.Rel(dir, path)
			if relErr == nil {
				files = append(files, filepath.ToSlash(rel))
			}
		}
		return nil
	})
	sort.Strings(files)
	return files
}

func normalizeDoctorCLIOutput(output []byte, home, dataHome, toolsDir string) string {
	text := string(output)
	for _, replacement := range [][2]string{{home, "$HOME"}, {dataHome, "$DATA"}, {toolsDir, "$TOOLS"}} {
		text = strings.ReplaceAll(text, replacement[0], replacement[1])
	}
	return text
}
