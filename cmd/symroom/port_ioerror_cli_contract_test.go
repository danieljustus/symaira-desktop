package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

const ioErrorCLIContractPath = "testdata/port/room/io-error-cli.json"

// ioErrorSources are the Go files whose error text and call order the fixture
// pins.
var ioErrorSources = []string{
	"cmd/symroom/main.go",
	"cmd/symroom/cmd_run.go",
	"cmd/symroom/cmd_checkpoint.go",
	"cmd/symroom/cmd_note.go",
	"cmd/symroom/cmd_decide.go",
	"cmd/symroom/cmd_member.go",
	"cmd/symroom/cmd_artifact.go",
	"cmd/symroom/cmd_verify.go",
	"cmd/symroom/cmd_log.go",
	"cmd/symroom/cmd_index.go",
	"cmd/symroom/cmd_doctor.go",
	"internal/room/room/journal_append.go",
	"internal/room/room/members.go",
	"internal/room/run/run.go",
	"internal/room/run/checkpoint.go",
	"internal/room/journal/journal.go",
	"internal/room/journal/verifier.go",
	"internal/room/artifact/artifact.go",
	"internal/room/doctor/doctor.go",
}

type ioErrorCLIContract struct {
	SchemaVersion  int               `json:"schema_version"`
	OracleRevision string            `json:"oracle_revision"`
	SourceHashes   map[string]string `json:"source_hashes"`
	IdentityKey    string            `json:"identity_key"`
	RoomTOML       string            `json:"room_toml"`
	// RoomTOMLVariants are the room.toml bodies of the lenient-scanner states.
	RoomTOMLVariants map[string]string `json:"room_toml_variants"`
	Cases            []ioErrorCLICase  `json:"cases"`
}

// ioErrorCLICase records one failing process. The host's wording of an OS error
// is replaced by a symbolic token (<ENOENT>, <ENOTDIR>, <EISDIR>, <EACCES>) and
// the disposable directories by <work> and <link>. Generated event and
// checkpoint ids are masked as <ev_id> and <chk_id>.
//
// UnixOnly marks a vector whose outcome depends on Unix errno semantics that
// Windows does not share (a missing parent directory or a file used as a
// directory is ERROR_PATH_NOT_FOUND there, which Go's syscall.Errno also
// reports as "does not exist"; chmod and symlinks are not portable). Windows
// replays only the vectors without that flag and counts the rest as SKIP, on
// both the Go and the Rust side.
type ioErrorCLICase struct {
	Name     string   `json:"name"`
	State    string   `json:"state"`
	Args     []string `json:"args"`
	UnixOnly bool     `json:"unix_only,omitempty"`
	Stdout   string   `json:"stdout"`
	Stderr   string   `json:"stderr"`
	ExitCode int      `json:"exit_code"`
	Files    []string `json:"files"`
	// Rooms lists `<segment>:<room>` for every event line the command left in
	// journal/*.jsonl, so the room id Go resolved from room.toml is visible.
	Rooms []string `json:"rooms,omitempty"`
}

type ioErrorVector struct {
	state string
	args  []string
}

// ioErrorRoomTOML are the room.toml bodies of the lenient-scanner states. Go's
// ReadRoomConfig is a key=value scanner, not a TOML parser.
var ioErrorRoomTOML = map[string]string{
	"toml-garbage": "this is not toml at all\n",
	"toml-noid":    "created = \"2026-09-23T10:00:00.000Z\"\n",
	"toml-lenient": "id = \"rm_first\"\n[section]\n  id\t= \"\"rm_last # kept\"\"  \r\ncreated=\n???\n",
}

// ioErrorUnixOnly are the states that need Unix errno semantics, chmod or
// symlinks (see ioErrorCLICase).
var ioErrorUnixOnly = map[string]bool{
	"nope": true, "filedir": true, "journalfile": true, "abs-journalfile": true,
	"segperm": true, "pwd-link": true,
}

