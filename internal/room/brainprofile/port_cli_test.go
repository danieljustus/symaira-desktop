package brainprofile

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
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/room/event"
	"github.com/danieljustus/symaira-desktop/internal/room/identity"
)

const brainProfileCLIFixture = "testdata/port/room/brain-profile-cli.json"

type brainProfileCLIFile struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Mode    uint32 `json:"mode,omitempty"`
}

type brainProfileCLICase struct {
	Name       string                `json:"name"`
	Args       []string              `json:"args"`
	PathMode   string                `json:"path_mode,omitempty"`
	ExitCode   int                   `json:"exit_code"`
	Stdout     string                `json:"stdout"`
	Stderr     string                `json:"stderr"`
	FinalFiles []brainProfileCLIFile `json:"final_files"`
	DirMode    uint32                `json:"dir_mode,omitempty"`
}

type brainProfileCLIContract struct {
	SchemaVersion  int                   `json:"schema_version"`
	OracleRevision string                `json:"oracle_revision"`
	SourceHashes   map[string]string     `json:"source_hashes"`
	MemberID       string                `json:"member_id"`
	RoomTOML       string                `json:"room_toml"`
	JournalFiles   []brainProfileCLIFile `json:"journal_files"`
	Cases          []brainProfileCLICase `json:"cases"`
}

// TestPortBrainProfileCLIContract records exact Go process output and install effects.
func TestPortBrainProfileCLIContract(t *testing.T) {
	root := brainProfileCLIRoot(t)
	fixture, err := makeBrainProfileCLIContract(t, root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, brainProfileCLIFixture)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", brainProfileCLIFixture)
		return
	}
	got, err := os.ReadFile(path) //nolint:gosec // path is the fixed repository fixture path
	if err != nil {
		t.Fatalf("read %s: %v (set PORT_GENERATE=1 to create it)", brainProfileCLIFixture, err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("Go brain-profile CLI fixture is stale; regenerate deliberately with PORT_GENERATE=1")
	}
}

