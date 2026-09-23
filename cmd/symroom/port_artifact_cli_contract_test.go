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

	"github.com/danieljustus/symaira-desktop/internal/room/artifact"
	"github.com/danieljustus/symaira-desktop/internal/room/identity"
)

const artifactCLIContractPath = "testdata/port/room/artifact-cli.json"

type artifactCLIContract struct {
	SchemaVersion  int               `json:"schema_version"`
	OracleRevision string            `json:"oracle_revision"`
	SourceHashes   map[string]string `json:"source_hashes"`
	IdentityKey    string            `json:"identity_key"`
	Cases          []artifactCLICase `json:"cases"`
}

type artifactCLIFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type artifactCLICase struct {
	Name           string            `json:"name"`
	Args           []string          `json:"args"`
	Files          []artifactCLIFile `json:"files,omitempty"`
	InitialJournal []artifactCLIFile `json:"initial_journal,omitempty"`
	DynamicEvent   bool              `json:"dynamic_event,omitempty"`
	ExitCode       int               `json:"exit_code"`
	Stdout         string            `json:"stdout"`
	Stderr         string            `json:"stderr"`
	FinalFiles     []artifactCLIFile `json:"final_files"`
}

type artifactCLIVector struct {
	name         string
	args         []string
	files        []artifactCLIFile
	linked       bool
	dynamicEvent bool
}

// TestPortArtifactCLIContract captures the shipped Go command's process output
// and resulting room filesystem. PORT_GENERATE=1 deliberately writes it.
func TestPortArtifactCLIContract(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	fixture, err := makeArtifactCLIContract(t, root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, artifactCLIContractPath)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", artifactCLIContractPath)
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (set PORT_GENERATE=1 to create it)", artifactCLIContractPath, err)
	}
	if !bytes.Equal(got, data) {
		t.Fatal("Go artifact CLI fixture is stale; regenerate deliberately with PORT_GENERATE=1")
	}
}

