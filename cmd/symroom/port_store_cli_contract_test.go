package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/danieljustus/symaira-desktop/internal/room/identity"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

// ROOMCLI-002: the identity store (`identity create|list|show|export` with
// arbitrary names, store states and umasks) and `index rebuild` success, both
// through the real Go process. `identity create` mints a random key, so the
// recorded tree masks key material after checking that it is consistent.
const storeCLIContractPath = "testdata/port/room/store-cli.json"

type storeOp struct {
	Op      string `json:"op"`
	Path    string `json:"path"`
	Content string `json:"content,omitempty"`
	Mode    string `json:"mode,omitempty"`
	Target  string `json:"target,omitempty"`
}

type storeEntry struct {
	Path    string `json:"path"`
	Kind    string `json:"kind"`
	Mode    string `json:"mode,omitempty"`
	Content string `json:"content,omitempty"`
	Target  string `json:"target,omitempty"`
	Fresh   bool   `json:"fresh,omitempty"`
}

type storeCase struct {
	Name     string    `json:"name"`
	UnixOnly bool      `json:"unix_only,omitempty"`
	Args     []string  `json:"args"`
	Ops      []storeOp `json:"ops,omitempty"`
	Xdg      string    `json:"xdg,omitempty"`
	Home     string    `json:"home,omitempty"`
	Umask    string    `json:"umask,omitempty"`
	RoomEnv  string    `json:"room_env,omitempty"`
	Cwd      string    `json:"cwd,omitempty"`
	NeedsOwn bool      `json:"needs_non_root,omitempty"`
	// Host restricts a case to one operating system (file-name policy, such as
	// APFS rejecting names that are not UTF-8, differs per OS).
	Host        string `json:"host,omitempty"`
	IdentityKey bool   `json:"identity_key,omitempty"`
	// KeyHex is a SYMROOM_IDENTITY_KEY other than the default seed (a 64-byte
	// key whose public half need not belong to its seed).
	KeyHex   string `json:"key_hex,omitempty"`
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	// StderrPrefixAt marks a failure whose tail is Go's JSON wording for an
	// event line (not ported): only the text up to it is compared.
	StderrPrefixAt string       `json:"-"`
	StderrIsPrefix bool         `json:"stderr_is_prefix,omitempty"`
	Tree           []storeEntry `json:"tree"`
}

type storeContract struct {
	SchemaVersion  int               `json:"schema_version"`
	OracleRevision string            `json:"oracle_revision"`
	SourceHashes   map[string]string `json:"source_hashes"`
	JournalLine    string            `json:"journal_line"`
	Cases          []storeCase       `json:"cases"`
}

var storeSources = []string{
	"cmd/symroom/main.go", "cmd/symroom/cmd_identity.go", "cmd/symroom/cmd_index.go",
	"internal/room/identity/identity.go", "internal/room/index/index.go",
}

func storeIdentityFile(seedName string) string {
	seed := sha256.Sum256([]byte("symroom-store-" + seedName))
	private := ed25519.NewKeyFromSeed(seed[:])
	public := private.Public().(ed25519.PublicKey)
	stored := identity.StoredIdentity{
		Name: seedName, MemberID: identity.ComputeMemberID(public),
		PublicKey: hex.EncodeToString(public), PrivateKey: hex.EncodeToString(private),
	}
	data, err := json.MarshalIndent(stored, "", "  ")
	if err != nil {
		panic(err)
	}
	return string(data)
}

