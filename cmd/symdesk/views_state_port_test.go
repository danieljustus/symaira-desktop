package main

import (
	"bytes"
	"encoding/hex"
	"fmt"
	"github.com/danieljustus/symaira-corekit/sqlitekit"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type viewsPortState struct {
	Files     map[string]string         `json:"files"`
	Databases map[string]map[string]int `json:"databases"`
	Tables    map[string][][]string     `json:"tables,omitempty"`
}
type viewsPortCase struct {
	duplicatesPortCase
	States []viewsPortState `json:"states"`
}
type viewsPortFixture struct {
	SchemaVersion int             `json:"schema_version"`
	Cases         []viewsPortCase `json:"cases"`
}

func observeViewsCase(t *testing.T, binary string, c duplicatesPortCase) viewsPortCase {
	return observeViewsCaseWithState(t, binary, c, observeViewsState)
}

func observeViewsCaseWithState(t *testing.T, binary string, c duplicatesPortCase, snapshot func(*testing.T, string) viewsPortState) viewsPortCase {
	t.Helper()
	// The service canonicalizes the vault root but the index stores the path
	// as given, so a root behind a symlink (macOS /var -> /private/var) or a
	// Windows 8.3 short name makes every printed path a long "../.." walk
	// between the two spellings. Observe from the canonical spelling so the
	// recorded bytes do not depend on the runner's temp directory.
	root := t.TempDir()
	if resolved, err := filepath.EvalSymlinks(root); err == nil {
		root = resolved
	}
	home := filepath.Join(root, "home")
	vault := filepath.Join(root, "vault")
	cwd := filepath.Join(root, "cwd")
	for _, directory := range []string{home, vault, cwd} {
		if err := os.MkdirAll(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	writeFiles := func(files map[string]string) {
		for name, content := range files {
			target := filepath.Join(vault, filepath.FromSlash(name))
			if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(target, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
		}
	}
	writeFiles(c.Files)
	environment := cleanDuplicatesEnv(os.Environ())
	environment = append(environment, "TZ=UTC", "HOME="+home, "USERPROFILE="+home,
		"XDG_CONFIG_HOME="+filepath.Join(home, "config"),
		"XDG_CACHE_HOME="+filepath.Join(home, "cache"),
		"XDG_DATA_HOME="+filepath.Join(home, "data"))
	run := func(args []string) (int, string, string) {
		cmd := exec.Command(binary, args...) //nolint:gosec // test-only command uses a fixed helper and controlled arguments
		cmd.Dir = cwd
		cmd.Env = environment
		var stdout, stderr bytes.Buffer
		cmd.Stdout, cmd.Stderr = &stdout, &stderr
		code := 0
		if err := cmd.Run(); err != nil {
			exit, ok := err.(*exec.ExitError)
			if !ok {
				t.Fatalf("run Go CLI %v: %v", args, err)
			}
			code = exit.ExitCode()
		}
		return code, stdout.String(), stderr.String()
	}
	if !c.SkipIndex && !c.NoVault {
		if code, _, stderr := run([]string{"--vault", vault, "index"}); code != 0 {
			t.Fatalf("setup index for %s exited %d: %s", c.Name, code, stderr)
		}
	}
	writeFiles(c.PostIndexFiles)
	for name, size := range c.SparseFiles {
		target := filepath.Join(vault, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			t.Fatal(err)
		}
		file, err := os.Create(target) //nolint:gosec // test-only path under a temporary vault
		if err != nil {
			t.Fatal(err)
		}
		if err := file.Truncate(size); err != nil {
			t.Fatal(err)
		}
		if err := file.Close(); err != nil {
			t.Fatal(err)
		}
	}
	observed := viewsPortCase{duplicatesPortCase: c}
	observed.Steps = nil
	for _, step := range c.Steps {
		args := append([]string(nil), step.Args...)
		if !c.NoVault {
			args = append([]string{"--vault", vault}, args...)
		}
		for i := range args {
			args[i] = materializePortProcessArg(args[i], root, string(filepath.Separator))
		}
		code, stdout, stderr := run(args)
		observed.Steps = append(observed.Steps, duplicatesPortStep{
			Args:     step.Args,
			ExitCode: code,
			Stdout:   normalizeRetryText(stdout, root),
			Stderr:   normalizeRetryText(stderr, root),
		})
		observed.States = append(observed.States, snapshot(t, root))
	}
	return observed
}

// Read commands must preserve every vault byte. The only permitted cold-start
// database effect is schema initialization: capture every table's row count,
// including migration and FTS tables, rather than ignoring the derived store.
func observeViewsState(t *testing.T, root string) viewsPortState {
	t.Helper()
	state := viewsPortState{Files: map[string]string{}, Databases: map[string]map[string]int{}}
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		if strings.HasPrefix(rel, "vault/") {
			//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
			data, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			state.Files[rel] = hex.EncodeToString(data)
		}
		if strings.HasSuffix(path, ".db") {
			db, err := sqlitekit.Open(path)
			if err != nil {
				return err
			}
			defer func() {
				if err := db.Close(); err != nil {
					t.Error(err)
				}
			}()
			rows, err := db.Query("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name")
			if err != nil {
				return err
			}
			var names []string
			for rows.Next() {
				var name string
				if err := rows.Scan(&name); err != nil {
					return err
				}
				names = append(names, name)
			}
			if err := rows.Err(); err != nil {
				return err
			}
			if err := rows.Close(); err != nil {
				return err
			}
			counts := map[string]int{}
			for _, name := range names {
				var n int
				//nolint:gosec // table name comes from the test-owned SQLite schema and embedded quotes are escaped.
				query := fmt.Sprintf("SELECT count(*) FROM \"%s\"", strings.ReplaceAll(name, "\"", "\"\""))
				if err := db.QueryRow(query).Scan(&n); err != nil {
					return err
				}
				counts[name] = n
			}
			state.Databases["sidecar"] = counts
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}
