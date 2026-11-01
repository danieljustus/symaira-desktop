package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// MCP-003: tool calls of the SymDesk MCP server that the earlier read/write
// process fixtures do not cover. Each case runs one or more real Go
// `symdesk mcp` processes (one per step, so concurrent tools/call dispatch
// cannot reorder output) inside one disposable root, then records the raw
// stdout/stderr/exit code of every step and the complete resulting state
// (files below the root and the rows of every SQLite database).

type mcpCallsStep struct {
	Request  string `json:"request"`
	Framed   bool   `json:"framed,omitempty"`
	Stdout   string `json:"stdout"`
	Stderr   string `json:"stderr"`
	ExitCode int    `json:"exit_code"`
}

type mcpCallsSeedSQL struct {
	DB  string   `json:"db"`
	SQL []string `json:"sql"`
}

type mcpCallsCase struct {
	ID         string                                 `json:"id"`
	AllowWrite bool                                   `json:"allow_write,omitempty"`
	NoVault    bool                                   `json:"no_vault,omitempty"`
	Indexed    bool                                   `json:"indexed,omitempty"`
	Files      map[string]string                      `json:"files,omitempty"`      // below the vault
	RootFiles  map[string]string                      `json:"root_files,omitempty"` // below the disposable root
	Env        map[string]string                      `json:"env,omitempty"`
	Prepare    [][]string                             `json:"prepare,omitempty"` // symdesk CLI runs before the seed SQL
	SeedSQL    []mcpCallsSeedSQL                      `json:"seed_sql,omitempty"`
	Steps      []mcpCallsStep                         `json:"steps"`
	AfterFiles map[string]string                      `json:"after_files"`
	AfterDB    map[string]map[string][]map[string]any `json:"after_db"`
}

type mcpCallsFixture struct {
	SchemaVersion int            `json:"schema_version"`
	Cases         []mcpCallsCase `json:"cases"`
}

// mcpCallsRequest builds a tools/call request around the raw arguments text,
// which may deliberately be malformed JSON.
func mcpCallsRequest(name, args string) string {
	quoted, err := json.Marshal(name)
	if err != nil {
		panic(err)
	}
	params := `{"name":` + string(quoted)
	if args != "" {
		params += `,"arguments":` + args
	}
	return `{"jsonrpc":"2.0","id":"call","method":"tools/call","params":` + params + `}}`
}

// mcpCallsCase1 builds a single-step case.
func mcpCallsCase1(id string, allowWrite bool, name, args string) mcpCallsCase {
	return mcpCallsCase{ID: id, AllowWrite: allowWrite, Steps: []mcpCallsStep{{Request: mcpCallsRequest(name, args)}}}
}

func mcpCallsAllCases() []mcpCallsCase {
	var cases []mcpCallsCase
	cases = append(cases, mcpCallsRulesCases()...)
	return cases
}

var (
	mcpCallsTimestamp = regexp.MustCompile(`\d{4}-\d{2}-\d{2}[T ]\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+-]\d{2}:\d{2})?`)
	mcpCallsAsset     = regexp.MustCompile(`pasted-\d{4}-\d{2}-\d{2}-\d{6}`)
	mcpCallsJournal   = regexp.MustCompile(`journal/\d{4}-\d{2}-\d{2}\.ndjson`)
)

func mcpCallsNormalize(s, root string) string {
	s = strings.ReplaceAll(s, root, "$ROOT")
	s = mcpCallsTimestamp.ReplaceAllString(s, "<TIME>")
	s = mcpCallsJournal.ReplaceAllString(s, "journal/<DATE>.ndjson")
	return mcpCallsAsset.ReplaceAllString(s, "pasted-<CLOCK>")
}

