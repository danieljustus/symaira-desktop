// sidecar-roundtrip is the executable RUST-005 acceptance gate. It builds
// each sidecar helper once, then drives both helpers as separate processes so
// the suite exercises the actual Go↔Rust SQLite file contract.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"time"
)

type fixture struct {
	SchemaVersion int                      `json:"schema_version"`
	Oracle        map[string]string        `json:"oracle"`
	Source        string                   `json:"source"`
	Documents     []map[string]interface{} `json:"documents"`
	RustUpdate    map[string]interface{}   `json:"rust_update"`
	RustAdded     map[string]interface{}   `json:"rust_added"`
	GoUpdate      map[string]interface{}   `json:"go_update"`
	RustSeed      map[string]interface{}   `json:"rust_seed"`
	GoAdded       map[string]interface{}   `json:"go_added"`
}

type largeCorpusFixture struct {
	SchemaVersion  int                     `json:"schema_version"`
	Oracle         map[string]string       `json:"oracle"`
	DocumentCount  int                     `json:"document_count"`
	SnapshotSHA256 string                  `json:"snapshot_sha256"`
	PathTemplate   string                  `json:"path_template"`
	TitleTemplate  string                  `json:"title_template"`
	SearchCases    []largeCorpusSearchCase `json:"search_cases"`
}

type provenanceFixture struct {
	Oracle map[string]string `json:"oracle"`
}

type largeCorpusSearchCase struct {
	Query         string   `json:"query"`
	ExpectedCount int      `json:"expected_count"`
	ExpectedPaths []string `json:"expected_paths"`
}

type helperResult struct {
	Outcome    string          `json:"outcome"`
	ErrorClass string          `json:"error_class"`
	Busy       bool            `json:"busy"`
	ElapsedMS  int64           `json:"elapsed_ms"`
	Snapshot   json.RawMessage `json:"snapshot"`
	Hits       json.RawMessage `json:"hits"`
}

func main() {
	if err := run(); err != nil {
		fatal(err)
	}
	fmt.Println("sidecar round-trip: PASS")
}

func run() error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	fixturePath := filepath.Join(root, "testdata", "port", "sidecar", "roundtrip.json")
	//nolint:gosec // fixturePath is fixed relative to the repository root
	data, err := os.ReadFile(fixturePath)
	if err != nil {
		return err
	}
	var f fixture
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.UseNumber()
	if err := decoder.Decode(&f); err != nil {
		return err
	}
	if f.SchemaVersion != 1 || f.Source == "" || f.Oracle["commit"] == "" || f.Oracle["release"] == "" {
		return errors.New("roundtrip fixture lacks source-bound provenance")
	}

	work, err := os.MkdirTemp("", "symdesk-sidecar-roundtrip-")
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(work) }()
	goBin := filepath.Join(work, "sidecar-go-helper"+exeSuffix())
	rustBin := filepath.Join(work, "sidecar-rust-helper"+exeSuffix())
	if out, err := runCommand(root, "go", "build", "-o", goBin, "./scripts/rust-port/cmd/sidecar-go-helper"); err != nil {
		return fmt.Errorf("build Go helper: %w\n%s", err, out)
	}
	manifestPath := filepath.Join(root, "Cargo.toml")
	metadata, err := runCommand(root, "cargo", "metadata", "--manifest-path", manifestPath, "--no-deps", "--format-version", "1")
	if err != nil {
		return fmt.Errorf("resolve Rust target directory: %w\n%s", err, metadata)
	}
	var cargoMetadata struct {
		TargetDirectory string `json:"target_directory"`
	}
	if err := json.Unmarshal([]byte(metadata), &cargoMetadata); err != nil {
		return fmt.Errorf("decode Cargo metadata: %w", err)
	}
	if cargoMetadata.TargetDirectory == "" {
		return errors.New("cargo metadata lacks target_directory")
	}
	if out, err := runCommand(root, "cargo", "build", "--manifest-path", manifestPath, "-p", "symdesk-index", "--bin", "sidecar-rust-helper", "--locked"); err != nil {
		return fmt.Errorf("build Rust helper: %w\n%s", err, out)
	}
	builtRust := filepath.Join(cargoMetadata.TargetDirectory, "debug", "sidecar-rust-helper"+exeSuffix())
	if _, err := os.Stat(builtRust); err != nil {
		return fmt.Errorf("rust helper missing after build: %w", err)
	}
	if err := copyFile(rustBin, builtRust); err != nil {
		return err
	}

	if err := roundTripA(work, goBin, rustBin, f); err != nil {
		return fmt.Errorf("round-trip A: %w", err)
	}
	if err := roundTripB(work, goBin, rustBin, f); err != nil {
		return fmt.Errorf("round-trip B: %w", err)
	}
	if err := largeCorpusCases(root, work, goBin, rustBin, f.Oracle); err != nil {
		return fmt.Errorf("large corpus: %w", err)
	}
	if err := rollbackCases(work, goBin, rustBin, f); err != nil {
		return fmt.Errorf("rollback: %w", err)
	}
	if err := corruptionCases(work, goBin, rustBin, f); err != nil {
		return fmt.Errorf("corruption: %w", err)
	}
	if os.Getenv("SIDECAR_NATIVE") == "1" {
		if err := readOnlyCases(work, goBin, rustBin, f); err != nil {
			return fmt.Errorf("read-only: %w", err)
		}
		if err := lockCases(work, goBin, rustBin, f); err != nil {
			return fmt.Errorf("locks: %w", err)
		}
	} else if runtime.GOOS == "windows" {
		fmt.Println("sidecar round-trip: chmod/lock cases delegated to native Windows CI")
	}
	return nil
}

