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
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/room/event"
	"github.com/danieljustus/symaira-desktop/internal/room/identity"
)

const decideCLIContractPath = "testdata/port/room/decide-cli.json"

type decideCLIContract struct {
	SchemaVersion  int               `json:"schema_version"`
	OracleRevision string            `json:"oracle_revision"`
	SourceHashes   map[string]string `json:"source_hashes"`
	IdentityKey    string            `json:"identity_key"`
	IdentityMember string            `json:"identity_member"`
	RoomTOML       string            `json:"room_toml"`
	JournalFiles   []noteJournalFile `json:"journal_files"`
	ObserverFiles  []noteJournalFile `json:"observer_journal_files"`
	Cases          []decideCLICase   `json:"cases"`
}

type decideCLICase struct {
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

// TestPortDecideCLIContract records Go process output and journal writes. The
// normal test is read-only; fixture generation is explicit.
func TestPortDecideCLIContract(t *testing.T) {
	root := noteCLIRoot(t)
	fixture, err := makeDecideCLIContract(t, root)
	if err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, decideCLIContractPath)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s", decideCLIContractPath)
		return
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (set PORT_GENERATE=1 to create it)", decideCLIContractPath, err)
	}
	if !bytes.Equal(got, data) {
		t.Fatalf("Go decide CLI fixture is stale; regenerate deliberately with PORT_GENERATE=1")
	}
}

func makeDecideCLIContract(t *testing.T, root string) (decideCLIContract, error) {
	t.Helper()
	seed := sha256.Sum256([]byte("symroom-decide-cli-fixture-identity"))
	privateKey := ed25519.NewKeyFromSeed(seed[:])
	publicKey := privateKey.Public().(ed25519.PublicKey)
	signer := &identity.Identity{
		Name: "decide-fixture", MemberID: identity.ComputeMemberID(publicKey),
		PublicKey: publicKey, PrivateKey: privateKey,
	}
	fixture := decideCLIContract{
		SchemaVersion:  1,
		OracleRevision: "b68f7bccb1e636a0b2c1e1093e5473c3709683b3",
		IdentityKey:    hex.EncodeToString(seed[:]),
		IdentityMember: signer.MemberID,
		RoomTOML:       "id = \"rm_decide_fixture\"\ncreated = \"2026-09-23T10:00:00.000Z\"\n",
		SourceHashes: map[string]string{
			"cmd/symroom/main.go":                  noteCLIFileHash(t, root, "cmd/symroom/main.go"),
			"cmd/symroom/cmd_decide.go":            noteCLIFileHash(t, root, "cmd/symroom/cmd_decide.go"),
			"internal/room/room/journal_append.go": noteCLIFileHash(t, root, "internal/room/room/journal_append.go"),
			"internal/room/event/event.go":         noteCLIFileHash(t, root, "internal/room/event/event.go"),
			"internal/room/journal/journal.go":     noteCLIFileHash(t, root, "internal/room/journal/journal.go"),
			"internal/room/members/members.go":     noteCLIFileHash(t, root, "internal/room/members/members.go"),
		},
	}
	fixture.JournalFiles = makeDecideJournal(t, signer, false)
	fixture.ObserverFiles = makeDecideJournal(t, signer, true)
	goBinary := buildNoteCLIOracle(t, root)
	temp := t.TempDir()
	for _, vector := range []struct {
		name     string
		argv     []string
		observer bool
		mutates  bool
		json     bool
	}{
		{name: "decide-text", argv: []string{"decide", "-identity", "oracle", "-refs", "ref-a,ref-b", "Choose B"}, mutates: true},
		{name: "decide-json-html", argv: []string{"decide", "--identity=oracle", "--refs=ref-a,,ref-c", "--json=TRUE", "Choose <A>&"}, mutates: true, json: true},
		{name: "decide-trailing-flags-ignored", argv: []string{"decide", "Choice", "--json=true", "--identity", "oracle"}},
		{name: "decide-usage", argv: []string{"decide"}},
		{name: "decide-invalid-bool", argv: []string{"decide", "--json=maybe", "Choice", "--identity", "oracle"}},
		{name: "decide-unknown-flag", argv: []string{"decide", "--bogus"}},
		{name: "decide-missing-identity-value", argv: []string{"decide", "--identity"}},
		{name: "decide-missing-refs-value", argv: []string{"decide", "--refs"}},
		{name: "decide-help", argv: []string{"decide", "--help"}},
		{name: "decide-observer", argv: []string{"decide", "--identity", "oracle", "--refs", "issue-1", "Cannot choose"}, observer: true},
	} {
		roomDir := filepath.Join(temp, vector.name)
		journalDir := filepath.Join(roomDir, "journal")
		if err := os.MkdirAll(journalDir, 0o700); err != nil {
			return decideCLIContract{}, err
		}
		if err := os.WriteFile(filepath.Join(roomDir, "room.toml"), []byte(fixture.RoomTOML), 0o600); err != nil {
			return decideCLIContract{}, err
		}
		initial := fixture.JournalFiles
		if vector.observer {
			initial = fixture.ObserverFiles
		}
		for _, file := range initial {
			if err := os.WriteFile(filepath.Join(journalDir, file.Name), []byte(file.Content), 0o600); err != nil {
				return decideCLIContract{}, err
			}
		}
		home := filepath.Join(temp, "env-"+vector.name, "home")
		dataHome := filepath.Join(temp, "env-"+vector.name, "data")
		tempDir := filepath.Join(temp, "env-"+vector.name, "tmp")
		for _, path := range []string{home, dataHome, tempDir} {
			if err := os.MkdirAll(path, 0o700); err != nil {
				return decideCLIContract{}, err
			}
		}
		cmd := exec.Command(goBinary, vector.argv...)
		cmd.Env = []string{
			"HOME=" + home, "XDG_DATA_HOME=" + dataHome, "TMPDIR=" + tempDir,
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
				return decideCLIContract{}, fmt.Errorf("run Go decide case %s: %w", vector.name, err)
			}
		}
		stdout = normalizeNoteOutput(stdout, vector.json)
		finalFiles, err := readNoteJournal(t, journalDir, signer.MemberID, vector.mutates)
		if err != nil {
			return decideCLIContract{}, err
		}
		fixture.Cases = append(fixture.Cases, decideCLICase{
			Name: vector.name, Args: vector.argv, Observer: vector.observer, Mutates: vector.mutates, JSONOutput: vector.json,
			ExitCode: code, Stdout: string(stdout), Stderr: stderr.String(), FinalJournalFiles: finalFiles,
		})
	}
	return fixture, nil
}

func makeDecideJournal(t *testing.T, signer *identity.Identity, observer bool) []noteJournalFile {
	t.Helper()
	ownerEvent := &event.Event{
		V: event.CurrentVersion, ID: "decide-fixture-room-created", Room: "rm_decide_fixture",
		Author: signer.MemberID, Seq: 1,
		Prev:    "sha256:0000000000000000000000000000000000000000000000000000000000000000",
		Lamport: 1, TS: "2026-09-23T10:00:00.000Z", Kind: event.KindRoomCreated,
		Body: json.RawMessage(fmt.Sprintf(`{"name":"Decide Fixture","public_key":"%s"}`, hex.EncodeToString(signer.PublicKey))),
	}
	if err := ownerEvent.Sign(signer); err != nil {
		t.Fatal(err)
	}
	events := []*event.Event{ownerEvent}
	if observer {
		events = append(events, &event.Event{
			V: event.CurrentVersion, ID: "decide-fixture-observer", Room: "rm_decide_fixture",
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
