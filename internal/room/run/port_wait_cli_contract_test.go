package run

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/room/event"
	"github.com/danieljustus/symaira-desktop/internal/room/identity"
)

const runWaitCLIContractFixture = "testdata/port/room/run-wait-cli.json"

type runWaitCLIContract struct {
	SchemaVersion  int               `json:"schema_version"`
	OracleRevision string            `json:"oracle_revision"`
	SourceHashes   map[string]string `json:"source_hashes"`
	JournalFiles   []runJournalFile  `json:"journal_files"`
	Cases          []runWaitCLICase  `json:"cases"`
}

type runWaitCLICase struct {
	Name   string   `json:"name"`
	Room   string   `json:"room"`
	Args   []string `json:"args"`
	Code   int      `json:"exit_code"`
	Stdout string   `json:"stdout"`
	Stderr string   `json:"stderr"`
}

// TestPortRunWaitCLIContract freezes the real Go run wait command. Normal
// verification compares the committed fixture without rewriting it.
func TestPortRunWaitCLIContract(t *testing.T) {
	root := runCLIRoot(t)
	fixture, err := makeRunWaitCLIContract(t, root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, runWaitCLIContractFixture)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", runWaitCLIContractFixture)
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (set PORT_GENERATE=1 to create it)", runWaitCLIContractFixture, err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Go run wait fixture is stale; regenerate deliberately with PORT_GENERATE=1")
	}
}

