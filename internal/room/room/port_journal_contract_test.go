package room

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/room/event"
	"github.com/danieljustus/symaira-desktop/internal/room/identity"
)

// portJournalFixture freezes the deterministic SymRoom journal read-back:
// sequence/hash chains, Lamport ceiling, membership projection, and append bytes.
type portJournalFixture struct {
	SchemaVersion int                   `json:"schema_version"`
	ZeroHash      string                `json:"zero_hash"`
	DirectoryMode string                `json:"directory_mode"`
	FileMode      string                `json:"file_mode"`
	StatsCases    []portJournalCase     `json:"stats_cases"`
	AppendCases   []portJournalAppend   `json:"append_cases"`
	ChainCases    []portJournalChainRow `json:"chain_cases"`
}

type portJournalFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
}

type portJournalMember struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	PublicKey string `json:"public_key"`
	Role      string `json:"role"`
	Kind      string `json:"kind"`
}

type portJournalCase struct {
	Name       string              `json:"name"`
	Files      []portJournalFile   `json:"files,omitempty"`
	CreateDir  bool                `json:"create_dir"`
	Author     string              `json:"author"`
	MaxLamport uint64              `json:"max_lamport"`
	AuthorSeq  uint64              `json:"author_seq"`
	AuthorPrev string              `json:"author_prev"`
	Members    []portJournalMember `json:"members"`
	Note       string              `json:"note,omitempty"`
}

type portJournalAppend struct {
	Name     string            `json:"name"`
	Existing []portJournalFile `json:"existing,omitempty"`
	Event    json.RawMessage   `json:"event"`
	File     string            `json:"file"`
	Content  string            `json:"content"`
	Entries  []string          `json:"entries"`
}

// portJournalChainRow records the hash chain Go builds when events are appended
// one after another: every append reads the previous line's SHA-256 back.
type portJournalChainRow struct {
	Author string          `json:"author"`
	Seq    uint64          `json:"seq"`
	Prev   string          `json:"prev"`
	Event  json.RawMessage `json:"event"`
}

const portJournalFixturePath = "../../../testdata/port/room/journal.json"

const portJournalZeroHash = "sha256:0000000000000000000000000000000000000000000000000000000000000000"

