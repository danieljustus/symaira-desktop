package room

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/room/event"
	"github.com/danieljustus/symaira-desktop/internal/room/identity"
)

// TestPortRoomIdentityEventContract records the Go room identity and event
// engine — Ed25519 identities, member ids, canonical signed bytes, signatures,
// the JSON line format and the identity file chain — as a differential fixture
// (contract row ROOM-001). crates/symroom-core replays it.
//
// Set PORT_GENERATE=1 to rewrite the fixture; a normal run verifies the
// checked-in fixture against the Go implementation, so a behaviour change
// cannot pass silently.
const (
	roomFixturePath   = "testdata/port/room/identity-events.json"
	roomFixtureSchema = 1

	// The oracle is pinned in docs/rust-port/architecture.md.
	roomOracleCommit  = "745c08e8b9d1b1a9cbb2e0ba1c1d0d0d5c0f0f7a"
	roomOracleRelease = "v0.1.0"
)

type roomIdentityVector struct {
	Label         string `json:"label"`
	SeedHex       string `json:"seed_hex"`
	PublicKeyHex  string `json:"public_key_hex"`
	PrivateKeyHex string `json:"private_key_hex"`
	MemberID      string `json:"member_id"`
	StoredFile    string `json:"stored_file"`
	StoredSize    int    `json:"stored_size"`
	StoredMode    *int   `json:"stored_mode"`
	StoredSHA256  string `json:"stored_sha256"`
}

type roomMemberIDVector struct {
	PublicKeyHex string `json:"public_key_hex"`
	MemberID     string `json:"member_id"`
}

type roomEventVector struct {
	ID              string          `json:"id"`
	Kind            string          `json:"kind"`
	Description     string          `json:"description"`
	Event           json.RawMessage `json:"event"`
	CanonicalBytes  string          `json:"canonical_bytes"`
	CanonicalSHA256 string          `json:"canonical_sha256"`
	Signature       string          `json:"signature"`
	JSONLine        string          `json:"json_line"`
	LineSHA256      string          `json:"line_sha256"`
	Verified        bool            `json:"verified"`
	KindKnown       bool            `json:"kind_known"`
}

type roomVerifyVector struct {
	ID          string          `json:"id"`
	Description string          `json:"description"`
	Event       json.RawMessage `json:"event"`
	PublicKey   string          `json:"public_key_hex"`
	Error       string          `json:"error"`
}

type roomFileVector struct {
	ID           string   `json:"id"`
	Description  string   `json:"description"`
	Channel      string   `json:"channel"`
	Names        []string `json:"names"`
	FileMode     *int     `json:"file_mode"`
	Error        string   `json:"error"`
	LoadedName   string   `json:"loaded_name"`
	LoadedMember string   `json:"loaded_member_id"`
	LoadedPublic string   `json:"loaded_public_key_hex"`
}

type roomIdentityEventFixture struct {
	SchemaVersion int                  `json:"schema_version"`
	GeneratedOn   string               `json:"generated_on"`
	Oracle        roomOracle           `json:"oracle"`
	SourceHashes  map[string]string    `json:"source_hashes"`
	Identities    []roomIdentityVector `json:"identities"`
	MemberIDs     []roomMemberIDVector `json:"member_ids"`
	Events        []roomEventVector    `json:"events"`
	VerifyCases   []roomVerifyVector   `json:"verify_cases"`
	FileCases     []roomFileVector     `json:"file_cases"`
	Notes         []string             `json:"notes"`
}

type roomOracle struct {
	Commit  string `json:"commit"`
	Release string `json:"release"`
}