func largeCorpusCases(root, work, goBin, rustBin string, oracle map[string]string) error {
	manifestPath := filepath.Join(root, "testdata", "port", "sidecar", "large-corpus.json")
	//nolint:gosec // manifestPath is fixed relative to the repository root
	data, err := os.ReadFile(manifestPath)
	if err != nil {
		return err
	}
	var manifest largeCorpusFixture
	if err := json.Unmarshal(data, &manifest); err != nil {
		return err
	}
	provenancePath := filepath.Join(root, "testdata", "port", "provenance.json")
	//nolint:gosec // provenancePath is fixed relative to the repository root
	provenanceData, err := os.ReadFile(provenancePath)
	if err != nil {
		return err
	}
	var provenance provenanceFixture
	if err := json.Unmarshal(provenanceData, &provenance); err != nil {
		return err
	}
	if err := validateLargeCorpusManifest(manifest, oracle, provenance.Oracle); err != nil {
		return err
	}

	goDB := filepath.Join(work, "10k Go ✓", "sidecar.db")
	rustDB := filepath.Join(work, "10k Rust ✓", "sidecar.db")
	payload := map[string]interface{}{"manifest": manifestPath}
	goCreated, err := invokeWithin(90*time.Second, goBin, "corpus-create", goDB, payload)
	if err != nil {
		return fmt.Errorf("go corpus create: %w", err)
	}
	rustCreated, err := invokeWithin(90*time.Second, rustBin, "corpus-create", rustDB, payload)
	if err != nil {
		return fmt.Errorf("rust corpus create: %w", err)
	}
	states := []struct {
		name string
		raw  []byte
	}{
		{"Go create", goCreated.Snapshot},
		{"Rust create", rustCreated.Snapshot},
	}
	for _, pair := range []struct {
		name, helper, db string
	}{
		{"Rust reopen Go", rustBin, goDB},
		{"Go reopen Rust", goBin, rustDB},
	} {
		raw, err := snapshot(pair.helper, pair.db)
		if err != nil {
			return fmt.Errorf("%s: %w", pair.name, err)
		}
		states = append(states, struct {
			name string
			raw  []byte
		}{pair.name, raw})
	}
	for _, state := range states {
		digest, err := canonicalJSONDigest(state.raw)
		if err != nil {
			return fmt.Errorf("%s snapshot: %w", state.name, err)
		}
		if digest != manifest.SnapshotSHA256 {
			return fmt.Errorf("%s snapshot digest=%s, want %s", state.name, digest, manifest.SnapshotSHA256)
		}
		if err := verifyLargeCounts(state.raw, manifest.DocumentCount); err != nil {
			return fmt.Errorf("%s: %w", state.name, err)
		}
	}

	for _, test := range manifest.SearchCases {
		var reference []byte
		for _, target := range []struct {
			name, helper, db string
		}{
			{"Go/Go", goBin, goDB},
			{"Rust/Go", rustBin, goDB},
			{"Go/Rust", goBin, rustDB},
			{"Rust/Rust", rustBin, rustDB},
		} {
			result, err := invoke(target.helper, "search", target.db, map[string]interface{}{"query": test.Query})
			if err != nil {
				return fmt.Errorf("%s search %q: %w", target.name, test.Query, err)
			}
			if err := verifySearchResult(result.Hits, test); err != nil {
				return fmt.Errorf("%s search %q: %w", target.name, test.Query, err)
			}
			if reference == nil {
				reference = result.Hits
			} else if !jsonEquivalent(reference, result.Hits) {
				return fmt.Errorf("%s search %q differs from Go-created Go result", target.name, test.Query)
			}
		}
	}
	return nil
}

