package sidecar

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
	"time"
)

// portSidecarMetadataFixture freezes the observable side effects of opening a
// per-vault sidecar: the `metadata.json` record Go writes next to `sidecar.db`
// (issue #1006). The encoding cases are byte-exact because they marshal fixed
// instants; the filesystem cases are structural because real open timestamps
// are not reproducible.
type portSidecarMetadataFixture struct {
	SchemaVersion    int                        `json:"schema_version"`
	MetadataFileName string                     `json:"metadata_file_name"`
	TempPattern      string                     `json:"temp_pattern"`
	DirectoryMode    string                     `json:"directory_mode"`
	FileMode         string                     `json:"file_mode"`
	KeyOrder         []string                   `json:"key_order"`
	EncodingCases    []portMetadataEncodingCase `json:"encoding_cases"`
	OpenForVault     portMetadataFilesystemCase `json:"open_for_vault"`
	Reopen           portMetadataFilesystemCase `json:"reopen"`
	ExplicitOverride portMetadataFilesystemCase `json:"explicit_override"`
	Listing          []portMetadataListingRow   `json:"listing"`
}

type portMetadataEncodingCase struct {
	Name      string `json:"name"`
	VaultPath string `json:"vault_path"`
	UnixSec   int64  `json:"unix_sec"`
	Nanos     int64  `json:"nanos"`
	Encoded   string `json:"encoded"`
}

type portMetadataFilesystemCase struct {
	Name                 string   `json:"name"`
	Entries              []string `json:"entries"`
	MetadataWritten      bool     `json:"metadata_written"`
	VaultPathIsCanonical bool     `json:"vault_path_is_canonical"`
	LastUsedIsUTC        bool     `json:"last_used_is_utc"`
	TempLeftovers        int      `json:"temp_leftovers"`
	LastUsedAdvances     bool     `json:"last_used_advances"`
}

type portMetadataListingRow struct {
	Directory string `json:"directory"`
	VaultPath string `json:"vault_path"`
	Metadata  bool   `json:"metadata"`
	Orphan    bool   `json:"orphan"`
}

const portSidecarMetadataFixturePath = "../../testdata/port/sidecar/metadata.json"

