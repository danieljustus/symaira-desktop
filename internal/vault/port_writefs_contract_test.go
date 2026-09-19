package vault

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
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
)

// TestPortVaultWriteFilesystemContract is the Go-owned filesystem harness for
// the vault write stack (contract row VAULT-004). It records, for every case,
// the exact bytes, permission bits, content hashes and the resulting file set
// produced by the real Go implementation of
//
//	writeFileAtomic  — internal/vault/vault.go (temp file, fsync, rename)
//	ParseFile        — the reader that must never observe a partial write
//	Walk/WalkAll     — the walker that must never surface temporary files
//
// plus the interruption and read-only-filesystem behaviour of the same code
// path. The fixture is replayed byte-for-byte by
// crates/symdesk-vault/tests/filesystem_write_contracts.rs.
//
// Regenerate deliberately with:
//
//	PORT_GENERATE=1 go test -count=1 ./internal/vault -run TestPortVaultWriteFilesystemContract
func TestPortVaultWriteFilesystemContract(t *testing.T) {
	fixture := buildWriteFilesystemFixture(t)
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')

	path := filepath.Clean(filepath.Join("..", "..", "testdata", "port", "vault", "filesystem-writes.json"))
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	//nolint:gosec // fixture path is fixed relative to the repository
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v (run PORT_GENERATE=1 go test ./internal/vault -run TestPortVaultWriteFilesystemContract)", err)
	}
	if !bytes.Equal(filterPlatform(current, runtime.GOOS), filterPlatform(encoded, runtime.GOOS)) {
		t.Fatal("vault write filesystem fixture is stale; regenerate deliberately from the pinned Go oracle")
	}
}

const writeFilesystemFixtureRel = "testdata/port/vault/filesystem-writes.json"

type writeFilesystemFixture struct {
	SchemaVersion int                 `json:"schema_version"`
	Oracle        writeFilesystemAide `json:"oracle"`
	TempPattern   tempNamePattern     `json:"temp_file_pattern"`
	SourceHashes  map[string]string   `json:"source_hashes"`
	Cases         []writeFilesystem   `json:"cases"`
}

type writeFilesystemAide struct {
	// Commit is the pinned Go behaviour oracle; the generator refuses to run
	// for a different revision so a fixture can never be captured against
	// repository-local changes.
	Commit  string `json:"commit"`
	Release string `json:"release"`
}

// tempNamePattern documents the temporary-file naming contract shared by the Go
// writer and the Rust port: a hidden name in the target directory with a fixed
// prefix and suffix, so crashes leave recognisable, ignorable leftovers.
type tempNamePattern struct {
	Prefix string `json:"prefix"`
	Suffix string `json:"suffix"`
}

type writeFilesystem struct {
	ID          string           `json:"id"`
	Operation   string           `json:"operation"`
	Platform    string           `json:"platform"`
	Setup       writeSetup       `json:"setup"`
	Args        writeArgs        `json:"args"`
	Before      writeState       `json:"before"`
	After       writeState       `json:"after"`
	ErrorClass  string           `json:"error_class"`
	ErrorPrefix string           `json:"error_prefix"`
	Target      *writeFileRecord `json:"target,omitempty"`
	TargetKind  string           `json:"target_kind"`
	Parse       *writeParse      `json:"parse,omitempty"`
	WalkPaths   []string         `json:"walk_paths,omitempty"`
	Invariant   *writeInvariant  `json:"invariant,omitempty"`
}

type writeSetup struct {
	DirMode uint32            `json:"dir_mode"`
	Entries []writeSetupEntry `json:"entries"`
}

type writeSetupEntry struct {
	Path          string  `json:"path"`
	Kind          string  `json:"kind"`
	Mode          *uint32 `json:"mode,omitempty"`
	ContentBase64 string  `json:"content_base64,omitempty"`
}

type writeArgs struct {
	Path         string  `json:"path"`
	DataBase64   string  `json:"data_base64,omitempty"`
	DataSHA256   string  `json:"data_sha256,omitempty"`
	DataLength   int     `json:"data_length,omitempty"`
	DataRepeat   string  `json:"data_repeat,omitempty"`
	ExistingMode *uint32 `json:"existing_mode,omitempty"`
}

type writeState struct {
	Markdown       []writeFileRecord `json:"markdown"`
	OtherFileCount int               `json:"other_file_count"`
	OtherDirCount  int               `json:"other_dir_count"`
	TempLeftovers  []string          `json:"temp_leftovers"`
}

