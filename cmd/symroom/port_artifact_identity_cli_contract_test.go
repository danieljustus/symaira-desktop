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
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/room/identity"
)

const artifactIdentityContractPath = "testdata/port/room/artifact-identity-cli.json"

type artifactIdentityFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type artifactIdentityCase struct {
	Name               string                 `json:"name"`
	Args               []string               `json:"args"`
	Files              []artifactIdentityFile `json:"files,omitempty"`
	InitialJournal     []artifactIdentityFile `json:"initial_journal,omitempty"`
	GlobalConfig       string                 `json:"global_config,omitempty"`
	ProjectConfig      string                 `json:"project_config,omitempty"`
	DefaultIdentityEnv string                 `json:"default_identity_env,omitempty"`
	SymdeskMode        string                 `json:"symdesk_mode,omitempty"`
	SymdeskArgs        []string               `json:"symdesk_args,omitempty"`
	DynamicEvent       bool                   `json:"dynamic_event,omitempty"`
	ConfigError        bool                   `json:"config_error,omitempty"`
	ExitCode           int                    `json:"exit_code"`
	Stdout             string                 `json:"stdout"`
	Stderr             string                 `json:"stderr"`
	FinalFiles         []artifactIdentityFile `json:"final_files"`
}

type artifactIdentityContract struct {
	SchemaVersion  int                    `json:"schema_version"`
	OracleRevision string                 `json:"oracle_revision"`
	SourceHashes   map[string]string      `json:"source_hashes"`
	IdentityKey    string                 `json:"identity_key"`
	Cases          []artifactIdentityCase `json:"cases"`
}

type artifactIdentityVector struct {
	name          string
	args          []string
	files         []artifactIdentityFile
	linked        bool
	globalConfig  string
	projectConfig string
	defaultEnv    string
	symdeskMode   string
	configError   bool
}

// TestPortArtifactIdentityCLIContract freezes default-identity resolution and
// optional symdesk inspect enrichment through the shipped Go process.
func TestPortArtifactIdentityCLIContract(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("fixture uses a Unix fake symdesk process")
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := makeArtifactIdentityContract(t, root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, artifactIdentityContractPath)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", artifactIdentityContractPath)
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (set PORT_GENERATE=1 to create it)", artifactIdentityContractPath, err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("Go artifact identity fixture is stale; regenerate deliberately with PORT_GENERATE=1")
	}
}