func storeCases(journalLine string) []storeCase {
	alpha := func() storeOp {
		return storeOp{Op: "file", Path: "xdg/symroom/identities/alpha.json", Content: storeIdentityFile("alpha"), Mode: "0600"}
	}
	create := func(name string, args ...string) []string {
		return append([]string{"identity", "create", name}, args...)
	}
	var cases []storeCase
	add := func(c storeCase) { cases = append(cases, c) }

	// Names accepted verbatim into a file name, or refused by the OS.
	for _, name := range []string{
		"fresh", "", ".", "..", "a/b", "a/../b", "../x", "./x", "-x", "--public", "-", "--",
		"ünï", "a b", "a\nb", "tab\there", "name.json", "/abs", "abs/", "a//b", "a<b>&c", "q\"uote", "back\\slash",
		"line\u2028sep", "bell\a", strings.Repeat("x", 251), strings.Repeat("x", 300),
	} {
		label := fmt.Sprintf("create-%q", name)
		if len(label) > 40 {
			label = fmt.Sprintf("create-long-%d", len(name))
		}
		add(storeCase{Name: label, UnixOnly: true, Args: create(name)})
	}
	add(storeCase{Name: "create-extra-args", UnixOnly: true, Args: create("fresh", "extra", "--flag", "-h")})
	add(storeCase{Name: "create-help-flag-is-the-name", UnixOnly: true, Args: create("-h")})
	add(storeCase{Name: "create-help-long-flag-is-the-name", UnixOnly: true, Args: create("--help")})
	// Where the store lives.
	add(storeCase{Name: "create-xdg-unset", UnixOnly: true, Args: create("fresh"), Xdg: "unset"})
	add(storeCase{Name: "create-xdg-empty", UnixOnly: true, Args: create("fresh"), Xdg: "empty"})
	add(storeCase{Name: "create-xdg-relative", UnixOnly: true, Args: create("fresh"), Xdg: "relative"})
	add(storeCase{Name: "create-xdg-dotdot", UnixOnly: true, Args: create("fresh"), Xdg: "dotdot"})
	add(storeCase{Name: "create-xdg-unset-home-empty", UnixOnly: true, Args: create("fresh"), Xdg: "unset", Home: "empty"})
	add(storeCase{Name: "create-xdg-unset-home-unset", UnixOnly: true, Args: create("fresh"), Xdg: "unset", Home: "unset"})
	// Store states.
	add(storeCase{Name: "create-symroom-is-a-file", UnixOnly: true, Args: create("fresh"), Ops: []storeOp{{Op: "file", Path: "xdg/symroom", Content: "x", Mode: "0600"}}})
	add(storeCase{Name: "create-identities-is-a-file", UnixOnly: true, Args: create("fresh"), Ops: []storeOp{{Op: "dir", Path: "xdg/symroom", Mode: "0700"}, {Op: "file", Path: "xdg/symroom/identities", Content: "x", Mode: "0600"}}})
	add(storeCase{Name: "create-xdg-is-a-file", UnixOnly: true, Args: create("fresh"), Ops: []storeOp{{Op: "remove", Path: "xdg"}, {Op: "file", Path: "xdg", Content: "x", Mode: "0600"}}})
	add(storeCase{Name: "create-xdg-readonly", UnixOnly: true, NeedsOwn: true, Args: create("fresh"), Ops: []storeOp{{Op: "chmod", Path: "xdg", Mode: "0500"}}})
	add(storeCase{Name: "create-store-readonly", UnixOnly: true, NeedsOwn: true, Args: create("fresh"), Ops: []storeOp{{Op: "dir", Path: "xdg/symroom/identities", Mode: "0700"}, {Op: "chmod", Path: "xdg/symroom/identities", Mode: "0500"}}})
	add(storeCase{Name: "create-store-mode-is-kept", UnixOnly: true, Args: create("fresh"), Ops: []storeOp{{Op: "dir", Path: "xdg/symroom", Mode: "0755"}, {Op: "dir", Path: "xdg/symroom/identities", Mode: "0755"}}})
	add(storeCase{Name: "create-overwrites-and-keeps-its-mode", UnixOnly: true, Args: create("alpha"), Ops: []storeOp{{Op: "dir", Path: "xdg/symroom/identities", Mode: "0700"}, {Op: "file", Path: "xdg/symroom/identities/alpha.json", Content: strings.Repeat("old ", 200), Mode: "0644"}, {Op: "chmod", Path: "xdg/symroom/identities/alpha.json", Mode: "0644"}}})
	add(storeCase{Name: "create-overwrite-readonly-file", UnixOnly: true, NeedsOwn: true, Args: create("alpha"), Ops: []storeOp{alpha(), {Op: "chmod", Path: "xdg/symroom/identities/alpha.json", Mode: "0400"}}})
	add(storeCase{Name: "create-name-is-a-directory", UnixOnly: true, Args: create("alpha"), Ops: []storeOp{{Op: "dir", Path: "xdg/symroom/identities/alpha.json", Mode: "0700"}}})
	add(storeCase{Name: "create-through-a-dangling-link", UnixOnly: true, Args: create("alpha"), Ops: []storeOp{{Op: "dir", Path: "xdg/symroom/identities", Mode: "0700"}, {Op: "symlink", Path: "xdg/symroom/identities/alpha.json", Target: "elsewhere.json"}}})
	add(storeCase{Name: "create-through-a-link-to-a-file", UnixOnly: true, Args: create("alpha"), Ops: []storeOp{{Op: "dir", Path: "xdg/symroom/identities", Mode: "0700"}, {Op: "file", Path: "xdg/target.json", Content: "keep?", Mode: "0640"}, {Op: "symlink", Path: "xdg/symroom/identities/alpha.json", Target: "../../target.json"}}})
	add(storeCase{Name: "create-identities-is-a-link-to-a-dir", UnixOnly: true, Args: create("fresh"), Ops: []storeOp{{Op: "dir", Path: "xdg/symroom", Mode: "0700"}, {Op: "dir", Path: "xdg/real", Mode: "0700"}, {Op: "symlink", Path: "xdg/symroom/identities", Target: "../real"}}})
	for _, mask := range []string{"077", "277", "000", "0"} {
		add(storeCase{Name: "create-umask-" + mask, UnixOnly: true, Args: create("fresh"), Umask: mask})
	}
	add(storeCase{Name: "create-umask-077-existing-parent", UnixOnly: true, Args: create("fresh"), Umask: "077", Ops: []storeOp{{Op: "dir", Path: "xdg/symroom", Mode: "0755"}}})
	// Reading the store with arbitrary names.
	for _, name := range []string{"alpha", "../identities/alpha", "a/../alpha", "./alpha", "/alpha", "alpha/", "nested/../alpha", "", "alpha.json", "missing", "a//b"} {
		add(storeCase{Name: fmt.Sprintf("show-%q", name), UnixOnly: true, Args: []string{"identity", "show", name}, Ops: []storeOp{alpha()}})
	}
	add(storeCase{Name: "export-public-path-name", UnixOnly: true, Args: []string{"identity", "export", "--public", "x/../alpha"}, Ops: []storeOp{alpha()}})
	add(storeCase{Name: "show-extra-args", UnixOnly: true, Args: []string{"identity", "show", "alpha", "extra"}, Ops: []storeOp{alpha()}})
	add(storeCase{Name: "show-store-unreadable", UnixOnly: true, NeedsOwn: true, Args: []string{"identity", "show", "alpha"}, Ops: []storeOp{alpha(), {Op: "chmod", Path: "xdg/symroom/identities", Mode: "0000"}}})
	add(storeCase{Name: "show-file-unreadable", UnixOnly: true, NeedsOwn: true, Args: []string{"identity", "show", "alpha"}, Ops: []storeOp{alpha(), {Op: "chmod", Path: "xdg/symroom/identities/alpha.json", Mode: "0000"}}})
	add(storeCase{Name: "show-name-is-a-directory", UnixOnly: true, Args: []string{"identity", "show", "d"}, Ops: []storeOp{{Op: "dir", Path: "xdg/symroom/identities/d.json", Mode: "0700"}}})
	// Listing.
	listOps := []storeOp{
		{Op: "file", Path: "xdg/symroom/identities/b.json", Content: "{}", Mode: "0600"},
		{Op: "file", Path: "xdg/symroom/identities/a.json", Content: "{}", Mode: "0600"},
		{Op: "file", Path: "xdg/symroom/identities/a-b.json", Content: "{}", Mode: "0600"},
		{Op: "file", Path: "xdg/symroom/identities/a.b.json", Content: "{}", Mode: "0600"},
		{Op: "file", Path: "xdg/symroom/identities/C.json", Content: "{}", Mode: "0600"},
		{Op: "file", Path: "xdg/symroom/identities/é.json", Content: "{}", Mode: "0600"},
		{Op: "file", Path: "xdg/symroom/identities/note.txt", Content: "x", Mode: "0600"},
		{Op: "file", Path: "xdg/symroom/identities/.json", Content: "{}", Mode: "0600"},
		{Op: "file", Path: "xdg/symroom/identities/x.json.bak", Content: "{}", Mode: "0600"},
		{Op: "file", Path: "xdg/symroom/identities/a b.json", Content: "{}", Mode: "0600"},
		{Op: "dir", Path: "xdg/symroom/identities/dir.json", Mode: "0700"},
		{Op: "symlink", Path: "xdg/symroom/identities/linkdir.json", Target: "dir.json"},
		{Op: "symlink", Path: "xdg/symroom/identities/linkfile.json", Target: "a.json"},
		{Op: "symlink", Path: "xdg/symroom/identities/dangling.json", Target: "nowhere"},
	}
	add(storeCase{Name: "list-mixed-store", UnixOnly: true, Args: []string{"identity", "list"}, Ops: listOps})
	add(storeCase{Name: "list-extra-args", UnixOnly: true, Args: []string{"identity", "list", "ignored", "--x"}, Ops: listOps[:3]})
	add(storeCase{Name: "list-no-store", UnixOnly: true, Args: []string{"identity", "list"}})
	add(storeCase{Name: "list-symroom-is-a-file", UnixOnly: true, Args: []string{"identity", "list"}, Ops: []storeOp{{Op: "file", Path: "xdg/symroom", Content: "x", Mode: "0600"}}})
	add(storeCase{Name: "list-identities-is-a-file", UnixOnly: true, Args: []string{"identity", "list"}, Ops: []storeOp{{Op: "dir", Path: "xdg/symroom", Mode: "0700"}, {Op: "file", Path: "xdg/symroom/identities", Content: "x", Mode: "0600"}}})
	add(storeCase{Name: "list-store-unreadable", UnixOnly: true, NeedsOwn: true, Args: []string{"identity", "list"}, Ops: []storeOp{{Op: "file", Path: "xdg/symroom/identities/a.json", Content: "{}", Mode: "0600"}, {Op: "chmod", Path: "xdg/symroom/identities", Mode: "0000"}}})
	add(storeCase{Name: "list-link-to-dir-store", UnixOnly: true, Args: []string{"identity", "list"}, Ops: []storeOp{{Op: "dir", Path: "xdg/symroom", Mode: "0700"}, {Op: "dir", Path: "xdg/real", Mode: "0700"}, {Op: "file", Path: "xdg/real/z.json", Content: "{}", Mode: "0600"}, {Op: "symlink", Path: "xdg/symroom/identities", Target: "../real"}}})
	// `index rebuild`: success output, repeated runs and the failures around it.
	journal := func(path string) storeOp {
		return storeOp{Op: "file", Path: path, Content: journalLine, Mode: "0600"}
	}
	roomOps := []storeOp{{Op: "dir", Path: "room/journal", Mode: "0700"}, journal("room/journal/mem_cli.jsonl")}
	add(storeCase{Name: "index-rebuild-success", Args: []string{"index", "rebuild"}, Ops: roomOps, RoomEnv: "room"})
	add(storeCase{Name: "index-rebuild-extra-args", Args: []string{"index", "rebuild", "ignored", "--x"}, Ops: roomOps, RoomEnv: "room"})
	add(storeCase{Name: "index-rebuild-cwd-room", Args: []string{"index", "rebuild"}, Ops: roomOps, Cwd: "room"})
	add(storeCase{Name: "index-rebuild-twice", Args: []string{"index", "rebuild"}, Ops: append(append([]storeOp(nil), roomOps...), storeOp{Op: "run", Path: "index rebuild"}), RoomEnv: "room"})
	add(storeCase{Name: "index-rebuild-empty-journal", Args: []string{"index", "rebuild"}, Ops: []storeOp{{Op: "dir", Path: "room/journal", Mode: "0700"}}, RoomEnv: "room"})
	add(storeCase{Name: "index-rebuild-no-journal-dir", Args: []string{"index", "rebuild"}, Ops: []storeOp{{Op: "dir", Path: "room", Mode: "0700"}}, RoomEnv: "room", UnixOnly: true})
	add(storeCase{Name: "index-rebuild-missing-room", Args: []string{"index", "rebuild"}, RoomEnv: "room", UnixOnly: true})
	add(storeCase{Name: "index-rebuild-state-dir-is-a-file", Args: []string{"index", "rebuild"}, Ops: append(append([]storeOp(nil), roomOps...), storeOp{Op: "file", Path: ".symroom", Content: "x", Mode: "0600"}), RoomEnv: "room", UnixOnly: true})
	add(storeCase{Name: "index-rebuild-state-dir-readonly", Args: []string{"index", "rebuild"}, NeedsOwn: true, Ops: append(append([]storeOp(nil), roomOps...), storeOp{Op: "dir", Path: ".symroom", Mode: "0700"}, storeOp{Op: "chmod", Path: ".symroom", Mode: "0500"}), RoomEnv: "room", UnixOnly: true})
	add(storeCase{Name: "index-rebuild-db-is-a-directory", Args: []string{"index", "rebuild"}, Ops: append(append([]storeOp(nil), roomOps...), storeOp{Op: "dir", Path: ".symroom/index.sqlite", Mode: "0700"}), RoomEnv: "room", UnixOnly: true})
	add(storeCase{Name: "index-rebuild-replaces-a-stale-db", Args: []string{"index", "rebuild"}, Ops: append(append([]storeOp(nil), roomOps...), storeOp{Op: "file", Path: ".symroom/index.sqlite", Content: "not a database", Mode: "0600"}), RoomEnv: "room", UnixOnly: true})
	add(storeCase{Name: "index-rebuild-state-dir-mode", Args: []string{"index", "rebuild"}, Ops: append(append([]storeOp(nil), roomOps...), storeOp{Op: "dir", Path: ".symroom", Mode: "0755"}), RoomEnv: "room", UnixOnly: true})
	add(storeCase{Name: "index-rebuild-umask-077", Args: []string{"index", "rebuild"}, Ops: roomOps, RoomEnv: "room", Umask: "077", UnixOnly: true})
	add(storeCase{Name: "index-rebuild-corrupt-journal", Args: []string{"index", "rebuild"}, Ops: []storeOp{{Op: "dir", Path: "room/journal", Mode: "0700"}, {Op: "file", Path: "room/journal/mem_cli.jsonl", Content: "{\"v\":\n", Mode: "0600"}}, RoomEnv: "room", UnixOnly: true, StderrPrefixAt: "unmarshal line:"})
	// Arguments that are not valid UTF-8 (Go strings are bytes): flag and
	// action messages quote them as \xNN, names reach the file system as bytes.
	bad, mid := "<hex:fffe>", "<hex:61e28262>"
	for _, c := range []storeCase{
		{Name: "bytes-undefined-flag", Args: []string{"note", "--" + bad}},
		{Name: "bytes-undefined-flag-with-value", Args: []string{"note", "--" + mid + "=x"}},
		{Name: "bytes-bad-bool", Args: []string{"version", "--json=" + bad}},
		{Name: "bytes-bad-int", Args: []string{"log", "--limit", mid}},
		{Name: "bytes-bad-int-inline", Args: []string{"log", "--limit=" + bad}},
		{Name: "bytes-bad-duration", Args: []string{"run", "approve", "--ttl", bad}},
		{Name: "bytes-bad-bool-in-run", Args: []string{"run", "list", "--json=" + mid}},
		{Name: "bytes-unknown-command", Args: []string{bad}},
		{Name: "bytes-unknown-identity-action", Args: []string{"identity", mid}},
		{Name: "bytes-unknown-run-action", Args: []string{"run", bad}},
		{Name: "bytes-unknown-member-action", Args: []string{"member", mid}},
		{Name: "bytes-unknown-checkpoint-action", Args: []string{"checkpoint", bad}},
		{Name: "bytes-unknown-artifact-action", Args: []string{"artifact", mid}},
		{Name: "bytes-show-name-from-env-key", Args: []string{"identity", "show", bad}, IdentityKey: true},
		{Name: "bytes-show-mid-name-from-env-key", Args: []string{"identity", "show", mid}, IdentityKey: true},
		{Name: "bytes-export-name-from-env-key", Args: []string{"identity", "export", "--public", bad}, IdentityKey: true},
		{Name: "bytes-show-missing-name", Args: []string{"identity", "show", bad}},
		{Name: "bytes-export-missing-name", Args: []string{"identity", "export", "--public", mid}},
		{Name: "bytes-run-title", Args: []string{"run", "request", "--identity", "x", "--title", mid}, IdentityKey: true, RoomEnv: "room", Ops: []storeOp{
			{Op: "file", Path: "room/room.toml", Content: "schema_version = 1\nid = \"rm_fixture\"\ncreated = \"2026-09-01T00:00:00Z\"\nroot_pubkey = \"ed25519:fixture\"\nroot_event = \"ev_fixture\"\n", Mode: "0600"},
			{Op: "dir", Path: "room/journal", Mode: "0700"}}},
		{Name: "bytes-run-plan-file", Args: []string{"run", "request", "--identity", "x", "--title", "t", "--plan-file", bad + "/" + mid}, IdentityKey: true, RoomEnv: "room", Ops: []storeOp{
			{Op: "file", Path: "room/room.toml", Content: "schema_version = 1\nid = \"rm_fixture\"\ncreated = \"2026-09-01T00:00:00Z\"\nroot_pubkey = \"ed25519:fixture\"\nroot_event = \"ev_fixture\"\n", Mode: "0600"},
			{Op: "dir", Path: "room/journal", Mode: "0700"}}},
		{Name: "bytes-init-identity-flag", Args: []string{"init", "--identity", bad, "dest"}},
		{Name: "bytes-note-identity-flag", Args: []string{"note", "--identity", mid, "text"}},
		{Name: "bytes-artifact-missing-path", Args: []string{"artifact", "link", "--identity", "x", "missing-" + bad}, IdentityKey: true, RoomEnv: "room", Ops: roomOps},
		{Name: "bytes-create-name", Args: create(bad), Host: "darwin"},
		{Name: "bytes-create-mid-name", Args: create(mid), Host: "darwin"},
		{Name: "bytes-init-dir", Args: []string{"init", "--identity", "x", "dest-" + bad}, IdentityKey: true, Host: "darwin"},
	} {
		c.UnixOnly = true
		add(c)
	}
	// ROOM-001: a 64-byte key is seed || public half and Go trusts the stored
	// half when it signs, so a half that does not belong to the seed still
	// produces a (deterministic) signature that verifies against nothing.
	signSeed := sha256.Sum256([]byte("room-001-sign-seed"))
	otherSeed := sha256.Sum256([]byte("room-001-other-seed"))
	derived := ed25519.NewKeyFromSeed(signSeed[:])
	other := ed25519.NewKeyFromSeed(otherSeed[:])
	roomFiles := []storeOp{
		{Op: "file", Path: "room/room.toml", Content: "schema_version = 1\nid = \"rm_fixture\"\ncreated = \"2026-09-01T00:00:00Z\"\nroot_pubkey = \"ed25519:fixture\"\nroot_event = \"ev_fixture\"\n", Mode: "0600"},
		{Op: "dir", Path: "room/journal", Mode: "0700"},
	}
	for _, k := range []struct {
		name string
		half []byte
	}{
		{"consistent", derived.Public().(ed25519.PublicKey)},
		{"other-valid-point", other.Public().(ed25519.PublicKey)},
		{"zero-half", make([]byte, 32)},
		{"not-a-point", bytes.Repeat([]byte{0xff}, 32)},
		{"seed-as-half", signSeed[:]},
	} {
		keyHex := hex.EncodeToString(append(append([]byte(nil), signSeed[:]...), k.half...))
		ops := append(append([]storeOp(nil), roomFiles...), storeOp{Op: "cli", Content: "run request --identity x --title t"})
		add(storeCase{Name: "sign-" + k.name + "-journal", Args: []string{"run", "request", "--identity", "x", "--title", "t"}, KeyHex: keyHex, RoomEnv: "room", Ops: roomFiles, UnixOnly: true})
		add(storeCase{Name: "sign-" + k.name + "-verify", Args: []string{"verify"}, KeyHex: keyHex, RoomEnv: "room", Ops: ops, UnixOnly: true})
		add(storeCase{Name: "sign-" + k.name + "-show", Args: []string{"identity", "show", "x"}, KeyHex: keyHex, UnixOnly: true})
	}
	return cases
}