func makeRunWaitCLIContract(t *testing.T, root string) (runWaitCLIContract, error) {
	t.Helper()
	alpha := runProjectionIdentity("wait-alpha")
	beta := runProjectionIdentity("wait-beta")
	identities := map[string]*identity.Identity{alpha.MemberID: alpha, beta.MemberID: beta}
	events := []*event.Event{
		projectionEvent("wait-request-approved", event.KindRunRequested, `{"run_id":"wait-approved","title":"Wait approved"}`, alpha.MemberID, "2026-04-01T10:00:00.000Z"),
		projectionEvent("wait-approve", event.KindRunApproved, `{"run_id":"wait-approved","approval_id":"wait-approval","scope":"room","expires_at":"2026-04-02T10:00:00Z"}`, beta.MemberID, "2026-04-01T10:00:01.000Z"),
		projectionEvent("wait-request-denied", event.KindRunRequested, `{"run_id":"wait-denied","title":"Wait denied"}`, alpha.MemberID, "2026-04-01T10:01:00.000Z"),
		projectionEvent("wait-deny", event.KindRunDenied, `{"run_id":"wait-denied","reason":"outside policy"}`, beta.MemberID, "2026-04-01T10:01:01.000Z"),
		projectionEvent("wait-request-cancelled", event.KindRunRequested, `{"run_id":"wait-cancelled","title":"Wait cancelled"}`, alpha.MemberID, "2026-04-01T10:02:00.000Z"),
		projectionEvent("wait-cancel", event.KindRunCancelled, `{"run_id":"wait-cancelled","reason":"no longer needed"}`, beta.MemberID, "2026-04-01T10:02:01.000Z"),
		projectionEvent("wait-request-pending", event.KindRunRequested, `{"run_id":"wait-pending","title":"Wait pending"}`, alpha.MemberID, "2026-04-01T10:03:00.000Z"),
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
			return runWaitCLIContract{}, err
		}
		line, err := ev.MarshalJSONLine()
		if err != nil {
			return runWaitCLIContract{}, err
		}
		contents[ev.Author] = append(contents[ev.Author], line...)
		prev[ev.Author] = journalLineHash(line)
	}

	fixture := runWaitCLIContract{
		SchemaVersion:  1,
		OracleRevision: "97280a946316682fc3ce3d7650597655ff0e46ae",
		SourceHashes: map[string]string{
			"cmd/symroom/main.go":              runCLIFileHash(t, root, "cmd/symroom/main.go"),
			"cmd/symroom/cmd_run.go":           runCLIFileHash(t, root, "cmd/symroom/cmd_run.go"),
			"internal/room/run/run.go":         runCLIFileHash(t, root, "internal/room/run/run.go"),
			"internal/room/run/wait.go":        runCLIFileHash(t, root, "internal/room/run/wait.go"),
			"internal/room/journal/journal.go": runCLIFileHash(t, root, "internal/room/journal/journal.go"),
		},
	}
	for author, content := range contents {
		fixture.JournalFiles = append(fixture.JournalFiles, runJournalFile{Name: author + ".jsonl", Content: string(content)})
	}
	sort.Slice(fixture.JournalFiles, func(i, j int) bool { return fixture.JournalFiles[i].Name < fixture.JournalFiles[j].Name })

	temp := t.TempDir()
	mainRoom := filepath.Join(temp, "main")
	journalDir := filepath.Join(mainRoom, "journal")
	if err := os.MkdirAll(journalDir, 0o700); err != nil {
		return runWaitCLIContract{}, err
	}
	for _, file := range fixture.JournalFiles {
		if err := os.WriteFile(filepath.Join(journalDir, file.Name), file.bytes(), 0o600); err != nil {
			return runWaitCLIContract{}, err
		}
	}
	badRoom := filepath.Join(temp, "bad-journal")
	if err := os.MkdirAll(badRoom, 0o700); err != nil {
		return runWaitCLIContract{}, err
	}
	if err := os.WriteFile(filepath.Join(badRoom, "journal"), []byte("not a directory"), 0o600); err != nil {
		return runWaitCLIContract{}, err
	}
	rooms := map[string]string{"main": mainRoom, "bad-journal": badRoom}
	executable := buildRunCLIOracle(t, root)
	for _, vector := range []struct {
		name string
		room string
		args []string
	}{
		{"approved-human", "main", []string{"run", "wait", "wait-approved"}},
		{"approved-json", "main", []string{"run", "wait", "--json", "wait-approved"}},
		{"approved-json-one", "main", []string{"run", "wait", "-json=1", "wait-approved"}},
		{"approved-json-false-alias", "main", []string{"run", "wait", "-json=f", "wait-approved"}},
		{"approved-before-immediate-timeout", "main", []string{"run", "wait", "--timeout=0s", "wait-approved"}},
		{"approved-flags-after-positional", "main", []string{"run", "wait", "wait-approved", "--timeout=0s", "--json"}},
		{"denied", "main", []string{"run", "wait", "wait-denied"}},
		{"cancelled", "main", []string{"run", "wait", "wait-cancelled"}},
		{"pending-timeout-equals", "main", []string{"run", "wait", "--timeout=0s", "wait-pending"}},
		{"pending-timeout-fractional", "main", []string{"run", "wait", "--timeout=0.000001s", "wait-pending"}},
		{"missing-timeout-separated", "main", []string{"run", "wait", "-timeout", "0s", "missing-run"}},
		{"bad-journal-timeout", "bad-journal", []string{"run", "wait", "--timeout=0s", "missing-run"}},
		{"negative-timeout", "main", []string{"run", "wait", "-timeout=-1ms", "wait-pending"}},
		{"missing-run-id", "main", []string{"run", "wait"}},
		{"invalid-timeout", "main", []string{"run", "wait", "--timeout=bad", "wait-pending"}},
		{"invalid-json-bool", "main", []string{"run", "wait", "--json=maybe", "wait-approved"}},
		{"missing-timeout-value", "main", []string{"run", "wait", "--timeout"}},
		{"wait-help", "main", []string{"run", "wait", "-h"}},
		{"unknown-flag", "main", []string{"run", "wait", "--unknown"}},
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
		stdout, err := cmd.Output()
		code := 0
		if err != nil {
			if exitError, ok := err.(*exec.ExitError); ok {
				code = exitError.ExitCode()
			} else {
				return runWaitCLIContract{}, fmt.Errorf("run Go oracle case %s: %w", vector.name, err)
			}
		}
		fixture.Cases = append(fixture.Cases, runWaitCLICase{
			Name: vector.name, Room: vector.room, Args: vector.args,
			Code: code, Stdout: string(stdout), Stderr: stderr.String(),
		})
	}
	return fixture, nil
}
