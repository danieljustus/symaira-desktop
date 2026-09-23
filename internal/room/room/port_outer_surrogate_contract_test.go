package room

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-desktop/internal/room/event"
	"github.com/danieljustus/symaira-desktop/internal/room/identity"
	"github.com/danieljustus/symaira-desktop/internal/room/journal"
)

const outerSurrogateFixturePath = "testdata/port/room/outer-surrogate.json"

type outerSurrogateVector struct {
	ID          string            `json:"id"`
	Line        string            `json:"line"`
	ParseError  string            `json:"parse_error"`
	Strings     map[string]string `json:"strings,omitempty"`
	Body        string            `json:"body"`
	Canonical   string            `json:"canonical"`
	Marshaled   string            `json:"marshaled"`
	VerifyError string            `json:"verify_error"`
}

type outerSurrogateChainVector struct {
	ID      string `json:"id"`
	Author  string `json:"author"`
	Content string `json:"content"`
	Code    string `json:"code"`
	Error   string `json:"error"`
}

func TestPortRoomOuterSurrogateContract(t *testing.T) {
	seed := sha256.Sum256([]byte("symroom-port-outer-surrogate-v1"))
	private := ed25519.NewKeyFromSeed(seed[:])
	public := private.Public().(ed25519.PublicKey)
	signer := &identity.Identity{MemberID: identity.ComputeMemberID(public), PublicKey: public, PrivateKey: private}
	// Deliberately preserve whitespace, duplicate keys and surrogate escape
	// spelling in the signed RawMessage, independently of the outer strings.
	body := `{ "raw":"\ud800", "\udc00":0, "raw":"\udc00", "pair":"\ud83d\ude00", "literal":"\\ud800" }`
	prefix := `{"v":1,"id":"base","room":"r","author":"` + signer.MemberID + `","seq":1,"prev":"p","lamport":2,"ts":"2026-09-23T00:00:00.000Z","kind":"note.posted","body":` + body + `,`
	type shape struct {
		id, fields string
		sign       bool
		malformed  bool
	}
	var shapes []shape
	for _, field := range []string{"id", "room", "author", "prev", "ts", "kind", "sig"} {
		for _, escape := range []string{`\ud800`, `\udc00`} {
			shapes = append(shapes, shape{field + "-" + escape[2:], `"` + field + `":"` + escape + `"`, field != "sig", false})
		}
	}
	shapes = append(shapes,
		shape{"pair", `"id":"\ud83d\ude00"`, true, false},
		shape{"literal-high", `"id":"\\ud800"`, true, false},
		shape{"literal-low", `"room":"\\udc00"`, true, false},
		shape{"high-high-low", `"id":"\ud800\ud83d\ude00"`, true, false},
		shape{"low-high", `"id":"\udc00\ud800"`, true, false},
		shape{"upper-hex", `"id":"\uD800\uDC00\uDFFF"`, true, false},
		shape{"escaped-quote", `"id":"a\"\ud800\\\udc00"`, true, false},
		shape{"duplicate-last", `"id":"\ud800","ID":"last\udc00"`, true, false},
		shape{"duplicate-null", `"id":"\ud800","ID":null`, true, false},
		shape{"null-first", `"id":null,"id":"\udc00"`, true, false},
		shape{"duplicate-body", `"id":"\ud800","body":{ "last":"\udc00" }`, true, false},
		shape{"null-body", `"id":"\ud800","body":null`, true, false},
		shape{"sig-duplicate-null", `"sig":"\ud800","SIG":null`, false, false},
		shape{"sig-duplicate-last", `"sig":"\ud800","SIG":"ed25519:\udc00"`, false, false},
		shape{"sig-null-first", `"sig":null,"sig":"\udc00"`, false, false},
		shape{"malformed-hex", `"id":"\ud80x"`, false, true},
		shape{"truncated-escape", `"room":"\ud80"`, false, true},
		shape{"malformed-after-high", `"id":"\ud800\udc0x"`, false, true},
		shape{"wrong-type-overwritten", `"id":42,"id":"\ud800"`, false, true},
		shape{"wrong-type-last", `"id":"\ud800","id":false`, false, true},
		shape{"malformed-body", `"id":"\ud800","body":{"raw":"\ud80x"}`, false, true},
		shape{"numeric-surrogate", `"seq":"\ud800"`, false, true},
	)
	var vectors []outerSurrogateVector
	for _, shape := range shapes {
		t.Run(shape.id, func(t *testing.T) {
			line := prefix + shape.fields + "}\n"
			if shape.sign {
				parsed, err := event.UnmarshalJSONLine([]byte(line))
				if err != nil {
					t.Fatal(err)
				}
				if err := parsed.Sign(signer); err != nil {
					t.Fatal(err)
				}
				sig, err := json.Marshal(parsed.Sig)
				if err != nil {
					t.Fatal(err)
				}
				// Append only the signature; retain the original outer escape
				// spellings and raw body instead of re-marshaling the event.
				line = strings.TrimSuffix(line, "}\n") + `,"sig":` + string(sig) + "}\n"
			}
			vector := outerSurrogateResult(t, shape.id, line, public)
			if (vector.ParseError != "") != shape.malformed {
				t.Fatalf("unexpected Go parse outcome: %q", vector.ParseError)
			}
			if shape.sign && vector.VerifyError != "" {
				t.Fatalf("signed vector: %s", vector.VerifyError)
			}
			vectors = append(vectors, vector)
		})
	}
	if t.Failed() {
		return
	}
	// Replacing even one raw-body escape changes the signed bytes. Keep the
	// genuine signature from the first vector as a negative control.
	tampered := strings.Replace(vectors[0].Line, `"raw":"\ud800"`, `"raw":"\ufffd"`, 1)
	negative := outerSurrogateResult(t, "wrong-signature-body-normalized", tampered, public)
	if negative.ParseError != "" || negative.VerifyError == "" {
		t.Fatal("body mutation did not invalidate the Go signature")
	}
	vectors = append(vectors, negative)
	if len(vectors) != 37 {
		t.Fatalf("expected 37 executed vectors, got %d", len(vectors))
	}
	zeroHash := "sha256:" + strings.Repeat("0", 64)
	firstUnsigned := strings.Replace(prefix, `"prev":"p"`, `"prev":"`+zeroHash+`"`, 1) + `"id":"\ud800"}` + "\n"
	first, err := event.UnmarshalJSONLine([]byte(firstUnsigned))
	if err != nil {
		t.Fatal(err)
	}
	if err := first.Sign(signer); err != nil {
		t.Fatal(err)
	}
	sig, err := json.Marshal(first.Sig)
	if err != nil {
		t.Fatal(err)
	}
	firstLine := strings.TrimSuffix(firstUnsigned, "}\n") + `,"sig":` + string(sig) + "}\n"
	second := &event.Event{V: 1, ID: "next", Room: "r", Author: signer.MemberID, Seq: 2,
		Prev: journal.ComputeLineHash([]byte(strings.TrimSuffix(firstLine, "\n"))),
		Kind: event.KindNotePosted, Body: json.RawMessage(body)}
	if err := second.Sign(signer); err != nil {
		t.Fatal(err)
	}
	secondLine, err := second.MarshalJSONLine()
	if err != nil {
		t.Fatal(err)
	}
	changedFirst := strings.Replace(firstLine, `"id":"\ud800"`, `"id":"\ufffd"`, 1)
	if changedFirst == firstLine {
		t.Fatal("journal hash mutation did not alter the original line")
	}
	chainCases := []outerSurrogateChainVector{
		{ID: "raw-surrogate-chain", Author: signer.MemberID, Content: firstLine + string(secondLine)},
		{ID: "normalized-raw-line-breaks-chain", Author: signer.MemberID, Content: changedFirst + string(secondLine)},
	}
	for i := range chainCases {
		item := &chainCases[i]
		segmentDir := t.TempDir()
		if err := os.WriteFile(filepath.Join(segmentDir, item.Author+".jsonl"), []byte(item.Content), 0o600); err != nil {
			t.Fatal(err)
		}
		err := journal.New(segmentDir).VerifyChain(item.Author)
		item.Code = "ok"
		if err != nil {
			if !errors.Is(err, journal.ErrChainBroken) {
				t.Fatalf("%s: unexpected Go chain error: %v", item.ID, err)
			}
			item.Code = "chain_broken"
			item.Error = err.Error()
		}
	}
	if chainCases[0].Code != "ok" || chainCases[1].Code != "chain_broken" {
		t.Fatalf("Go journal did not distinguish raw and normalized lines: %+v", chainCases)
	}
	fixture := struct {
		SchemaVersion int                         `json:"schema_version"`
		SourceHashes  map[string]string           `json:"source_hashes"`
		PublicKey     string                      `json:"public_key"`
		Cases         []outerSurrogateVector      `json:"cases"`
		ChainCases    []outerSurrogateChainVector `json:"chain_cases"`
	}{1, map[string]string{}, hex.EncodeToString(public), vectors, chainCases}
	for _, source := range []string{
		"internal/room/event/event.go",
		"internal/room/identity/identity.go",
		"internal/room/journal/journal.go",
		"internal/room/room/port_identity_event_contract_test.go", // shared fixture helpers
		"internal/room/room/port_outer_surrogate_contract_test.go",
	} {
		fixture.SourceHashes[source] = roomFileSHA256(t, source)
	}
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	path := filepath.Join(roomRepoRoot(t), outerSurrogateFixturePath)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	//nolint:gosec // fixed fixture under the test source's repository root.
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(current) != string(encoded) {
		t.Fatal("Go outer surrogate fixture drift: regenerate explicitly with PORT_GENERATE=1")
	}
}

func outerSurrogateResult(t *testing.T, id, line string, public ed25519.PublicKey) outerSurrogateVector {
	t.Helper()
	result := outerSurrogateVector{ID: id, Line: line}
	parsed, err := event.UnmarshalJSONLine([]byte(line))
	if err != nil {
		result.ParseError = err.Error()
		return result
	}
	result.Strings = map[string]string{
		"id": parsed.ID, "room": parsed.Room, "author": parsed.Author,
		"prev": parsed.Prev, "ts": parsed.TS, "kind": parsed.Kind, "sig": parsed.Sig,
	}
	result.Body = string(parsed.Body)
	canonical, err := event.CanonicalBytes(parsed)
	if err != nil {
		t.Fatal(err)
	}
	result.Canonical = string(canonical)
	marshaled, err := parsed.MarshalJSONLine()
	if err != nil {
		t.Fatal(err)
	}
	result.Marshaled = string(marshaled)
	if err := parsed.VerifySignature(public); err != nil {
		result.VerifyError = err.Error()
	}
	return result
}