// storeIdentityKey is a fixed seed for cases that resolve an identity from the
// environment instead of the store.
const storeIdentityKey = "0102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f20"

var (
	storeMemberID = regexp.MustCompile(`mem_[0-9a-f]{16}`)
	storeHexRun   = regexp.MustCompile(`[0-9a-f]{64,128}`)
)

// maskStoredIdentity checks a freshly written identity file for consistency and
// returns its content with the random key material replaced.
func maskStoredIdentity(content string) (string, bool) {
	var stored identity.StoredIdentity
	if err := json.Unmarshal([]byte(content), &stored); err != nil {
		return content, false
	}
	priv, errPriv := hex.DecodeString(stored.PrivateKey)
	pub, errPub := hex.DecodeString(stored.PublicKey)
	if errPriv != nil || errPub != nil || len(priv) != ed25519.PrivateKeySize || len(pub) != ed25519.PublicKeySize {
		return content, false
	}
	if !bytes.Equal(priv[32:], pub) || identity.ComputeMemberID(pub) != stored.MemberID {
		return content, false
	}
	if !bytes.Equal(ed25519.NewKeyFromSeed(priv[:32]), priv) {
		return content, false
	}
	masked := strings.ReplaceAll(content, stored.PrivateKey, "<private_key>")
	masked = strings.ReplaceAll(masked, stored.PublicKey, "<public_key>")
	masked = strings.ReplaceAll(masked, stored.MemberID, "<member_id>")
	return masked, true
}

