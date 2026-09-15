// Command historygen runs the live Go history engine oracle to generate an exact execution report.
package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/history"
)

const (
	defaultOracleCommit  = "982fe718f2d64629102b4078b1d65a46645c90c5"
	defaultOracleRelease = "unreleased-982fe718"
)

type Document struct {
	SchemaVersion   int               `json:"schema_version"`
	Oracle          Oracle            `json:"oracle"`
	SourceHashes    map[string]string `json:"source_hashes"`
	InitialFiles    []FileSpec        `json:"initial_files"`
	Operations      []OperationRecord `json:"operations"`
	FinalFilesystem []FsEntry         `json:"final_filesystem"`
}

type Oracle struct {
	Commit  string `json:"commit"`
	Release string `json:"release"`
}

type FileSpec struct {
	Path          string `json:"path"`
	Content       string `json:"content,omitempty"`
	ContentBase64 string `json:"content_base64,omitempty"`
}

type EntryDTO struct {
	ID        string `json:"id"`
	Timestamp string `json:"timestamp"`
	Size      int64  `json:"size"`
}

type OperationRecord struct {
	Step                int         `json:"step"`
	Op                  string      `json:"op"`
	Path                string      `json:"path,omitempty"`
	ID                  string      `json:"id,omitempty"`
	Content             string      `json:"content,omitempty"`
	ContentBase64       string      `json:"content_base64,omitempty"`
	CreatedTimestamps   []string    `json:"created_timestamps,omitempty"`
	SnapshotResult      *EntryDTO   `json:"snapshot_result,omitempty"`
	ListResult          *[]EntryDTO `json:"list_result,omitempty"`
	ContentResultBase64 string      `json:"content_result_base64,omitempty"`
	RestoreResult       *EntryDTO   `json:"restore_result,omitempty"`
	Error               string      `json:"error,omitempty"`
	ErrorClass          string      `json:"error_class,omitempty"`
}

func (r OperationRecord) MarshalJSON() ([]byte, error) {
	type Alias OperationRecord
	if r.Op == "list" && r.Error == "" {
		type ListRecord struct {
			Alias
			ListResult *[]EntryDTO `json:"list_result"`
		}
		return json.Marshal(ListRecord{
			Alias:      Alias(r),
			ListResult: r.ListResult,
		})
	}
	return json.Marshal(Alias(r))
}

type FsEntry struct {
	Path          string `json:"path"`
	Mode          uint32 `json:"mode"`
	Size          int64  `json:"size"`
	SHA256        string `json:"sha256,omitempty"`
	ContentBase64 string `json:"content_base64,omitempty"`
	IsDir         bool   `json:"is_dir"`
}

func main() {
	output := flag.String("output", "", "oracle fixture report output path")
	commit := flag.String("oracle-commit", defaultOracleCommit, "Go oracle commit")
	release := flag.String("oracle-release", defaultOracleRelease, "Go oracle release")
	flag.Parse()

	if *output == "" {
		fatal("output path is required (specify --output)")
	}

	root, err := repoRoot()
	if err != nil {
		fatal("find repository root: %v", err)
	}

	sourceHashes, err := verifyOracle(root, *commit, *release)
	if err != nil {
		fatal("verify pinned Go oracle: %v", err)
	}

	doc, err := runOracle(Oracle{Commit: *commit, Release: *release}, sourceHashes)
	if err != nil {
		fatal("build oracle report: %v", err)
	}

	content, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		fatal("marshal oracle report: %v", err)
	}
	content = append(content, '\n')

	outPath := *output
	if !filepath.IsAbs(outPath) {
		outPath = filepath.Join(root, filepath.FromSlash(outPath))
	}
	if err := os.MkdirAll(filepath.Dir(outPath), 0o750); err != nil {
		fatal("create output dir: %v", err)
	}
	if err := os.WriteFile(outPath, content, 0o600); err != nil {
		fatal("write oracle report: %v", err)
	}
	fmt.Printf("PASS history oracle report generated (%d operations) -> %s\n", len(doc.Operations), outPath)
}