func TestPortRoomJournalContract(t *testing.T) {
	fixture := portJournalFixture{
		SchemaVersion: 1,
		ZeroHash:      portJournalZeroHash,
		DirectoryMode: "0700",
		FileMode:      "0600",
		StatsCases:    portJournalStatsCases(t),
		AppendCases:   portJournalAppendCases(t),
		ChainCases:    portJournalChainCases(t),
	}

	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')

	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(portJournalFixturePath, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	//nolint:gosec // the fixture path is fixed relative to the repository
	current, err := os.ReadFile(portJournalFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded) {
		t.Fatal("room journal fixture is stale; run make room-journal-fixtures-generate")
	}
}

func portJournalStatsCases(t *testing.T) []portJournalCase {
	t.Helper()
	line := func(author string, seq, lamport uint64, prev string) string {
		return string(portJournalLine(t, author, seq, lamport, prev))
	}
	alice := portJournalIdentityFor(t, "alpha").MemberID
	bob := portJournalIdentityFor(t, "beta").MemberID
	owner := portJournalIdentityFor(t, "alpha")
	guest := portJournalIdentityFor(t, "beta")
	if owner.MemberID > guest.MemberID {
		owner, guest = guest, owner
	}
	memberLine := func(author string, seq, lamport uint64, kind, body string) string {
		t.Helper()
		ev := portJournalEvent(t, author, seq, lamport, portJournalZeroHash)
		ev.Kind = kind
		ev.Body = json.RawMessage(body)
		if err := ev.Sign(portJournalIdentity(t, author)); err != nil {
			t.Fatal(err)
		}
		line, err := ev.MarshalJSONLine()
		if err != nil {
			t.Fatal(err)
		}
		return string(line)
	}
	rootBody := `{"name":"Room","public_key":"` + hex.EncodeToString(owner.PublicKey) + `"}`
	guestBody := `{"id":"` + guest.MemberID + `","name":"Guest","public_key":"` + hex.EncodeToString(guest.PublicKey) + `","role":"member","kind":"human"}`

	inputs := []struct {
		name      string
		files     []portJournalFile
		createDir bool
		author    string
		note      string
	}{
		{
			name:      "missing-journal-directory",
			createDir: false,
			author:    alice,
			note:      "Go tolerates the absent directory and reports an empty journal.",
		},
		{
			name:      "empty-journal-directory",
			createDir: true,
			author:    alice,
		},
		{
			name:      "single-author-chain",
			createDir: true,
			author:    alice,
			files: []portJournalFile{{
				Name: alice + ".jsonl",
				Content: line(alice, 1, 1, portJournalZeroHash) +
					line(alice, 2, 2, "sha256:deadbeef"),
			}},
		},
		{
			name:      "blank-lines-are-skipped",
			createDir: true,
			author:    alice,
			files: []portJournalFile{{
				Name: alice + ".jsonl",
				Content: line(alice, 1, 4, portJournalZeroHash) +
					"\n   \n\t\n" + line(alice, 2, 5, "sha256:deadbeef") + "\n\n",
			}},
			note: "Blank and whitespace-only lines count neither for seq nor for the hash.",
		},
		{
			name:      "undecodable-lines-are-skipped-for-lamport-but-counted-for-seq",
			createDir: true,
			author:    alice,
			files: []portJournalFile{{
				Name: alice + ".jsonl",
				Content: line(alice, 1, 9, portJournalZeroHash) +
					"{not json}\n" + line(alice, 2, 3, "sha256:deadbeef"),
			}},
			note: "ReadJournalStats ignores undecodable lines; GetAuthorStats counts every non-blank line.",
		},
		{
			name:      "lamport-ceiling-across-authors",
			createDir: true,
			author:    alice,
			files: []portJournalFile{
				{Name: alice + ".jsonl", Content: line(alice, 1, 2, portJournalZeroHash)},
				{Name: bob + ".jsonl", Content: line(bob, 1, 17, portJournalZeroHash)},
			},
		},
		{
			name:      "non-jsonl-files-are-ignored",
			createDir: true,
			author:    alice,
			files: []portJournalFile{
				{Name: alice + ".jsonl", Content: line(alice, 1, 2, portJournalZeroHash)},
				{Name: "notes.txt", Content: line(bob, 1, 99, portJournalZeroHash)},
				{Name: bob + ".jsonl.bak", Content: line(bob, 1, 98, portJournalZeroHash)},
			},
		},
		{
			name:      "author-without-own-file",
			createDir: true,
			author:    bob,
			files: []portJournalFile{
				{Name: alice + ".jsonl", Content: line(alice, 1, 6, portJournalZeroHash)},
			},
			note: "An author that has never written starts at seq 0 with the zero hash.",
		},
		{
			name:      "empty-author-file",
			createDir: true,
			author:    alice,
			files:     []portJournalFile{{Name: alice + ".jsonl", Content: ""}},
		},
		{
			name:      "author-file-without-trailing-newline",
			createDir: true,
			author:    alice,
			files: []portJournalFile{{
				Name: alice + ".jsonl",
				Content: line(alice, 1, 1, portJournalZeroHash) +
					trimNewline(line(alice, 2, 2, "sha256:deadbeef")),
			}},
			note: "The final line is hashed without its terminator either way.",
		},
		{
			name:      "membership-across-sorted-author-files",
			createDir: true,
			author:    owner.MemberID,
			files: []portJournalFile{
				{Name: owner.MemberID + ".jsonl", Content: memberLine(owner.MemberID, 1, 1, event.KindRoomCreated, rootBody) +
					memberLine(owner.MemberID, 2, 2, event.KindMemberAdded, guestBody) +
					memberLine(owner.MemberID, 3, 3, event.KindMemberRoleChanged, `{"id":"`+guest.MemberID+`","role":"agent"}`)},
				{Name: guest.MemberID + ".jsonl", Content: memberLine(guest.MemberID, 1, 4, event.KindMemberRemoved, `{"id":"`+owner.MemberID+`"}`) +
					memberLine(guest.MemberID, 2, 5, event.KindRunApproved, `{}`)},
			},
			note: "Go replays sorted author files, ignores rejected owner and approval actions, and still counts their Lamport clocks.",
		},
		{
			name:      "membership-removal-after-undecodable-line",
			createDir: true,
			author:    owner.MemberID,
			files: []portJournalFile{{Name: owner.MemberID + ".jsonl", Content: memberLine(owner.MemberID, 1, 1, event.KindRoomCreated, rootBody) +
				memberLine(owner.MemberID, 2, 2, event.KindMemberAdded, guestBody) + "{not json}\n" +
				memberLine(owner.MemberID, 3, 20, event.KindMemberRemoved, `{"id":"`+guest.MemberID+`"}`)}},
			note: "An undecodable line neither changes the projection nor stops replay of a later removal.",
		},
	}

	cases := make([]portJournalCase, 0, len(inputs))
	for _, input := range inputs {
		roomDir := t.TempDir()
		if input.createDir {
			portJournalWrite(t, roomDir, input.files)
		}
		stats, err := ReadJournalStats(roomDir)
		if err != nil {
			t.Fatalf("%s: %v", input.name, err)
		}
		seq, prev, err := GetAuthorStats(roomDir, input.author)
		if err != nil {
			t.Fatalf("%s: %v", input.name, err)
		}
		if stats.MemberState == nil {
			t.Fatalf("%s: Go returned no membership state", input.name)
		}
		switch input.name {
		case "membership-across-sorted-author-files":
			if len(stats.MemberState.Members) != 2 || stats.MemberState.Members[owner.MemberID] == nil ||
				stats.MemberState.Members[guest.MemberID] == nil || string(stats.MemberState.Members[guest.MemberID].Role) != "agent" || stats.MaxLamport != 5 {
				t.Fatalf("%s: member projection or rejected-event Lamport drift: %+v", input.name, stats)
			}
		case "membership-removal-after-undecodable-line":
			if len(stats.MemberState.Members) != 1 || stats.MemberState.Members[owner.MemberID] == nil || stats.MaxLamport != 20 {
				t.Fatalf("%s: removal after malformed line drift: %+v", input.name, stats)
			}
		}
		memberViews := make([]portJournalMember, 0, len(stats.MemberState.Members))
		for _, member := range stats.MemberState.Members {
			memberViews = append(memberViews, portJournalMember{
				ID: member.ID, Name: member.Name, PublicKey: hex.EncodeToString(member.PublicKey),
				Role: string(member.Role), Kind: string(member.Kind),
			})
		}
		sort.Slice(memberViews, func(i, j int) bool { return memberViews[i].ID < memberViews[j].ID })
		cases = append(cases, portJournalCase{
			Name:       input.name,
			Files:      input.files,
			CreateDir:  input.createDir,
			Author:     input.author,
			MaxLamport: stats.MaxLamport,
			AuthorSeq:  seq,
			AuthorPrev: prev,
			Members:    memberViews,
			Note:       input.note,
		})
	}
	return cases
}

func portJournalAppendCases(t *testing.T) []portJournalAppend {
	t.Helper()
	alice := portJournalIdentityFor(t, "alpha").MemberID
	inputs := []struct {
		name     string
		existing []portJournalFile
	}{
		{name: "append-to-new-journal"},
		{
			name: "append-to-existing-author-file",
			existing: []portJournalFile{{
				Name:    alice + ".jsonl",
				Content: string(portJournalLine(t, alice, 1, 1, portJournalZeroHash)),
			}},
		},
		{
			name: "append-after-file-without-trailing-newline",
			existing: []portJournalFile{{
				Name:    alice + ".jsonl",
				Content: trimNewline(string(portJournalLine(t, alice, 1, 1, portJournalZeroHash))),
			}},
			// Go appends without inserting a separator, so the two events end up
			// on one physical line. The port must reproduce that, not repair it.
		},
	}

	cases := make([]portJournalAppend, 0, len(inputs))
	for _, input := range inputs {
		roomDir := t.TempDir()
		if len(input.existing) > 0 {
			portJournalWrite(t, roomDir, input.existing)
		}
		ev := portJournalEvent(t, alice, 2, 2, "sha256:deadbeef")
		if err := AppendEvent(roomDir, ev); err != nil {
			t.Fatalf("%s: %v", input.name, err)
		}
		journalDir := filepath.Join(roomDir, "journal")
		//nolint:gosec // journalDir is the isolated per-case temporary directory
		content, err := os.ReadFile(filepath.Join(journalDir, alice+".jsonl"))
		if err != nil {
			t.Fatal(err)
		}
		marshalled, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, portJournalAppend{
			Name:     input.name,
			Existing: input.existing,
			Event:    marshalled,
			File:     alice + ".jsonl",
			Content:  string(content),
			Entries:  portJournalEntries(t, journalDir),
		})
	}
	return cases
}