func makeBrainProfileCLIContract(t *testing.T, root string) (brainProfileCLIContract, error) {
	t.Helper()
	seed := sha256.Sum256([]byte("symroom-brain-profile-cli-fixture"))
	privateKey := ed25519.NewKeyFromSeed(seed[:])
	publicKey := privateKey.Public().(ed25519.PublicKey)
	memberID := identity.ComputeMemberID(publicKey)
	fixture := brainProfileCLIContract{
		SchemaVersion:  1,
		OracleRevision: "07eb8d9fefcc9e150302558ea0613d49e6dfc201",
		MemberID:       memberID,
		RoomTOML:       "id = \"rm_brain_profile_fixture\"\ncreated = \"2026-09-23T00:00:00.000Z\"\n",
		SourceHashes:   map[string]string{},
	}
	for _, rel := range []string{"cmd/symroom/main.go", "cmd/symroom/cmd_brainprofile.go", "internal/room/brainprofile/brainprofile.go"} {
		data, err := os.ReadFile(filepath.Join(root, rel)) //nolint:gosec // rel is a fixed repository source path
		if err != nil {
			return brainProfileCLIContract{}, err
		}
		sum := sha256.Sum256(data)
		fixture.SourceHashes[rel] = hex.EncodeToString(sum[:])
	}
	body, err := json.Marshal(map[string]string{"name": "Project Alpha", "public_key": hex.EncodeToString(publicKey)})
	if err != nil {
		return brainProfileCLIContract{}, err
	}
	ev := &event.Event{
		V: 1, ID: "ev_brainprofile_fixture", Room: "rm_brain_profile_fixture", Author: memberID,
		Seq: 1, Prev: "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		Lamport: 1, TS: "2026-09-23T00:00:00.000Z", Kind: event.KindRoomCreated, Body: body,
	}
	journal, err := ev.MarshalJSONLine()
	if err != nil {
		return brainProfileCLIContract{}, err
	}
	fixture.JournalFiles = []brainProfileCLIFile{{Path: memberID + ".jsonl", Content: string(journal)}}
	goBinary := buildBrainProfileCLIOracle(t, root)
	binDir := t.TempDir()
	helperSource := filepath.Join(binDir, "symbrain.go")
	if err := os.WriteFile(helperSource, []byte(brainProfileHelper), 0o600); err != nil {
		return brainProfileCLIContract{}, err
	}
	helperBinary := filepath.Join(binDir, "symbrain")
	if runtime.GOOS == "windows" {
		helperBinary += ".exe"
	}
	build := exec.Command("go", "build", "-o", helperBinary, helperSource) //nolint:gosec // fixed Go build command for a temporary test helper
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		return brainProfileCLIContract{}, fmt.Errorf("build fake symbrain: %w\n%s", err, output)
	}
	binPath := filepath.Dir(helperBinary)
	for _, vector := range []struct {
		name     string
		args     []string
		pathMode string
	}{
		{"usage", []string{"brain-profile"}, "empty"},
		{"unknown-flag", []string{"brain-profile", "--bogus"}, "empty"},
		{"missing-member", []string{"brain-profile", "--member", "mem_missing"}, "empty"},
		{"render", []string{"brain-profile", "--member", memberID}, "empty"},
		{"install-no-symbrain", []string{"brain-profile", "--member", memberID, "--install"}, "empty"},
		{"install-symbrain", []string{"brain-profile", "--member", memberID, "--install"}, "helper"},
		{"install-fallback", []string{"brain-profile", "--member", memberID, "--install"}, "helper-fails"},
	} {
		caseDir := t.TempDir()
		home := filepath.Join(caseDir, "home")
		roomDir := filepath.Join(caseDir, "room")
		journalDir := filepath.Join(roomDir, "journal")
		if err := os.MkdirAll(journalDir, 0o700); err != nil {
			return brainProfileCLIContract{}, err
		}
		if err := os.WriteFile(filepath.Join(roomDir, "room.toml"), []byte(fixture.RoomTOML), 0o600); err != nil {
			return brainProfileCLIContract{}, err
		}
		for _, file := range fixture.JournalFiles {
			if err := os.WriteFile(filepath.Join(journalDir, file.Path), []byte(file.Content), 0o600); err != nil {
				return brainProfileCLIContract{}, err
			}
		}
		if err := os.MkdirAll(home, 0o700); err != nil {
			return brainProfileCLIContract{}, err
		}
		path := ""
		switch vector.pathMode {
		case "empty":
			path = filepath.Join(caseDir, "empty-path")
			if err := os.Mkdir(path, 0o700); err != nil {
				return brainProfileCLIContract{}, err
			}
		case "helper", "helper-fails":
			path = binPath
		}
		cmd := exec.Command(goBinary, vector.args...) //nolint:gosec // goBinary is the test-built helper and args are fixed vectors
		cmd.Env = []string{"HOME=" + home, "USERPROFILE=" + home, "PATH=" + path, "SYMROOM_ROOM_DIR=" + roomDir, "TZ=UTC", "LC_ALL=C", "LANG=C"}
		if vector.pathMode == "helper-fails" {
			cmd.Env = append(cmd.Env, "FAKE_SYMBRAIN_FAIL=1")
		}
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		runErr := cmd.Run()
		code := 0
		if runErr != nil {
			if exit, ok := runErr.(*exec.ExitError); ok {
				code = exit.ExitCode()
			} else {
				return brainProfileCLIContract{}, fmt.Errorf("run Go symroom %s: %w", vector.name, runErr)
			}
		}
		result := brainProfileCLICase{Name: vector.name, Args: vector.args, PathMode: vector.pathMode, ExitCode: code,
			Stdout: strings.ReplaceAll(stdout.String(), home, "<HOME>"), Stderr: stderr.String(), FinalFiles: []brainProfileCLIFile{}}
		profileDir := filepath.Join(home, ".config", "symbrain", "profiles")
		if info, err := os.Stat(profileDir); err == nil && info.IsDir() {
			result.DirMode = uint32(info.Mode().Perm())
			entries, err := os.ReadDir(profileDir)
			if err != nil {
				return brainProfileCLIContract{}, err
			}
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				path := filepath.Join(profileDir, entry.Name())
				data, err := os.ReadFile(path) //nolint:gosec // path is a test fixture beneath t.TempDir
				if err != nil {
					return brainProfileCLIContract{}, err
				}
				fileInfo, err := os.Stat(path)
				if err != nil {
					return brainProfileCLIContract{}, err
				}
				result.FinalFiles = append(result.FinalFiles, brainProfileCLIFile{Path: entry.Name(), Content: string(data), Mode: uint32(fileInfo.Mode().Perm())})
			}
		}
		fixture.Cases = append(fixture.Cases, result)
	}
	return fixture, nil
}

const brainProfileHelper = `package main
import ("fmt"; "io"; "os")
func main() {
	input, _ := io.ReadAll(os.Stdin)
	fmt.Printf("called %s %s %s\n%s", os.Args[1], os.Args[2], os.Args[3], input)
	if os.Getenv("FAKE_SYMBRAIN_FAIL") == "1" { os.Exit(1) }
}
`

func buildBrainProfileCLIOracle(t *testing.T, root string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "symroom-go")
	if runtime.GOOS == "windows" {
		path += ".exe"
	}
	cmd := exec.Command("go", "build", "-o", path, "./cmd/symroom") //nolint:gosec // fixed Go build command for the test oracle
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build Go symroom oracle: %v\n%s", err, output)
	}
	return path
}

func brainProfileCLIRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve brain-profile CLI contract test path")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(filename), "../../.."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}
