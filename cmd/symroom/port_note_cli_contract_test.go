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

	"github.com/danieljustus/symaira-desktop/internal/room/event"
	"github.com/danieljustus/symaira-desktop/internal/room/identity"
)

const noteCLIContractPath = "testdata/port/room/note-cli.json"

type noteCLIContract struct {
	SchemaVersion        int               `json:"schema_version"`
	OracleRevision       string            `json:"oracle_revision"`
	SourceHashes         map[string]string `json:"source_hashes"`
	IdentityKey          string            `json:"identity_key"`
	IdentityMember       string            `json:"identity_member"`
	RoomTOML             string            `json:"room_toml"`
	JournalFiles         []noteJournalFile `json:"journal_files"`
	ObserverJournalFiles []noteJournalFile `json:"observer_journal_files"`
	Cases                []noteCLICase     `json:"cases"`
}

type noteJournalFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type noteCLICase struct {
	Name              string            `json:"name"`
	Args              []string          `json:"args"`
	Observer          bool              `json:"observer,omitempty"`
	Mutates           bool              `json:"mutates,omitempty"`
	JSONOutput        bool              `json:"json_output,omitempty"`
	ExitCode          int               `json:"exit_code"`
	Stdout            string            `json:"stdout"`
	Stderr            string            `json:"stderr"`
	FinalJournalFiles []noteJournalFile `json:"final_journal_files"`
}

// TestPortNoteCLIContract records Go process output and note journal writes.
// Fixture generation is explicit; the normal test is read-only.
func TestPortNoteCLIContract(t *testing.T) {
	root := noteCLIRoot(t)
	fixture, err := makeNoteCLIContract(t, root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, noteCLIContractPath)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // generated contract fixture is intentionally world-readable
			t.Fatal(err)
		}
		t.Logf("wrote %s", noteCLIContractPath)
		return
	}
	got, err := os.ReadFile(path) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
	if err != nil {
		t.Fatalf("read %s: %v (set PORT_GENERATE=1 to create it)", noteCLIContractPath, err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Go note CLI fixture is stale (first differing field: %s); regenerate deliberately with PORT_GENERATE=1", noteCLIContractDifference(got, data))
	}
}

func noteCLIContractDifference(got, want []byte) string {
	var stored, current map[string]json.RawMessage
	if json.Unmarshal(got, &stored) != nil || json.Unmarshal(want, &current) != nil {
		return "invalid JSON"
	}
	keys := make([]string, 0, len(current))
	for key := range current {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		if bytes.Equal(stored[key], current[key]) {
			continue
		}
		if key == "cases" {
			var oldCases, newCases []map[string]json.RawMessage
			if json.Unmarshal(stored[key], &oldCases) == nil && json.Unmarshal(current[key], &newCases) == nil {
				for i, newCase := range newCases {
					if i >= len(oldCases) {
						break
					}
					for field, value := range newCase {
						if !bytes.Equal(oldCases[i][field], value) {
							return fmt.Sprintf("cases[%d].%s", i, field)
						}
					}
				}
			}
		}
		return key
	}
	return "JSON formatting"
}

func TestNoteCLIContractDifference(t *testing.T) {
	got := []byte(`{"cases":[{"stdout":"old"}]}`)
	want := []byte(`{"cases":[{"stdout":"new"}]}`)
	if actual := noteCLIContractDifference(got, want); actual != "cases[0].stdout" {
		t.Fatalf("differing field: %s", actual)
	}
}