func makeArtifactCLIContract(t *testing.T, root string) (artifactCLIContract, error) {
	t.Helper()
	seed := sha256.Sum256([]byte("symroom-artifact-cli-owner"))
	private := ed25519.NewKeyFromSeed(seed[:])
	owner := &identity.Identity{Name: "owner", MemberID: identity.ComputeMemberID(private.Public().(ed25519.PublicKey)), PublicKey: private.Public().(ed25519.PublicKey), PrivateKey: private}
	fixture := artifactCLIContract{
		SchemaVersion:  1,
		OracleRevision: "32a4f9739fedb8aadbe282f59f5e7d4d60956b92",
		IdentityKey:    hex.EncodeToString(seed[:]),
		SourceHashes: map[string]string{
			"cmd/symroom/main.go":                artifactCLIFileHash(t, root, "cmd/symroom/main.go"),
			"cmd/symroom/cmd_artifact.go":        artifactCLIFileHash(t, root, "cmd/symroom/cmd_artifact.go"),
			"internal/room/artifact/artifact.go": artifactCLIFileHash(t, root, "internal/room/artifact/artifact.go"),
		},
	}
	goBinary := filepath.Join(t.TempDir(), "symroom-go-artifact-oracle")
	build := exec.Command("go", "build", "-o", goBinary, "./cmd/symroom")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		return fixture, fmt.Errorf("build Go symroom artifact oracle: %w\n%s", err, output)
	}
	content := "artifact source\n"
	vectors := []artifactCLIVector{
		{name: "usage", args: []string{"artifact"}},
		{name: "unknown-action", args: []string{"artifact", "bogus"}},
		{name: "action-help-is-unknown", args: []string{"artifact", "--help"}},
		{name: "link-usage", args: []string{"artifact", "link"}},
		{name: "link-help", args: []string{"artifact", "link", "--help"}},
		{name: "link-unknown-flag", args: []string{"artifact", "link", "--identity", "owner", "--bogus"}},
		{name: "link-success-title-and-identity", args: []string{"artifact", "link", "--identity", "owner", "--title", "Quarterly report", "report.md"}, files: []artifactCLIFile{{Name: "report.md", Content: content}}, dynamicEvent: true},
		{name: "link-default-title", args: []string{"artifact", "link", "--identity", "owner", "report.md"}, files: []artifactCLIFile{{Name: "report.md", Content: content}}, dynamicEvent: true},
		{name: "link-outside-root", args: []string{"artifact", "link", "--identity", "owner", "../outside.md"}},
		{name: "unlink-usage", args: []string{"artifact", "unlink"}},
		{name: "unlink-success", args: []string{"artifact", "unlink", "--identity", "owner", "<artifact-id>"}, files: []artifactCLIFile{{Name: "report.md", Content: content}}, linked: true, dynamicEvent: true},
		{name: "list-empty-human", args: []string{"artifact", "list"}},
		{name: "list-empty-json", args: []string{"artifact", "list", "--json"}},
		{name: "list-help", args: []string{"artifact", "list", "--help"}},
		{name: "list-invalid-bool", args: []string{"artifact", "list", "--json=maybe"}},
		{name: "list-ok-human", args: []string{"artifact", "list"}, files: []artifactCLIFile{{Name: "report.md", Content: content}}, linked: true},
		{name: "list-ok-json", args: []string{"artifact", "list", "--json=true"}, files: []artifactCLIFile{{Name: "report.md", Content: content}}, linked: true},
		{name: "list-modified", args: []string{"artifact", "list"}, files: []artifactCLIFile{{Name: "report.md", Content: "changed\n"}}, linked: true},
		{name: "list-missing", args: []string{"artifact", "list", "--json"}, linked: true},
	}
	for _, vector := range vectors {
		caseDir := filepath.Join(t.TempDir(), vector.name)
		roomDir := filepath.Join(caseDir, "room")
		journalDir := filepath.Join(roomDir, "journal")
		if err := os.MkdirAll(roomDir, 0o700); err != nil {
			return fixture, err
		}
		for _, file := range vector.files {
			path := filepath.Join(roomDir, filepath.FromSlash(file.Name))
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				return fixture, err
			}
			if err := os.WriteFile(path, []byte(file.Content), 0o600); err != nil {
				return fixture, err
			}
		}
		var initialJournal []artifactCLIFile
		if vector.linked {
			initialFile, err := artifactCLIInitialJournal(t, owner, content)
			if err != nil {
				return fixture, err
			}
			initialJournal = initialFile
			if err := os.MkdirAll(journalDir, 0o700); err != nil {
				return fixture, err
			}
			for _, file := range initialJournal {
				if err := os.WriteFile(filepath.Join(journalDir, file.Name), []byte(file.Content), 0o600); err != nil {
					return fixture, err
				}
			}
		}
		args := append([]string(nil), vector.args...)
		if vector.name == "unlink-success" {
			var linkBody struct {
				ArtifactID string `json:"artifact_id"`
			}
			var line map[string]json.RawMessage
			if err := json.Unmarshal([]byte(strings.Split(initialJournal[0].Content, "\n")[0]), &line); err != nil {
				return fixture, err
			}
			if err := json.Unmarshal(line["body"], &linkBody); err != nil {
				return fixture, err
			}
			args[len(args)-1] = linkBody.ArtifactID
		}
		home, tmp := filepath.Join(caseDir, "home"), filepath.Join(caseDir, "tmp")
		for _, path := range []string{home, tmp} {
			if err := os.MkdirAll(path, 0o700); err != nil {
				return fixture, err
			}
		}
		cmd := exec.Command(goBinary, args...)
		cmd.Dir = roomDir
		cmd.Env = []string{"HOME=" + home, "USERPROFILE=" + home, "TMPDIR=" + tmp, "TZ=UTC", "LC_ALL=C", "LANG=C", "PATH=" + tmp, "SYMROOM_ROOM_DIR=.", "SYMROOM_IDENTITY_KEY=" + fixture.IdentityKey}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		stdout, runErr := cmd.Output()
		code := 0
		if runErr != nil {
			if exitError, ok := runErr.(*exec.ExitError); ok {
				code = exitError.ExitCode()
			} else {
				return fixture, fmt.Errorf("run Go artifact case %s: %w", vector.name, runErr)
			}
		}
		finalFiles, err := artifactCLIReadFiles(roomDir)
		if err != nil {
			return fixture, err
		}
		output := string(stdout)
		if vector.dynamicEvent {
			output = normalizeArtifactEventID(output)
		}
		fixture.Cases = append(fixture.Cases, artifactCLICase{Name: vector.name, Args: args, Files: vector.files, InitialJournal: initialJournal, DynamicEvent: vector.dynamicEvent, ExitCode: code, Stdout: output, Stderr: stderr.String(), FinalFiles: finalFiles})
	}
	return fixture, nil
}

func artifactCLIInitialJournal(t *testing.T, owner *identity.Identity, content string) ([]artifactCLIFile, error) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "report.md")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return nil, err
	}
	if _, err := artifact.Link(root, "", path, "Quarterly report", owner); err != nil {
		return nil, err
	}
	return artifactCLIReadFiles(filepath.Join(root, "journal"))
}

func artifactCLIReadFiles(root string) ([]artifactCLIFile, error) {
	files := []artifactCLIFile{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		name, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if strings.HasSuffix(name, ".jsonl") {
			data, err = normalizeArtifactJournal(data)
			if err != nil {
				return err
			}
		}
		files = append(files, artifactCLIFile{Name: filepath.ToSlash(name), Content: string(data)})
		return nil
	})
	if os.IsNotExist(err) {
		return files, nil
	}
	if err != nil {
		return nil, err
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}

func normalizeArtifactJournal(data []byte) ([]byte, error) {
	lines := bytes.Split(data, []byte{'\n'})
	for i, line := range lines {
		if len(line) == 0 {
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

func normalizeArtifactEventID(output string) string {
	if index := strings.Index(output, "ev_"); index >= 0 && len(output) >= index+19 {
		return output[:index] + "<event-id>" + output[index+19:]
	}
	return output
}

func artifactCLIFileHash(t *testing.T, root, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