func storeSnapshot(t *testing.T, root string, before map[string]string) []storeEntry {
	t.Helper()
	var entries []storeEntry
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil || rel == "." {
			return relErr
		}
		rel = filepath.ToSlash(rel)
		if rel == "tmp" || rel == "bin" || strings.HasPrefix(rel, "tmp/") {
			return nil
		}
		info, infoErr := os.Lstat(path)
		if infoErr != nil {
			return infoErr
		}
		entry := storeEntry{Path: rel}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			target, linkErr := os.Readlink(path)
			if linkErr != nil {
				return linkErr
			}
			entry.Kind, entry.Target = "symlink", target
		case info.IsDir():
			entry.Kind, entry.Mode = "dir", fmt.Sprintf("%04o", info.Mode().Perm())
		default:
			entry.Kind, entry.Mode = "file", fmt.Sprintf("%04o", info.Mode().Perm())
			data, readErr := os.ReadFile(path) //nolint:gosec // scratch tree under t.TempDir
			if readErr != nil {
				// An unreadable fixture file is recorded by its mode only.
				break
			}
			content := string(data)
			if strings.HasSuffix(rel, ".sqlite") {
				if bytes.HasPrefix(data, []byte("SQLite format 3\x00")) {
					content = "<sqlite database>"
				}
			} else if masked, ok := maskStoredIdentity(content); ok {
				content = masked
			}
			entry.Content = content
			entry.Fresh = before[rel] != string(data)
		}
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	return entries
}