func verifyOracle(root, revision, release string) (map[string]string, error) {
	if revision != defaultOracleCommit {
		return nil, fmt.Errorf("oracle commit must be pinned to %s", defaultOracleCommit)
	}
	if release != defaultOracleRelease {
		return nil, fmt.Errorf("oracle release must be pinned to %s", defaultOracleRelease)
	}
	if _, err := gitOutput(root, "rev-parse", "--verify", revision+"^{commit}"); err != nil {
		return nil, fmt.Errorf("revision %s: %w", revision, err)
	}
	list, err := gitOutput(root, "ls-tree", "-r", "--name-only", revision, "--", "internal/history")
	if err != nil {
		return nil, err
	}
	pinnedSources := goSourcePaths(list)
	if len(pinnedSources) == 0 {
		return nil, fmt.Errorf("pinned revision has incomplete Go source set")
	}

	currentList, err := gitOutput(root, "ls-files", "--cached", "--others", "--exclude-standard", "--", "internal/history")
	if err != nil {
		return nil, err
	}
	currentSources := goSourcePaths(currentList)
	actualSources, err := enumerateGoSources(root)
	if err != nil {
		return nil, fmt.Errorf("enumerate current internal/history Go sources: %w", err)
	}
	if !sameStrings(pinnedSources, currentSources) || !sameStrings(pinnedSources, actualSources) {
		return nil, fmt.Errorf("current internal/history Go source set differs from pinned Git source set")
	}

	paths := append([]string{}, pinnedSources...)
	paths = append(paths, "go.mod", "go.sum")
	hashes := make(map[string]string, len(paths))
	for _, path := range paths {
		if !filepath.IsLocal(path) {
			return nil, fmt.Errorf("source path %q is not local", path)
		}
		pinned, err := gitOutput(root, "show", revision+":"+path)
		if err != nil {
			return nil, fmt.Errorf("read %s from %s: %w", path, revision, err)
		}
		// #nosec G304 -- path is validated via filepath.IsLocal and verified against pinned Git tree
		current, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			return nil, fmt.Errorf("read current %s: %w", path, err)
		}
		if !bytes.Equal(current, pinned) {
			return nil, fmt.Errorf("current %s differs from pinned Git blob", path)
		}
		digest := sha256.Sum256(pinned)
		hashes[path] = hex.EncodeToString(digest[:])
	}
	return hashes, nil
}

func goSourcePaths(output []byte) []string {
	paths := make([]string, 0)
	for _, path := range strings.Split(strings.TrimSpace(string(output)), "\n") {
		if strings.HasSuffix(path, ".go") && !strings.HasSuffix(path, "_test.go") {
			paths = append(paths, filepath.ToSlash(path))
		}
	}
	sort.Strings(paths)
	return paths
}

func enumerateGoSources(root string) ([]string, error) {
	base := filepath.Join(root, "internal", "history")
	paths := make([]string, 0)
	err := filepath.WalkDir(base, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if strings.HasSuffix(filepath.ToSlash(relative), ".go") &&
			!strings.HasSuffix(filepath.ToSlash(relative), "_test.go") {
			paths = append(paths, filepath.ToSlash(relative))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

func sameStrings(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

const gitCommandTimeout = 30 * time.Second

func gitOutput(root string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gitCommandTimeout)
	defer cancel()
	// #nosec G204 -- git subprocess invocation with fixed executable and controlled arguments within repo root
	command := exec.CommandContext(ctx, "git", args...)
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), ctx.Err())
		}
		return nil, fmt.Errorf("git %s: %w", strings.Join(args, " "), err)
	}
	return output, nil
}

func repoRoot() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("go.mod not found")
		}
		dir = parent
	}
}

func collectManifestTimestamps(vaultRoot string) map[string]map[string]bool {
	res := make(map[string]map[string]bool)
	manifestDir := filepath.Join(vaultRoot, ".symdesk", "history", "manifest")
	rootHandle, err := os.OpenRoot(vaultRoot)
	if err != nil {
		return res
	}
	defer func() { _ = rootHandle.Close() }()

	_ = filepath.Walk(manifestDir, func(p string, info fs.FileInfo, err error) error {
		if err != nil || info.IsDir() || !strings.HasSuffix(p, ".json") {
			return nil
		}
		rel, relErr := filepath.Rel(vaultRoot, p)
		if relErr != nil || !filepath.IsLocal(rel) {
			return nil
		}
		data, readErr := rootHandle.ReadFile(rel)
		if readErr != nil {
			return nil
		}
		var entries []history.Entry
		if err := json.Unmarshal(data, &entries); err != nil {
			return nil
		}
		tsMap := make(map[string]bool)
		for _, e := range entries {
			tsMap[e.Timestamp.Format(time.RFC3339Nano)] = true
		}
		res[rel] = tsMap
		return nil
	})
	return res
}