// portJournalChainCases appends three events for two authors through the real
// `GetAuthorStats` → `AppendEvent` cycle and records the resulting chain, so a
// port cannot satisfy the contract by hashing something other than the previous
// stored line.
func portJournalChainCases(t *testing.T) []portJournalChainRow {
	t.Helper()
	roomDir := t.TempDir()
	rows := make([]portJournalChainRow, 0, 4)
	alice := portJournalIdentityFor(t, "alpha").MemberID
	bob := portJournalIdentityFor(t, "beta").MemberID
	for _, author := range []string{alice, alice, bob, alice} {
		seq, prev, err := GetAuthorStats(roomDir, author)
		if err != nil {
			t.Fatal(err)
		}
		ev := portJournalEvent(t, author, seq+1, seq+1, prev)
		marshalled, err := json.Marshal(ev)
		if err != nil {
			t.Fatal(err)
		}
		rows = append(rows, portJournalChainRow{
			Author: author,
			Seq:    seq + 1,
			Prev:   prev,
			Event:  marshalled,
		})
		if err := AppendEvent(roomDir, ev); err != nil {
			t.Fatal(err)
		}
	}
	return rows
}

// portJournalEvent builds a fully deterministic signed event: the identity seed
// and every field are fixed, so the marshalled line is stable across runs.
func portJournalEvent(t *testing.T, author string, seq, lamport uint64, prev string) *event.Event {
	t.Helper()
	ev := &event.Event{
		V:       event.CurrentVersion,
		ID:      "ev_" + author + "_" + portJournalDigits(seq),
		Room:    "room_port_journal",
		Author:  author,
		Seq:     seq,
		Prev:    prev,
		Lamport: lamport,
		TS:      "2026-01-01T01:04:05.000000042Z",
		Kind:    event.KindNotePosted,
		Body:    json.RawMessage(`{"text":"port"}`),
	}
	if err := ev.Sign(portJournalIdentity(t, author)); err != nil {
		t.Fatal(err)
	}
	return ev
}