func ioErrorVectors() []ioErrorVector {
	id := []string{"--identity", "a"}
	with := func(prefix []string, rest ...string) []string {
		out := append([]string(nil), prefix...)
		out = append(out, id...)
		return append(out, rest...)
	}
	read := [][]string{
		{"run", "list"},
		{"run", "list", "--json"},
		{"run", "show", "r1"},
		{"member", "list"},
		{"artifact", "list"},
		{"verify"},
		{"verify", "--json"},
		{"log"},
		{"log", "--json"},
		{"index", "rebuild"},
	}
	write := [][]string{
		with([]string{"run", "request"}, "--title", "t"),
		with([]string{"run", "start"}, "r1"),
		with([]string{"run", "cancel"}, "r1"),
		with([]string{"run", "approve"}, "r1"),
		with([]string{"run", "deny"}, "--reason", "no", "r1"),
		with([]string{"checkpoint", "resolve"}, "--answer", "y", "c1"),
		with([]string{"note"}, "hi"),
		with([]string{"decide"}, "d"),
		with([]string{"member", "remove"}, "mem_x"),
		with([]string{"member", "role"}, "mem_x", "member"),
		with([]string{"artifact", "unlink"}, "art_x"),
	}
	var vectors []ioErrorVector
	add := func(state string, sets ...[][]string) {
		for _, set := range sets {
			for _, args := range set {
				vectors = append(vectors, ioErrorVector{state, args})
			}
		}
	}
	add("empty", read[:3], write[:8], [][]string{with([]string{"artifact", "link"}, "f.txt")})
	add("nope", read[:3], write[:8])
	add("filedir", read, write)
	add("tomldir", read[:3], write[:8])
	add("journalfile", read, write)
	add("artdir", [][]string{
		with([]string{"artifact", "link"}, "f.txt"),
		with([]string{"artifact", "link"}, "../out"),
	})
	add("abs-journalfile", [][]string{{"doctor"}})
	// Lenient room.toml scanner: nothing here is an I/O error, every command
	// must succeed or fail on its own merits.
	lenient := [][]string{{"member", "list"}, {"artifact", "list"}, {"verify"}, {"log"}}
	add("toml-garbage", read[:3], write[:8], lenient)
	add("toml-noid", read[:3], write[:8], lenient)
	add("toml-lenient", read[:3], write[:8])
	// An unreadable segment is wrapped as `read segment <author>: ...`.
	add("segperm", read, write)
	// $PWD naming the symlinked working directory is what Go's Abs prints.
	add("pwd-link", [][]string{{"doctor"}, {"doctor", "--json"}})
	return vectors
}