func validateLargeCorpusManifest(manifest largeCorpusFixture, roundTripOracle, provenanceOracle map[string]string) error {
	if manifest.SchemaVersion != 1 || manifest.DocumentCount != 10_000 || manifest.SnapshotSHA256 == "" {
		return errors.New("large corpus manifest is incomplete or not exactly 10,000 documents")
	}
	if manifest.PathTemplate != "corpus/%05d.md" || manifest.TitleTemplate != "Corpus document %05d" {
		return errors.New("large corpus templates differ from the exact supported grammar")
	}
	pinnedOracle := map[string]string{"commit": "b37ca57258174e2c7f9e321f1418a25c82ce00a6", "release": "post-v0.12.2-security-880"}
	if !reflect.DeepEqual(manifest.Oracle, roundTripOracle) || !reflect.DeepEqual(manifest.Oracle, provenanceOracle) || !reflect.DeepEqual(manifest.Oracle, pinnedOracle) {
		return errors.New("large corpus oracle differs from round-trip, provenance, or pinned oracle")
	}
	if len(manifest.SearchCases) == 0 {
		return errors.New("large corpus must contain search cases")
	}
	for _, test := range manifest.SearchCases {
		if strings.TrimSpace(test.Query) == "" || test.ExpectedCount < 0 || len(test.ExpectedPaths) != test.ExpectedCount {
			return fmt.Errorf("invalid complete expectation for search query %q", test.Query)
		}
	}
	return nil
}

func roundTripA(work, goBin, rustBin string, f fixture) error {
	db := filepath.Join(work, "A path with spaces ✓", "sidecar.db")
	if _, err := invoke(goBin, "create", db, map[string]interface{}{"documents": f.Documents}); err != nil {
		return err
	}
	mutation := map[string]interface{}{"documents": []interface{}{f.RustUpdate, f.RustAdded}, "delete": []string{"linked.md"}}
	if _, err := invoke(rustBin, "mutate", db, mutation); err != nil {
		return err
	}
	a, err := snapshot(goBin, db)
	if err != nil {
		return err
	}
	b, err := snapshot(rustBin, db)
	if err != nil {
		return err
	}
	if !jsonEquivalent(a, b) {
		return errors.New("go reopen snapshot differs from Rust snapshot")
	}
	if err := verifyNames(a, []string{"go-seed.md", "nullable.md", "rust-added.md", "unknown-time.md"}); err != nil {
		return err
	}
	if err := verifyAbsent(a, "linked.md"); err != nil {
		return err
	}
	if err := verifyFile(a, "go-seed.md", "Go Seed Rust Updated", int64(1768478400123456790)); err != nil {
		return err
	}
	if err := verifyFile(a, "rust-added.md", "Rust Added", int64(1768478401123456789)); err != nil {
		return err
	}
	for _, query := range []string{"vineyard", "Rust Added", "missing"} {
		if err := compareSearch(goBin, rustBin, db, query); err != nil {
			return err
		}
	}
	if err := verifyTimesAndNulls(a); err != nil {
		return err
	}
	if _, err := invoke(rustBin, "mutate", db, mutation); err != nil {
		return fmt.Errorf("repeat identical Rust mutation: %w", err)
	}
	afterNoop, err := snapshot(goBin, db)
	if err != nil {
		return err
	}
	if !jsonEquivalent(a, afterNoop) {
		return errors.New("repeated identical Rust mutation changed logical state")
	}
	if r, err := invoke(goBin, "integrity", db, nil); err != nil || r.Outcome != "ok" {
		return fmt.Errorf("go integrity failed: %v", err)
	}
	if r, err := invoke(rustBin, "integrity", db, nil); err != nil || r.Outcome != "ok" {
		return fmt.Errorf("rust integrity failed: %v", err)
	}
	return nil
}