func applyStoreOps(t *testing.T, root string, ops []storeOp, runIndex func(), runCLI func(string)) map[string]string {
	t.Helper()
	before := map[string]string{}
	for _, op := range ops {
		path := filepath.Join(root, filepath.FromSlash(op.Path))
		mode := func() fs.FileMode {
			var m uint32
			if _, err := fmt.Sscanf(op.Mode, "%o", &m); err != nil {
				t.Fatalf("mode %q: %v", op.Mode, err)
			}
			return fs.FileMode(m)
		}
		switch op.Op {
		case "dir":
			if err := os.MkdirAll(path, 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, mode()); err != nil {
				t.Fatal(err)
			}
		case "file":
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(op.Content), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Chmod(path, mode()); err != nil {
				t.Fatal(err)
			}
			before[op.Path] = op.Content
		case "symlink":
			if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(op.Target, path); err != nil {
				t.Fatal(err)
			}
		case "chmod":
			if err := os.Chmod(path, mode()); err != nil {
				t.Fatal(err)
			}
		case "remove":
			if err := os.RemoveAll(path); err != nil {
				t.Fatal(err)
			}
		case "run":
			runIndex()
		case "cli":
			runCLI(op.Content)
		default:
			t.Fatalf("unknown store op %q", op.Op)
		}
	}
	return before
}