type writeFileRecord struct {
	Path          string  `json:"path"`
	Mode          *uint32 `json:"mode,omitempty"`
	Size          int64   `json:"size"`
	SHA256        string  `json:"sha256"`
	ContentBase64 string  `json:"content_base64,omitempty"`
}

type writeParse struct {
	Title  string `json:"title"`
	Body   string `json:"body"`
	SHA256 string `json:"sha256"`
	Error  string `json:"error"`
}

type writeInvariant struct {
	Trials      int `json:"trials"`
	TornTargets int `json:"torn_targets"`
	// OldStates and NewStates are per-run observations of a killed writer and
	// are deliberately not part of the fixture: recording them would make the
	// document unstable. Only the invariant is contractual.
	OldStates       int    `json:"-"`
	NewStates       int    `json:"-"`
	Statement       string `json:"statement"`
	CompletionCheck string `json:"completion_check"`
}

// filterPlatform drops unix-only cases from a fixture document and clears
// permission bits so the drift check can run unchanged on Windows, where those
// cases cannot execute and modes are not observable.
func filterPlatform(document []byte, goos string) []byte {
	if goos != "windows" {
		return document
	}
	var value writeFilesystemFixture
	if err := json.Unmarshal(document, &value); err != nil {
		return document
	}
	kept := value.Cases[:0]
	for _, item := range value.Cases {
		if item.Platform != "unix" {
			kept = append(kept, item)
		}
	}
	value.Cases = kept
	for index := range value.Cases {
		normalizeUnixModes(&value.Cases[index])
	}
	encoded, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return document
	}
	return append(encoded, '\n')
}

// normalizeUnixModes clears every recorded permission bit for a case.
func normalizeUnixModes(item *writeFilesystem) {
	item.Args.ExistingMode = nil
	for _, state := range []*writeState{&item.Before, &item.After} {
		for index := range state.Markdown {
			state.Markdown[index].Mode = nil
		}
	}
	if item.Target != nil {
		item.Target.Mode = nil
	}
}

const (
	writeFilesystemOracleCommit  = "745c08e8144971c61133c5d0e5d61c7ce405aad2"
	writeFilesystemOracleRelease = "post-v0.12.2-security-880"
	tempNamePrefix               = ".symdesk-frontmatter-"
	tempNameSuffix               = ".tmp"
	interruptTrials              = 12
)

func buildWriteFilesystemFixture(t *testing.T) writeFilesystemFixture {
	t.Helper()
	cases := []writeFilesystem{
		runAtomicCase(t, atomicCase{
			id:      "atomic-create-new",
			path:    "note.md",
			data:    []byte("---\ntitle: created\n---\nbody\n"),
			dirMode: 0o750,
		}),
		runAtomicCase(t, atomicCase{
			id:         "atomic-overwrite-existing",
			path:       "note.md",
			data:       []byte("---\ntitle: replaced\n---\nnew body\n"),
			dirMode:    0o750,
			existing:   []byte("---\ntitle: original\n---\nold body\n"),
			existingMD: 0o640,
		}),
		runAtomicCase(t, atomicCase{
			id:         "atomic-overwrite-empty-payload",
			path:       "note.md",
			data:       []byte{},
			dirMode:    0o750,
			existing:   []byte("---\ntitle: original\n---\nold body\n"),
			existingMD: 0o640,
		}),
		runAtomicCase(t, atomicCase{
			id:      "atomic-write-invalid-utf8",
			path:    "note.md",
			data:    append([]byte("---\ntitle: opaque\n---\n"), 0xff, 0xfe, 'x', '\n'),
			dirMode: 0o750,
		}),
		runAtomicCase(t, atomicCase{
			id:         "atomic-write-large-payload",
			path:       "note.md",
			data:       bytes.Repeat([]byte(largePayloadUnit), 2600),
			dataRepeat: largePayloadUnit,
			dirMode:    0o750,
		}),
		runAtomicCase(t, atomicCase{
			id:      "atomic-missing-parent",
			path:    filepath.ToSlash(filepath.Join("missing", "note.md")),
			data:    []byte("---\ntitle: nowhere\n---\n"),
			dirMode: 0o750,
		}),
		runAtomicCase(t, atomicCase{
			id:            "atomic-target-is-directory",
			path:          "note.md",
			data:          []byte("---\ntitle: cannot replace a directory\n---\n"),
			dirMode:       0o750,
			seedDirectory: true,
		}),
		runAtomicCase(t, atomicCase{
			id:           "atomic-parent-read-only",
			path:         filepath.ToSlash(filepath.Join("locked", "note.md")),
			data:         []byte("---\ntitle: read only parent\n---\n"),
			dirMode:      0o750,
			platform:     "unix",
			lockedParent: true,
		}),
		runAtomicCase(t, atomicCase{
			id:         "atomic-target-read-only-file",
			path:       "note.md",
			data:       []byte("---\ntitle: replaced despite read-only target\n---\n"),
			dirMode:    0o750,
			platform:   "unix",
			existing:   []byte("---\ntitle: read only\n---\nold body\n"),
			existingMD: 0o400,
		}),
		runAtomicCase(t, atomicCase{
			id:        "atomic-stale-temp-present",
			path:      "note.md",
			data:      []byte("---\ntitle: write beside a stale temp\n---\n"),
			dirMode:   0o750,
			staleTemp: []byte("stale partial bytes"),
		}),
		runCrashRecoveryCase(t),
		runInterruptionCase(t),
	}
	return writeFilesystemFixture{
		SchemaVersion: 1,
		Oracle: writeFilesystemAide{
			Commit:  writeFilesystemOracleCommit,
			Release: writeFilesystemOracleRelease,
		},
		TempPattern:  tempNamePattern{Prefix: tempNamePrefix, Suffix: tempNameSuffix},
		SourceHashes: writeFilesystemSourceHashes(t),
		Cases:        cases,
	}
}