func roundTripB(work, goBin, rustBin string, f fixture) error {
	db := filepath.Join(work, "B path with spaces ✓", "sidecar.db")
	if _, err := invoke(rustBin, "create", db, map[string]interface{}{"documents": []map[string]interface{}{f.RustSeed, f.Documents[1], f.Documents[2]}}); err != nil {
		return err
	}
	mutation := map[string]interface{}{"documents": []interface{}{f.GoUpdate, f.GoAdded}, "delete": []string{"linked.md"}}
	if _, err := invoke(goBin, "mutate", db, mutation); err != nil {
		return err
	}
	a, err := snapshot(goBin, db)
	if err != nil {
		return err
	}
	b, err := snapshot(rustBin, db)
	if err != nil {
		return err
	}
	if !jsonEquivalent(a, b) {
		return errors.New("rust reopen snapshot differs from Go snapshot")
	}
	if err := verifyNames(a, []string{"rust-seed.md", "nullable.md", "go-added.md"}); err != nil {
		return err
	}
	if err := verifyAbsent(a, "linked.md"); err != nil {
		return err
	}
	if err := verifyFile(a, "rust-seed.md", "Rust Seed Go Updated", int64(1768478402123456789)); err != nil {
		return err
	}
	if err := verifyFile(a, "go-added.md", "Go Added", int64(1768478403123456789)); err != nil {
		return err
	}
	if err := compareSearch(goBin, rustBin, db, "Go addition"); err != nil {
		return err
	}
	if _, err := invoke(goBin, "mutate", db, mutation); err != nil {
		return fmt.Errorf("repeat identical Go mutation: %w", err)
	}
	afterNoop, err := snapshot(rustBin, db)
	if err != nil {
		return err
	}
	if !jsonEquivalent(a, afterNoop) {
		return errors.New("repeated identical Go mutation changed logical state")
	}
	return nil
}

func rollbackCases(work, goBin, rustBin string, f fixture) error {
	for _, origin := range []string{"go", "rust"} {
		db := filepath.Join(work, "rollback "+origin, "sidecar.db")
		creator := goBin
		other := rustBin
		if origin == "rust" {
			creator, other = rustBin, goBin
		}
		if _, err := invoke(creator, "create", db, map[string]interface{}{"documents": f.Documents}); err != nil {
			return err
		}
		before, err := snapshot(creator, db)
		if err != nil {
			return err
		}
		bad := map[string]interface{}{"path": "go-seed.md", "markdown": "---\ntitle: Duplicate Link\n---\npartial\n", "mtime_ns": int64(1768478400123456789), "links": []string{"same-target", "same-target"}}
		for _, helper := range []string{creator, other} {
			r, err := invokeResult(helper, "rollback", db, map[string]interface{}{"documents": []interface{}{bad}})
			if err != nil {
				return err
			}
			if r.Outcome != "error" || r.ErrorClass != "constraint" {
				return fmt.Errorf("duplicate-link rollback class=%q outcome=%q", r.ErrorClass, r.Outcome)
			}
			after, err := snapshot(helper, db)
			if err != nil {
				return err
			}
			if !jsonEquivalent(before, after) {
				return errors.New("failed rollback left database residue")
			}
		}
	}
	return nil
}