func mcpCallsEnv(root, dbPath string, extra map[string]string) []string {
	env := []string{"HOME=" + filepath.Join(root, "home"), "USERPROFILE=" + filepath.Join(root, "home"), "XDG_CONFIG_HOME=" + filepath.Join(root, "config"), "XDG_CACHE_HOME=" + filepath.Join(root, "cache"), "XDG_DATA_HOME=" + filepath.Join(root, "data"), "TMPDIR=" + filepath.Join(root, "tmp"), "TEMP=" + filepath.Join(root, "tmp"), "TMP=" + filepath.Join(root, "tmp"), "SYMDESK_SIDECAR=" + dbPath, "TZ=UTC", "LANG=C", "LC_ALL=C", "PATH="}
	if s := os.Getenv("SYSTEMROOT"); s != "" {
		env = append(env, "SYSTEMROOT="+s)
	}
	keys := make([]string, 0, len(extra))
	for k := range extra {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+strings.ReplaceAll(extra[k], "$ROOT", root))
	}
	return env
}

func mcpCallsRun(t *testing.T, binary, root string, c mcpCallsCase, args []string, input string) (string, string, int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	//nolint:gosec // binary is the locally built symdesk fixture; arguments come from fixed test cases.
	cmd := exec.CommandContext(ctx, binary, args...)
	cmd.Dir = filepath.Join(root, "cwd")
	cmd.Env = mcpCallsEnv(root, filepath.Join(root, "data", "sidecar.db"), c.Env)
	var stdout, stderr bytes.Buffer
	cmd.Stdin = strings.NewReader(input)
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	code := 0
	if err := cmd.Run(); err != nil {
		exit, ok := err.(*exec.ExitError)
		if !ok {
			t.Fatalf("%s: %v", c.ID, err)
		}
		code = exit.ExitCode()
	}
	if !utf8.Valid(stdout.Bytes()) || !utf8.Valid(stderr.Bytes()) {
		t.Fatalf("%s: process output is not UTF-8", c.ID)
	}
	return stdout.String(), stderr.String(), code
}

