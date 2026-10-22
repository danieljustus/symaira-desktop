package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/inventory"
)

const flagGrammarCLIContractPath = "testdata/port/room/flag-grammar-cli.json"

// flagGrammarSources are the Go files whose flag definitions the fixture pins.
var flagGrammarSources = []string{
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
	"cmd/symroom/cmd_identity.go",
	"cmd/symroom/cmd_init.go",
	"cmd/symroom/cmd_doctor.go",
	"cmd/symroom/cmd_watch.go",
	"cmd/symroom/cmd_brainprofile.go",
	"cmd/symroom/cmd_mcp.go",
	"cmd/symroom/cmd_version.go",
}

type flagGrammarCLIContract struct {
	SchemaVersion  int                  `json:"schema_version"`
	OracleRevision string               `json:"oracle_revision"`
	SourceHashes   map[string]string    `json:"source_hashes"`
	IdentityKey    string               `json:"identity_key"`
	RoomTOML       string               `json:"room_toml"`
	Cases          []flagGrammarCLICase `json:"cases"`
}

// flagGrammarCLICase records one process. State "empty" is a directory without
// room.toml, "room" one with room.toml and an empty journal directory. A case
// whose output names a host OS error (<ENOENT> and friends) is UnixOnly: the
// wording is tokenised but the semantics differ on Windows.
type flagGrammarCLICase struct {
	Name     string   `json:"name"`
	State    string   `json:"state"`
	Args     []string `json:"args"`
	UnixOnly bool     `json:"unix_only,omitempty"`
	Stdout   string   `json:"stdout"`
	Stderr   string   `json:"stderr"`
	ExitCode int      `json:"exit_code"`
	Files    []string `json:"files"`
	Rooms    []string `json:"rooms,omitempty"`
}

type flagGrammarVector struct {
	state string
	args  []string
}

// flagGrammarPath is one command path of the parser grammar with its flags.
type flagGrammarPath struct {
	path  []string
	flags []inventory.SymRoomFlag
}

func flagGrammarPaths(doc inventory.SymRoomGrammarDocument) []flagGrammarPath {
	var paths []flagGrammarPath
	for _, sub := range doc.Subcommands {
		if len(sub.Actions) == 0 {
			paths = append(paths, flagGrammarPath{path: []string{sub.Name}, flags: sub.Flags})
			continue
		}
		for _, action := range sub.Actions {
			if sub.Name == "index" {
				// `index` has no flag set; `index rebuild <anything>` opens a
				// SQLite database whose success depends on the host's
				// temporary-directory permissions, so only the usage path
				// `index <anything>` is pinned here (index-cli.json pins the
				// rebuild).
				paths = append(paths, flagGrammarPath{path: []string{sub.Name}})
				break
			}
			paths = append(paths, flagGrammarPath{path: []string{sub.Name, action.Name}, flags: action.Flags})
		}
	}
	return paths
}

// flagGrammarSkipsProceeding lists the paths whose success path is not a
// deterministic, terminating process result (`identity create` writes a random
// identity, `run wait` polls for up to 15 minutes for a run that never appears).
func flagGrammarSkipsProceeding(path []string) bool {
	joined := strings.Join(path, " ")
	return joined == "identity create" || joined == "run wait"
}

