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
	Cases          []ioErrorCLICase  `json:"cases"`
}

// ioErrorCLICase records one failing process. The host's wording of an OS error
// is replaced by a symbolic token (<ENOENT>, <ENOTDIR>, <EISDIR>) and the
// disposable work directory by <work>, so one fixture serves every native OS;
// the replay expands both for its own host.
type ioErrorCLICase struct {
	Name     string   `json:"name"`
	State    string   `json:"state"`
	Args     []string `json:"args"`
	Stdout   string   `json:"stdout"`
	Stderr   string   `json:"stderr"`
	ExitCode int      `json:"exit_code"`
	Files    []string `json:"files"`
}

type ioErrorVector struct {
	state string
	args  []string
}

// ioErrorStates names the disposable room layouts. "nope" and "filedir" point
// SYMROOM_ROOM_DIR at a missing path and at a regular file; "abs-journalfile"
// passes the room as an absolute path (the doctor resolves paths with Abs).
var ioErrorStates = []string{"empty", "nope", "filedir", "tomldir", "journalfile", "artdir", "abs-journalfile"}

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
	return vectors
}

func TestPortIOErrorCLIContract(t *testing.T) {
	root := memberCLIRoot(t)
	fixture := ioErrorCLIContract{
		SchemaVersion:  1,
		OracleRevision: fixtureoracle.Current().Commit,
		SourceHashes:   map[string]string{},
		RoomTOML:       "id = \"rm_io_error_fixture\"\ncreated = \"2026-09-23T10:00:00.000Z\"\n",
	}
	seed := sha256.Sum256([]byte("symroom-io-error-cli-fixture-identity"))
	fixture.IdentityKey = hex.EncodeToString(seed[:])
	for _, source := range ioErrorSources {
		fixture.SourceHashes[source] = noteCLIFileHash(t, root, source)
	}
	oracle := buildGrammarProcessOracle(t, root)
	for _, vector := range ioErrorVectors() {
		fixture.Cases = append(fixture.Cases, runIOErrorCase(t, oracle, fixture, vector))
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(root, ioErrorCLIContractPath)
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil { //nolint:gosec // generated contract fixture is intentionally world-readable
			t.Fatal(err)
		}
		t.Logf("wrote %s", ioErrorCLIContractPath)
		return
	}
	got, err := os.ReadFile(path) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
	if err != nil {
		t.Fatalf("read %s: %v (set PORT_GENERATE=1 to create it)", ioErrorCLIContractPath, err)
	}
	if !bytes.Equal(got, data) {
		var want ioErrorCLIContract
		if json.Unmarshal(got, &want) == nil {
			for i := range min(len(want.Cases), len(fixture.Cases)) {
				w, l := want.Cases[i], fixture.Cases[i]
				if w.Stdout != l.Stdout || w.Stderr != l.Stderr || w.ExitCode != l.ExitCode || strings.Join(w.Files, "|") != strings.Join(l.Files, "|") {
					t.Logf("case %q drifts: committed exit=%d stderr=%q files=%v; live exit=%d stderr=%q files=%v",
						l.Name, w.ExitCode, w.Stderr, w.Files, l.ExitCode, l.Stderr, l.Files)
				}
			}
		}
		t.Fatalf("Go I/O error fixture is stale; regenerate deliberately with PORT_GENERATE=1")
	}
}

func runIOErrorCase(t *testing.T, oracle string, fixture ioErrorCLIContract, vector ioErrorVector) ioErrorCLICase {
	t.Helper()
	temp := t.TempDir()
	work := filepath.Join(temp, "work")
	home := filepath.Join(temp, "home")
	dataHome := filepath.Join(temp, "data")
	tmp := filepath.Join(temp, "tmp")
	for _, dir := range []string{work, home, dataHome, tmp} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
	}
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
	default:
		t.Fatalf("unknown state %s", vector.state)
	}
	cmd := exec.Command(oracle, vector.args...) //nolint:gosec // test-only command uses a fixed helper and controlled arguments
	cmd.Dir, cmd.Env = work, env
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
		Stdout:   normalizeIOError(stdout.String(), work),
		Stderr:   normalizeIOError(stderr.String(), work),
		ExitCode: exitCode,
		Files:    listIOErrorFiles(t, work),
	}
}

// normalizeIOError replaces the disposable work directory and the host wording
// of the three OS errors the fixture provokes, and uses '/' in every path.
func normalizeIOError(text, work string) string {
	text = strings.ReplaceAll(text, work, "<work>")
	if runtime.GOOS == "windows" {
		text = strings.ReplaceAll(text, `\`, "/")
		text = strings.ReplaceAll(text, "The system cannot find the file specified.", "<ENOENT>")
		text = strings.ReplaceAll(text, "The system cannot find the path specified.", "<ENOTDIR>")
		text = strings.ReplaceAll(text, "Incorrect function.", "<EISDIR>")
		return text
	}
	text = strings.ReplaceAll(text, "no such file or directory", "<ENOENT>")
	text = strings.ReplaceAll(text, "not a directory", "<ENOTDIR>")
	return strings.ReplaceAll(text, "is a directory", "<EISDIR>")
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