func makeArtifactIdentityContract(t *testing.T, root string) (artifactIdentityContract, error) {
	t.Helper()
	seed := sha256.Sum256([]byte("symroom-artifact-cli-owner"))
	private := ed25519.NewKeyFromSeed(seed[:])
	owner := &identity.Identity{Name: "owner", MemberID: identity.ComputeMemberID(private.Public().(ed25519.PublicKey)), PublicKey: private.Public().(ed25519.PublicKey), PrivateKey: private}
	fixture := artifactIdentityContract{
		SchemaVersion:  1,
		OracleRevision: "e4773d8ebbabbc7ae66a6bae1ae2548cc26545c9",
		IdentityKey:    hex.EncodeToString(seed[:]),
		SourceHashes:   map[string]string{},
	}
	for _, source := range []string{
		"cmd/symroom/main.go", "cmd/symroom/cmd_artifact.go", "internal/room/artifact/artifact.go",
		"internal/room/desk/desk.go", "internal/room/config/config.go",
	} {
		data, err := os.ReadFile(filepath.Join(root, source))
		if err != nil {
			return fixture, err
		}
		sum := sha256.Sum256(data)
		fixture.SourceHashes[source] = hex.EncodeToString(sum[:])
	}
	goBinary := filepath.Join(t.TempDir(), "symroom-go-artifact-identity-oracle")
	build := exec.Command("go", "build", "-o", goBinary, "./cmd/symroom")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		return fixture, fmt.Errorf("build Go symroom artifact identity oracle: %w\n%s", err, output)
	}

	content := "artifact identity source\n"
	baseArgs := []string{"artifact", "link", "report.md"}
	vectors := []artifactIdentityVector{
		{name: "link-default-global", args: baseArgs, files: []artifactIdentityFile{{Name: "report.md", Content: content}}, globalConfig: "default_identity = \"owner\"\n"},
		{name: "link-default-project-over-global", args: baseArgs, files: []artifactIdentityFile{{Name: "report.md", Content: content}}, globalConfig: "default_identity = \"missing\"\n", projectConfig: "default_identity = \"owner\"\n"},
		{name: "link-default-env-over-global", args: baseArgs, files: []artifactIdentityFile{{Name: "report.md", Content: content}}, globalConfig: "default_identity = \"missing\"\n", defaultEnv: "owner"},
		{name: "link-explicit-identity-over-invalid-config", args: []string{"artifact", "link", "--identity", "owner", "report.md"}, files: []artifactIdentityFile{{Name: "report.md", Content: content}}, globalConfig: "[adapters.deploy]\ncommand = [\"echo\"]\n"},
		{name: "unlink-default-project", args: []string{"artifact", "unlink"}, files: []artifactIdentityFile{{Name: "report.md", Content: content}}, linked: true, projectConfig: "default_identity = \"owner\"\n"},
		{name: "link-default-missing", args: baseArgs, files: []artifactIdentityFile{{Name: "report.md", Content: content}}},
		{name: "link-default-invalid-config", args: baseArgs, files: []artifactIdentityFile{{Name: "report.md", Content: content}}, globalConfig: "[adapters.deploy]\ncommand = [\"echo\"]\n", configError: true},
		{name: "link-inspect-success", args: baseArgs, files: []artifactIdentityFile{{Name: "report.md", Content: content}}, globalConfig: "default_identity = \"owner\"\n", symdeskMode: "success"},
		{name: "link-inspect-nonzero-fallback", args: baseArgs, files: []artifactIdentityFile{{Name: "report.md", Content: content}}, globalConfig: "default_identity = \"owner\"\n", symdeskMode: "exit"},
		{name: "link-inspect-invalid-json-fallback", args: baseArgs, files: []artifactIdentityFile{{Name: "report.md", Content: content}}, globalConfig: "default_identity = \"owner\"\n", symdeskMode: "invalid"},
		{name: "link-inspect-timeout-fallback", args: baseArgs, files: []artifactIdentityFile{{Name: "report.md", Content: content}}, globalConfig: "default_identity = \"owner\"\n", symdeskMode: "hang"},
		{name: "link-inspect-missing-fallback", args: baseArgs, files: []artifactIdentityFile{{Name: "report.md", Content: content}}, globalConfig: "default_identity = \"owner\"\n"},
	}
	for _, vector := range vectors {
		caseDir := filepath.Join(t.TempDir(), vector.name)
		roomDir := filepath.Join(caseDir, "room")
		journalDir := filepath.Join(roomDir, "journal")
		if err := os.MkdirAll(roomDir, 0o700); err != nil {
			return fixture, err
		}
		for _, file := range vector.files {
			if err := os.WriteFile(filepath.Join(roomDir, filepath.FromSlash(file.Name)), []byte(file.Content), 0o600); err != nil {
				return fixture, err
			}
		}
		var initialJournal []artifactIdentityFile
		args := append([]string(nil), vector.args...)
		if vector.linked {
			initial, err := artifactCLIInitialJournal(t, owner, content)
			if err != nil {
				return fixture, err
			}
			initialJournal = make([]artifactIdentityFile, 0, len(initial))
			for _, file := range initial {
				initialJournal = append(initialJournal, artifactIdentityFile{Name: file.Name, Content: file.Content})
			}
			if err := os.MkdirAll(journalDir, 0o700); err != nil {
				return fixture, err
			}
			for _, file := range initialJournal {
				if err := os.WriteFile(filepath.Join(journalDir, file.Name), []byte(file.Content), 0o600); err != nil {
					return fixture, err
				}
			}
			var line struct {
				Body struct {
					ArtifactID string `json:"artifact_id"`
				} `json:"body"`
			}
			if err := json.Unmarshal([]byte(strings.Split(initialJournal[0].Content, "\n")[0]), &line); err != nil {
				return fixture, err
			}
			args = []string{"artifact", "unlink", line.Body.ArtifactID}
		}
		home, dataHome, tmp, pathDir := filepath.Join(caseDir, "home"), filepath.Join(caseDir, "data"), filepath.Join(caseDir, "tmp"), filepath.Join(caseDir, "path")
		for _, path := range []string{home, dataHome, tmp, pathDir} {
			if err := os.MkdirAll(path, 0o700); err != nil {
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
		argsFile := filepath.Join(caseDir, "symdesk-args.txt")
		if vector.symdeskMode != "" {
			script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$SYMDESK_ARGS_FILE\"\n"
			switch vector.symdeskMode {
			case "success":
				script += "printf '{\"document_id\":\"doc-fixture-1\",\"vault_name\":\"fixture\",\"valid\":true}\\n'\n"
			case "exit":
				script += "printf '{\"document_id\":\"ignored\"}\\n'\nexit 7\n"
			case "invalid":
				script += "printf 'not-json\\n'\n"
			case "hang":
				script += "exec /bin/sleep 60\n"
			}
			if err := os.WriteFile(filepath.Join(pathDir, "symdesk"), []byte(script), 0o755); err != nil {
				return fixture, err
			}
		}
		cmd := exec.Command(goBinary, args...)
		cmd.Dir = roomDir
		cmd.Env = []string{
			"HOME=" + home, "USERPROFILE=" + home, "XDG_DATA_HOME=" + dataHome, "TMPDIR=" + tmp,
			"TZ=UTC", "LC_ALL=C", "LANG=C", "PATH=" + pathDir,
			"SYMROOM_ROOM_DIR=.", "SYMROOM_IDENTITY_KEY=" + fixture.IdentityKey,
			"SYMDESK_ARGS_FILE=" + argsFile,
		}
		if vector.defaultEnv != "" {
			cmd.Env = append(cmd.Env, "SYMROOM_DEFAULT_IDENTITY="+vector.defaultEnv)
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		stdout, runErr := cmd.Output()
		code := 0
		if runErr != nil {
			if exitError, ok := runErr.(*exec.ExitError); ok {
				code = exitError.ExitCode()
			} else {
				return fixture, fmt.Errorf("run Go artifact identity case %s: %w", vector.name, runErr)
			}
		}
		finalFiles, err := artifactCLIReadFiles(roomDir)
		if err != nil {
			return fixture, err
		}
		finalIdentityFiles := make([]artifactIdentityFile, 0, len(finalFiles))
		for _, file := range finalFiles {
			finalIdentityFiles = append(finalIdentityFiles, artifactIdentityFile{Name: file.Name, Content: file.Content})
		}
		stderrText := stderr.String()
		if vector.configError {
			stderrText = strings.ReplaceAll(stderrText, filepath.Join(home, ".config", "symroom", "config.toml"), "<config-path>")
		}
		dynamicEvent := code == 0 && len(args) > 1 && (args[1] == "link" || args[1] == "unlink")
		output := string(stdout)
		if dynamicEvent {
			output = normalizeArtifactEventID(output)
		}
		result := artifactIdentityCase{
			Name: vector.name, Args: args, Files: vector.files, InitialJournal: initialJournal,
			GlobalConfig: vector.globalConfig, ProjectConfig: vector.projectConfig,
			DefaultIdentityEnv: vector.defaultEnv, SymdeskMode: vector.symdeskMode,
			DynamicEvent: dynamicEvent,
			ConfigError:  vector.configError, ExitCode: code, Stdout: output, Stderr: stderrText, FinalFiles: finalIdentityFiles,
		}
		if vector.symdeskMode != "" {
			data, err := os.ReadFile(argsFile)
			if err != nil {
				return fixture, err
			}
			result.SymdeskArgs = strings.Fields(string(data))
		}
		fixture.Cases = append(fixture.Cases, result)
	}
	return fixture, nil
}