func TestPortSidecarMetadataContract(t *testing.T) {
	fixture := portSidecarMetadataFixture{
		SchemaVersion:    1,
		MetadataFileName: metadataFileName,
		TempPattern:      ".metadata-*.tmp",
		DirectoryMode:    "0700",
		FileMode:         "0600",
		KeyOrder:         []string{"vault_path", "last_used"},
		EncodingCases:    portMetadataEncodingCases(t),
	}
	fixture.OpenForVault, fixture.Reopen = portMetadataOpenCases(t)
	fixture.ExplicitOverride = portMetadataExplicitCase(t)
	fixture.Listing = portMetadataListingRows(t)

	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')

	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(portSidecarMetadataFixturePath, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	//nolint:gosec // the fixture path is fixed relative to the repository
	current, err := os.ReadFile(portSidecarMetadataFixturePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded) {
		t.Fatal("sidecar metadata fixture is stale; run make sidecar-metadata-fixtures-generate")
	}
}

// portMetadataEncodingCases marshals the production metadata record at fixed
// instants. Go's time.Time JSON encoding is RFC3339 with a nanosecond fraction
// whose trailing zeros are removed, which a port must reproduce exactly.
func portMetadataEncodingCases(t *testing.T) []portMetadataEncodingCase {
	t.Helper()
	inputs := []struct {
		name      string
		vaultPath string
		unixSec   int64
		nanos     int64
	}{
		{"whole-second", "/vaults/plain", 1767229445, 0},
		{"half-second", "/vaults/plain", 1767229445, 500000000},
		{"two-digit-fraction", "/vaults/plain", 1767229445, 120000000},
		{"full-nanoseconds", "/vaults/plain", 1767229445, 123456789},
		{"single-nanosecond", "/vaults/plain", 1767229445, 1},
		{"trailing-zero-nanoseconds", "/vaults/plain", 1767229445, 123456780},
		{"unicode-path", "/vaults/Ünïcøde/Ordner mit Leerzeichen", 1767229445, 42},
		{"escaped-path", `/vaults/quote"back\slash`, 1767229445, 42},
		{"html-escaped-path", "/vaults/<tag>&amp", 1767229445, 42},
		{"line-separator-path", "/vaults/line\u2028sep\u2029arator", 1767229445, 42},
		{"control-character-path", "/vaults/tab	newline\n", 1767229445, 42},
		{"epoch", "/vaults/plain", 0, 0},
		{"windows-style-path", `C:\Users\daniel\vault`, 1767229445, 7},
	}
	cases := make([]portMetadataEncodingCase, 0, len(inputs))
	for _, input := range inputs {
		payload, err := json.Marshal(sidecarMetadata{
			VaultPath: input.vaultPath,
			LastUsed:  time.Unix(input.unixSec, input.nanos).UTC(),
		})
		if err != nil {
			t.Fatal(err)
		}
		cases = append(cases, portMetadataEncodingCase{
			Name:      input.name,
			VaultPath: input.vaultPath,
			UnixSec:   input.unixSec,
			Nanos:     input.nanos,
			Encoded:   string(payload),
		})
	}
	return cases
}

func portMetadataOpenCases(t *testing.T) (portMetadataFilesystemCase, portMetadataFilesystemCase) {
	t.Helper()
	dataRoot := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataRoot)
	t.Setenv("SYMDESK_SIDECAR", "")
	vaultRoot := t.TempDir()

	first := portMetadataOpenOnce(t, vaultRoot, "open_for_vault")
	firstRecorded := portMetadataRead(t, vaultRoot)
	time.Sleep(2 * time.Millisecond)
	second := portMetadataOpenOnce(t, vaultRoot, "reopen")
	secondRecorded := portMetadataRead(t, vaultRoot)
	second.LastUsedAdvances = secondRecorded.LastUsed.After(firstRecorded.LastUsed)
	return first, second
}

func portMetadataOpenOnce(t *testing.T, vaultRoot, name string) portMetadataFilesystemCase {
	t.Helper()
	db, err := OpenForVault(vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	dir := portMetadataVaultDir(t, vaultRoot)
	recorded := portMetadataRead(t, vaultRoot)
	canonical := portMetadataCanonical(t, vaultRoot)
	return portMetadataFilesystemCase{
		Name:                 name,
		Entries:              portMetadataEntries(t, dir),
		MetadataWritten:      true,
		VaultPathIsCanonical: recorded.VaultPath == canonical,
		LastUsedIsUTC:        recorded.LastUsed.Location() == time.UTC,
		TempLeftovers:        portMetadataTempLeftovers(t, dir),
	}
}

func portMetadataExplicitCase(t *testing.T) portMetadataFilesystemCase {
	t.Helper()
	dataRoot := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataRoot)
	vaultRoot := t.TempDir()
	explicitDir := t.TempDir()
	explicit := filepath.Join(explicitDir, "explicit.db")
	t.Setenv("SYMDESK_SIDECAR", explicit)

	db, err := OpenForVault(vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	return portMetadataFilesystemCase{
		Name:            "explicit_override",
		Entries:         portMetadataEntries(t, explicitDir),
		MetadataWritten: portMetadataExists(t, filepath.Join(explicitDir, metadataFileName)),
		TempLeftovers:   portMetadataTempLeftovers(t, explicitDir),
	}
}

func portMetadataListingRows(t *testing.T) []portMetadataListingRow {
	t.Helper()
	dataRoot := t.TempDir()
	t.Setenv("XDG_DATA_HOME", dataRoot)
	t.Setenv("SYMDESK_SIDECAR", "")
	root, err := SidecarRoot()
	if err != nil {
		t.Fatal(err)
	}
	liveVault := filepath.Join(dataRoot, "live-vault")
	if err := os.MkdirAll(liveVault, 0o700); err != nil {
		t.Fatal(err)
	}
	directories := map[string]string{
		"live":   liveVault,
		"orphan": filepath.Join(dataRoot, "missing-vault"),
	}
	for _, name := range []string{"live", "orphan", "unidentified"} {
		dir := filepath.Join(root, name)
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "sidecar.db"), []byte("derived"), 0o600); err != nil {
			t.Fatal(err)
		}
		if vaultPath, ok := directories[name]; ok {
			if err := recordSidecarMetadata(dir, vaultPath); err != nil {
				t.Fatal(err)
			}
		}
	}
	entries, err := ListSidecars()
	if err != nil {
		t.Fatal(err)
	}
	rows := make([]portMetadataListingRow, 0, len(entries))
	for _, entry := range entries {
		vaultPath := ""
		switch {
		case entry.VaultPath == liveVault:
			vaultPath = "<LIVE_VAULT>"
		case entry.VaultPath != "":
			vaultPath = "<MISSING_VAULT>"
		}
		rows = append(rows, portMetadataListingRow{
			Directory: filepath.Base(filepath.Dir(entry.Path)),
			VaultPath: vaultPath,
			Metadata:  entry.Metadata,
			Orphan:    entry.Orphan,
		})
	}
	return rows
}