func corruptionCases(work, goBin, rustBin string, f fixture) error {
	source := filepath.Join(work, "corruption-source.db")
	if _, err := invoke(goBin, "create", source, map[string]interface{}{"documents": f.Documents}); err != nil {
		return err
	}
	//nolint:gosec // source is the locally created round-trip database
	original, err := os.ReadFile(source)
	if err != nil {
		return err
	}
	originalHash := digest(original)
	for name, mutate := range map[string]func([]byte) []byte{
		"header": func(b []byte) []byte {
			out := append([]byte(nil), b...)
			copy(out[:16], []byte("not-a-sqlite-db!"))
			return out
		},
		"truncated": func(b []byte) []byte { return append([]byte(nil), b[:min(len(b), 100)]...) },
		"payload": func(b []byte) []byte {
			out := append([]byte(nil), b...)
			if len(out) > 4096 {
				// Flip the b-tree page-type byte on a payload page. The header
				// remains a valid SQLite file, but integrity_check must reject it.
				out[4096] ^= 0x01
			}
			return out
		},
	} {
		for _, helper := range []string{goBin, rustBin} {
			path := filepath.Join(work, name+"-"+filepath.Base(helper)+".db")
			if err := os.WriteFile(path, mutate(original), 0600); err != nil {
				return err
			}
			r, err := invokeResult(helper, "open-check", path, nil)
			if err != nil {
				return err
			}
			if r.Outcome == "ok" {
				r, err = invokeResult(helper, "integrity", path, nil)
				if err != nil {
					return err
				}
			}
			if r.Outcome != "error" || r.ErrorClass != "corrupt" {
				return fmt.Errorf("%s classified as %q", name, r.ErrorClass)
			}
			//nolint:gosec // path is a locally created corruption probe
			got, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if digest(got) != digest(mutate(original)) || digest(original) != originalHash {
				return errors.New("corruption probe changed original bytes")
			}
		}
	}
	return nil
}

func readOnlyCases(work, goBin, rustBin string, f fixture) error {
	db := filepath.Join(work, "readonly.db")
	if _, err := invoke(goBin, "create", db, map[string]interface{}{"documents": f.Documents}); err != nil {
		return err
	}
	before, err := snapshot(goBin, db)
	if err != nil {
		return err
	}
	if err := setReadOnly(db, true); err != nil {
		return err
	}
	defer func() { _ = setReadOnly(db, false) }()
	for _, helper := range []string{goBin, rustBin} {
		r, err := invokeResult(helper, "open-check", db, nil)
		if err != nil {
			return err
		}
		if r.Outcome == "ok" {
			m, err := invokeResult(helper, "writer", db, map[string]interface{}{"documents": []interface{}{f.RustAdded}})
			if err != nil {
				return err
			}
			if m.Outcome != "error" || m.ErrorClass != "readonly" {
				return fmt.Errorf("read-only mutation classified %q", m.ErrorClass)
			}
		} else if r.ErrorClass != "readonly" {
			return fmt.Errorf("read-only open classified %q", r.ErrorClass)
		}
		after, err := snapshot(goBin, db)
		if err != nil {
			return err
		}
		if !jsonEquivalent(before, after) {
			return errors.New("read-only database changed")
		}
	}
	vault := filepath.Join(work, "read-only source")
	if err := os.MkdirAll(vault, 0700); err != nil {
		return err
	}
	src := filepath.Join(vault, "note.md")
	if err := os.WriteFile(src, []byte("---\ntitle: Source\n---\nold\n"), 0600); err != nil {
		return err
	}
	refresh := map[string]interface{}{"vault": vault}
	if _, err := invoke(goBin, "refresh", filepath.Join(work, "source.db"), refresh); err != nil {
		return err
	}
	if err := os.WriteFile(src, []byte("---\ntitle: Source\n---\nnew\n"), 0600); err != nil {
		return err
	}
	if err := setReadOnly(src, true); err != nil {
		return err
	}
	defer func() { _ = setReadOnly(src, false) }()
	infoBefore, err := os.Stat(src)
	if err != nil {
		return err
	}
	//nolint:gosec // src is a locally created read-only source fixture
	bytesBefore, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	for _, helper := range []string{rustBin, goBin} {
		if _, err := invoke(helper, "refresh", filepath.Join(work, "source.db"), refresh); err != nil {
			return err
		}
	}
	infoAfter, err := os.Stat(src)
	if err != nil {
		return err
	}
	//nolint:gosec // src is a locally created read-only source fixture
	bytesAfter, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	if !bytes.Equal(bytesBefore, bytesAfter) || infoBefore.Size() != infoAfter.Size() || !infoBefore.ModTime().Equal(infoAfter.ModTime()) {
		return errors.New("read-only source bytes or metadata changed")
	}

	stale := map[string]interface{}{"path": "stale-after-refresh.md", "markdown": "---\ntitle: Stale\n---\nstale\n", "mtime_ns": int64(1768478400000000000)}
	if _, err := invoke(goBin, "mutate", filepath.Join(work, "source.db"), map[string]interface{}{"documents": []interface{}{stale}}); err != nil {
		return err
	}
	beforePrune, err := snapshot(goBin, filepath.Join(work, "source.db"))
	if err != nil {
		return err
	}
	if _, err := invoke(goBin, "prune", filepath.Join(work, "source.db"), map[string]interface{}{"vault": vault}); err != nil {
		return err
	}
	afterPrune, err := snapshot(rustBin, filepath.Join(work, "source.db"))
	if err != nil {
		return err
	}
	if jsonEquivalent(beforePrune, afterPrune) {
		return errors.New("prune did not mutate the sidecar")
	}
	if err := verifyAbsent(afterPrune, "stale-after-refresh.md"); err != nil {
		return err
	}
	return nil
}

