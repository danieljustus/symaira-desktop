package sidecar

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type portLifecycleFixture struct {
	SchemaVersion       int                  `json:"schema_version"`
	Oracle              portOracle           `json:"oracle"`
	Inputs              []portLifecycleInput `json:"inputs"`
	Initial             portSidecarState     `json:"initial"`
	Fast                portSidecarState     `json:"fast"`
	StatOnly            portSidecarState     `json:"stat_only"`
	SameSize            portSidecarState     `json:"same_size"`
	DerivedTransition   portSidecarState     `json:"derived_transition"`
	MissingAfterRefresh portSidecarState     `json:"missing_after_refresh"`
	AfterPrune          portSidecarState     `json:"after_prune"`
	Lifecycle           []portLifecycleRow   `json:"lifecycle_after_prune"`
	PruneRemoved        int                  `json:"prune_removed"`
	FastIndexedAtSame   bool                 `json:"fast_indexed_at_same"`
	StatIndexedAtSame   bool                 `json:"stat_indexed_at_same"`
	SameSizeLength      bool                 `json:"same_size_length"`
	UppercaseIgnored    bool                 `json:"uppercase_ignored"`
}

type portOracle struct {
	Commit  string `json:"commit"`
	Release string `json:"release"`
}

type portLifecycleInput struct {
	Path        string `json:"path"`
	Initial     string `json:"initial"`
	StatRefresh string `json:"stat_refresh,omitempty"`
	Edit        string `json:"edit,omitempty"`
	Derived     string `json:"derived,omitempty"`
	MTimeNS     int64  `json:"mtime_ns"`
	RefreshNS   int64  `json:"refresh_mtime_ns,omitempty"`
	EditNS      int64  `json:"edit_mtime_ns,omitempty"`
	DerivedNS   int64  `json:"derived_mtime_ns,omitempty"`
}

type portLifecycleRow struct {
	Path      string `json:"path"`
	State     string `json:"state"`
	Reason    string `json:"reason"`
	UpdatedAt string `json:"updated_at"`
}