func storeEnvironment(root string, c storeCase) []string {
	env := []string{
		"TMPDIR=" + filepath.Join(root, "tmp"), "TMP=" + filepath.Join(root, "tmp"), "TEMP=" + filepath.Join(root, "tmp"),
		"TZ=UTC", "PATH=" + filepath.Join(root, "bin"),
	}
	if root := os.Getenv("SystemRoot"); root != "" {
		env = append(env, "SystemRoot="+root)
	}
	switch c.Home {
	case "unset":
	case "empty":
		env = append(env, "HOME=")
	default:
		env = append(env, "HOME="+filepath.Join(root, "home"), "USERPROFILE="+filepath.Join(root, "home"))
	}
	switch c.Xdg {
	case "unset":
	case "empty":
		env = append(env, "XDG_DATA_HOME=")
	case "relative":
		env = append(env, "XDG_DATA_HOME=rel/xdg")
	case "dotdot":
		env = append(env, "XDG_DATA_HOME="+filepath.Join(root, "xdg", "..", "xdg", "sub", ".."))
	default:
		env = append(env, "XDG_DATA_HOME="+filepath.Join(root, "xdg"))
	}
	if c.RoomEnv != "" {
		env = append(env, "SYMROOM_ROOM_DIR="+filepath.Join(root, c.RoomEnv))
	}
	if c.IdentityKey {
		env = append(env, "SYMROOM_IDENTITY_KEY="+storeIdentityKey)
	}
	if c.KeyHex != "" {
		env = append(env, "SYMROOM_IDENTITY_KEY="+c.KeyHex)
	}
	return env
}