func lockCases(work, goBin, rustBin string, f fixture) error {
	db := filepath.Join(work, "locks.db")
	if _, err := invoke(goBin, "create", db, map[string]interface{}{"documents": f.Documents}); err != nil {
		return err
	}
	for _, pair := range [][2]string{{goBin, rustBin}, {rustBin, goBin}} {
		if err := lockPair(work, db, pair[0], pair[1], f, false); err != nil {
			return err
		}
		if err := lockPair(work, db, pair[0], pair[1], f, true); err != nil {
			return err
		}
	}
	return nil
}
func lockPair(work, db, holder, writer string, f fixture, timeout bool) error {
	d := filepath.Join(work, fmt.Sprintf("lock-%d", time.Now().UnixNano()))
	ready, goFile, release := d+"-ready", d+"-go", d+"-release"
	in := map[string]interface{}{"ready": ready, "go": goFile, "hold_ms": 1500}
	if timeout {
		in["hold_ms"] = 6000
		in["release"] = ""
	} else {
		in["release"] = release
	}
	input, err := writeInput(work, in)
	if err != nil {
		return err
	}
	//nolint:gosec // holder is one of the two locally built helper binaries
	cmd := exec.Command(holder, "lock-holder", "--db", db, "--input", input)
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return err
	}
	defer func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	if err := waitPath(ready, 5*time.Second); err != nil {
		return err
	}
	if err := os.WriteFile(goFile, []byte("go\n"), 0600); err != nil {
		return err
	}
	var r helperResult
	var invokeErr error
	done := make(chan struct{})
	writerCtx, cancelWriter := context.WithCancel(context.Background())
	defer cancelWriter()
	go func() {
		r, invokeErr = invokeResultContext(writerCtx, writer, "writer", db, map[string]interface{}{"documents": []interface{}{f.RustAdded}})
		close(done)
	}()
	if !timeout {
		time.Sleep(800 * time.Millisecond)
		if err := os.WriteFile(release, []byte("release\n"), 0600); err != nil {
			return err
		}
	}
	select {
	case <-done:
	case <-time.After(8 * time.Second):
		cancelWriter()
		<-done
		return errors.New("writer exceeded lock bound")
	}
	if invokeErr != nil {
		return invokeErr
	}
	if timeout {
		if r.Outcome != "error" || !r.Busy || r.ErrorClass != "locked" || r.ElapsedMS < 4500 || r.ElapsedMS > 7500 {
			return fmt.Errorf("timeout lock result %+v", r)
		}
	} else {
		if r.Outcome != "ok" || r.ElapsedMS < 250 {
			return fmt.Errorf("early lock result %+v", r)
		}
	}
	if err := cmd.Wait(); err != nil && !timeout {
		return err
	}
	return nil
}

func invoke(bin, command, db string, payload interface{}) (helperResult, error) {
	return invokeWithin(30*time.Second, bin, command, db, payload)
}

func invokeWithin(timeout time.Duration, bin, command, db string, payload interface{}) (helperResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	r, err := invokeResultContext(ctx, bin, command, db, payload)
	if err != nil {
		return r, err
	}
	if err := requireSuccess(r); err != nil {
		return r, fmt.Errorf("%s %s: %w", filepath.Base(bin), command, err)
	}
	return r, nil
}

func requireSuccess(result helperResult) error {
	if result.Outcome != "ok" {
		return fmt.Errorf("helper outcome=%q class=%q", result.Outcome, result.ErrorClass)
	}
	return nil
}

func invokeResult(bin, command, db string, payload interface{}) (helperResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	return invokeResultContext(ctx, bin, command, db, payload)
}