func TestPortIOErrorCLIContract(t *testing.T) {
	root := memberCLIRoot(t)
	fixture := ioErrorCLIContract{
		SchemaVersion:    1,
		OracleRevision:   fixtureoracle.Current().Commit,
		SourceHashes:     map[string]string{},
		RoomTOML:         "id = \"rm_io_error_fixture\"\ncreated = \"2026-09-23T10:00:00.000Z\"\n",
		RoomTOMLVariants: ioErrorRoomTOML,
	}
	seed := sha256.Sum256([]byte("symroom-io-error-cli-fixture-identity"))
	fixture.IdentityKey = hex.EncodeToString(seed[:])
	for _, source := range ioErrorSources {
		fixture.SourceHashes[source] = noteCLIFileHash(t, root, source)
	}
	path := filepath.Join(root, ioErrorCLIContractPath)
	generate := os.Getenv("PORT_GENERATE") == "1"
	committed := map[string]ioErrorCLICase{}
	if !generate {
		raw, err := os.ReadFile(path) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
		if err != nil {
			t.Fatalf("read %s: %v (set PORT_GENERATE=1 to create it)", ioErrorCLIContractPath, err)
		}
		var want ioErrorCLIContract
		if err := json.Unmarshal(raw, &want); err != nil {
			t.Fatalf("parse %s: %v", ioErrorCLIContractPath, err)
		}
		for _, c := range want.Cases {
			committed[c.Name] = c
		}
	}
	oracle := buildGrammarProcessOracle(t, root)
	var skipped []string
	for _, vector := range ioErrorVectors() {
		name := vector.state + ": " + strings.Join(vector.args, " ")
		if reason := ioErrorHostSkip(t, vector); reason != "" {
			if generate {
				t.Fatalf("cannot generate %q on this host: %s", name, reason)
			}
			// Skip is loud and counted. The committed case stands in so the
			// whole-file comparison below still covers every other case.
			want, ok := committed[name]
			if !ok {
				t.Fatalf("committed fixture has no case %q", name)
			}
			t.Logf("SKIP %s: %s", name, reason)
			skipped = append(skipped, name)
			fixture.Cases = append(fixture.Cases, want)
			continue
		}
		fixture.Cases = append(fixture.Cases, runIOErrorCase(t, oracle, fixture, vector))
	}
	t.Logf("I/O error cases: %d total, %d replayed against Go, %d SKIPPED on %s", len(fixture.Cases), len(fixture.Cases)-len(skipped), len(skipped), runtime.GOOS)
	if len(fixture.Cases) == len(skipped) {
		t.Fatal("every I/O error case was skipped: nothing was verified")
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if generate {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // generated contract fixture is intentionally world-readable
			t.Fatal(err)
		}
		t.Logf("wrote %s (%d cases)", ioErrorCLIContractPath, len(fixture.Cases))
		return
	}
	got, err := os.ReadFile(path) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		var want ioErrorCLIContract
		if json.Unmarshal(got, &want) == nil {
			for i := range min(len(want.Cases), len(fixture.Cases)) {
				w, l := want.Cases[i], fixture.Cases[i]
				if w.Stdout != l.Stdout || w.Stderr != l.Stderr || w.ExitCode != l.ExitCode || strings.Join(w.Files, "|") != strings.Join(l.Files, "|") || strings.Join(w.Rooms, "|") != strings.Join(l.Rooms, "|") {
					t.Logf("case %q drifts: committed exit=%d stderr=%q files=%v rooms=%v; live exit=%d stderr=%q files=%v rooms=%v",
						l.Name, w.ExitCode, w.Stderr, w.Files, w.Rooms, l.ExitCode, l.Stderr, l.Files, l.Rooms)
				}
			}
		}
		t.Fatalf("Go I/O error fixture is stale; regenerate deliberately with PORT_GENERATE=1")
	}
}

