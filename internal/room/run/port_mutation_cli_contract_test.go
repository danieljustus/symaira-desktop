package run

import (
	"bytes"
	"crypto/ed25519"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/room/event"
	"github.com/danieljustus/symaira-desktop/internal/room/identity"
)

const runMutationCLIContractFixture = "testdata/port/room/run-mutations-cli.json"

type runMutationCLIContract struct {
	SchemaVersion  int                  `json:"schema_version"`
	OracleRevision string               `json:"oracle_revision"`
	SourceHashes   map[string]string    `json:"source_hashes"`
	IdentityKey    string               `json:"identity_key"`
	IdentityMember string               `json:"identity_member"`
	JournalFiles   []runJournalFile     `json:"journal_files"`
	Cases          []runMutationCLICase `json:"cases"`
}

type runMutationCLICase struct {
	Name              string           `json:"name"`
	Args              []string         `json:"args"`
	ExitCode          int              `json:"exit_code"`
	Stdout            string           `json:"stdout"`
	Stderr            string           `json:"stderr"`
	FinalJournalFiles []runJournalFile `json:"final_journal_files"`
}

// TestPortRunMutationCLIContract records real Go CLI output and signed journal
// effects. Generation is explicit; normal verification only reads fixture data.
func TestPortRunMutationCLIContract(t *testing.T) {
	root := runCLIRoot(t)
	fixture, err := makeRunMutationCLIContract(t, root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, runMutationCLIContractFixture)
	if os.Getenv("PORT_GENERATE") == "1" || os.Getenv("ROOM_CLI_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", runMutationCLIContractFixture)
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (set PORT_GENERATE=1 to create it)", runMutationCLIContractFixture, err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Go run mutation CLI fixture is stale; regenerate deliberately with PORT_GENERATE=1")
	}
}

func makeRunMutationCLIContract(t *testing.T, root string) (runMutationCLIContract, error) {
	t.Helper()
	signer := runProjectionIdentity("mutations-oracle")
	roomOwner := runProjectionIdentity("mutations-room")
	fixture := runMutationCLIContract{
		SchemaVersion:  1,
		OracleRevision: "a9f42980e4695e20b2c948d7f17fe67734eff901",
		IdentityKey:    hex.EncodeToString(signer.PrivateKey[:ed25519.SeedSize]),
		IdentityMember: signer.MemberID,
		SourceHashes: map[string]string{
			"cmd/symroom/main.go":                runCLIFileHash(t, root, "cmd/symroom/main.go"),
			"cmd/symroom/cmd_run.go":             runCLIFileHash(t, root, "cmd/symroom/cmd_run.go"),
			"internal/room/run/run.go":           runCLIFileHash(t, root, "internal/room/run/run.go"),
			"internal/room/journal/journal.go":   runCLIFileHash(t, root, "internal/room/journal/journal.go"),
			"internal/room/event/event.go":       runCLIFileHash(t, root, "internal/room/event/event.go"),
			"internal/room/identity/identity.go": runCLIFileHash(t, root, "internal/room/identity/identity.go"),
		},
	}
	initial, err := runMutationJournal(t, roomOwner)
	if err != nil {
		return runMutationCLIContract{}, err
	}
	fixture.JournalFiles = initial

	temp := t.TempDir()
	executable := buildRunCLIOracle(t, root)
	for _, vector := range []struct {
		name string
		args []string
	}{
		{"request-html-body", []string{"run", "request", "--title=Generated <A>&", "--plan-file", "plans/a.md", "--adapter=local", "--identity=oracle"}},
		{"request-title-with-empty-optional-fields", []string{"run", "request", "-identity", "oracle", "-title", "No optional fields"}},
		{"request-title-required", []string{"run", "request", "--identity", "oracle"}},
		{"request-identity-required", []string{"run", "request", "--title", "No identity"}},
		{"request-string-flag-consumes-dash", []string{"run", "request", "--title", "--identity"}},
		{"start-approved", []string{"run", "start", "--identity", "oracle", "mut-approved"}},
		{"start-invalid-transition", []string{"run", "start", "--identity=oracle", "mut-pending"}},
		{"start-expired-approval", []string{"run", "start", "--identity", "oracle", "mut-expired"}},
		{"start-not-found", []string{"run", "start", "--identity", "oracle", "mut-missing"}},
		{"start-usage", []string{"run", "start"}},
		{"start-identity-after-run-id", []string{"run", "start", "mut-approved", "--identity", "oracle"}},
		{"cancel-active", []string{"run", "cancel", "--reason", "operator stopped", "--identity", "oracle", "mut-pending"}},
		{"cancel-empty-reason", []string{"run", "cancel", "--identity=oracle", "mut-approved"}},
		{"cancel-terminal", []string{"run", "cancel", "--reason=too late", "--identity", "oracle", "mut-finished"}},
		{"cancel-not-found", []string{"run", "cancel", "--identity", "oracle", "mut-missing"}},
		{"cancel-usage", []string{"run", "cancel"}},
	} {
		caseRoom := filepath.Join(temp, vector.name)
		journalDir := filepath.Join(caseRoom, "journal")
		if err := os.MkdirAll(journalDir, 0o700); err != nil {
			return runMutationCLIContract{}, err
		}
		for _, file := range initial {
			if err := os.WriteFile(filepath.Join(journalDir, file.Name), file.bytes(), 0o600); err != nil {
				return runMutationCLIContract{}, err
			}
		}
		caseEnv := filepath.Join(temp, "env-"+vector.name)
		home, dataHome, tempDir := makeRunCLIEnv(t, caseEnv)
		cmd := exec.Command(executable, vector.args...)
		cmd.Env = []string{
			"HOME=" + home, "XDG_DATA_HOME=" + dataHome, "TMPDIR=" + tempDir,
			"TZ=UTC", "LC_ALL=C", "LANG=C", "SYMROOM_ROOM_DIR=" + caseRoom,
			"SYMROOM_IDENTITY_KEY=" + fixture.IdentityKey,
		}
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		stdout, err := cmd.Output()
		code := 0
		if err != nil {
			if exitError, ok := err.(*exec.ExitError); ok {
				code = exitError.ExitCode()
			} else {
				return runMutationCLIContract{}, fmt.Errorf("run Go mutation case %s: %w", vector.name, err)
			}
		}
		finalFiles, err := readRunMutationJournal(t, journalDir, signer.MemberID)
		if err != nil {
			return runMutationCLIContract{}, err
		}
		fixture.Cases = append(fixture.Cases, runMutationCLICase{
			Name: vector.name, Args: vector.args, ExitCode: code,
			Stdout: string(stdout), Stderr: stderr.String(), FinalJournalFiles: finalFiles,
		})
	}
	return fixture, nil
}