func invokeResultContext(ctx context.Context, bin, command, db string, payload interface{}) (helperResult, error) {
	var r helperResult
	input := ""
	var err error
	if payload != nil {
		input, err = writeInput(os.TempDir(), payload)
		if err != nil {
			return r, err
		}
		defer func() { _ = os.Remove(input) }()
	}
	args := []string{command, "--db", db}
	if input != "" {
		args = append(args, "--input", input)
	}
	//nolint:gosec // bin is one of the two locally built helper binaries
	c := exec.CommandContext(ctx, bin, args...)
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	err = c.Run()
	if err != nil {
		return r, fmt.Errorf("%s %s: %w: %s", filepath.Base(bin), command, err, stderr.String())
	}
	lines := strings.Split(strings.TrimSpace(stdout.String()), "\n")
	if len(lines) == 0 || lines[len(lines)-1] == "" {
		return r, fmt.Errorf("%s %s emitted no JSON", bin, command)
	}
	if err := json.Unmarshal([]byte(lines[len(lines)-1]), &r); err != nil {
		return r, fmt.Errorf("invalid helper JSON: %w (%s)", err, stdout.String())
	}
	return r, nil
}
func snapshot(bin, db string) ([]byte, error) {
	r, err := invoke(bin, "snapshot", db, nil)
	if err != nil {
		return nil, err
	}
	if r.Outcome != "ok" {
		return nil, fmt.Errorf("snapshot outcome %q", r.Outcome)
	}
	return r.Snapshot, nil
}
func compareSearch(a, b, db, q string) error {
	payload := map[string]interface{}{"query": q}
	ra, err := invoke(a, "search", db, payload)
	if err != nil {
		return err
	}
	rb, err := invoke(b, "search", db, payload)
	if err != nil {
		return err
	}
	if !jsonEquivalent(ra.Hits, rb.Hits) {
		return fmt.Errorf("search %q differs", q)
	}
	return nil
}
func canonicalJSONDigest(raw []byte) (string, error) {
	var value interface{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&value); err != nil {
		return "", err
	}
	canonical, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return digest(canonical), nil
}

func verifyLargeCounts(raw []byte, documentCount int) error {
	var state map[string]json.RawMessage
	if err := json.Unmarshal(raw, &state); err != nil {
		return err
	}
	want := map[string]int{
		"files":      documentCount,
		"properties": documentCount*5 + 3,
		"links":      1,
		"fts_search": documentCount,
		"fts_norm":   documentCount,
		"fts_tri":    documentCount,
	}
	for key, expected := range want {
		var rows []json.RawMessage
		if err := json.Unmarshal(state[key], &rows); err != nil {
			return fmt.Errorf("decode %s rows: %w", key, err)
		}
		if len(rows) != expected {
			return fmt.Errorf("%s row count=%d, want %d", key, len(rows), expected)
		}
	}
	return nil
}

func verifySearchResult(raw []byte, test largeCorpusSearchCase) error {
	var hits []struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(raw, &hits); err != nil {
		return err
	}
	if len(hits) != test.ExpectedCount {
		return fmt.Errorf("hit count=%d, want %d", len(hits), test.ExpectedCount)
	}
	if len(test.ExpectedPaths) > 0 {
		paths := make([]string, len(hits))
		for index, hit := range hits {
			paths[index] = hit.Path
		}
		if !reflect.DeepEqual(paths, test.ExpectedPaths) {
			return fmt.Errorf("hit paths=%v, want %v", paths, test.ExpectedPaths)
		}
	}
	return nil
}

func verifyNames(raw []byte, want []string) error {
	var s map[string]interface{}
	if err := json.Unmarshal(raw, &s); err != nil {
		return err
	}
	files, ok := s["files"].([]interface{})
	if !ok {
		return errors.New("snapshot files are missing or malformed")
	}
	if len(files) != len(want) {
		return fmt.Errorf("snapshot has %d files, want exactly %d", len(files), len(want))
	}
	got := map[string]bool{}
	for _, v := range files {
		m, ok := v.(map[string]interface{})
		if !ok {
			return errors.New("snapshot file row is malformed")
		}
		path, ok := m["path"].(string)
		if !ok {
			return errors.New("snapshot file path is malformed")
		}
		got[path] = true
	}
	for _, p := range want {
		if !got[p] {
			return fmt.Errorf("missing path %s", p)
		}
	}
	return nil
}