var storeHexToken = regexp.MustCompile(`<hex:([0-9a-f]*)>`)

// decodeStoreArg replaces every "<hex:digits>" token with the raw bytes it
// names, so an argument can carry bytes that are not valid UTF-8.
func decodeStoreArg(arg string) string {
	return storeHexToken.ReplaceAllStringFunc(arg, func(token string) string {
		raw, err := hex.DecodeString(storeHexToken.FindStringSubmatch(token)[1])
		if err != nil {
			panic(err)
		}
		return string(raw)
	})
}

func storeCommand(binary string, c storeCase, root string, rawArgs []string) *exec.Cmd {
	args := make([]string, len(rawArgs))
	for i, arg := range rawArgs {
		args[i] = decodeStoreArg(arg)
	}
	argv := append([]string{binary}, args...)
	var cmd *exec.Cmd
	if c.Umask != "" && runtime.GOOS != "windows" {
		cmd = exec.Command("/bin/sh", append([]string{"-c", "umask " + c.Umask + "; exec \"$@\"", "sh"}, argv...)...) //nolint:gosec // test-only fixed helper
	} else {
		cmd = exec.Command(binary, args...) //nolint:gosec // test-only fixed helper
	}
	cmd.Dir = root
	if c.Cwd != "" {
		cmd.Dir = filepath.Join(root, c.Cwd)
	}
	cmd.Env = storeEnvironment(root, c)
	return cmd
}

// normalizeStoreText masks the scratch root and member ids; output that is not
// valid UTF-8 is recorded as "hex:<digits>".
func normalizeStoreText(text []byte, root string) string {
	text = bytes.ReplaceAll(text, []byte(root), []byte("<root>"))
	text = bytes.ReplaceAll(text, []byte(filepath.ToSlash(root)), []byte("<root>"))
	text = storeMemberID.ReplaceAll(text, []byte("<member_id>"))
	if utf8.Valid(text) {
		return string(text)
	}
	return "hex:" + hex.EncodeToString(text)
}