func writeFilesystemSourceHashes(t *testing.T) map[string]string {
	t.Helper()
	hashes := map[string]string{}
	for _, rel := range []string{"internal/vault/vault.go", "internal/vault/root.go"} {
		//nolint:gosec // fixed repository-relative source paths
		data, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		hashes[rel] = hex.EncodeToString(sum[:])
	}
	return hashes
}

// writeArgsOf keeps the fixture small: payloads above the inline threshold are
// pinned by length and SHA-256 instead of being embedded verbatim.
func writeArgsOf(path string, data []byte, repeat string, existingMode *uint32) writeArgs {
	args := writeArgs{Path: path, DataLength: len(data), ExistingMode: existingMode}
	switch {
	case repeat != "":
		if !bytes.Equal(data, bytes.Repeat([]byte(repeat), len(data)/len(repeat))) {
			panic("large payload must be an exact repetition of its recorded unit")
		}
		args.DataRepeat = repeat
	case len(data) <= 2048:
		args.DataBase64 = base64.StdEncoding.EncodeToString(data)
	default:
		panic("payload above the inline threshold needs a repeat unit")
	}
	if repeat != "" {
		sum := sha256.Sum256(data)
		args.DataSHA256 = hex.EncodeToString(sum[:])
	}
	return args
}

// largePayloadUnit is the repeat unit of the payload that exceeds the inline
// fixture threshold; it is recorded so the Rust replay rebuilds the same bytes.
const largePayloadUnit = "symdesk-payload-0123456789\n"

// interruptPayloadUnit is the repeat unit of the payload written by the killed
// writer process.
const interruptPayloadUnit = "interrupted-payload-0123456789\n"

type atomicCase struct {
	id            string
	path          string
	data          []byte
	dataRepeat    string
	dirMode       uint32
	platform      string
	existing      []byte
	existingMD    uint32
	seedDirectory bool
	staleTemp     []byte
	lockedParent  bool
}