func portMetadataVaultDir(t *testing.T, vaultRoot string) string {
	t.Helper()
	path, err := PathForVault(vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Dir(path)
}

func portMetadataCanonical(t *testing.T, vaultRoot string) string {
	t.Helper()
	canonical, err := filepath.Abs(vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	if resolved, err := filepath.EvalSymlinks(canonical); err == nil {
		canonical = resolved
	}
	return canonical
}

func portMetadataRead(t *testing.T, vaultRoot string) sidecarMetadata {
	t.Helper()
	dir := portMetadataVaultDir(t, vaultRoot)
	//nolint:gosec // dir is derived from the isolated test data root
	payload, err := os.ReadFile(filepath.Join(dir, metadataFileName))
	if err != nil {
		t.Fatal(err)
	}
	var recorded sidecarMetadata
	if err := json.Unmarshal(payload, &recorded); err != nil {
		t.Fatal(err)
	}
	return recorded
}

// portMetadataEntries lists the durable directory contents. SQLite's WAL and
// shared-memory files are transient companions of an open connection and are
// not part of the recorded contract.
func portMetadataEntries(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, "-wal") || strings.HasSuffix(name, "-shm") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func portMetadataTempLeftovers(t *testing.T, dir string) int {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(dir, ".metadata-*.tmp"))
	if err != nil {
		t.Fatal(err)
	}
	return len(matches)
}

func portMetadataExists(t *testing.T, path string) bool {
	t.Helper()
	_, err := os.Stat(path)
	if err == nil {
		return true
	}
	if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return false
}

// TestPortSidecarMetadataModes asserts the POSIX modes the port must match.
// Windows does not carry them, so the recorded fixture stays mode-free and this
// check runs natively where the modes exist.
func TestPortSidecarMetadataModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX modes are not observable on Windows")
	}
	t.Setenv("XDG_DATA_HOME", t.TempDir())
	t.Setenv("SYMDESK_SIDECAR", "")
	vaultRoot := t.TempDir()
	db, err := OpenForVault(vaultRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	dir := portMetadataVaultDir(t, vaultRoot)
	dirInfo, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := dirInfo.Mode().Perm(); got != 0o700 {
		t.Fatalf("sidecar directory mode = %04o, want 0700", got)
	}
	fileInfo, err := os.Stat(filepath.Join(dir, metadataFileName))
	if err != nil {
		t.Fatal(err)
	}
	if got := fileInfo.Mode().Perm(); got != 0o600 {
		t.Fatalf("metadata mode = %04o, want 0600", got)
	}
}
