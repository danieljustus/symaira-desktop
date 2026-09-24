package retrieval

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-corekit/sqlitekit"
	"github.com/danieljustus/symaira-desktop/internal/retrieval/internal/config"
)

type indexLocationCase struct {
	ID           string            `json:"id"`
	Environment  map[string]string `json:"environment"`
	VaultRoot    string            `json:"vault_root"`
	ConfigTOML   string            `json:"config_toml,omitempty"`
	LegacyJSON   string            `json:"legacy_json,omitempty"`
	SeedFiles    map[string]string `json:"seed_files,omitempty"`
	ExpectedPath string            `json:"expected_path,omitempty"`
	ErrorPrefix  string            `json:"error_prefix,omitempty"`
	ConfigAfter  json.RawMessage   `json:"config_after,omitempty"`
	Migrated     bool              `json:"migrated_json_config,omitempty"`
}

type indexRelocationFixture struct {
	InputRows                  []relocatePortRow `json:"input_rows"`
	SourceRowsAfter            []relocatePortRow `json:"source_rows_after"`
	RelocatedRows              []relocatePortRow `json:"relocated_rows"`
	RelocatedPath              string            `json:"relocated_path"`
	ConfigAfter                json.RawMessage   `json:"config_after"`
	SourcePreserved            bool              `json:"source_preserved"`
	DestinationReplaced        bool              `json:"destination_replaced"`
	VaultRelocationError       string            `json:"vault_relocation_error"`
	ConfigUnchangedAfterReject bool              `json:"config_unchanged_after_reject"`
	RejectedDestinationAbsent  bool              `json:"rejected_destination_absent"`
}

type indexLocationFixture struct {
	SchemaVersion int                    `json:"schema_version"`
	Cases         []indexLocationCase    `json:"cases"`
	Relocation    indexRelocationFixture `json:"relocation"`
}

func TestIndexLocationPortFixture(t *testing.T) {
	fixture := indexLocationFixture{SchemaVersion: 1}
	fixture.Cases = makeIndexLocationCases()
	for index := range fixture.Cases {
		t.Run(fixture.Cases[index].ID, func(t *testing.T) {
			observeIndexLocationCase(t, &fixture.Cases[index])
		})
	}
	fixture.Relocation = observeIndexRelocation(t)
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("source file location unavailable")
	}
	path := filepath.Join(filepath.Dir(source), "../../testdata/port/retrieval/index-location.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path) //nolint:gosec // test-only path is constrained by fixed or temporary fixture inputs
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded) {
		t.Fatal("index location fixture is stale; regenerate explicitly with PORT_GENERATE=1 go test ./internal/retrieval -run '^TestIndexLocationPortFixture$'")
	}
}

func TestConfiguredIndexPathClampsAtFilesystemRoot(t *testing.T) {
	root := string(filepath.Separator)
	if volume := filepath.VolumeName(os.TempDir()); volume != "" {
		root = volume + string(filepath.Separator)
	}
	path := root + strings.Repeat(".."+string(filepath.Separator), 3) + filepath.Join("tmp", "index.db")
	actual, err := configuredIndexPath(&config.Config{IndexPath: path})
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(root, "tmp", "index.db")
	if actual != want {
		t.Fatalf("configuredIndexPath(%q) = %q, want root-clamped %q", path, actual, want)
	}
}

