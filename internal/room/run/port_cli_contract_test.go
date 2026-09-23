package run

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/room/event"
	"github.com/danieljustus/symaira-desktop/internal/room/identity"
)

const runCLIContractFixture = "testdata/port/room/run-cli.json"

type runCLIContract struct {
	SchemaVersion  int               `json:"schema_version"`
	OracleRevision string            `json:"oracle_revision"`
	SourceHashes   map[string]string `json:"source_hashes"`
	JournalFiles   []runJournalFile  `json:"journal_files"`
	Cases          []runCLICase      `json:"cases"`
}

type runCLICase struct {
	Name   string   `json:"name"`
	Room   string   `json:"room"`
	Args   []string `json:"args"`
	Code   int      `json:"exit_code"`
	Stdout string   `json:"stdout"`
	Stderr string   `json:"stderr"`
}

// TestPortRunCLIContract records actual Go symroom run list/show process behavior.
// Generation is deliberate; normal verification only compares fixture bytes.
func TestPortRunCLIContract(t *testing.T) {
	root := runCLIRoot(t)
	fixture, err := makeRunCLIContract(t, root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, runCLIContractFixture)
	if os.Getenv("PORT_GENERATE") == "1" || os.Getenv("ROOM_CLI_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", runCLIContractFixture)
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (set PORT_GENERATE=1 to create it)", runCLIContractFixture, err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Go run CLI fixture is stale; regenerate deliberately with PORT_GENERATE=1")
	}
}