func TestPortRoomIdentityEventContract(t *testing.T) {
	for _, depth := range []int{9999, 10000, 10001} {
		body := strings.Repeat("[", depth) + "0" + strings.Repeat("]", depth)
		value := &event.Event{Body: json.RawMessage(body)}
		_, canonicalErr := event.CanonicalBytes(value)
		_, lineErr := value.MarshalJSONLine()
		if (canonicalErr == nil) != (depth <= 10000) || (lineErr == nil) != (depth <= 10000) {
			t.Fatalf("Go RawMessage depth %d: canonical=%v line=%v", depth, canonicalErr, lineErr)
		}
		line := []byte(`{"v":1,"id":"","room":"","author":"","seq":0,"prev":"","lamport":0,"ts":"","kind":"","body":` + body + `}`)
		_, parseErr := event.UnmarshalJSONLine(line)
		if (parseErr == nil) != (depth <= 9999) {
			t.Fatalf("Go event envelope depth %d: parse=%v", depth, parseErr)
		}
	}
	quoted := []byte(`{"v":1,"body":"` + strings.Repeat(`\"[`, 10001) + `"}`)
	if _, err := event.UnmarshalJSONLine(quoted); err != nil {
		t.Fatalf("brackets inside escaped JSON string do not count toward depth: %v", err)
	}
	fixture := buildRoomFixture(t)
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')

	path := filepath.Join(roomRepoRoot(t), roomFixturePath)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		//nolint:gosec // fixture path is derived from the repository root
		if err := os.WriteFile(path, encoded, 0o644); err != nil {
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d bytes)", roomFixturePath, len(encoded))
		return
	}

	current, err := os.ReadFile(path) //nolint:gosec // fixture path is derived from the repository root
	if err != nil {
		t.Fatalf("read %s: %v (run with PORT_GENERATE=1 to create it)", roomFixturePath, err)
	}
	want, wantErr := roomPlatformDocument(current)
	got, gotErr := roomPlatformDocument(encoded)
	if wantErr != nil || gotErr != nil {
		t.Fatalf("normalise fixture for this platform: %v %v", wantErr, gotErr)
	}
	if string(want) != string(got) {
		t.Fatalf("room identity/event fixture is stale; regenerate deliberately from the pinned Go oracle\n%s",
			roomVectorDifference(t, want, got))
	}
}

func buildRoomFixture(t *testing.T) roomIdentityEventFixture {
	t.Helper()
	identities := roomIdentities(t)
	return roomIdentityEventFixture{
		SchemaVersion: roomFixtureSchema,
		GeneratedOn:   runtime.GOOS,
		Oracle:        roomOracle{Commit: roomOracleCommit, Release: roomOracleRelease},
		SourceHashes: map[string]string{
			"internal/room/identity/identity.go": roomFileSHA256(t, "internal/room/identity/identity.go"),
			"internal/room/event/event.go":       roomFileSHA256(t, "internal/room/event/event.go"),
		},
		Identities:  roomIdentityVectors(t, identities),
		MemberIDs:   roomMemberIDVectors(identities),
		Events:      roomEventVectors(t, identities),
		VerifyCases: roomVerifyCases(t, identities),
		FileCases:   roomFileCases(t, identities),
		Notes: []string{
			"Generated by internal/room/room/port_identity_event_contract_test.go; never hand-edit.",
			"canonical_bytes is the exact JSON the Go engine signs: sorted keys, no whitespace, body verbatim.",
			"json_line is MarshalJSONLine output including the trailing newline.",
			"stored_file is StoredIdentity marshalled with a two-space indent; stored_mode is null off Unix.",
			"file_cases exercise Save/List/Load through the XDG data directory and both environment chains.",
			"generated_on names the platform that observed the file modes; the Go drift check clears them elsewhere.",
		},
	}
}

// roomIdentities derives Ed25519 identities from a documented seed, so the
// vectors need no stored private keys and stay reproducible.
func roomIdentities(t *testing.T) map[string]*identity.Identity {
	t.Helper()
	out := make(map[string]*identity.Identity, 3)
	for _, label := range roomIdentityLabels {
		sum := sha256.Sum256([]byte("symroom-port-identity/" + label))
		priv := ed25519.NewKeyFromSeed(sum[:])
		pub := priv.Public().(ed25519.PublicKey)
		out[label] = &identity.Identity{
			Name:       label,
			MemberID:   identity.ComputeMemberID(pub),
			PublicKey:  pub,
			PrivateKey: priv,
		}
	}
	return out
}

var roomIdentityLabels = []string{"alpha", "beta", "gamma"}