func makeNoteCLIContract(t *testing.T, root string) (noteCLIContract, error) {
	t.Helper()
	seed := sha256.Sum256([]byte("symroom-note-cli-fixture-identity"))
	privateKey := ed25519.NewKeyFromSeed(seed[:])
	publicKey := privateKey.Public().(ed25519.PublicKey)
	signer := &identity.Identity{
		Name: "note-fixture", MemberID: identity.ComputeMemberID(publicKey),
		PublicKey: publicKey, PrivateKey: privateKey,
	}
	fixture := noteCLIContract{
		SchemaVersion:  1,
		OracleRevision: "f2f139bd6b7182d116e02369becba332da52484b",
		IdentityKey:    hex.EncodeToString(seed[:]),
		IdentityMember: signer.MemberID,
		RoomTOML:       "id = \"rm_note_fixture\"\ncreated = \"2026-09-23T10:00:00.000Z\"\n",
		SourceHashes: map[string]string{
			"cmd/symroom/main.go":                  noteCLIFileHash(t, root, "cmd/symroom/main.go"),
			"cmd/symroom/cmd_note.go":              noteCLIFileHash(t, root, "cmd/symroom/cmd_note.go"),
			"internal/room/room/journal_append.go": noteCLIFileHash(t, root, "internal/room/room/journal_append.go"),
			"internal/room/event/event.go":         noteCLIFileHash(t, root, "internal/room/event/event.go"),
			"internal/room/journal/journal.go":     noteCLIFileHash(t, root, "internal/room/journal/journal.go"),
			"internal/room/members/members.go":     noteCLIFileHash(t, root, "internal/room/members/members.go"),
		},
	}
	fixture.JournalFiles = makeNoteJournal(t, signer, false)
	fixture.ObserverJournalFiles = makeNoteJournal(t, signer, true)
	goBinary := buildNoteCLIOracle(t, root)
	temp := t.TempDir()
	for _, vector := range []struct {
		name     string
		args     []string
		observer bool
		json     bool
		mutates  bool
	}{
		{"note-human", []string{"note", "--identity", "oracle", "hello note"}, false, false, true},
		{"note-json-html", []string{"note", "--identity=oracle", "--json=TRUE", "hello <note>&"}, false, true, true},
		{"note-flags-after-message", []string{"note", "hello", "--json=true", "--identity", "oracle"}, false, false, false},
		{"note-usage", []string{"note"}, false, false, false},
		{"note-invalid-bool", []string{"note", "--json=maybe", "hello", "--identity", "oracle"}, false, false, false},
		{"note-unknown-flag", []string{"note", "--bogus"}, false, false, false},
		{"note-help", []string{"note", "--help"}, false, false, false},
		{"note-observer", []string{"note", "--identity", "oracle", "cannot post"}, true, false, false},
	} {
		roomDir := filepath.Join(temp, vector.name)
		journalDir := filepath.Join(roomDir, "journal")
		if err := os.MkdirAll(journalDir, 0o700); err != nil {
			return noteCLIContract{}, err
		}
		if err := os.WriteFile(filepath.Join(roomDir, "room.toml"), []byte(fixture.RoomTOML), 0o600); err != nil {
			return noteCLIContract{}, err
		}
		initial := fixture.JournalFiles
		if vector.observer {
			initial = fixture.ObserverJournalFiles
		}
		for _, file := range initial {
			if err := os.WriteFile(filepath.Join(journalDir, file.Name), []byte(file.Content), 0o600); err != nil {
				return noteCLIContract{}, err
			}
		}
		home := filepath.Join(temp, "env-"+vector.name, "home")
		dataHome := filepath.Join(temp, "env-"+vector.name, "data")
		tempDir := filepath.Join(temp, "env-"+vector.name, "tmp")
		for _, path := range []string{home, dataHome, tempDir} {
			if err := os.MkdirAll(path, 0o700); err != nil {
				return noteCLIContract{}, err
			}
		}
		cmd := exec.Command(goBinary, vector.args...) //nolint:gosec // test-only command uses a fixed helper and controlled arguments
		cmd.Env = []string{
			"HOME=" + home, "USERPROFILE=" + home, "XDG_DATA_HOME=" + dataHome, "TMPDIR=" + tempDir,
			"TZ=UTC", "LC_ALL=C", "LANG=C", "SYMROOM_ROOM_DIR=" + roomDir,
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
				return noteCLIContract{}, fmt.Errorf("run Go note case %s: %w", vector.name, err)
			}
		}
		stdout = normalizeNoteOutput(stdout, vector.json)
		finalFiles, err := readNoteJournal(t, journalDir, signer.MemberID, vector.mutates)
		if err != nil {
			return noteCLIContract{}, err
		}
		fixture.Cases = append(fixture.Cases, noteCLICase{
			Name: vector.name, Args: vector.args, Observer: vector.observer, Mutates: vector.mutates, JSONOutput: vector.json,
			ExitCode: code, Stdout: string(stdout), Stderr: stderr.String(),
			FinalJournalFiles: finalFiles,
		})
	}
	return fixture, nil
}