func runMutationJournal(t *testing.T, owner *identity.Identity) ([]runJournalFile, error) {
	t.Helper()
	events := []*event.Event{
		projectionEvent("mut-request-approved", event.KindRunRequested, `{"run_id":"mut-approved","title":"Approved"}`, owner.MemberID, "2026-03-01T10:00:00.000Z"),
		projectionEvent("mut-approve", event.KindRunApproved, `{"run_id":"mut-approved","approval_id":"ap-1","scope":"room","expires_at":"2099-01-01T00:00:00Z"}`, owner.MemberID, "2026-03-01T10:01:00.000Z"),
		projectionEvent("mut-request-pending", event.KindRunRequested, `{"run_id":"mut-pending","title":"Pending"}`, owner.MemberID, "2026-03-01T10:02:00.000Z"),
		projectionEvent("mut-request-finished", event.KindRunRequested, `{"run_id":"mut-finished","title":"Finished"}`, owner.MemberID, "2026-03-01T10:03:00.000Z"),
		projectionEvent("mut-finish", event.KindRunFinished, `{"run_id":"mut-finished","summary":"done"}`, owner.MemberID, "2026-03-01T10:04:00.000Z"),
		projectionEvent("mut-request-expired", event.KindRunRequested, `{"run_id":"mut-expired","title":"Expired"}`, owner.MemberID, "2026-03-01T10:05:00.000Z"),
		projectionEvent("mut-expire", event.KindRunApproved, `{"run_id":"mut-expired","approval_id":"ap-expired","scope":"room","expires_at":"2000-01-01T00:00:00Z"}`, owner.MemberID, "2026-03-01T10:06:00.000Z"),
	}
	seq := uint64(0)
	prev := "sha256:0000000000000000000000000000000000000000000000000000000000000000"
	var content []byte
	for index, ev := range events {
		seq++
		ev.Seq = seq
		ev.Prev = prev
		ev.Lamport = uint64(index + 1)
		if err := ev.Sign(owner); err != nil {
			return nil, err
		}
		line, err := ev.MarshalJSONLine()
		if err != nil {
			return nil, err
		}
		content = append(content, line...)
		prev = journalLineHash(line)
	}
	return []runJournalFile{{Name: owner.MemberID + ".jsonl", Content: string(content)}}, nil
}

func readRunMutationJournal(t *testing.T, dir, dynamicAuthor string) ([]runJournalFile, error) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := make([]runJournalFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			return nil, err
		}
		if entry.Name() == dynamicAuthor+".jsonl" {
			data, err = normalizeGeneratedRunLines(data)
			if err != nil {
				return nil, err
			}
		}
		files = append(files, runJournalFile{Name: entry.Name(), Content: string(data)})
	}
	sortRunJournalFiles(files)
	return files, nil
}

func normalizeGeneratedRunLines(data []byte) ([]byte, error) {
	var output []byte
	for _, line := range bytes.SplitAfter(data, []byte{'\n'}) {
		if len(line) == 0 {
			continue
		}
		trimmed := bytes.TrimSuffix(line, []byte{'\n'})
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(trimmed, &fields); err != nil {
			return nil, err
		}
		fields["ts"] = json.RawMessage(`"<dynamic-clock>"`)
		fields["sig"] = json.RawMessage(`"<signature-of-dynamic-clock>"`)
		canonical, err := json.Marshal(fields)
		if err != nil {
			return nil, err
		}
		output = append(output, canonical...)
		output = append(output, '\n')
	}
	return output, nil
}
