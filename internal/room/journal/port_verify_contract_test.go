package journal

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/room/event"
	"github.com/danieljustus/symaira-desktop/internal/room/identity"
)

type verifyContract struct {
	SchemaVersion int               `json:"schema_version"`
	SourceHashes  map[string]string `json:"source_hashes"`
	Cases         []verifyCase      `json:"cases"`
}

type verifyCase struct {
	Name   string            `json:"name"`
	Files  map[string]string `json:"files"`
	Report *Report           `json:"report,omitempty"`
	Error  string            `json:"error,omitempty"`
}

func TestPortRoomVerifyContract(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("source location unavailable")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "../../.."))
	contract := verifyContract{SchemaVersion: 1, SourceHashes: map[string]string{}}
	for _, rel := range []string{"internal/room/journal/verifier.go", "internal/room/journal/journal.go", "internal/room/members/members.go", "internal/room/event/event.go"} {
		data, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatal(err)
		}
		hash := sha256.Sum256(data)
		contract.SourceHashes[rel] = hex.EncodeToString(hash[:])
	}
	owner := verifyFixtureIdentity("owner")
	unknown := verifyFixtureIdentity("unknown")
	agent := verifyFixtureIdentity("agent")
	member := verifyFixtureIdentity("member")
	rootBody, _ := json.Marshal(map[string]string{"name": "Verify Room", "public_key": hex.EncodeToString(owner.PublicKey)})
	rootEvent := verifyFixtureEvent(t, owner, "ev_root", 1, zeroHash, 1, event.KindRoomCreated, rootBody)
	rootLine := verifyFixtureLine(t, rootEvent)
	note := verifyFixtureEvent(t, owner, "ev_note", 2, ComputeLineHash(bytes.TrimSuffix([]byte(rootLine), []byte("\n"))), 2, event.KindNotePosted, []byte(`{"text":"hello"}`))
	noteLine := verifyFixtureLine(t, note)
	invalid := *note
	invalid.Body = json.RawMessage(`{"text":"tampered"}`)
	invalidLine := verifyFixtureLine(t, &invalid)
	wrongSeq := verifyFixtureEvent(t, owner, "ev_wrong_seq", 4, ComputeLineHash(bytes.TrimSuffix([]byte(rootLine), []byte("\n"))), 2, event.KindNotePosted, []byte(`{"text":"wrong seq"}`))
	wrongPrev := verifyFixtureEvent(t, owner, "ev_wrong_prev", 2, zeroHash, 2, event.KindNotePosted, []byte(`{"text":"wrong prev"}`))
	unknownEvent := verifyFixtureEvent(t, unknown, "ev_unknown", 1, zeroHash, 2, event.KindNotePosted, []byte(`{"text":"unknown"}`))
	memberBody := func(id *identity.Identity, role, kind string) []byte {
		body, err := json.Marshal(map[string]string{"id": id.MemberID, "name": id.Name, "public_key": hex.EncodeToString(id.PublicKey), "role": role, "kind": kind})
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	rootPrev := ComputeLineHash(bytes.TrimSuffix([]byte(rootLine), []byte("\n")))
	addAgent := verifyFixtureEvent(t, owner, "ev_add_agent", 2, rootPrev, 2, event.KindMemberAdded, memberBody(agent, "agent", "agent"))
	addMember := verifyFixtureEvent(t, owner, "ev_add_member", 2, rootPrev, 2, event.KindMemberAdded, memberBody(member, "member", "human"))
	agentApproval := verifyFixtureEvent(t, agent, "ev_agent_approve", 1, zeroHash, 3, event.KindRunApproved, []byte(`{}`))
	unauthorized := verifyFixtureEvent(t, member, "ev_bad_member", 1, zeroHash, 3, event.KindMemberAdded, memberBody(unknown, "member", "human"))
	forkA := verifyFixtureEvent(t, owner, "ev_fork_a", 2, rootPrev, 2, event.KindNotePosted, []byte(`{"text":"a"}`))
	forkB := verifyFixtureEvent(t, owner, "ev_fork_b", 2, rootPrev, 3, event.KindNotePosted, []byte(`{"text":"b"}`))
	contract.Cases = []verifyCase{
		{Name: "empty", Files: map[string]string{}},
		{Name: "valid", Files: map[string]string{owner.MemberID + ".jsonl": rootLine + noteLine}},
		{Name: "tampered-signature", Files: map[string]string{owner.MemberID + ".jsonl": rootLine + invalidLine}},
		{Name: "wrong-sequence", Files: map[string]string{owner.MemberID + ".jsonl": rootLine + verifyFixtureLine(t, wrongSeq)}},
		{Name: "wrong-prev", Files: map[string]string{owner.MemberID + ".jsonl": rootLine + verifyFixtureLine(t, wrongPrev)}},
		{Name: "unknown-author", Files: map[string]string{owner.MemberID + ".jsonl": rootLine, unknown.MemberID + ".jsonl": verifyFixtureLine(t, unknownEvent)}},
		{Name: "agent-approval", Files: map[string]string{owner.MemberID + ".jsonl": rootLine + verifyFixtureLine(t, addAgent), agent.MemberID + ".jsonl": verifyFixtureLine(t, agentApproval)}},
		{Name: "unauthorized-member", Files: map[string]string{owner.MemberID + ".jsonl": rootLine + verifyFixtureLine(t, addMember), member.MemberID + ".jsonl": verifyFixtureLine(t, unauthorized)}},
		{Name: "fork", Files: map[string]string{owner.MemberID + ".jsonl": rootLine + verifyFixtureLine(t, forkA) + verifyFixtureLine(t, forkB)}},
		{Name: "malformed", Files: map[string]string{owner.MemberID + ".jsonl": rootLine + "{\"v\":\n"}},
	}
	for i := range contract.Cases {
		row := &contract.Cases[i]
		journalDir := filepath.Join(t.TempDir(), "journal")
		if err := os.MkdirAll(journalDir, 0o700); err != nil {
			t.Fatal(err)
		}
		for name, content := range row.Files {
			if err := os.WriteFile(filepath.Join(journalDir, name), []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		var err error
		row.Report, err = New(journalDir).Verify()
		if err != nil {
			row.Error = err.Error()
		}
	}
	encoded, err := json.MarshalIndent(contract, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join(root, "testdata/port/room/verify.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(encoded, want) {
		t.Fatal("Go Room verify fixture changed; regenerate explicitly with PORT_GENERATE=1")
	}
}

func verifyFixtureIdentity(name string) *identity.Identity {
	seed := sha256.Sum256([]byte("room-verify-" + name))
	private := ed25519.NewKeyFromSeed(seed[:])
	public := private.Public().(ed25519.PublicKey)
	return &identity.Identity{Name: name, MemberID: identity.ComputeMemberID(public), PublicKey: public, PrivateKey: private}
}

func verifyFixtureEvent(t *testing.T, signer *identity.Identity, id string, seq uint64, prev string, lamport uint64, kind string, body []byte) *event.Event {
	t.Helper()
	e := &event.Event{V: 1, ID: id, Room: "rm_verify", Author: signer.MemberID, Seq: seq, Prev: prev, Lamport: lamport, TS: "2026-09-23T10:00:00.000Z", Kind: kind, Body: body}
	if err := e.Sign(signer); err != nil {
		t.Fatal(err)
	}
	return e
}

func verifyFixtureLine(t *testing.T, e *event.Event) string {
	t.Helper()
	line, err := e.MarshalJSONLine()
	if err != nil {
		t.Fatal(err)
	}
	return string(line)
}