func mcpCallsSeedFiles(t *testing.T, base string, files map[string]string) {
	t.Helper()
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		full := filepath.Join(base, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(files[p]), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func mcpCallsIndexVault(t *testing.T, root, vaultRoot string, files map[string]string) {
	t.Helper()
	db, err := sidecar.Open(filepath.Join(root, "data", "sidecar.db"))
	if err != nil {
		t.Fatal(err)
	}
	paths := make([]string, 0, len(files))
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		doc, err := vault.ParseBytes(filepath.Join(vaultRoot, filepath.FromSlash(p)), []byte(files[p]))
		if err != nil {
			t.Fatal(err)
		}
		if err := db.IndexDocument(doc); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
}

func mcpCallsSnapshot(t *testing.T, root string) (map[string]string, map[string]map[string][]map[string]any) {
	t.Helper()
	files := map[string]string{}
	dbs := map[string]map[string][]map[string]any{}
	err := filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			if rel == "cwd" || rel == "tmp" || rel == "cache" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(rel, "-wal") || strings.HasSuffix(rel, "-shm") || strings.HasSuffix(rel, "-journal") {
			return nil
		}
		//nolint:gosec // path is below the test-owned temporary root.
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if strings.HasSuffix(rel, ".db") {
			dbs[rel] = mcpCallsDumpDB(t, path, root)
			return nil
		}
		key := mcpCallsNormalize(rel, root)
		if utf8.Valid(data) {
			files[key] = mcpCallsNormalize(string(data), root)
		} else {
			files[key] = "<BINARY_HEX>" + hex.EncodeToString(data)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return files, dbs
}

func mcpCallsDumpDB(t *testing.T, path, root string) map[string][]map[string]any {
	t.Helper()
	raw, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = raw.Close() }()
	rows, err := raw.Query("SELECT name, sql FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name")
	if err != nil {
		t.Fatal(err)
	}
	type table struct{ name, sql string }
	var tables []table
	for rows.Next() {
		var tb table
		var ddl sql.NullString
		if err := rows.Scan(&tb.name, &ddl); err != nil {
			t.Fatal(err)
		}
		tb.sql = ddl.String
		tables = append(tables, tb)
	}
	if err := rows.Close(); err != nil {
		t.Fatal(err)
	}
	virtual := []string{}
	for _, tb := range tables {
		if strings.HasPrefix(strings.ToUpper(tb.sql), "CREATE VIRTUAL TABLE") {
			virtual = append(virtual, tb.name+"_")
		}
	}
	out := map[string][]map[string]any{}
	for _, tb := range tables {
		skip := strings.HasPrefix(strings.ToUpper(tb.sql), "CREATE VIRTUAL TABLE")
		for _, prefix := range virtual {
			if strings.HasPrefix(tb.name, prefix) {
				skip = true
			}
		}
		if skip {
			continue
		}
		//nolint:gosec // SQL identifiers come from the SQLite schema of a test-owned database.
		result, err := raw.Query(fmt.Sprintf("SELECT * FROM %q ORDER BY 1,2", tb.name))
		if err != nil {
			t.Fatal(err)
		}
		cols, err := result.Columns()
		if err != nil {
			t.Fatal(err)
		}
		entries := []map[string]any{}
		for result.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if err := result.Scan(ptrs...); err != nil {
				t.Fatal(err)
			}
			entry := map[string]any{}
			for i, col := range cols {
				v := vals[i]
				switch x := v.(type) {
				case time.Time:
					v = "<TIME>"
				case []byte:
					if utf8.Valid(x) {
						v = mcpCallsNormalize(string(x), root)
					} else {
						v = "<BLOB>" + hex.EncodeToString(x)
					}
				case string:
					v = mcpCallsNormalize(x, root)
				}
				if strings.Contains(col, "mtime") && v != nil {
					v = "<MTIME>"
				}
				entry[col] = v
			}
			entries = append(entries, entry)
		}
		if err := result.Err(); err != nil {
			t.Fatal(err)
		}
		if err := result.Close(); err != nil {
			t.Fatal(err)
		}
		out[tb.name] = entries
	}
	return out
}

func TestMCPCallsProcessPortFixture(t *testing.T) {
	binary := processFixtureBinary(t)
	fixture := mcpCallsFixture{SchemaVersion: 1}
	seen := map[string]bool{}
	for _, c := range mcpCallsAllCases() {
		if seen[c.ID] {
			t.Fatalf("duplicate case id %s", c.ID)
		}
		seen[c.ID] = true
		root, err := filepath.EvalSymlinks(t.TempDir())
		if err != nil {
			t.Fatal(err)
		}
		for _, dir := range []string{"home", "config", "data", "cache", "tmp", "vault", "cwd"} {
			if err := os.MkdirAll(filepath.Join(root, dir), 0o700); err != nil {
				t.Fatal(err)
			}
		}
		vaultRoot := filepath.Join(root, "vault")
		mcpCallsSeedFiles(t, vaultRoot, c.Files)
		mcpCallsSeedFiles(t, root, c.RootFiles)
		if c.Indexed {
			mcpCallsIndexVault(t, root, vaultRoot, c.Files)
		}
		for _, prepare := range c.Prepare {
			args := append([]string{"--vault", vaultRoot}, prepare...)
			mcpCallsRun(t, binary, root, c, args, "")
		}
		for _, seed := range c.SeedSQL {
			raw, err := sql.Open("sqlite", filepath.Join(root, filepath.FromSlash(seed.DB)))
			if err != nil {
				t.Fatal(err)
			}
			for _, statement := range seed.SQL {
				if _, err := raw.Exec(statement); err != nil {
					t.Fatalf("%s: seed %q: %v", c.ID, statement, err)
				}
			}
			if err := raw.Close(); err != nil {
				t.Fatal(err)
			}
		}
		for i, step := range c.Steps {
			args := []string{"mcp"}
			if !c.NoVault {
				args = []string{"--vault", vaultRoot, "mcp"}
			}
			if c.AllowWrite {
				args = append(args, "--allow-write")
			}
			input := step.Request + "\n"
			if step.Framed {
				input = fmt.Sprintf("Content-Length: %d\r\n\r\n%s", len(step.Request), step.Request)
			}
			stdout, stderr, code := mcpCallsRun(t, binary, root, c, args, input)
			c.Steps[i].Stdout = mcpCallsNormalize(stdout, root)
			c.Steps[i].Stderr = mcpCallsNormalize(stderr, root)
			c.Steps[i].ExitCode = code
		}
		c.AfterFiles, c.AfterDB = mcpCallsSnapshot(t, root)
		fixture.Cases = append(fixture.Cases, c)
	}
	data, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	checkMCPSurfaceBytes(t, "../../testdata/port/mcp/calls-process.json", append(data, '\n'))
}