// flagGrammarVectors derives argv from the Go flag definitions: for every
// command path the shapes the standard `flag` package parses specially (a
// `--flag=value` form, a lone `-`, the `--` terminator, malformed names,
// help spellings), and for every flag its type's value forms.
func flagGrammarVectors(doc inventory.SymRoomGrammarDocument) []flagGrammarVector {
	var vectors []flagGrammarVector
	for _, p := range flagGrammarPaths(doc) {
		path := p.path
		proceeds := !flagGrammarSkipsProceeding(path)
		add := func(args ...string) {
			full := append(append([]string(nil), path...), args...)
			vectors = append(vectors, flagGrammarVector{"empty", full})
			if proceeds {
				vectors = append(vectors, flagGrammarVector{"room", full})
			}
		}
		// Shapes that always fail while parsing, in any state.
		failing := func(args ...string) {
			full := append(append([]string(nil), path...), args...)
			vectors = append(vectors, flagGrammarVector{"empty", full})
		}
		failing("--zzz=1")
		failing("-zzz=1")
		failing("-zzz")
		failing("---zzz")
		failing("-=x")
		failing("--=x")
		failing("-h=1")
		failing("-help=1")
		failing("--zzz", "x")
		if proceeds {
			add("-")
			add("--")
			add("--", "x")
			add("--", "--zzz")
			add("-", "--zzz")
			add("x", "--zzz")
			add("x", "-")
			add("x", "--")
		}
		for _, flag := range p.flags {
			name := flag.Name
			switch flag.Type {
			case "bool":
				failing("--"+name+"=x", "--zzz")
				failing("--" + name + "=")
				failing("-" + name + "=maybe")
				failing("--"+name, "--zzz")
				failing("--"+name+"=true", "--zzz")
				if proceeds {
					add("--" + name + "=false")
					add("-" + name + "=1")
					add("--" + name + "=T")
					add("--" + name + "=TRUE")
					add("--" + name + "=0")
					add("--" + name + "=f")
					add("--"+name, "false")
					add("--"+name, "--")
					add("--"+name, "-")
					add("--"+name+"=true", "--"+name+"=false")
					add("--"+name, "x", "--"+name)
				}
			case "int":
				failing("--" + name + "=x")
				failing("--" + name + "=")
				failing("--" + name + "=1.5")
				failing("--" + name)
				failing("--"+name+"=99999999999999999999", "--zzz")
				failing("--" + name + "=1__0")
				failing("--" + name + "=1_")
				failing("--" + name + "=0b2")
				failing("--"+name+"=1", "--zzz")
				if proceeds {
					add("--" + name + "=0x10")
					add("--" + name + "=1_0")
					add("--" + name + "=+3")
					add("--" + name + "=-3")
					add("--" + name + "=010")
					add("--"+name, "5")
					add("--"+name, "-")
				}
			case "duration":
				failing("--" + name + "=x")
				failing("--" + name + "=")
				failing("--" + name + "=5")
				failing("--" + name)
				failing("--"+name+"=1s", "--zzz")
				if proceeds {
					add("--" + name + "=1h30m")
					add("--" + name + "=-5s")
					add("--" + name + "=1.5s")
					add("--" + name + "=100ms")
					add("--" + name + "=1µs")
				}
			default:
				failing("--" + name)
				failing("-" + name)
				failing("--"+name, "--zzz")
				failing("--"+name+"=x", "--zzz")
				failing("--"+name+"=", "--zzz")
				if proceeds {
					add("--" + name + "=")
					add("--"+name, "")
					add("--" + name + "=x")
					add("-"+name, "x")
					add("--"+name, "--")
					add("--"+name, "-")
					add("--"+name, "--help")
					add("--"+name+"=a", "--"+name+"=b")
					add("--"+name, "x", "y")
					add("--"+name, "x", "--"+name, "y")
				}
			}
		}
	}
	return dedupeFlagGrammarVectors(vectors)
}

func dedupeFlagGrammarVectors(vectors []flagGrammarVector) []flagGrammarVector {
	seen := map[string]bool{}
	var out []flagGrammarVector
	for _, vector := range vectors {
		key := vector.state + "\x00" + strings.Join(vector.args, "\x00")
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, vector)
	}
	return out
}

// TestPortFlagGrammarCLIContract records the shipped Go process result for the
// flag shapes of the standard `flag` package across every command path
// (ROOMCLI-001): exit code, both streams and the files left behind.
func TestPortFlagGrammarCLIContract(t *testing.T) {
	root := memberCLIRoot(t)
	doc, err := buildSymRoomGrammar(fixtureoracle.Current())
	if err != nil {
		t.Fatal(err)
	}
	fixture := flagGrammarCLIContract{
		SchemaVersion:  1,
		OracleRevision: fixtureoracle.Current().Commit,
		SourceHashes:   map[string]string{},
		RoomTOML:       "id = \"rm_flag_grammar_fixture\"\ncreated = \"2026-09-23T10:00:00.000Z\"\n",
	}
	seed := sha256.Sum256([]byte("symroom-flag-grammar-cli-fixture-identity"))
	fixture.IdentityKey = hex.EncodeToString(seed[:])
	for _, source := range flagGrammarSources {
		fixture.SourceHashes[source] = noteCLIFileHash(t, root, source)
	}
	path := filepath.Join(root, flagGrammarCLIContractPath)
	generate := os.Getenv("PORT_GENERATE") == "1"
	committed := map[string]flagGrammarCLICase{}
	if !generate {
		raw, err := os.ReadFile(path) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
		if err != nil {
			t.Fatalf("read %s: %v (set PORT_GENERATE=1 to create it)", flagGrammarCLIContractPath, err)
		}
		var want flagGrammarCLIContract
		if err := json.Unmarshal(raw, &want); err != nil {
			t.Fatalf("parse %s: %v", flagGrammarCLIContractPath, err)
		}
		for _, c := range want.Cases {
			committed[c.Name] = c
		}
	}
	oracle := buildGrammarProcessOracle(t, root)
	var skipped []string
	for _, vector := range flagGrammarVectors(doc) {
		name := vector.state + ": " + strings.Join(vector.args, " ")
		if vector.state == "room" && !generate {
			// A room-state case exists only where parsing succeeded; the
			// committed fixture decides which ones those are.
			if _, ok := committed[name]; !ok {
				continue
			}
		}
		if want, ok := committed[name]; ok && runtime.GOOS == "windows" && want.UnixOnly {
			t.Logf("SKIP %s: Unix-only vector (errno semantics)", name)
			skipped = append(skipped, name)
			fixture.Cases = append(fixture.Cases, want)
			continue
		}
		c := runFlagGrammarCase(t, oracle, fixture, vector)
		if vector.state == "room" && strings.Contains(c.Stderr, "Usage of ") {
			// Identical to the empty-state parse failure: not kept.
			continue
		}
		fixture.Cases = append(fixture.Cases, c)
	}
	t.Logf("flag grammar cases: %d total, %d replayed against Go, %d SKIPPED on %s", len(fixture.Cases), len(fixture.Cases)-len(skipped), len(skipped), runtime.GOOS)
	if len(fixture.Cases) == len(skipped) {
		t.Fatal("every flag grammar case was skipped: nothing was verified")
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
		t.Logf("wrote %s (%d cases)", flagGrammarCLIContractPath, len(fixture.Cases))
		return
	}
	got, err := os.ReadFile(path) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, data) {
		var want flagGrammarCLIContract
		if json.Unmarshal(got, &want) == nil {
			t.Logf("committed %d cases, live %d", len(want.Cases), len(fixture.Cases))
			for i := range min(len(want.Cases), len(fixture.Cases)) {
				w, l := want.Cases[i], fixture.Cases[i]
				if w.Name != l.Name || w.Stdout != l.Stdout || w.Stderr != l.Stderr || w.ExitCode != l.ExitCode || strings.Join(w.Files, "|") != strings.Join(l.Files, "|") || strings.Join(w.Rooms, "|") != strings.Join(l.Rooms, "|") {
					t.Logf("case %q drifts: committed exit=%d stderr=%q; live exit=%d stderr=%q", l.Name, w.ExitCode, truncateForDrift(w.Stderr), l.ExitCode, truncateForDrift(l.Stderr))
				}
			}
		}
		t.Fatalf("Go flag grammar fixture is stale; regenerate deliberately with PORT_GENERATE=1")
	}
}