func TestPortSidecarLifecycleContract(t *testing.T) {
	oracle, err := portLifecycleOracle()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	db, err := Open(filepath.Join(t.TempDir(), "sidecar.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	fixed := time.Date(2026, 1, 15, 12, 0, 0, 0, time.UTC)
	inputs := []portLifecycleInput{
		{
			Path: "note.md", MTimeNS: fixed.UnixNano(), RefreshNS: fixed.Add(10 * time.Second).UnixNano(), EditNS: fixed.Add(20 * time.Second).UnixNano(),
			Initial: "---\ntitle: Stable Note\n---\nAAAA\n", Edit: "---\ntitle: Edited Note\n---\nBBBB\n",
		},
		{
			Path: "stat.md", MTimeNS: fixed.Add(time.Second).UnixNano(), RefreshNS: fixed.Add(11 * time.Second).UnixNano(),
			Initial: "---\ntitle: Stat Note\n---\nunchanged\n", StatRefresh: "---\ntitle: Stat Note\n---\nunchanged\n",
		},
		{
			Path: "derived.md", MTimeNS: fixed.Add(2 * time.Second).UnixNano(), DerivedNS: fixed.Add(30 * time.Second).UnixNano(),
			Initial: "---\ntitle: Generated\n---\nsource\n", Derived: "---\ntitle: Generated\nderived_from: source.md\n---\ngenerated\n",
		},
		{
			Path: "remove.md", MTimeNS: fixed.Add(3 * time.Second).UnixNano(),
			Initial: "---\ntitle: Removed Later\n---\nremove me\n",
		},
		{
			Path: "UPPER.MD", MTimeNS: fixed.Add(4 * time.Second).UnixNano(),
			Initial: "---\ntitle: Ignored Uppercase\n---\nignored\n",
		},
	}
	for _, input := range inputs {
		writeLifecycleFile(t, root, input.Path, input.Initial, input.MTimeNS)
	}

	if err := db.RefreshIndex(root); err != nil {
		t.Fatalf("initial RefreshIndex: %v", err)
	}
	fixture := portLifecycleFixture{
		SchemaVersion: 1,
		Oracle:        oracle,
		Inputs:        inputs,
		Initial:       normalizeLifecycleState(root, portSnapshot(t, db.conn)),
	}
	initialIndexedAt := fileIndexedAt(t, db, filepath.Join(root, "note.md"))

	if err := db.RefreshIndex(root); err != nil {
		t.Fatalf("fast RefreshIndex: %v", err)
	}
	fixture.Fast = normalizeLifecycleState(root, portSnapshot(t, db.conn))
	fixture.FastIndexedAtSame = initialIndexedAt == fileIndexedAt(t, db, filepath.Join(root, "note.md"))

	stat := inputs[1]
	writeLifecycleFile(t, root, stat.Path, stat.StatRefresh, stat.RefreshNS)
	beforeStat := fileIndexedAt(t, db, filepath.Join(root, stat.Path))
	if err := db.RefreshIndex(root); err != nil {
		t.Fatalf("stat-only RefreshIndex: %v", err)
	}
	fixture.StatOnly = normalizeLifecycleState(root, portSnapshot(t, db.conn))
	fixture.StatIndexedAtSame = beforeStat == fileIndexedAt(t, db, filepath.Join(root, stat.Path))

	note := inputs[0]
	if len(note.Initial) != len(note.Edit) {
		t.Fatalf("same-size edit fixture invariant broken")
	}
	writeLifecycleFile(t, root, note.Path, note.Edit, note.EditNS)
	if err := db.RefreshIndex(root); err != nil {
		t.Fatalf("same-size RefreshIndex: %v", err)
	}
	fixture.SameSize = normalizeLifecycleState(root, portSnapshot(t, db.conn))
	fixture.SameSizeLength = len(note.Initial) == len(note.Edit)

	derived := inputs[2]
	writeLifecycleFile(t, root, derived.Path, derived.Derived, derived.DerivedNS)
	if err := db.RefreshIndex(root); err != nil {
		t.Fatalf("derived transition RefreshIndex: %v", err)
	}
	fixture.DerivedTransition = normalizeLifecycleState(root, portSnapshot(t, db.conn))

	if err := os.Remove(filepath.Join(root, "remove.md")); err != nil {
		t.Fatal(err)
	}
	if err := db.RefreshIndex(root); err != nil {
		t.Fatalf("missing-file RefreshIndex: %v", err)
	}
	fixture.MissingAfterRefresh = normalizeLifecycleState(root, portSnapshot(t, db.conn))

	staleStatus := filepath.Join(root, "stale-status.md")
	keptStatus := filepath.Join(root, "note.md")
	for path, state := range map[string][2]string{
		staleStatus: {"failed", "missing"},
		keptStatus:  {"indexed", ""},
	} {
		if _, err := db.conn.Exec(`INSERT INTO index_lifecycle(path, state, reason, updated_at) VALUES (?, ?, ?, ?)`, path, state[0], state[1], fixed.Format(time.RFC3339Nano)); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.DeleteDocument(filepath.Join(root, "never-indexed.md")); err != nil {
		t.Fatalf("absent DeleteDocument: %v", err)
	}
	beforePruneDelete := normalizeLifecycleState(root, portSnapshot(t, db.conn))
	removed, err := db.Prune(root)
	if err != nil {
		t.Fatalf("Prune: %v", err)
	}
	fixture.PruneRemoved = removed
	fixture.AfterPrune = normalizeLifecycleState(root, portSnapshot(t, db.conn))
	fixture.Lifecycle = lifecycleRows(t, db.conn, root)
	if removed != 2 {
		t.Fatalf("Prune removed %d rows, want 2", removed)
	}
	if len(beforePruneDelete.Files) != len(fixture.MissingAfterRefresh.Files) {
		t.Fatalf("absent DeleteDocument changed the snapshot")
	}
	fixture.UppercaseIgnored = !stateHasPath(fixture.Initial, "UPPER.MD")

	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	fixturePath := filepath.Join("..", "..", "testdata", "port", "sidecar", "lifecycle.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(fixturePath, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	//nolint:gosec // fixturePath is fixed relative to the repository
	current, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded) {
		t.Fatal("sidecar lifecycle fixture is stale; run make sidecar-fixtures-generate")
	}
}

const (
	portgenSidecarOracleCommitEnv  = "PORTGEN_SIDECAR_ORACLE_COMMIT"
	portgenSidecarOracleReleaseEnv = "PORTGEN_SIDECAR_ORACLE_RELEASE"
)

// portLifecycleOracle uses portgen's explicit P identity when the fixture is
// generated or checked from its immutable snapshot. Direct test invocations
// fall back to the committed provenance metadata for developer ergonomics.
func portLifecycleOracle() (portOracle, error) {
	return resolvePortLifecycleOracle(
		os.LookupEnv,
		filepath.Join("..", "..", "testdata", "port", "provenance.json"),
	)
}

func resolvePortLifecycleOracle(lookup func(string) (string, bool), provenancePath string) (portOracle, error) {
	commit, hasCommit := lookup(portgenSidecarOracleCommitEnv)
	release, hasRelease := lookup(portgenSidecarOracleReleaseEnv)
	if hasCommit || hasRelease {
		if !hasCommit || !hasRelease {
			return portOracle{}, fmt.Errorf("%s and %s must be set together", portgenSidecarOracleCommitEnv, portgenSidecarOracleReleaseEnv)
		}
		oracle := portOracle{Commit: commit, Release: release}
		if err := validatePortLifecycleOracle(oracle); err != nil {
			return portOracle{}, fmt.Errorf("invalid portgen sidecar oracle: %w", err)
		}
		return oracle, nil
	}

	//nolint:gosec // provenancePath is a fixed committed fixture path.
	data, err := os.ReadFile(provenancePath)
	if err != nil {
		return portOracle{}, fmt.Errorf("read committed port provenance: %w", err)
	}
	var provenance struct {
		Oracle portOracle `json:"oracle"`
	}
	if err := json.Unmarshal(data, &provenance); err != nil {
		return portOracle{}, fmt.Errorf("decode committed port provenance: %w", err)
	}
	if err := validatePortLifecycleOracle(provenance.Oracle); err != nil {
		return portOracle{}, fmt.Errorf("invalid committed port provenance oracle: %w", err)
	}
	return provenance.Oracle, nil
}

func validatePortLifecycleOracle(oracle portOracle) error {
	if len(oracle.Commit) != 40 {
		return fmt.Errorf("commit must be a 40-character lowercase SHA")
	}
	for _, character := range oracle.Commit {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return fmt.Errorf("commit must be a 40-character lowercase SHA")
		}
	}
	if strings.TrimSpace(oracle.Release) == "" {
		return fmt.Errorf("release must not be empty")
	}
	return nil
}

func writeLifecycleFile(t *testing.T, root, relative, content string, mtimeNS int64) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	mtime := time.Unix(0, mtimeNS)
	if err := os.Chtimes(path, mtime, mtime); err != nil {
		t.Fatal(err)
	}
}

func normalizeLifecycleState(root string, state portSidecarState) portSidecarState {
	for i := range state.Files {
		state.Files[i].Path = normalizeLifecyclePath(root, state.Files[i].Path)
	}
	for i := range state.Links {
		state.Links[i].From = normalizeLifecyclePath(root, state.Links[i].From)
		state.Links[i].To = normalizeLifecyclePath(root, state.Links[i].To)
	}
	return state
}

func normalizeLifecyclePath(root, path string) string {
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == ".." || len(relative) >= 3 && relative[:3] == "../" {
		return path
	}
	return filepath.ToSlash(relative)
}

func lifecycleRows(t *testing.T, conn interface {
	Query(string, ...interface{}) (*sql.Rows, error)
}, root string) []portLifecycleRow {
	t.Helper()
	rows, err := conn.Query("SELECT path, state, reason, updated_at FROM index_lifecycle ORDER BY path")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var result []portLifecycleRow
	for rows.Next() {
		var row portLifecycleRow
		if err := rows.Scan(&row.Path, &row.State, &row.Reason, &row.UpdatedAt); err != nil {
			t.Fatal(err)
		}
		row.Path = filepath.ToSlash(normalizeLifecyclePath(root, row.Path))
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func stateHasPath(state portSidecarState, path string) bool {
	for _, row := range state.Files {
		if row.Path == path {
			return true
		}
	}
	return false
}