func runAtomicCase(t *testing.T, spec atomicCase) writeFilesystem {
	t.Helper()
	root := t.TempDir()
	if canonical, err := filepath.EvalSymlinks(root); err == nil {
		root = canonical
	}
	platform := spec.platform
	if platform == "" {
		platform = "any"
	}
	if spec.lockedParent {
		dir := filepath.Join(root, "locked")
		if err := os.MkdirAll(dir, 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dir, 0o500); err != nil {
			t.Fatal(err)
		}
		//nolint:gosec // 0o500 is the read-only case under test
		t.Cleanup(func() { _ = os.Chmod(dir, 0o750) })
	} else if err := os.Chmod(root, os.FileMode(spec.dirMode)); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(root, filepath.FromSlash(spec.path))
	switch {
	case spec.seedDirectory:
		if err := os.MkdirAll(target, 0o750); err != nil {
			t.Fatal(err)
		}
	case spec.existing != nil:
		if err := os.WriteFile(target, spec.existing, os.FileMode(spec.existingMD)); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(target, os.FileMode(spec.existingMD)); err != nil {
			t.Fatal(err)
		}
	}
	if spec.staleTemp != nil {
		stale := filepath.Join(root, tempNamePrefix+"stale"+tempNameSuffix)
		if err := os.WriteFile(stale, spec.staleTemp, 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(stale, 0o600); err != nil {
			t.Fatal(err)
		}
	}

	before := writeStateOf(t, root)
	err := writeFileAtomic(target, spec.data)

	result := writeFilesystem{
		ID:          spec.id,
		Operation:   "atomic_write",
		Platform:    platform,
		Setup:       writeSetup{DirMode: spec.dirMode},
		Args:        writeArgsOf(spec.path, spec.data, spec.dataRepeat, optionalMode(spec.existingMD, spec.existing != nil)),
		Before:      before,
		After:       writeStateOf(t, root),
		ErrorClass:  writeFilesystemErrorClass(err),
		ErrorPrefix: writeFilesystemErrorPrefix(err),
		TargetKind:  "file",
	}
	if record, ok := readFileRecord(root, spec.path); ok {
		result.Target = &record
	}
	if spec.seedDirectory {
		result.TargetKind = "directory"
	}
	result.WalkPaths = walkMarkdownPaths(t, root)
	return result
}

// runCrashRecoveryCase records the reader behaviour for a crash state: the
// target still holds the previous content and a partial temporary file is left
// behind. Nothing may lose the previous content, and the walker must not
// surface the temporary file as a document.
func runCrashRecoveryCase(t *testing.T) writeFilesystem {
	t.Helper()
	root := t.TempDir()
	if canonical, err := filepath.EvalSymlinks(root); err == nil {
		root = canonical
	}
	target := filepath.Join(root, "note.md")
	old := []byte("---\ntitle: before crash\n---\ncomplete body\n")
	if err := os.WriteFile(target, old, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o640); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(root, tempNamePrefix+"crash"+tempNameSuffix)
	if err := os.WriteFile(stale, []byte("---\ntitle: partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(stale, 0o600); err != nil {
		t.Fatal(err)
	}

	before := writeStateOf(t, root)
	parsed, parseErr := ParseFile(target)
	parse := &writeParse{Error: ""}
	if parseErr != nil {
		parse.Error = parseErr.Error()
	} else {
		parse.Title = parsed.Title
		parse.Body = parsed.Body
		sum := sha256.Sum256([]byte(parsed.Body))
		parse.SHA256 = hex.EncodeToString(sum[:])
	}
	walkBefore := walkMarkdownPaths(t, root)

	data := []byte("---\ntitle: after crash recovery\n---\nrecovered body\n")
	err := writeFileAtomic(target, data)
	result := writeFilesystem{
		ID:          "crash-recovery-leftover-temp",
		Operation:   "atomic_write_after_crash",
		Platform:    "any",
		Setup:       writeSetup{DirMode: 0o750},
		Args:        writeArgs{Path: "note.md", DataBase64: base64.StdEncoding.EncodeToString(data)},
		Before:      before,
		After:       writeStateOf(t, root),
		ErrorClass:  writeFilesystemErrorClass(err),
		ErrorPrefix: writeFilesystemErrorPrefix(err),
		Parse:       parse,
		TargetKind:  "file",
	}
	if record, ok := readFileRecord(root, "note.md"); ok {
		result.Target = &record
	}
	result.WalkPaths = walkBefore
	if len(result.After.Markdown) != 1 || result.After.Markdown[0].Path != "note.md" {
		t.Fatalf("crash recovery changed the markdown file set: %#v", result.After.Markdown)
	}
	return result
}

// runInterruptionCase kills a real writer process mid-operation and records
// whether the target ever held anything other than a complete old or a
// complete new document. Only invariants are recorded (never byte counts) so
// the fixture stays reproducible across runs and machines.
func runInterruptionCase(t *testing.T) writeFilesystem {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("interruption case requires POSIX signals")
	}
	root := t.TempDir()
	if canonical, err := filepath.EvalSymlinks(root); err == nil {
		root = canonical
	}
	target := filepath.Join(root, "note.md")
	old := []byte("---\ntitle: before interruption\n---\ncomplete old body\n")
	data := bytes.Repeat([]byte(interruptPayloadUnit), 40000)
	if err := os.WriteFile(target, old, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(target, 0o640); err != nil {
		t.Fatal(err)
	}

	invariant := &writeInvariant{
		Trials:          interruptTrials,
		Statement:       "the target is always either the complete previous bytes or the complete new bytes; a killed writer never leaves a partial or truncated target",
		CompletionCheck: "the writer signals completion of its first full write before it is killed",
	}
	for trial := 0; trial < interruptTrials; trial++ {
		state := interruptWriter(t, root, target, old, data)
		switch state {
		case interruptOld:
			invariant.OldStates++
		case interruptNew:
			invariant.NewStates++
		case interruptTorn:
			invariant.TornTargets++
		}
		if err := os.WriteFile(target, old, 0o640); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(target, 0o640); err != nil {
			t.Fatal(err)
		}
	}
	if invariant.TornTargets != 0 {
		t.Fatalf("interruption exposed torn target content in %d of %d trials", invariant.TornTargets, invariant.Trials)
	}
	if invariant.OldStates+invariant.NewStates != invariant.Trials {
		t.Fatalf("interruption classification lost trials: %#v", invariant)
	}

	result := writeFilesystem{
		ID:         "interruption-kill-mid-write",
		Operation:  "kill_writer",
		Platform:   "unix",
		Setup:      writeSetup{DirMode: 0o750},
		Args:       writeArgsOf("note.md", data, interruptPayloadUnit, nil),
		Before:     writeState{Markdown: []writeFileRecord{mustRecord(t, "note.md", old, 0o640)}, TempLeftovers: []string{}},
		After:      writeState{Markdown: []writeFileRecord{mustRecord(t, "note.md", old, 0o640)}, TempLeftovers: []string{}},
		Invariant:  invariant,
		TargetKind: "file",
	}
	return result
}

type interruptOutcome int

const (
	interruptOld interruptOutcome = iota
	interruptNew
	interruptTorn
)

// interruptWriter starts the helper process, waits until it has completed one
// full atomic write, kills it, and classifies what the target holds afterwards.
func interruptWriter(t *testing.T, root, target string, old, data []byte) interruptOutcome {
	t.Helper()
	ready := filepath.Join(root, tempNamePrefix+"ready"+tempNameSuffix)
	payload := filepath.Join(root, tempNamePrefix+"payload"+tempNameSuffix)
	_ = os.Remove(ready)
	if err := os.WriteFile(payload, data, 0o600); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // re-executing the test binary under the current toolchain
	command := exec.Command(os.Args[0], "-test.run=^TestPortVaultWriteFilesystemInterruptWriter$", "-test.v")
	command.Env = append(os.Environ(),
		"SYMDESK_PORT_INTERRUPT_WRITER=1",
		"SYMDESK_PORT_INTERRUPT_TARGET="+target,
		"SYMDESK_PORT_INTERRUPT_READY="+ready,
		"SYMDESK_PORT_INTERRUPT_PAYLOAD_FILE="+payload,
	)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		if time.Now().After(deadline) {
			_ = command.Process.Kill()
			_ = command.Wait()
			t.Fatalf("interruption writer never completed a first write: %s", output.String())
		}
		time.Sleep(2 * time.Millisecond)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = command.Wait()
	_ = os.Remove(ready)

	//nolint:gosec // target is the case file created above
	actual, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read interrupted target: %v", err)
	}
	switch {
	case bytes.Equal(actual, old):
		return interruptOld
	case bytes.Equal(actual, data):
		return interruptNew
	default:
		return interruptTorn
	}
}

// TestPortVaultWriteFilesystemInterruptWriter is the helper process body. It is
// inert unless the parent starts it with SYMDESK_PORT_INTERRUPT_WRITER=1.
func TestPortVaultWriteFilesystemInterruptWriter(t *testing.T) {
	if os.Getenv("SYMDESK_PORT_INTERRUPT_WRITER") != "1" {
		t.Skip("helper process body")
	}
	target := os.Getenv("SYMDESK_PORT_INTERRUPT_TARGET")
	ready := os.Getenv("SYMDESK_PORT_INTERRUPT_READY")
	//nolint:gosec // payload and paths are supplied by the owning test process
	data, err := os.ReadFile(os.Getenv("SYMDESK_PORT_INTERRUPT_PAYLOAD_FILE"))
	if err != nil {
		t.Fatal(err)
	}
	first := true
	for {
		if err := writeFileAtomic(target, data); err != nil {
			t.Fatalf("writer failed: %v", err)
		}
		if first {
			first = false
			if err := os.WriteFile(ready, []byte("done"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func writeStateOf(t *testing.T, root string) writeState {
	t.Helper()
	state := writeState{Markdown: []writeFileRecord{}, TempLeftovers: []string{}}
	//nolint:gosec // root is a private test directory
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		relative = filepath.ToSlash(relative)
		if relative == "." {
			return nil
		}
		if entry.IsDir() {
			state.OtherDirCount++
			return nil
		}
		if strings.HasPrefix(entry.Name(), tempNamePrefix) && strings.HasSuffix(entry.Name(), tempNameSuffix) {
			state.TempLeftovers = append(state.TempLeftovers, relative)
			return nil
		}
		if !strings.HasSuffix(relative, ".md") {
			state.OtherFileCount++
			return nil
		}
		record, err := fileRecord(root, relative)
		if err != nil {
			return err
		}
		state.Markdown = append(state.Markdown, record)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(state.TempLeftovers)
	sort.Slice(state.Markdown, func(i, j int) bool { return state.Markdown[i].Path < state.Markdown[j].Path })
	return state
}

func fileRecord(root, relative string) (writeFileRecord, error) {
	path := filepath.Join(root, filepath.FromSlash(relative))
	//nolint:gosec // relative path is produced by the case walker
	data, err := os.ReadFile(path)
	if err != nil {
		return writeFileRecord{}, err
	}
	record := recordFor(relative, data)
	if mode := permissionBits(path); mode != nil {
		record.Mode = mode
	}
	return record, nil
}

func recordFor(relative string, data []byte) writeFileRecord {
	sum := sha256.Sum256(data)
	record := writeFileRecord{
		Path:   relative,
		Size:   int64(len(data)),
		SHA256: hex.EncodeToString(sum[:]),
	}
	if len(data) <= 2048 {
		record.ContentBase64 = base64.StdEncoding.EncodeToString(data)
	}
	return record
}

func mustRecord(t *testing.T, relative string, data []byte, mode uint32) writeFileRecord {
	t.Helper()
	record := recordFor(relative, data)
	if runtime.GOOS != "windows" {
		value := mode
		record.Mode = &value
	}
	return record
}

func readFileRecord(root, relative string) (writeFileRecord, bool) {
	info, err := os.Stat(filepath.Join(root, filepath.FromSlash(relative)))
	if err != nil || info.IsDir() {
		return writeFileRecord{}, false
	}
	record, err := fileRecord(root, relative)
	if err != nil {
		return writeFileRecord{}, false
	}
	return record, true
}

func walkMarkdownPaths(t *testing.T, root string) []string {
	t.Helper()
	paths := []string{}
	//nolint:gosec // root is a private test directory
	err := WalkAll(root, func(path string, entry fs.DirEntry) error {
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		paths = append(paths, filepath.ToSlash(relative))
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(paths)
	if paths == nil {
		paths = []string{}
	}
	return paths
}

func permissionBits(path string) *uint32 {
	if runtime.GOOS == "windows" {
		return nil
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil
	}
	mode := uint32(info.Mode().Perm())
	return &mode
}

func optionalMode(mode uint32, present bool) *uint32 {
	if !present || runtime.GOOS == "windows" {
		return nil
	}
	value := mode
	return &value
}

// writeFilesystemErrorClass is deliberately coarse: the fixture pins the exact
// Go error prefix separately, and the class only tells the Rust replay which
// stage must fail.
func writeFilesystemErrorClass(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	switch {
	case strings.HasPrefix(message, "create temp file: "):
		return "create_temp"
	case strings.HasPrefix(message, "write temp file: "):
		return "write_temp"
	case strings.HasPrefix(message, "sync temp file: "):
		return "sync_temp"
	case strings.HasPrefix(message, "close temp file: "):
		return "close_temp"
	case strings.HasPrefix(message, "rename temp file: "):
		return "rename_temp"
	default:
		return "filesystem"
	}
}

func writeFilesystemErrorPrefix(err error) string {
	if err == nil {
		return ""
	}
	message := err.Error()
	if index := strings.Index(message, ": "); index >= 0 {
		return message[:index+2]
	}
	return ""
}