// portJournalIdentity derives the same deterministic identities the ROOM-001
// vectors use, keyed by the member id the journal stores as `author`.
func portJournalIdentity(t *testing.T, author string) *identity.Identity {
	t.Helper()
	for _, label := range []string{"alpha", "beta", "gamma"} {
		id := portJournalIdentityFor(t, label)
		if id.MemberID == author {
			return id
		}
	}
	t.Fatalf("no port identity for author %q", author)
	return nil
}

func portJournalIdentityFor(t *testing.T, label string) *identity.Identity {
	t.Helper()
	sum := sha256.Sum256([]byte("symroom-port-identity/" + label))
	priv := ed25519.NewKeyFromSeed(sum[:])
	pub, ok := priv.Public().(ed25519.PublicKey)
	if !ok {
		t.Fatal("unexpected public key type")
	}
	return &identity.Identity{
		Name:       label,
		MemberID:   identity.ComputeMemberID(pub),
		PublicKey:  pub,
		PrivateKey: priv,
	}
}

func portJournalLine(t *testing.T, author string, seq, lamport uint64, prev string) []byte {
	t.Helper()
	line, err := portJournalEvent(t, author, seq, lamport, prev).MarshalJSONLine()
	if err != nil {
		t.Fatal(err)
	}
	return line
}

func portJournalWrite(t *testing.T, roomDir string, files []portJournalFile) {
	t.Helper()
	journalDir := filepath.Join(roomDir, "journal")
	if err := os.MkdirAll(journalDir, 0o700); err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		if err := os.WriteFile(filepath.Join(journalDir, file.Name), []byte(file.Content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func portJournalEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	return names
}

func portJournalDigits(value uint64) string {
	if value == 0 {
		return "0"
	}
	digits := make([]byte, 0, 20)
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}

func trimNewline(value string) string {
	for len(value) > 0 && (value[len(value)-1] == '\n' || value[len(value)-1] == '\r') {
		value = value[:len(value)-1]
	}
	return value
}

// TestPortRoomJournalModes pins the POSIX modes `AppendEvent` creates. Windows
// does not carry them, so the recorded fixture stays mode-free.
func TestPortRoomJournalModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes are not observable on Windows")
	}
	roomDir := t.TempDir()
	alice := portJournalIdentityFor(t, "alpha").MemberID
	if err := AppendEvent(roomDir, portJournalEvent(t, alice, 1, 1, portJournalZeroHash)); err != nil {
		t.Fatal(err)
	}
	journalDir := filepath.Join(roomDir, "journal")
	dirInfo, err := os.Stat(journalDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("journal directory mode = %04o, want 0700", got)
	}
	fileInfo, err := os.Stat(filepath.Join(journalDir, alice+".jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("journal file mode = %04o, want 0600", got)
	}
}