func findNewTimestamps(before, after map[string]map[string]bool) []string {
	var newlyCreated []string
	for path, afterTimestamps := range after {
		beforeTimestamps := before[path]
		for ts := range afterTimestamps {
			if !beforeTimestamps[ts] {
				newlyCreated = append(newlyCreated, ts)
			}
		}
	}
	sort.Strings(newlyCreated)
	return newlyCreated
}

func runOracle(oracle Oracle, sourceHashes map[string]string) (Document, error) {
	vaultRoot, err := os.MkdirTemp("", "symdesk-history-oracle-")
	if err != nil {
		return Document{}, err
	}
	defer func() { _ = os.RemoveAll(vaultRoot) }()

	htmlPath := htmlPathForGOOS(runtime.GOOS)
	initial := []FileSpec{
		{Path: "notes/initial.md", Content: "Initial notes content\n"},
		{Path: "notes/nested/deep/doc.md", Content: "Deeply nested note content\n"},
		{Path: "binary/data.bin", ContentBase64: base64.StdEncoding.EncodeToString([]byte{0x00, 0xFF, 0xFE, 0x01, 0x80, 0xAA, 0x55, 0x00})},
		{Path: "unicode/mädchen_übersicht.md", Content: "Unicode file path and content: Grüß Gott 🚀\n"},
		{Path: htmlPath, Content: "HTML / injection path test\n"},
	}

	for _, f := range initial {
		p := filepath.Join(vaultRoot, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			return Document{}, err
		}
		var data []byte
		if f.ContentBase64 != "" {
			var decErr error
			data, decErr = base64.StdEncoding.DecodeString(f.ContentBase64)
			if decErr != nil {
				return Document{}, decErr
			}
		} else {
			data = []byte(f.Content)
		}
		// #nosec G306 -- fixture file creation with standard 0644 permissions required for Go/Rust differential testing
		if err := os.WriteFile(p, data, 0o644); err != nil {
			return Document{}, err
		}
	}

	store := history.NewStore(vaultRoot)
	var records []OperationRecord
	step := 0

	recordOp := func(op string, path, id, content, contentBase64 string, fn func() (any, error)) {
		step++
		rec := OperationRecord{
			Step:          step,
			Op:            op,
			Path:          path,
			ID:            id,
			Content:       content,
			ContentBase64: contentBase64,
		}
		if op == "write_raw" {
			p := filepath.Join(vaultRoot, filepath.FromSlash(path))
			_ = os.MkdirAll(filepath.Dir(p), 0o750)
			var data []byte
			if contentBase64 != "" {
				var decErr error
				data, decErr = base64.StdEncoding.DecodeString(contentBase64)
				if decErr != nil {
					rec.Error = decErr.Error()
				}
			} else {
				data = []byte(content)
			}
			// #nosec G306 -- write_raw creates fixture files with standard 0644 permissions required for Go/Rust differential testing
			if err := os.WriteFile(p, data, 0o644); err != nil {
				rec.Error = err.Error()
			}
			records = append(records, rec)
			return
		}

		manifestsBefore := collectManifestTimestamps(vaultRoot)
		res, err := fn()
		manifestsAfter := collectManifestTimestamps(vaultRoot)
		rec.CreatedTimestamps = findNewTimestamps(manifestsBefore, manifestsAfter)

		if err != nil {
			rec.Error = err.Error()
			rec.ErrorClass = classifyError(err)
		} else if op == "list" {
			if res == nil {
				rec.ListResult = nil
			} else if entries, ok := res.([]history.Entry); ok {
				if entries == nil {
					rec.ListResult = nil
				} else {
					dtos := make([]EntryDTO, len(entries))
					for i, e := range entries {
						dtos[i] = entryToDTO(e)
					}
					rec.ListResult = &dtos
				}
			}
		} else if res != nil {
			switch v := res.(type) {
			case *history.Entry:
				if v != nil {
					dto := entryToDTO(*v)
					switch op {
					case "snapshot":
						rec.SnapshotResult = &dto
					case "restore":
						rec.RestoreResult = &dto
					}
				}
			case []byte:
				rec.ContentResultBase64 = base64.StdEncoding.EncodeToString(v)
			}
		}
		records = append(records, rec)
	}

	// 1. Initial snapshot
	var v1Entry, v2Entry, v3Entry, binEntry *history.Entry
	recordOp("snapshot", "notes/initial.md", "", "", "", func() (any, error) {
		e, err := store.Snapshot("notes/initial.md")
		v1Entry = e
		return e, err
	})

	// 2. Dedup snapshot
	recordOp("snapshot", "notes/initial.md", "", "", "", func() (any, error) {
		return store.Snapshot("notes/initial.md")
	})

	// 3. Mutate notes/initial.md to v2
	recordOp("write_raw", "notes/initial.md", "", "Updated notes content v2\n", "", nil)

	// 4. Snapshot v2
	recordOp("snapshot", "notes/initial.md", "", "", "", func() (any, error) {
		e, err := store.Snapshot("notes/initial.md")
		v2Entry = e
		return e, err
	})

	// 5. Mutate notes/initial.md to v3
	recordOp("write_raw", "notes/initial.md", "", "Updated notes content v3\n", "", nil)

	// 6. Snapshot v3
	recordOp("snapshot", "notes/initial.md", "", "", "", func() (any, error) {
		e, err := store.Snapshot("notes/initial.md")
		v3Entry = e
		return e, err
	})

	// 7. Missing file snapshot (no-op)
	recordOp("snapshot", "notes/missing.md", "", "", "", func() (any, error) {
		return store.Snapshot("notes/missing.md")
	})

	// 8-12. Invalid paths for snapshot
	for _, bad := range []string{"../outside.md", "/abs/evil.md", ".", "", "notes/../../evil.md"} {
		recordOp("snapshot", bad, "", "", "", func() (any, error) {
			return store.Snapshot(bad)
		})
	}

	// 13. List notes/initial.md
	recordOp("list", "notes/initial.md", "", "", "", func() (any, error) {
		return store.List("notes/initial.md")
	})

	// 14. List never snapshotted file
	recordOp("list", "notes/never_snapshotted.md", "", "", "", func() (any, error) {
		return store.List("notes/never_snapshotted.md")
	})

	// 15. List invalid path
	recordOp("list", "../evil.md", "", "", "", func() (any, error) {
		return store.List("../evil.md")
	})

	// 16-17. Content reads for v1 and v2
	if v1Entry != nil {
		recordOp("content", "", v1Entry.ID, "", "", func() (any, error) {
			return store.Content(v1Entry.ID)
		})
	}
	if v2Entry != nil {
		recordOp("content", "", v2Entry.ID, "", "", func() (any, error) {
			return store.Content(v2Entry.ID)
		})
	}

	// 18-20. Content invalid IDs
	recordOp("content", "", "invalid_short_id", "", "", func() (any, error) {
		return store.Content("invalid_short_id")
	})
	recordOp("content", "", "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdeg", "", "", func() (any, error) {
		return store.Content("0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdeg")
	})
	recordOp("content", "", "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff", "", "", func() (any, error) {
		return store.Content("ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff")
	})

	// 21-22. Opaque binary snapshot and content
	recordOp("snapshot", "binary/data.bin", "", "", "", func() (any, error) {
		e, err := store.Snapshot("binary/data.bin")
		binEntry = e
		return e, err
	})
	if binEntry != nil {
		recordOp("content", "", binEntry.ID, "", "", func() (any, error) {
			return store.Content(binEntry.ID)
		})
	}

	// 23-26. Unicode & HTML paths
	recordOp("snapshot", "unicode/mädchen_übersicht.md", "", "", "", func() (any, error) {
		return store.Snapshot("unicode/mädchen_übersicht.md")
	})
	recordOp("list", "unicode/mädchen_übersicht.md", "", "", "", func() (any, error) {
		return store.List("unicode/mädchen_übersicht.md")
	})
	recordOp("snapshot", htmlPath, "", "", "", func() (any, error) {
		return store.Snapshot(htmlPath)
	})
	recordOp("list", htmlPath, "", "", "", func() (any, error) {
		return store.List(htmlPath)
	})

	// 27-28. Deeply nested paths
	recordOp("snapshot", "notes/nested/deep/doc.md", "", "", "", func() (any, error) {
		return store.Snapshot("notes/nested/deep/doc.md")
	})
	recordOp("list", "notes/nested/deep/doc.md", "", "", "", func() (any, error) {
		return store.List("notes/nested/deep/doc.md")
	})

	// 29. Restore by full ID (restores v1, pre-restore v3 is snapshotted)
	if v1Entry != nil {
		recordOp("restore", "notes/initial.md", v1Entry.ID, "", "", func() (any, error) {
			return store.Restore("notes/initial.md", v1Entry.ID)
		})
	}

	// 30. Restore by unique prefix (8 chars of v2)
	if v2Entry != nil {
		prefix := v2Entry.ID[:8]
		recordOp("restore", "notes/initial.md", prefix, "", "", func() (any, error) {
			return store.Restore("notes/initial.md", prefix)
		})
	}

	// 31-32. Modify working copy, then restore latest ("")
	recordOp("write_raw", "notes/initial.md", "", "working copy content\n", "", nil)
	recordOp("restore", "notes/initial.md", "", "", "", func() (any, error) {
		return store.Restore("notes/initial.md", "")
	})

	// 33-34. Restore errors (no snapshot for path, no snapshots recorded)
	recordOp("restore", "notes/initial.md", "ffffffffffffffff", "", "", func() (any, error) {
		return store.Restore("notes/initial.md", "ffffffffffffffff")
	})
	recordOp("restore", "notes/unseen.md", "", "", "", func() (any, error) {
		return store.Restore("notes/unseen.md", "")
	})

	// 35-36. Corrupt manifest handling
	recordOp("write_raw", ".symdesk/history/manifest/corrupt.md.json", "", "invalid manifest json {", "", nil)
	recordOp("list", "corrupt.md", "", "", "", func() (any, error) {
		return store.List("corrupt.md")
	})

	// 37-38. Null manifest handling
	recordOp("write_raw", ".symdesk/history/manifest/null_manifest.md.json", "", "null", "", nil)
	recordOp("list", "null_manifest.md", "", "", "", func() (any, error) {
		return store.List("null_manifest.md")
	})

	// 39-40. Empty manifest handling
	recordOp("write_raw", ".symdesk/history/manifest/empty_manifest.md.json", "", "[]", "", nil)
	recordOp("list", "empty_manifest.md", "", "", "", func() (any, error) {
		return store.List("empty_manifest.md")
	})

	// 41-44. Manifest with omitted fields and explicit nulls
	recordOp("write_raw", ".symdesk/history/manifest/default_manifest.md.json", "", "[{}]", "", nil)
	recordOp("list", "default_manifest.md", "", "", "", func() (any, error) {
		return store.List("default_manifest.md")
	})
	recordOp("write_raw", ".symdesk/history/manifest/default_manifest.md.json", "", `[{"id":null,"timestamp":null,"size":null}]`, "", nil)
	recordOp("list", "default_manifest.md", "", "", "", func() (any, error) {
		return store.List("default_manifest.md")
	})

	// 45-46. Manifest with duplicate non-null then null fields
	recordOp("write_raw", ".symdesk/history/manifest/duplicate_manifest.md.json", "", `[{"id":"2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae","id":null,"timestamp":"2026-09-15T04:00:00Z","timestamp":null,"size":128,"size":null}]`, "", nil)
	recordOp("list", "duplicate_manifest.md", "", "", "", func() (any, error) {
		return store.List("duplicate_manifest.md")
	})

	// 47-48. Manifest with mixed ASCII-case keys and Unicode long-s size alias
	recordOp("write_raw", ".symdesk/history/manifest/folded_manifest.md.json", "", `[{"ID":"2c26b46b68ffc68ff99b453c1d30413413422d706483bfa0f98a5e886266e7ae","TimeStamp":"2026-09-15T04:00:00Z","ſize":128}]`, "", nil)
	recordOp("list", "folded_manifest.md", "", "", "", func() (any, error) {
		return store.List("folded_manifest.md")
	})

	// 49-50. Manifest with null entry element
	recordOp("write_raw", ".symdesk/history/manifest/null_element_manifest.md.json", "", "[null]", "", nil)
	recordOp("list", "null_element_manifest.md", "", "", "", func() (any, error) {
		return store.List("null_element_manifest.md")
	})

	// 51-54. Manifest with non-UTC timestamps (fractional +05:30 and negative -00:30), List results, and rewriting Snapshot
	recordOp("write_raw", ".symdesk/history/manifest/notes/tz.md.json", "", `[{"id":"1111111111111111111111111111111111111111111111111111111111111111","timestamp":"2026-09-15T15:04:05.123456+05:30","size":64},{"id":"2222222222222222222222222222222222222222222222222222222222222222","timestamp":"2026-09-15T09:00:00-00:30","size":128}]`, "", nil)
	recordOp("write_raw", "notes/tz.md", "", "Timezone test note content\n", "", nil)
	recordOp("list", "notes/tz.md", "", "", "", func() (any, error) {
		return store.List("notes/tz.md")
	})
	recordOp("snapshot", "notes/tz.md", "", "", "", func() (any, error) {
		return store.Snapshot("notes/tz.md")
	})

	// 55-56. Native path backslash filename handling
	recordOp("write_raw", "notes\\backslash.md", "", "Backslash path test content\n", "", nil)
	recordOp("snapshot", "notes\\backslash.md", "", "", "", func() (any, error) {
		return store.Snapshot("notes\\backslash.md")
	})

	// Collect final filesystem state
	vaultRootHandle, err := os.OpenRoot(vaultRoot)
	if err != nil {
		return Document{}, fmt.Errorf("open vault root: %w", err)
	}
	defer func() { _ = vaultRootHandle.Close() }()

	var fsEntries []FsEntry
	err = filepath.Walk(vaultRoot, func(p string, info fs.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if p == vaultRoot {
			return nil
		}
		rel, relErr := filepath.Rel(vaultRoot, p)
		if relErr != nil {
			return relErr
		}
		if !filepath.IsLocal(rel) {
			return fmt.Errorf("vault relative path %q is not local", rel)
		}
		slashRel := filepath.ToSlash(rel)
		entry := FsEntry{
			Path:  slashRel,
			Mode:  uint32(info.Mode().Perm()),
			Size:  info.Size(),
			IsDir: info.IsDir(),
		}
		if !info.IsDir() {
			data, readErr := vaultRootHandle.ReadFile(rel)
			if readErr != nil {
				return readErr
			}
			sum := sha256.Sum256(data)
			entry.SHA256 = hex.EncodeToString(sum[:])
			entry.ContentBase64 = base64.StdEncoding.EncodeToString(data)
		}
		fsEntries = append(fsEntries, entry)
		return nil
	})
	if err != nil {
		return Document{}, fmt.Errorf("walk final filesystem: %w", err)
	}
	sort.Slice(fsEntries, func(i, j int) bool {
		return fsEntries[i].Path < fsEntries[j].Path
	})

	_ = v3Entry

	return Document{
		SchemaVersion:   1,
		Oracle:          oracle,
		SourceHashes:    sourceHashes,
		InitialFiles:    initial,
		Operations:      records,
		FinalFilesystem: fsEntries,
	}, nil
}