func makeIndexLocationCases() []indexLocationCase {
	baseEnv := func() map[string]string {
		return map[string]string{
			"HOME":            "$ROOT/home",
			"USERPROFILE":     "$ROOT/home",
			"XDG_DATA_HOME":   "$ROOT/data",
			"XDG_CONFIG_HOME": "$ROOT/elsewhere-config",
			"TMPDIR":          "$ROOT/tmp",
			"TMP":             "$ROOT/tmp",
			"TEMP":            "$ROOT/tmp",
		}
	}
	vault := "$ROOT/cwd/vault"
	return []indexLocationCase{
		{ID: "standalone-default", Environment: baseEnv()},
		{ID: "trimmed-xdg-data-home", Environment: map[string]string{
			"HOME": "$ROOT/home", "USERPROFILE": "$ROOT/home", "XDG_DATA_HOME": " $ROOT/data ", "TMPDIR": "$ROOT/tmp", "TMP": "$ROOT/tmp", "TEMP": "$ROOT/tmp",
		}},
		{ID: "standalone-symseek-before-legacy", Environment: baseEnv(), SeedFiles: map[string]string{
			"$ROOT/data/symdesk/symseek.db":                   "old primary",
			"$ROOT/home/.local/share/symaira-seek/symseek.db": "legacy",
		}},
		{ID: "standalone-primary-wins", Environment: baseEnv(), SeedFiles: map[string]string{
			"$ROOT/data/symdesk/retrieval.db":                 "primary",
			"$ROOT/data/symdesk/symseek.db":                   "old primary",
			"$ROOT/home/.local/share/symaira-seek/symseek.db": "legacy",
		}},
		{ID: "vault-data-home", Environment: baseEnv(), VaultRoot: vault},
		{ID: "vault-temp-data-home", Environment: map[string]string{
			"HOME": "$ROOT/home", "USERPROFILE": "$ROOT/home", "XDG_DATA_HOME": "", "TMPDIR": "$ROOT/tmp", "TMP": "$ROOT/tmp", "TEMP": "$ROOT/tmp",
		}, VaultRoot: "$TMPDIR/vault"},
		{ID: "configured-relative-over-vault", Environment: baseEnv(), VaultRoot: vault,
			ConfigTOML: "index_path = \"custom/nested/../override.db\"\nmodel = \"pinned-model\"\n",
			SeedFiles:  map[string]string{"$ROOT/elsewhere-config/symseek/config.toml": "index_path = \"wrong.db\"\n"}},
		{ID: "whitespace-override-falls-back", Environment: baseEnv(),
			ConfigTOML: "index_path = \"  \"\n"},
		{ID: "whitespace-vault-is-standalone", Environment: baseEnv(), VaultRoot: "   "},
		{ID: "legacy-json-migration", Environment: baseEnv(), LegacyJSON: `{"model":"migrated-model","embedding_dim":512,"vector_quantization":"turbo-prod"}`},
		{ID: "invalid-toml", Environment: baseEnv(), ConfigTOML: "index_path = [1]\n"},
	}
}