func makeNoteJournal(t *testing.T, signer *identity.Identity, observer bool) []noteJournalFile {
	t.Helper()
	ownerEvent := &event.Event{
		V: event.CurrentVersion, ID: "note-fixture-room-created", Room: "rm_note_fixture",
		Author: signer.MemberID, Seq: 1,
		Prev:    "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		Lamport: 1, TS: "2026-09-23T10:00:00.000Z", Kind: event.KindRoomCreated,
		Body: json.RawMessage(fmt.Sprintf(`{"name":"Note Fixture","public_key":"%s"}`, hex.EncodeToString(signer.PublicKey))),
	}
	if err := ownerEvent.Sign(signer); err != nil {
		t.Fatal(err)
	}
	events := []*event.Event{ownerEvent}
	if observer {
		events = append(events, &event.Event{
			V: event.CurrentVersion, ID: "note-fixture-observer", Room: "rm_note_fixture",
			Author: signer.MemberID, Seq: 2, Prev: noteLineHash(t, ownerEvent), Lamport: 2,
			TS: "2026-09-23T10:01:00.000Z", Kind: event.KindMemberRoleChanged,
			Body: json.RawMessage(fmt.Sprintf(`{"id":"%s","role":"observer"}`, signer.MemberID)),
		})
	}
	var content []byte
	for _, ev := range events {
		if err := ev.Sign(signer); err != nil {
			t.Fatal(err)
		}
		line, err := ev.MarshalJSONLine()
		if err != nil {
			t.Fatal(err)
		}
		content = append(content, line...)
	}
	return []noteJournalFile{{Name: signer.MemberID + ".jsonl", Content: string(content)}}
}

func noteLineHash(t *testing.T, ev *event.Event) string {
	t.Helper()
	line, err := ev.MarshalJSONLine()
	if err != nil {
		t.Fatal(err)
	}
	hash := sha256.Sum256(bytes.TrimSuffix(line, []byte{'\n'}))
	return "sha256:" + hex.EncodeToString(hash[:])
}

var noteEventID = regexp.MustCompile(`ev_[0-9a-f]{20}`)

func normalizeNoteOutput(output []byte, jsonOutput bool) []byte {
	if !jsonOutput || len(output) == 0 {
		return noteEventID.ReplaceAll(output, []byte("<event-id>"))
	}
	return normalizeNoteEventLine(output)
}

func normalizeNoteEventLine(line []byte) []byte {
	// Go generates a random 80-bit event ID and uses the wall clock; the
	// signature covers both values, so these three fields are normalized only.
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(bytes.TrimSuffix(line, []byte{'\n'}), &fields); err != nil {
		panic(err)
	}
	fields["id"] = json.RawMessage(`"<event-id>"`)
	fields["ts"] = json.RawMessage(`"<dynamic-clock>"`)
	fields["sig"] = json.RawMessage(`"<signature-of-dynamic-event>"`)
	data, err := json.Marshal(fields)
	if err != nil {
		panic(err)
	}
	return append(data, '\n')
}

func readNoteJournal(t *testing.T, dir, author string, normalizeLast bool) ([]noteJournalFile, error) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := make([]noteJournalFile, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".jsonl" {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, entry.Name())) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
		if err != nil {
			return nil, err
		}
		if normalizeLast && entry.Name() == author+".jsonl" {
			lines := bytes.SplitAfter(data, []byte{'\n'})
			if len(lines) > 0 {
				lines[len(lines)-2] = normalizeNoteEventLine(lines[len(lines)-2])
				data = bytes.Join(lines[:len(lines)-1], nil)
			}
		}
		files = append(files, noteJournalFile{Name: entry.Name(), Content: string(data)})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}

func buildNoteCLIOracle(t *testing.T, root string) string {
	t.Helper()
	path := oracleExecutablePath(t, "symroom-go")
	cmd := exec.Command("go", "build", "-o", path, "./cmd/symroom") //nolint:gosec // test-only command uses a fixed helper and controlled arguments
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build Go symroom oracle: %v\n%s", err, output)
	}
	return path
}

func noteCLIRoot(t *testing.T) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve note CLI contract test path")
	}
	root, err := filepath.Abs(filepath.Join(filepath.Dir(filename), "../.."))
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func noteCLIFileHash(t *testing.T, root, path string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, path)) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:])
}