func makeRunCLIContract(t *testing.T, root string) (runCLIContract, error) {
	t.Helper()
	alpha := runProjectionIdentity("cli-alpha")
	beta := runProjectionIdentity("cli-beta")
	identities := map[string]*identity.Identity{alpha.MemberID: alpha, beta.MemberID: beta}
	events := []*event.Event{
		projectionEvent("cli-request-finished", event.KindRunRequested, `{"run_id":"cli-finished","title":"CLI <Finished>&","plan_file":"plans/final.md","adapter":"local"}`, alpha.MemberID, "2026-03-01T10:00:00.000Z"),
		projectionEvent("cli-finish", event.KindRunFinished, `{"run_id":"cli-finished","summary":"done"}`, beta.MemberID, "2026-03-01T10:01:00.000Z"),
		projectionEvent("cli-checkpoint-request", event.KindCheckpointReq, `{"checkpoint_id":"cli-checkpoint","run_id":"cli-finished","question":"Review output?"}`, alpha.MemberID, "2026-03-01T10:02:00.000Z"),
		projectionEvent("cli-checkpoint-resolve", event.KindCheckpointResolved, `{"checkpoint_id":"cli-checkpoint","answer":"approved"}`, beta.MemberID, "2026-03-01T10:03:00.000Z"),
		projectionEvent("cli-request-pending", event.KindRunRequested, `{"run_id":"cli-pending","title":"CLI pending"}`, alpha.MemberID, "2026-03-01T11:00:00.000Z"),
		projectionEvent("cli-approve", event.KindRunApproved, `{"run_id":"cli-pending","approval_id":"approval-cli","scope":"room","expires_at":"2026-03-02T00:00:00Z"}`, beta.MemberID, "2026-03-01T11:01:00.000Z"),
	}
	seq := make(map[string]uint64)
	prev := make(map[string]string)
	contents := make(map[string][]byte)
	for index, ev := range events {
		seq[ev.Author]++
		ev.Seq = seq[ev.Author]
		ev.Prev = prev[ev.Author]
		if ev.Prev == "" {
			ev.Prev = "sha256:0000000000000000000000000000000000000000000000000000000000000000"
		}
		ev.Lamport = uint64(index + 1)
		if err := ev.Sign(identities[ev.Author]); err != nil {
			return runCLIContract{}, err
		}
		line, err := ev.MarshalJSONLine()
		if err != nil {
			return runCLIContract{}, err
		}
		contents[ev.Author] = append(contents[ev.Author], line...)
		prev[ev.Author] = journalLineHash(line)
	}

	fixture := runCLIContract{
		SchemaVersion: 1, OracleRevision: "97280a946316682fc3ce3d7650597655ff0e46ae",
		SourceHashes: map[string]string{
			"cmd/symroom/main.go":              runCLIFileHash(t, root, "cmd/symroom/main.go"),
			"cmd/symroom/cmd_run.go":           runCLIFileHash(t, root, "cmd/symroom/cmd_run.go"),
			"internal/room/run/run.go":         runCLIFileHash(t, root, "internal/room/run/run.go"),
			"internal/room/journal/journal.go": runCLIFileHash(t, root, "internal/room/journal/journal.go"),
		},
	}
	for author, content := range contents {
		name := author + ".jsonl"
		fixture.JournalFiles = append(fixture.JournalFiles, runJournalFile{Name: name, Content: string(content)})
	}
	sortRunJournalFiles(fixture.JournalFiles)

	temp := t.TempDir()
	executable := buildRunCLIOracle(t, root)
	mainRoom := filepath.Join(temp, "main")
	journalDir := filepath.Join(mainRoom, "journal")
	if err := os.MkdirAll(journalDir, 0o700); err != nil {
		return runCLIContract{}, err
	}
	for _, file := range fixture.JournalFiles {
		if err := os.WriteFile(filepath.Join(journalDir, file.Name), file.bytes(), 0o600); err != nil {
			return runCLIContract{}, err
		}
	}
	rooms := map[string]string{"main": mainRoom, "empty": filepath.Join(temp, "empty")}
	for _, vector := range []struct {
		name string
		room string
		args []string
	}{
		{"list-human", "main", []string{"run", "list"}},
		{"list-json", "main", []string{"run", "list", "--json"}},
		{"list-pending-json", "main", []string{"run", "list", "--pending", "--json"}},
		{"list-json-bool-one", "main", []string{"run", "list", "-json=1"}},
		{"list-json-bool-uppercase-false", "main", []string{"run", "list", "--json=FALSE"}},
		{"list-pending-bool-alias", "main", []string{"run", "list", "-pending=T", "-json=t"}},
		{"list-pending-bool-false", "main", []string{"run", "list", "-pending=f", "-json=true"}},
		{"list-json-flag-after-positional", "main", []string{"run", "list", "ignored", "--json"}},
		{"list-terminator", "main", []string{"run", "list", "--", "--json"}},
		{"list-help", "main", []string{"run", "list", "-h"}},
		{"list-invalid-bool", "main", []string{"run", "list", "--json=maybe"}},
		{"list-empty-json", "empty", []string{"run", "list", "--json"}},
		{"show-human", "main", []string{"run", "show", "cli-finished"}},
		{"show-json", "main", []string{"run", "show", "--json", "cli-finished"}},
		{"show-json-bool-true", "main", []string{"run", "show", "-json=TRUE", "cli-finished"}},
		{"show-json-bool-zero", "main", []string{"run", "show", "--json=0", "cli-finished"}},
		{"show-json-bool-false-alias", "main", []string{"run", "show", "-json=F", "cli-finished"}},
		{"show-invalid-bool", "main", []string{"run", "show", "-json=maybe", "cli-finished"}},
		{"show-flag-after-positional", "main", []string{"run", "show", "cli-finished", "--json"}},
		{"show-terminator", "main", []string{"run", "show", "--", "cli-finished", "--json"}},
		{"show-help", "main", []string{"run", "show", "--help"}},
		{"show-not-found", "main", []string{"run", "show", "missing"}},
		{"show-usage", "main", []string{"run", "show"}},
		{"list-unknown-flag", "main", []string{"run", "list", "--unknown"}},
	} {
		cmd := exec.Command(executable, vector.args...)
		caseEnv := filepath.Join(temp, "env-"+vector.name)
		home, dataHome, tempDir := makeRunCLIEnv(t, caseEnv)
		cmd.Env = []string{
			"HOME=" + home, "XDG_DATA_HOME=" + dataHome, "TMPDIR=" + tempDir,
			"TZ=UTC", "LC_ALL=C", "LANG=C", "SYMROOM_ROOM_DIR=" + rooms[vector.room],
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		output, err := cmd.Output()
		code := 0
		if err != nil {
			if exitError, ok := err.(*exec.ExitError); ok {
				code = exitError.ExitCode()
			} else {
				return runCLIContract{}, fmt.Errorf("run Go oracle case %s: %w", vector.name, err)
			}
		}
		fixture.Cases = append(fixture.Cases, runCLICase{
			Name: vector.name, Room: vector.room, Args: vector.args,
			Code: code, Stdout: string(output), Stderr: stderr.String(),
		})
	}
	return fixture, nil
}

func makeRunCLIEnv(t *testing.T, root string) (home, dataHome, tempDir string) {
	t.Helper()
	home = filepath.Join(root, "home")
	dataHome = filepath.Join(root, "data")
	tempDir = filepath.Join(root, "tmp")
	for _, path := range []string{home, dataHome, tempDir} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	return home, dataHome, tempDir
}

func buildRunCLIOracle(t *testing.T, root string) string {
	t.Helper()
	executable := filepath.Join(t.TempDir(), "symroom-go")
	build := exec.Command("go", "build", "-o", executable, "./cmd/symroom")
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Go symroom oracle: %v\n%s", err, output)
	}
	return executable
}

func runCLIRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve Go CLI contract test path")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(filename), "..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func runCLIFileHash(t *testing.T, root, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, path))
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}

func journalLineHash(line []byte) string {
	trimmed := bytes.TrimSuffix(line, []byte{'\n'})
	digest := sha256.Sum256(trimmed)
	return "sha256:" + hex.EncodeToString(digest[:])
}

func sortRunJournalFiles(files []runJournalFile) {
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
}