func observeIndexLocationCase(t *testing.T, c *indexLocationCase) {
	t.Helper()
	root := t.TempDir()
	setupIndexFixtureCase(t, root, c.Environment, c.ConfigTOML, c.LegacyJSON, c.SeedFiles)
	vaultRoot := expandFixturePath(c.VaultRoot, root)
	if vaultRoot != "" {
		if err := os.MkdirAll(vaultRoot, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	actual, err := IndexLocationForVault(vaultRoot)
	if err != nil {
		c.ErrorPrefix = strings.SplitN(err.Error(), ":", 2)[0]
		return
	}
	c.ExpectedPath = normalizeFixturePath(actual, root)
	loaded, err := config.Reload()
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	c.ConfigAfter = normalizeConfigJSON(t, encoded, root)
	_, statErr := os.Stat(filepath.Join(root, "home", ".config", "symseek", "config.toml"))
	c.Migrated = c.LegacyJSON != "" && statErr == nil
}

func observeIndexRelocation(t *testing.T) indexRelocationFixture {
	t.Helper()
	root := t.TempDir()
	environment := map[string]string{
		"HOME": "$ROOT/home", "USERPROFILE": "$ROOT/home", "XDG_DATA_HOME": "$ROOT/data", "TMPDIR": "$ROOT/tmp", "TMP": "$ROOT/tmp", "TEMP": "$ROOT/tmp",
	}
	configTOML := "model = \"preserve-model\"\nembedding_dim = 512\nretry_count = 4\nvector_quantization = \"turbo-prod\"\nvector_quant_bits = 3\nvector_exact_rerank = false\nrerank_query = true\nrerank_model = \"rerank-model\"\nexpand_query = true\nexpand_model = \"expand-model\"\n"
	setupIndexFixtureCase(t, root, environment, configTOML, "", nil)
	location, err := IndexLocationForVault("")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(location), 0o700); err != nil {
		t.Fatal(err)
	}
	input := []relocatePortRow{{ID: 1, Body: "preserve WAL and source"}, {ID: 2, Body: "Müller / 東京"}}
	db, err := sqlitekit.Open(location)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("CREATE TABLE relocation_rows (id INTEGER PRIMARY KEY, body TEXT NOT NULL)"); err != nil {
		_ = db.Close()
		t.Fatal(err)
	}
	for _, row := range input {
		if _, err := db.Exec("INSERT INTO relocation_rows (id, body) VALUES (?, ?)", row.ID, row.Body); err != nil {
			_ = db.Close()
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(root, "cwd", "relocated", "retrieval.db")
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("old destination"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := RelocateIndexForVault("", destination); err != nil {
		t.Fatal(err)
	}
	fixture := indexRelocationFixture{InputRows: input, RelocatedPath: normalizeFixturePath(destination, root), DestinationReplaced: true}
	fixture.SourcePreserved, err = regularFileExists(location)
	if err != nil {
		t.Fatal(err)
	}
	fixture.SourceRowsAfter, err = readRelocationRows(location)
	if err != nil {
		t.Fatal(err)
	}
	fixture.RelocatedRows, err = readRelocationRows(destination)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Reload()
	if err != nil {
		t.Fatal(err)
	}
	configBytes, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	fixture.ConfigAfter = normalizeConfigJSON(t, configBytes, root)
	if loaded.IndexPath != destination {
		t.Fatalf("saved index_path = %q, want %q", loaded.IndexPath, destination)
	}
	beforeReject, err := json.Marshal(loaded)
	if err != nil {
		t.Fatal(err)
	}
	rejectedDestination := filepath.Join(root, "cwd", "must-not-exist.db")
	fixture.VaultRelocationError = "cannot relocate a vault-scoped retrieval index; use backup/restore or run relocate without --vault for a deliberate global index_path override"
	if err := RelocateIndexForVault(" ", rejectedDestination); err == nil || err.Error() != fixture.VaultRelocationError {
		t.Fatalf("vault-scoped relocation error = %v, want %q", err, fixture.VaultRelocationError)
	}
	afterReject, err := config.Reload()
	if err != nil {
		t.Fatal(err)
	}
	afterBytes, err := json.Marshal(afterReject)
	if err != nil {
		t.Fatal(err)
	}
	fixture.ConfigUnchangedAfterReject = bytes.Equal(beforeReject, afterBytes)
	_, statErr := os.Stat(rejectedDestination)
	fixture.RejectedDestinationAbsent = os.IsNotExist(statErr)
	if !fixture.ConfigUnchangedAfterReject || !fixture.RejectedDestinationAbsent {
		t.Fatal("vault-scoped rejection had side effects")
	}
	return fixture
}

func setupIndexFixtureCase(t *testing.T, root string, environment map[string]string, configTOML, legacyJSON string, seedFiles map[string]string) {
	t.Helper()
	cwd := filepath.Join(root, "cwd")
	for _, directory := range []string{cwd, filepath.Join(root, "home"), filepath.Join(root, "tmp")} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(cwd)
	for key, value := range environment {
		t.Setenv(key, expandFixturePath(value, root))
	}
	if configTOML != "" {
		path := filepath.Join(root, "home", ".config", "symseek", "config.toml")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(configTOML), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if legacyJSON != "" {
		path := filepath.Join(root, "home", ".config", "symseek", "config.json")
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(legacyJSON), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, contents := range seedFiles {
		path := expandFixturePath(name, root)
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func expandFixturePath(value, root string) string {
	value = strings.ReplaceAll(value, "$ROOT", root)
	value = strings.ReplaceAll(value, "$TMPDIR", filepath.Join(root, "tmp"))
	return filepath.FromSlash(value)
}

func normalizeFixturePath(value, root string) string {
	value = filepath.Clean(value)
	root = filepath.Clean(root)
	if strings.HasPrefix(value, root+string(filepath.Separator)) || value == root {
		value = "$ROOT" + strings.TrimPrefix(value, root)
	}
	parts := strings.Split(filepath.ToSlash(value), "/")
	for index := 0; index+1 < len(parts); index++ {
		if (parts[index] == "vaults" || parts[index] == "test-vaults") && len(parts[index+1]) == 16 {
			parts[index+1] = "$VAULT_HASH"
		}
	}
	return strings.Join(parts, "/")
}

func normalizeConfigJSON(t *testing.T, source []byte, root string) json.RawMessage {
	t.Helper()
	var config map[string]any
	if err := json.Unmarshal(source, &config); err != nil {
		t.Fatal(err)
	}
	if path, ok := config["index_path"].(string); ok && path != "" {
		config["index_path"] = normalizeFixturePath(path, root)
	}
	encoded, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func regularFileExists(path string) (bool, error) {
	info, err := os.Stat(path)
	if err != nil {
		return false, err
	}
	return info.Mode().IsRegular(), nil
}

func readRelocationRows(path string) ([]relocatePortRow, error) {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query("SELECT id, body FROM relocation_rows ORDER BY id")
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	result := make([]relocatePortRow, 0)
	for rows.Next() {
		var row relocatePortRow
		if err := rows.Scan(&row.ID, &row.Body); err != nil {
			return nil, err
		}
		result = append(result, row)
	}
	return result, rows.Err()
}