func runStoreCase(t *testing.T, binary string, c storeCase) storeCase {
	t.Helper()
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{"home", "tmp", "bin", "xdg"} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	runAgain := func() {
		cmd := storeCommand(binary, c, root, c.Args)
		_, _ = cmd.CombinedOutput()
	}
	runOther := func(args string) {
		cmd := storeCommand(binary, c, root, strings.Split(args, " "))
		_, _ = cmd.CombinedOutput()
	}
	before := applyStoreOps(t, root, c.Ops, runAgain, runOther)
	cmd := storeCommand(binary, c, root, c.Args)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		exitErr, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("%s: %v", c.Name, err)
		}
		code = exitErr.ExitCode()
	}
	// Restore permissions so the scratch tree can be read and removed.
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if d != nil && d.IsDir() {
			_ = os.Chmod(path, 0o700)
		}
		return nil
	})
	c.Ops = append([]storeOp(nil), c.Ops...)
	c.ExitCode = code
	c.Stdout = normalizeStoreText(stdout.Bytes(), root)
	c.Stderr = normalizeStoreText(stderr.Bytes(), root)
	if c.StderrPrefixAt != "" {
		end := strings.Index(c.Stderr, c.StderrPrefixAt)
		if end < 0 {
			t.Fatalf("%s: stderr %q lacks %q", c.Name, c.Stderr, c.StderrPrefixAt)
		}
		c.Stderr, c.StderrIsPrefix = c.Stderr[:end+len(c.StderrPrefixAt)], true
	}
	c.Tree = storeSnapshot(t, root, before)
	if c.Tree == nil {
		c.Tree = []storeEntry{}
	}
	return c
}

func TestPortStoreCLIContract(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	binary := oracleExecutablePath(t, "symroom-go-store-oracle")
	build := exec.Command("go", "build", "-o", binary, "./cmd/symroom") //nolint:gosec // test-only fixed helper
	build.Dir = root
	if output, buildErr := build.CombinedOutput(); buildErr != nil {
		t.Fatalf("build Go symroom oracle: %v\n%s", buildErr, output)
	}
	line := `{"v":1,"id":"ev_cli_note","room":"rm_cli","author":"mem_cli","seq":1,"prev":"","lamport":1,"ts":"2026-09-23T10:00:00.000Z","kind":"note.posted","body":{"text":"from another room"}}` + "\n"
	fixture := storeContract{SchemaVersion: 1, OracleRevision: fixtureoracle.Current().Commit, SourceHashes: map[string]string{}, JournalLine: line}
	for _, rel := range storeSources {
		data, readErr := os.ReadFile(filepath.Join(root, rel)) //nolint:gosec // fixed source path
		if readErr != nil {
			t.Fatal(readErr)
		}
		sum := sha256.Sum256(data)
		fixture.SourceHashes[rel] = hex.EncodeToString(sum[:])
	}
	path := filepath.Join(root, storeCLIContractPath)
	generate := os.Getenv("PORT_GENERATE") == "1"
	if generate && runtime.GOOS == "windows" {
		t.Skip("store cases are generated on Unix")
	}
	var frozen storeContract
	if !generate {
		data, readErr := os.ReadFile(path) //nolint:gosec // fixed fixture path
		if readErr != nil {
			t.Fatalf("read %s: %v (set PORT_GENERATE=1 to create it)", storeCLIContractPath, readErr)
		}
		if err := json.Unmarshal(data, &frozen); err != nil {
			t.Fatal(err)
		}
	}
	cases := storeCases(line)
	if !generate && len(frozen.Cases) != len(cases) {
		t.Fatalf("fixture has %d cases, generator defines %d", len(frozen.Cases), len(cases))
	}
	replayed, skipped := 0, 0
	isRoot := runtime.GOOS != "windows" && os.Geteuid() == 0
	for i, c := range cases {
		switch {
		case c.Host != "" && runtime.GOOS != c.Host:
			t.Logf("SKIP %s: file-name policy of %s", c.Name, c.Host)
		case c.UnixOnly && runtime.GOOS == "windows":
			t.Logf("SKIP %s: Unix path, mode and errno semantics", c.Name)
		case c.NeedsOwn && isRoot:
			if generate {
				t.Fatalf("%s needs a non-root user to generate", c.Name)
			}
			t.Logf("SKIP %s: root ignores file permissions", c.Name)
		default:
			fixture.Cases = append(fixture.Cases, runStoreCase(t, binary, c))
			replayed++
			continue
		}
		skipped++
		fixture.Cases = append(fixture.Cases, frozen.Cases[i])
	}
	t.Logf("store cases: %d total, %d replayed, %d SKIPPED", len(cases), replayed, skipped)
	if replayed == 0 {
		t.Fatal("no store case ran")
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if generate {
		if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // generated contract fixture is intentionally world-readable
			t.Fatal(err)
		}
		t.Logf("wrote %s", storeCLIContractPath)
		return
	}
	want, err := os.ReadFile(path) //nolint:gosec // fixed fixture path
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(data, want) {
		t.Fatal("Go store CLI fixture is stale; regenerate deliberately with PORT_GENERATE=1")
	}
}