func entryToDTO(e history.Entry) EntryDTO {
	return EntryDTO{
		ID:        e.ID,
		Timestamp: e.Timestamp.Format(time.RFC3339Nano),
		Size:      e.Size,
	}
}

func classifyError(err error) string {
	msg := err.Error()
	switch {
	case strings.HasPrefix(msg, "invalid vault-relative path"):
		return "invalid_path"
	case strings.HasPrefix(msg, "invalid snapshot id"):
		return "invalid_id"
	case strings.Contains(msg, "snapshot object") && strings.Contains(msg, "not found"):
		return "not_found"
	case strings.HasPrefix(msg, "no snapshots recorded for"):
		return "no_snapshots"
	case strings.Contains(msg, "is ambiguous for"):
		return "ambiguous_prefix"
	case strings.HasPrefix(msg, "no snapshot") && strings.Contains(msg, "for"):
		return "no_snapshot"
	case strings.HasPrefix(msg, "corrupt history manifest for"):
		return "corrupt_manifest"
	default:
		return "other"
	}
}

func fatal(format string, args ...any) {
	_, _ = fmt.Fprintf(os.Stderr, "FAIL "+format+"\n", args...)
	os.Exit(1)
}

func htmlPathForGOOS(goos string) string {
	if goos == "windows" {
		return "html/ampersand&injection.md"
	}
	return "html/<div><script>alert(1)</script>.md"
}