func verifyFile(raw []byte, path, title string, mtimeNS int64) error {
	var s map[string]interface{}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	if err := decoder.Decode(&s); err != nil {
		return err
	}
	files, ok := s["files"].([]interface{})
	if !ok {
		return errors.New("snapshot files are missing or malformed")
	}
	for _, value := range files {
		row, ok := value.(map[string]interface{})
		if !ok || row["path"] != path {
			continue
		}
		if row["title"] != title {
			return fmt.Errorf("%s title=%v, want %q", path, row["title"], title)
		}
		mtime, ok := row["mtime_ns"].(json.Number)
		gotMtime, err := mtime.Int64()
		if !ok || err != nil || gotMtime != mtimeNS {
			return fmt.Errorf("%s mtime_ns=%v, want %d", path, row["mtime_ns"], mtimeNS)
		}
		return nil
	}
	return fmt.Errorf("missing expected file %s", path)
}
func jsonEquivalent(a, b []byte) bool {
	var left, right interface{}
	if json.Unmarshal(a, &left) != nil || json.Unmarshal(b, &right) != nil {
		return false
	}
	return reflect.DeepEqual(left, right)
}

func verifyAbsent(raw []byte, unwanted string) error {
	var s map[string]interface{}
	if err := json.Unmarshal(raw, &s); err != nil {
		return err
	}
	files, _ := s["files"].([]interface{})
	for _, v := range files {
		if m, ok := v.(map[string]interface{}); ok && m["path"] == unwanted {
			return fmt.Errorf("unexpected path %s", unwanted)
		}
	}
	return nil
}

func verifyTimesAndNulls(raw []byte) error {
	var s map[string]interface{}
	if err := json.Unmarshal(raw, &s); err != nil {
		return err
	}
	files := s["files"].([]interface{})
	foundNullable, foundUnknown := false, false
	for _, v := range files {
		m := v.(map[string]interface{})
		if m["path"] == "nullable.md" {
			foundNullable = true
			for _, k := range []string{"document_date", "person", "status", "due_date", "confidence", "ocr_json_path", "simhash", "asn"} {
				if m[k] != nil {
					return fmt.Errorf("nullable %s is not NULL", k)
				}
			}
			if m["created_at"] != "" {
				return errors.New("empty created_at was not preserved as TEXT")
			}
		}
		if m["path"] == "unknown-time.md" {
			foundUnknown = true
			if m["mtime_ns"] != nil {
				return errors.New("unknown mtime was not preserved as NULL")
			}
		}
	}
	if !foundNullable || !foundUnknown {
		return errors.New("NULL/time fixture rows are missing")
	}
	return nil
}
func writeInput(dir string, v interface{}) (string, error) {
	if err := os.MkdirAll(dir, 0700); err != nil {
		return "", err
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	f, err := os.CreateTemp(dir, "sidecar-op-*.json")
	if err != nil {
		return "", err
	}
	name := f.Name()
	if _, err = f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(name)
		return "", err
	}
	if err = f.Close(); err != nil {
		return "", err
	}
	return name, nil
}
func runCommand(dir, bin string, args ...string) (string, error) {
	//nolint:gosec // bin is a fixed compiler/tool executable selected by this harness
	c := exec.Command(bin, args...)
	c.Dir = dir
	// Keep machine-readable stdout separate from toolchain diagnostics.
	var stderr bytes.Buffer
	c.Stderr = &stderr
	out, err := c.Output()
	if err != nil {
		return string(out), fmt.Errorf("%w\nstderr: %s", err, stderr.String())
	}
	return string(out), err
}
func copyFile(dst, src string) error {
	//nolint:gosec // src is the fixed local Cargo build output
	b, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	//nolint:gosec // dst is a local temporary helper executable path
	if err := os.WriteFile(dst, b, 0600); err != nil {
		return err
	}
	return os.Chmod(dst, 0700) //nolint:gosec // helper must be executable
}
func digest(b []byte) string { h := sha256.Sum256(b); return hex.EncodeToString(h[:]) }
func waitPath(path string, d time.Duration) error {
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if _, err := os.Stat(path); err == nil {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("timeout waiting for %s", path)
}
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
func exeSuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}
func fatal(err error) { fmt.Fprintln(os.Stderr, "sidecar round-trip: FAIL:", err); os.Exit(1) }