// ioErrorHostSkip explains why this host cannot replay a vector, or returns "".
func ioErrorHostSkip(t *testing.T, vector ioErrorVector) string {
	t.Helper()
	if runtime.GOOS == "windows" && ioErrorUnixOnly[vector.state] {
		return "Unix-only vector (errno, chmod or symlink semantics; Windows has no equivalent expectation derivable from the Go source)"
	}
	if vector.state == "segperm" {
		// A privileged user reads a mode-000 file, so nothing fails.
		probe := filepath.Join(t.TempDir(), "segment")
		if err := os.WriteFile(probe, nil, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(probe, 0); err != nil {
			t.Fatal(err)
		}
		if f, err := os.Open(probe); err == nil { //nolint:gosec // probe file is created in a fresh temp directory
			_ = f.Close()
			return "mode 000 files are readable here (privileged user)"
		}
	}
	return ""
}

func runIOErrorCase(t *testing.T, oracle string, fixture ioErrorCLIContract, vector ioErrorVector) ioErrorCLICase {
	t.Helper()
	temp := t.TempDir()
	work := filepath.Join(temp, "work")
	link := filepath.Join(temp, "link")
	home := filepath.Join(temp, "home")
	dataHome := filepath.Join(temp, "data")
	tmp := filepath.Join(temp, "tmp")
	for _, dir := range []string{work, home, dataHome, tmp} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// Go's exec sets PWD to cmd.Dir; it is spelled out because os.Getwd (and so
	// filepath.Abs) prefers $PWD over the kernel's name for the directory.
	cwd := work
	env := []string{
		"HOME=" + home, "USERPROFILE=" + home, "XDG_DATA_HOME=" + dataHome, "TMPDIR=" + tmp,
		"TZ=UTC", "LC_ALL=C", "LANG=C", "PATH=" + tmp, "SYMROOM_IDENTITY_KEY=" + fixture.IdentityKey,
	}
	if systemRoot := os.Getenv("SYSTEMROOT"); systemRoot != "" {
		env = append(env, "SYSTEMROOT="+systemRoot)
	}
	write := func(rel, content string) {
		if err := os.WriteFile(filepath.Join(work, rel), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	mkdir := func(rel string) {
		if err := os.MkdirAll(filepath.Join(work, rel), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	switch vector.state {
	case "empty":
	case "nope":
		env = append(env, "SYMROOM_ROOM_DIR=nope")
	case "filedir":
		write("afile", "x")
		env = append(env, "SYMROOM_ROOM_DIR=afile")
	case "tomldir":
		mkdir("room.toml")
	case "journalfile":
		write("room.toml", fixture.RoomTOML)
		write("journal", "x")
	case "artdir":
		write("room.toml", fixture.RoomTOML)
		mkdir("journal")
		mkdir("f.txt")
	case "abs-journalfile":
		write("room.toml", fixture.RoomTOML)
		write("journal", "x")
		env = append(env, "SYMROOM_ROOM_DIR="+work)
	case "toml-garbage", "toml-noid", "toml-lenient":
		write("room.toml", fixture.RoomTOMLVariants[vector.state])
	case "segperm":
		write("room.toml", fixture.RoomTOML)
		mkdir("journal")
		write("journal/mem_other.jsonl", "")
		if err := os.Chmod(filepath.Join(work, "journal", "mem_other.jsonl"), 0); err != nil {
			t.Fatal(err)
		}
	case "pwd-link":
		write("room.toml", fixture.RoomTOML)
		write("journal", "x")
		if err := os.Symlink(work, link); err != nil {
			t.Fatal(err)
		}
		cwd = link
	default:
		t.Fatalf("unknown state %s", vector.state)
	}
	env = append(env, "PWD="+cwd)
	cmd := exec.Command(oracle, vector.args...) //nolint:gosec // test-only command uses a fixed helper and controlled arguments
	cmd.Dir, cmd.Env = cwd, env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	done := make(chan error, 1)
	if err := cmd.Start(); err != nil {
		t.Fatalf("start Go symroom %v: %v", vector.args, err)
	}
	go func() { done <- cmd.Wait() }()
	var runErr error
	select {
	case runErr = <-done:
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("Go symroom %v did not exit", vector.args)
	}
	exitCode := processExitCode(runErr)
	return ioErrorCLICase{
		Name:     vector.state + ": " + strings.Join(vector.args, " "),
		State:    vector.state,
		Args:     vector.args,
		UnixOnly: ioErrorUnixOnly[vector.state],
		Stdout:   normalizeIOError(stdout.String(), work, link),
		Stderr:   normalizeIOError(stderr.String(), work, link),
		ExitCode: exitCode,
		Files:    listIOErrorFiles(t, work),
		Rooms:    listIOErrorRooms(t, work),
	}
}

var ioErrorGeneratedID = regexp.MustCompile(`\b(ev|chk)_[0-9a-f]{16,}\b`)

// ioErrorSlashLiterals are Go source string literals that contain a '/' which
// is NOT a path separator: doctor's remediation text. Go prints them unchanged
// on every host, so they must not be rewritten to the Windows separator on
// replay. The fixture records each '/' of such a literal as slashToken, while a
// plain '/' means "host path separator" (filepath.Join output). The first
// element is the exact span in internal/room/doctor/doctor.go.
var ioErrorSlashLiterals = []struct{ span, tokenized string }{
	{"create/select", "create" + ioErrorSlashToken + "select"},
	{"rebuild .symroom/index.sqlite", "rebuild .symroom" + ioErrorSlashToken + "index.sqlite"},
}

// ioErrorSlashToken stands for a literal '/' in a recorded stream.
const ioErrorSlashToken = "<slash>"

// normalizeIOError replaces the disposable directories, the generated ids and
// the host wording of the OS errors the fixture provokes, and uses '/' in every
// path. A '/' inside a Go source literal (ioErrorSlashLiterals) is tokenised
// first so that the replay can tell it from a host path separator.
func normalizeIOError(text, work, link string) string {
	for _, literal := range ioErrorSlashLiterals {
		text = strings.ReplaceAll(text, literal.span, literal.tokenized)
	}
	// link is checked first: it is a sibling of work, never a prefix of it.
	text = strings.ReplaceAll(text, link, "<link>")
	text = strings.ReplaceAll(text, work, "<work>")
	text = ioErrorGeneratedID.ReplaceAllString(text, "<${1}_id>")
	if runtime.GOOS == "windows" {
		text = strings.ReplaceAll(text, `\`, "/")
		text = strings.ReplaceAll(text, "The system cannot find the file specified.", "<ENOENT>")
		text = strings.ReplaceAll(text, "The system cannot find the path specified.", "<ENOTDIR>")
		text = strings.ReplaceAll(text, "Incorrect function.", "<EISDIR>")
		return strings.ReplaceAll(text, "Access is denied.", "<EACCES>")
	}
	text = strings.ReplaceAll(text, "no such file or directory", "<ENOENT>")
	text = strings.ReplaceAll(text, "not a directory", "<ENOTDIR>")
	text = strings.ReplaceAll(text, "permission denied", "<EACCES>")
	return strings.ReplaceAll(text, "is a directory", "<EISDIR>")
}

// TestIOErrorSlashLiteralsExistInGoSource keeps ioErrorSlashLiterals honest: each
// tokenised span must still be a literal of the doctor source, so a reworded
// message cannot leave a stale classification behind.
func TestIOErrorSlashLiteralsExistInGoSource(t *testing.T) {
	root := memberCLIRoot(t)
	raw, err := os.ReadFile(filepath.Join(root, "internal", "room", "doctor", "doctor.go")) //nolint:gosec // fixed repository source path
	if err != nil {
		t.Fatal(err)
	}
	for _, literal := range ioErrorSlashLiterals {
		if !strings.Contains(string(raw), literal.span) {
			t.Errorf("slash literal %q is not in internal/room/doctor/doctor.go", literal.span)
		}
		if strings.Contains(literal.tokenized, "/") {
			t.Errorf("tokenised span %q still contains '/'", literal.tokenized)
		}
	}
}

// listIOErrorRooms records the `room` of every event line a command appended.
func listIOErrorRooms(t *testing.T, work string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(work, "journal", "*.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(matches)
	var rooms []string
	for _, match := range matches {
		data, err := os.ReadFile(match) //nolint:gosec // match is a journal segment inside the disposable work directory
		if err != nil {
			continue
		}
		for _, line := range strings.Split(string(data), "\n") {
			if strings.TrimSpace(line) == "" {
				continue
			}
			var event struct {
				Room string `json:"room"`
			}
			if err := json.Unmarshal([]byte(line), &event); err != nil {
				t.Fatalf("journal line in %s: %v", match, err)
			}
			rooms = append(rooms, strings.TrimSuffix(filepath.Base(match), ".jsonl")+":"+event.Room)
		}
	}
	return rooms
}

// listIOErrorFiles records the work directory after the command, so a failed
// command that creates or removes state is visible.
func listIOErrorFiles(t *testing.T, work string) []string {
	t.Helper()
	files := []string{}
	err := filepath.WalkDir(work, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(work, path)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if entry.IsDir() {
			rel += "/"
		}
		files = append(files, rel)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(files)
	return files
}