func roomIdentityVectors(t *testing.T, identities map[string]*identity.Identity) []roomIdentityVector {
	t.Helper()
	out := make([]roomIdentityVector, 0, len(roomIdentityLabels))
	for _, label := range roomIdentityLabels {
		id := identities[label]
		stored := identity.StoredIdentity{
			Name:       id.Name,
			MemberID:   id.MemberID,
			PublicKey:  hex.EncodeToString(id.PublicKey),
			PrivateKey: hex.EncodeToString(id.PrivateKey),
		}
		//nolint:gosec // the vector records the exact identity file Go writes, private key included
		data, err := json.MarshalIndent(stored, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		out = append(out, roomIdentityVector{
			Label:         label,
			SeedHex:       hex.EncodeToString(id.PrivateKey.Seed()),
			PublicKeyHex:  hex.EncodeToString(id.PublicKey),
			PrivateKeyHex: hex.EncodeToString(id.PrivateKey),
			MemberID:      id.MemberID,
			StoredFile:    string(data),
			StoredSize:    len(data),
			StoredMode:    roomPlatformMode(0o600),
			StoredSHA256:  roomSHA256(data),
		})
	}
	return out
}

func roomMemberIDVectors(identities map[string]*identity.Identity) []roomMemberIDVector {
	out := make([]roomMemberIDVector, 0, len(roomIdentityLabels))
	for _, label := range roomIdentityLabels {
		id := identities[label]
		out = append(out, roomMemberIDVector{
			PublicKeyHex: hex.EncodeToString(id.PublicKey),
			MemberID:     identity.ComputeMemberID(id.PublicKey),
		})
	}
	return out
}

// roomEventVectors signs one event per kind plus body-shape samples and records
// the canonical bytes, signature, JSON line and verification result.
func roomEventVectors(t *testing.T, identities map[string]*identity.Identity) []roomEventVector {
	t.Helper()
	author := identities["alpha"]
	stamp := time.Date(2026, 9, 18, 12, 30, 45, 0, time.UTC)

	kinds := []string{
		event.KindRoomCreated, event.KindRoomRenamed, event.KindPolicyChanged,
		event.KindMemberAdded, event.KindMemberRemoved, event.KindMemberRoleChanged,
		event.KindNotePosted, event.KindDecisionRecorded, event.KindArtifactLinked,
		event.KindArtifactUnlinked, event.KindArtifactChanged, event.KindRunRequested,
		event.KindRunApproved, event.KindRunDenied, event.KindRunStarted,
		event.KindRunFinished, event.KindRunFailed, event.KindRunCancelled,
		event.KindCheckpointReq, event.KindCheckpointResolved,
	}

	out := make([]roomEventVector, 0, len(kinds)+5)
	for i, kind := range kinds {
		out = append(out, roomEventVectorFor(t, author, roomEventSpec{
			ID:          "kind-" + strings.ReplaceAll(kind, ".", "-"),
			Description: "one signed event of this kind",
			Kind:        kind,
			Room:        fmt.Sprintf("room-%02d", i),
			Seq:         uint64(i + 1),
			Lamport:     uint64(i*3 + 1),
			Stamp:       stamp,
			Body:        fmt.Sprintf(`{"index":%d,"kind":%q}`, i, kind),
		}))
	}

	shapes := []roomEventSpec{
		{
			ID: "body-empty-object", Description: "an empty object body", Kind: event.KindNotePosted,
			Room: "room-shape", Seq: 1, Lamport: 1, Stamp: stamp, Body: `{}`,
		},
		{
			ID: "body-array", Description: "an array body stays verbatim", Kind: event.KindDecisionRecorded,
			Room: "room-shape", Seq: 2, Lamport: 2, Prev: "sha256:previous", Stamp: stamp, Body: `[1,2,3]`,
		},
		{
			ID: "body-string", Description: "a string body stays verbatim", Kind: event.KindNotePosted,
			Room: "room-shape", Seq: 3, Lamport: 3, Prev: "sha256:previous", Stamp: stamp, Body: `"plain text"`,
		},
		{
			ID: "body-nested", Description: "nested objects and unicode survive encoding", Kind: event.KindArtifactLinked,
			Room: "room-shape", Seq: 4, Lamport: 4, Prev: "sha256:previous", Stamp: stamp,
			Body: `{"text":"Grüße 世界","flags":["a","b"],"n":1.5}`,
		},
		{
			ID: "body-null", Description: "a null body stays null", Kind: event.KindArtifactUnlinked,
			Room: "room-shape", Seq: 5, Lamport: 5, Prev: "sha256:previous", Stamp: stamp, Body: `null`,
		},
		{
			ID: "body-html-unicode", Description: "HTML-sensitive characters and Unicode separators are escaped by Go", Kind: event.KindNotePosted,
			Room: "room-<>&\u2028\u2029", Seq: 6, Lamport: 6, Stamp: stamp,
			Body: `{"text":"<>&` + "\u2028\u2029" + `"}`,
		},
		{
			ID: "body-escaped-html", Description: "the original spelling of JSON escapes survives Go RawMessage compaction", Kind: event.KindNotePosted,
			Room: "room-shape", Seq: 7, Lamport: 7, Stamp: stamp,
			Body: `{"text":"\u003C\u003e\u0026"}`,
		},
		{
			ID: "no-version", Description: "Sign fills a missing version with the current one", Kind: event.KindNotePosted,
			Room: "room-version", Seq: 9, Lamport: 9, Stamp: stamp, Body: `{"note":"versionless"}`, OmitVersion: true,
		},
	}
	for _, shape := range shapes {
		out = append(out, roomEventVectorFor(t, author, shape))
	}
	return out
}

type roomEventSpec struct {
	ID          string
	Description string
	Kind        string
	Room        string
	Seq         uint64
	Lamport     uint64
	Prev        string
	Stamp       time.Time
	Body        string
	OmitVersion bool
}

func roomEventVectorFor(t *testing.T, author *identity.Identity, spec roomEventSpec) roomEventVector {
	t.Helper()
	value := event.Event{
		Room:    spec.Room,
		Author:  author.MemberID,
		Seq:     spec.Seq,
		Prev:    spec.Prev,
		Lamport: spec.Lamport,
		TS:      event.FormatTimestamp(spec.Stamp),
		Kind:    spec.Kind,
		Body:    json.RawMessage(spec.Body),
	}
	if !spec.OmitVersion {
		value.V = event.CurrentVersion
	}
	value.ID = fmt.Sprintf("evt_%s_%d", spec.Room, spec.Seq)
	if err := value.Sign(author); err != nil {
		t.Fatal(err)
	}
	canonical, err := event.CanonicalBytes(&value)
	if err != nil {
		t.Fatal(err)
	}
	line, err := value.MarshalJSONLine()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(&value)
	if err != nil {
		t.Fatal(err)
	}
	return roomEventVector{
		ID:              spec.ID,
		Kind:            spec.Kind,
		Description:     spec.Description,
		Event:           raw,
		CanonicalBytes:  string(canonical),
		CanonicalSHA256: roomSHA256(canonical),
		Signature:       value.Sig,
		JSONLine:        string(line),
		LineSHA256:      roomSHA256(line),
		Verified:        value.VerifySignature(author.PublicKey) == nil,
		KindKnown:       event.KnownKinds[spec.Kind],
	}
}

// roomVerifyCases covers the signature check paths: a valid signature, a
// tampered body, the three malformed-signature shapes and a foreign key.
func roomVerifyCases(t *testing.T, identities map[string]*identity.Identity) []roomVerifyVector {
	t.Helper()
	author := identities["alpha"]
	stamp := time.Date(2026, 9, 18, 12, 30, 45, 0, time.UTC)

	base := func() event.Event {
		value := event.Event{
			V: event.CurrentVersion, ID: "evt_verify_1", Room: "room-verify",
			Author: author.MemberID, Seq: 1, Lamport: 1,
			TS: event.FormatTimestamp(stamp), Kind: event.KindNotePosted,
			Body: json.RawMessage(`{"note":"verify"}`),
		}
		if err := value.Sign(author); err != nil {
			t.Fatal(err)
		}
		return value
	}

	out := make([]roomVerifyVector, 0, 6)
	appendCase := func(id, description string, value event.Event, key ed25519.PublicKey) {
		raw, err := json.Marshal(&value)
		if err != nil {
			t.Fatal(err)
		}
		verifyErr := value.VerifySignature(key)
		message := ""
		if verifyErr != nil {
			message = verifyErr.Error()
		}
		out = append(out, roomVerifyVector{
			ID:          id,
			Description: description,
			Event:       raw,
			PublicKey:   hex.EncodeToString(key),
			Error:       message,
		})
	}

	appendCase("valid", "the signed event verifies with the author's key", base(), author.PublicKey)

	tampered := base()
	tampered.Body = json.RawMessage(`{"note":"tampered"}`)
	appendCase("tampered-body", "changing the body invalidates the signature", tampered, author.PublicKey)

	foreign := base()
	appendCase("foreign-key", "another member's key rejects the signature", foreign, identities["beta"].PublicKey)

	noPrefix := base()
	noPrefix.Sig = strings.TrimPrefix(noPrefix.Sig, event.SigPrefix)
	appendCase("missing-prefix", "a signature without the ed25519: prefix is rejected", noPrefix, author.PublicKey)

	badBase64 := base()
	badBase64.Sig = event.SigPrefix + "!!!not-base64!!!"
	appendCase("invalid-base64", "a non-base64 signature payload is rejected", badBase64, author.PublicKey)

	unknownKind := event.Event{
		V: event.CurrentVersion, ID: "evt_verify_unknown", Room: "room-verify",
		Author: author.MemberID, Seq: 2, Lamport: 2,
		TS: event.FormatTimestamp(stamp), Kind: "room.invented",
		Body: json.RawMessage(`{"note":"unknown kind"}`),
	}
	if err := unknownKind.Sign(author); err != nil {
		t.Fatal(err)
	}
	appendCase("unknown-kind", "a correctly signed event with an unknown kind still verifies: signature checking is not kind validation",
		unknownKind, author.PublicKey)

	return out
}

// roomFileCases exercise identity.Save, List and Load, including both
// environment chains and a corrupt file.
func roomFileCases(t *testing.T, identities map[string]*identity.Identity) []roomFileVector {
	t.Helper()
	out := make([]roomFileVector, 0, 6)

	// Save, List, Load round trip in an isolated XDG data directory.
	dataHome := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataHome)
	t.Setenv("SYMROOM_IDENTITY_KEY", "")
	for _, label := range []string{"beta", "alpha"} {
		if err := identity.Save(identities[label]); err != nil {
			t.Fatal(err)
		}
	}
	names, err := identity.List()
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := identity.Load("alpha")
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, roomFileVector{
		ID:           "save-list-load",
		Description:  "saving two identities lists both and loads the recorded key material",
		Channel:      "file",
		Names:        names,
		FileMode:     roomStatMode(t, filepath.Join(identity.IdentitiesDir(), "alpha.json")),
		LoadedName:   loaded.Name,
		LoadedMember: loaded.MemberID,
		LoadedPublic: hex.EncodeToString(loaded.PublicKey),
	})

	missing, err := identity.Load("does-not-exist")
	if missing != nil {
		t.Fatalf("load of a missing identity returned an identity: %+v", missing)
	}
	out = append(out, roomFileVector{
		ID:          "missing-identity",
		Description: "loading an unknown name reports identity not found",
		Channel:     "file",
		Names:       []string{},
		Error:       roomErrorMessage(err),
	})

	//nolint:gosec // the corrupt fixture is deliberately readable
	if err := os.WriteFile(filepath.Join(dataHome, "symroom", "identities", "broken.json"), []byte(`{"name":"broken","public_key":"nope","private_key":"nope"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	broken, err := identity.Load("broken")
	if broken != nil {
		t.Fatalf("load of a corrupt identity returned an identity: %+v", broken)
	}
	out = append(out, roomFileVector{
		ID:          "invalid-key",
		Description: "a file whose key material is not hex reports invalid key data",
		Channel:     "file",
		Names:       []string{},
		Error:       roomErrorMessage(err),
	})

	// Environment chain 1: a 32-byte seed is expanded into a full private key.
	seed := identities["alpha"].PrivateKey.Seed()
	t.Setenv("SYMROOM_IDENTITY_KEY", hex.EncodeToString(seed))
	fromSeed, err := identity.Load("alpha")
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, roomFileVector{
		ID:           "env-seed",
		Description:  "a 32-byte SYMROOM_IDENTITY_KEY seed yields the same member id",
		Channel:      "env",
		Names:        []string{},
		LoadedName:   fromSeed.Name,
		LoadedMember: fromSeed.MemberID,
		LoadedPublic: hex.EncodeToString(fromSeed.PublicKey),
	})

	// Environment chain 1: a 64-byte private key is used as-is.
	t.Setenv("SYMROOM_IDENTITY_KEY", hex.EncodeToString(identities["beta"].PrivateKey))
	fromKey, err := identity.Load("beta")
	if err != nil {
		t.Fatal(err)
	}
	out = append(out, roomFileVector{
		ID:           "env-private-key",
		Description:  "a 64-byte SYMROOM_IDENTITY_KEY is used verbatim",
		Channel:      "env",
		Names:        []string{},
		LoadedName:   fromKey.Name,
		LoadedMember: fromKey.MemberID,
		LoadedPublic: hex.EncodeToString(fromKey.PublicKey),
	})

	// An unusable environment value falls through to the next chain.
	t.Setenv("SYMROOM_IDENTITY_KEY", "not-hex")
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	fallthroughValue, err := identity.Load("alpha")
	if fallthroughValue != nil {
		t.Fatalf("load with an unusable env key returned an identity: %+v", fallthroughValue)
	}
	out = append(out, roomFileVector{
		ID:          "env-invalid-falls-through",
		Description: "an unusable environment key falls through to the file chain",
		Channel:     "env",
		Names:       []string{},
		Error:       roomErrorMessage(err),
	})

	t.Setenv("SYMROOM_IDENTITY_KEY", "")
	return out
}

// roomPlatformDocument prepares both sides of the drift check: the generating
// platform is metadata, and the Unix-only file modes are cleared where the Go
// oracle cannot observe them.
func roomPlatformDocument(document []byte) ([]byte, error) {
	var parsed roomIdentityEventFixture
	if err := json.Unmarshal(document, &parsed); err != nil {
		return nil, err
	}
	parsed.GeneratedOn = ""
	if runtime.GOOS == "windows" {
		for i := range parsed.Identities {
			parsed.Identities[i].StoredMode = nil
		}
		for i := range parsed.FileCases {
			parsed.FileCases[i].FileMode = nil
		}
	}
	encoded, err := json.MarshalIndent(parsed, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(encoded, '\n'), nil
}

func roomErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// roomStatMode records a file mode, or null where the platform does not carry
// Unix permission bits.
func roomStatMode(t *testing.T, path string) *int {
	t.Helper()
	if runtime.GOOS == "windows" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	mode := int(info.Mode().Perm())
	return &mode
}

func roomPlatformMode(mode int) *int {
	if runtime.GOOS == "windows" {
		return nil
	}
	return &mode
}

func roomSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func roomFileSHA256(t *testing.T, rel string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(roomRepoRoot(t), filepath.FromSlash(rel)))
	if err != nil {
		t.Fatal(err)
	}
	return roomSHA256(data)
}

func roomRepoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("repository root (go.mod) not found")
		}
		dir = parent
	}
}

// roomVectorDifference attributes a drift to one vector so a stale fixture is
// actionable; a line diff misleads as soon as one side omits a vector.
func roomVectorDifference(t *testing.T, want, got []byte) string {
	t.Helper()
	compare := func(section string, wantRaw, gotRaw json.RawMessage) string {
		var wantItems, gotItems []map[string]any
		if err := json.Unmarshal(wantRaw, &wantItems); err != nil {
			return fmt.Sprintf("%s: %v", section, err)
		}
		if err := json.Unmarshal(gotRaw, &gotItems); err != nil {
			return fmt.Sprintf("%s: %v", section, err)
		}
		gotByID := make(map[string]any, len(gotItems))
		for _, item := range gotItems {
			gotByID[fmt.Sprint(item["id"])] = item
		}
		for _, item := range wantItems {
			id := fmt.Sprint(item["id"])
			counterpart, ok := gotByID[id]
			if !ok {
				return fmt.Sprintf("%s: fixture vector %q is missing from the generated document", section, id)
			}
			left, _ := json.Marshal(item)
			right, _ := json.Marshal(counterpart)
			if string(left) != string(right) {
				return fmt.Sprintf("%s: vector %q differs\n  fixture:   %s\n  generated: %s", section, id, left, right)
			}
		}
		return ""
	}

	var wantDoc, gotDoc map[string]json.RawMessage
	if err := json.Unmarshal(want, &wantDoc); err != nil {
		return err.Error()
	}
	if err := json.Unmarshal(got, &gotDoc); err != nil {
		return err.Error()
	}
	for _, section := range []string{"events", "verify_cases", "file_cases"} {
		if message := compare(section, wantDoc[section], gotDoc[section]); message != "" {
			return message
		}
	}
	if message := compare("identities", roomReidentify(wantDoc["identities"]), roomReidentify(gotDoc["identities"])); message != "" {
		return message
	}
	if message := compare("member_ids", roomReidentify(wantDoc["member_ids"]), roomReidentify(gotDoc["member_ids"])); message != "" {
		return message
	}
	// Sections without an id field: compare them as JSON.
	for _, section := range []string{"oracle", "source_hashes", "notes"} {
		if string(wantDoc[section]) != string(gotDoc[section]) {
			return fmt.Sprintf("%s differs\n  fixture:   %s\n  generated: %s", section, wantDoc[section], gotDoc[section])
		}
	}
	return "vectors are equal; the surrounding document differs"
}

// roomReidentify adds an id to vectors that identify themselves differently, so
// the shared comparison can attribute them.
func roomReidentify(raw json.RawMessage) json.RawMessage {
	var items []map[string]any
	if err := json.Unmarshal(raw, &items); err != nil {
		return raw
	}
	for _, item := range items {
		if _, ok := item["id"]; ok {
			continue
		}
		switch {
		case item["label"] != nil:
			item["id"] = item["label"]
		default:
			item["id"] = item["public_key_hex"]
		}
	}
	encoded, err := json.Marshal(items)
	if err != nil {
		return raw
	}
	return encoded
}