var (
	flagGrammarRoomID   = regexp.MustCompile(`\brm_[0-9a-f]{16}\b`)
	flagGrammarMemberID = regexp.MustCompile(`\bmem_[0-9a-f]{16}\b`)
)

// normalizeFlagGrammar tokenises the disposable directory, host OS errors and
// the generated ids (`init` mints a random room id). The link placeholder of
// normalizeIOError is unused here: its replacement of an empty link would
// interleave the token between every character.
func normalizeFlagGrammar(text, work string, args []string) string {
	text = normalizeIOError(text, work, "\x00unused-link")
	text = flagGrammarRoomID.ReplaceAllString(text, "<rm_id>")
	if len(args) >= 2 && args[0] == "identity" && args[1] == "create" {
		// `identity create` mints a random member id; the name it echoes is
		// the first positional, which is what the fixture pins.
		text = flagGrammarMemberID.ReplaceAllString(text, "<mem_id>")
	}
	return text
}

func runFlagGrammarCase(t *testing.T, oracle string, fixture flagGrammarCLIContract, vector flagGrammarVector) flagGrammarCLICase {
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
		"PWD=" + work,
	}
	if systemRoot := os.Getenv("SYSTEMROOT"); systemRoot != "" {
		env = append(env, "SYSTEMROOT="+systemRoot)
	}
	switch vector.state {
	case "empty":
	case "room":
		if err := os.WriteFile(filepath.Join(work, "room.toml"), []byte(fixture.RoomTOML), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Join(work, "journal"), 0o700); err != nil {
			t.Fatal(err)
		}
	default:
		t.Fatalf("unknown state %s", vector.state)
	}
	cmd := exec.Command(oracle, vector.args...) //nolint:gosec // test-only command uses a fixed helper and controlled arguments
	cmd.Dir, cmd.Env = work, env
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Start(); err != nil {
		t.Fatalf("start Go symroom %v: %v", vector.args, err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var runErr error
	select {
	case runErr = <-done:
	case <-time.After(20 * time.Second):
		_ = cmd.Process.Kill()
		t.Fatalf("Go symroom %v did not exit", vector.args)
	}
	c := flagGrammarCLICase{
		Name:     vector.state + ": " + strings.Join(vector.args, " "),
		State:    vector.state,
		Args:     vector.args,
		Stdout:   normalizeFlagGrammar(stdout.String(), work, vector.args),
		Stderr:   normalizeFlagGrammar(stderr.String(), work, vector.args),
		ExitCode: processExitCode(runErr),
		Files:    listIOErrorFiles(t, work),
	}
	for _, room := range listIOErrorRooms(t, work) {
		c.Rooms = append(c.Rooms, normalizeFlagGrammar(room, work, vector.args))
	}
	for _, token := range []string{"<ENOENT>", "<ENOTDIR>", "<EISDIR>", "<EACCES>"} {
		if strings.Contains(c.Stdout, token) || strings.Contains(c.Stderr, token) {
			c.UnixOnly = true
		}
	}
	// No flag grammar case prints a path, so a '/' left after normalisation is
	// a Go source literal that ioErrorSlashLiterals has not classified: the
	// replay would turn it into a backslash on Windows. Fail instead of
	// recording it.
	for _, stream := range []string{c.Stdout, c.Stderr} {
		if strings.Contains(stream, "/") {
			t.Fatalf("%s prints an unclassified '/': add the Go literal to ioErrorSlashLiterals (host path separators belong in the io-error fixture)\n%s", c.Name, stream)
		}
	}
	return c
}
