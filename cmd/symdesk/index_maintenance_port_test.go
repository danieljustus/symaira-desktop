package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-corekit/sqlitekit"
)

type indexMaintenanceProcessResult struct {
	ExitCode int    `json:"exit_code"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
}

type indexMaintenanceProcessRow struct {
	ID   int    `json:"id"`
	Body string `json:"body"`
}

type indexMaintenanceProcessFixture struct {
	SchemaVersion                  int                           `json:"schema_version"`
	Location                       indexMaintenanceProcessResult `json:"location"`
	LocationText                   indexMaintenanceProcessResult `json:"location_text"`
	Backup                         indexMaintenanceProcessResult `json:"backup"`
	BackupRows                     []indexMaintenanceProcessRow  `json:"backup_rows"`
	Restore                        indexMaintenanceProcessResult `json:"restore"`
	SourceRowsAfterRestore         []indexMaintenanceProcessRow  `json:"source_rows_after_restore"`
	Relocate                       indexMaintenanceProcessResult `json:"relocate"`
	LocationAfterRelocate          indexMaintenanceProcessResult `json:"location_after_relocate"`
	SourceRowsAfterRelocate        []indexMaintenanceProcessRow  `json:"source_rows_after_relocate"`
	RelocatedRows                  []indexMaintenanceProcessRow  `json:"relocated_rows"`
	VaultRelocateRejected          indexMaintenanceProcessResult `json:"vault_relocate_rejected"`
	BackupMissingOutput            indexMaintenanceProcessResult `json:"backup_missing_output"`
	SourcePreservedAfterRelocation bool                          `json:"source_preserved_after_relocation"`
	DestinationReplaced            bool                          `json:"destination_replaced"`
}

func TestIndexMaintenanceProcessPortFixture(t *testing.T) {
	fixture := observeIndexMaintenanceProcess(t)
	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path unavailable")
	}
	path := filepath.Join(filepath.Dir(source), "../../testdata/port/cli/index-maintenance-process.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded) {
		t.Fatal("index maintenance process fixture is stale; regenerate explicitly with PORT_GENERATE=1 go test ./cmd/symdesk -run '^TestIndexMaintenanceProcessPortFixture$'")
	}
}

func observeIndexMaintenanceProcess(t *testing.T) indexMaintenanceProcessFixture {
	t.Helper()
	root := t.TempDir()
	home := filepath.Join(root, "home")
	cwd := filepath.Join(root, "cwd")
	dataHome := filepath.Join(root, "data")
	tempRoot := filepath.Join(root, "tmp")
	vault := filepath.Join(root, "vault")
	for _, directory := range []string{home, cwd, dataHome, tempRoot, vault} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	binary := filepath.Join(root, "symdesk")
	if runtime.GOOS == "windows" {
		binary += ".exe"
	}
	if err := os.MkdirAll(filepath.Join(root, "go-build"), 0o700); err != nil {
		t.Fatal(err)
	}
	currentUser, err := user.Current()
	if err != nil {
		t.Fatalf("resolve build cache home: %v", err)
	}
	goEnv := exec.Command("go", "env", "GOMODCACHE")
	goEnv.Env = append(os.Environ(), "HOME="+currentUser.HomeDir)
	moduleCache, err := goEnv.Output()
	if err != nil {
		t.Fatalf("resolve Go module cache: %v", err)
	}
	build := exec.Command("go", "build", "-o", binary, ".")
	build.Dir = "."
	build.Env = append(os.Environ(),
		"HOME="+currentUser.HomeDir,
		"GOMODCACHE="+strings.TrimSpace(string(moduleCache)),
		"GOTMPDIR="+filepath.Join(root, "go-build"),
	)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build Go CLI process fixture: %v\n%s", err, output)
	}

	source := filepath.Join(dataHome, "retrieval.db")
	configPath := filepath.Join(home, ".config", "symseek", "config.toml")
	if err := os.MkdirAll(filepath.Dir(configPath), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte("index_path = "+strconvQuote(source)+"\nmodel = \"process-fixture\"\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	db, err := sqlitekit.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("PRAGMA wal_autocheckpoint=0"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("CREATE TABLE process_rows (id INTEGER PRIMARY KEY, body TEXT NOT NULL)"); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec("INSERT INTO process_rows (id, body) VALUES (?, ?)", 1, "committed only to WAL"); err != nil {
		t.Fatal(err)
	}

	environment := []string{
		"HOME=" + home,
		"USERPROFILE=" + home,
		"XDG_CONFIG_HOME=" + filepath.Join(home, "wrong-config"),
		"XDG_DATA_HOME=" + dataHome,
		"TMPDIR=" + tempRoot,
		"TMP=" + tempRoot,
		"TEMP=" + tempRoot,
		"LANG=C",
		"LC_ALL=C",
		"TZ=UTC",
		"TERM=dumb",
		"NO_COLOR=1",
	}
	run := func(args ...string) indexMaintenanceProcessResult {
		command := exec.Command(binary, args...)
		command.Dir = cwd
		command.Env = environment
		output, err := command.Output()
		result := indexMaintenanceProcessResult{Stdout: normalizeProcessOutput(string(output), root)}
		if err == nil {
			result.ExitCode = 0
			return result
		}
		if exit, ok := err.(*exec.ExitError); ok {
			result.ExitCode = exit.ExitCode()
			result.Stderr = normalizeProcessOutput(string(exit.Stderr), root)
			return result
		}
		t.Fatalf("run Go CLI process %q: %v", strings.Join(args, " "), err)
		return result
	}

	fixture := indexMaintenanceProcessFixture{SchemaVersion: 1}
	fixture.Location = run("--json", "index", "maintenance", "location")
	fixture.LocationText = run("index", "maintenance", "location")
	backup := filepath.Join(root, "backup.db")
	fixture.Backup = run("--output", "json", "index", "maintenance", "backup", "--output", backup)
	fixture.BackupRows = readIndexMaintenanceRows(t, backup)
	if _, err := db.Exec("INSERT INTO process_rows (id, body) VALUES (?, ?)", 2, "later write must be removed by restore"); err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	fixture.Restore = run("--output", "json", "index", "maintenance", "restore", "--input", backup)
	fixture.SourceRowsAfterRestore = readIndexMaintenanceRows(t, source)

	destination := filepath.Join(root, "relocated", "retrieval.db")
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(destination, []byte("old destination"), 0o600); err != nil {
		t.Fatal(err)
	}
	fixture.Relocate = run("--output", "json", "index", "maintenance", "relocate", "--output", destination)
	fixture.LocationAfterRelocate = run("--output", "json", "index", "maintenance", "location")
	fixture.SourceRowsAfterRelocate = readIndexMaintenanceRows(t, source)
	fixture.RelocatedRows = readIndexMaintenanceRows(t, destination)
	fixture.SourcePreservedAfterRelocation = regularIndexFile(t, source)
	fixture.DestinationReplaced = len(fixture.RelocatedRows) == 1 && fixture.RelocatedRows[0].Body == "committed only to WAL"
	fixture.VaultRelocateRejected = run("--output", "json", "--vault", vault, "index", "maintenance", "relocate", "--output", filepath.Join(root, "rejected.db"))
	fixture.BackupMissingOutput = run("--json", "index", "maintenance", "backup")
	return fixture
}

func readIndexMaintenanceRows(t *testing.T, path string) []indexMaintenanceProcessRow {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	rows, err := db.Query("SELECT id, body FROM process_rows ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	result := make([]indexMaintenanceProcessRow, 0)
	for rows.Next() {
		var row indexMaintenanceProcessRow
		if err := rows.Scan(&row.ID, &row.Body); err != nil {
			t.Fatal(err)
		}
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return result
}

func regularIndexFile(t *testing.T, path string) bool {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	return info.Mode().IsRegular()
}

func normalizeProcessOutput(value, root string) string {
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		value = strings.ReplaceAll(value, resolved, "$ROOT")
	}
	value = strings.ReplaceAll(value, root, "$ROOT")
	value = filepath.ToSlash(value)
	if strings.HasPrefix(value, "map[") && strings.HasSuffix(value, "]\n") {
		fields := strings.Fields(strings.TrimSuffix(strings.TrimPrefix(value, "map["), "]\n"))
		sort.Strings(fields)
		return "map[" + strings.Join(fields, " ") + "]\n"
	}
	return value
}

func strconvQuote(value string) string {
	encoded, _ := json.Marshal(value)
	return string(encoded)
}
