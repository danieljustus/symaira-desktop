package main

import (
	"database/sql"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// Snapshot authoritative file bytes and logical derived-index rows. Index
// timestamps and SQLite page layout are intentionally not byte contracts.
func linkProcessState(t *testing.T, vault, home string) map[string][][]string {
	t.Helper()
	state := map[string][][]string{"vault": {}}
	if err := filepath.WalkDir(vault, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(vault, path)
		if err != nil {
			return err
		}
		state["vault"] = append(state["vault"], []string{filepath.ToSlash(rel), string(data)})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if err := filepath.WalkDir(home, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || entry.Name() != "sidecar.db" {
			return nil
		}
		db, err := sql.Open("sqlite", path+"?mode=ro")
		if err != nil {
			return err
		}
		defer func() {
			if err := db.Close(); err != nil {
				t.Error(err)
			}
		}()
		for key, query := range map[string]string{
			"files":      "SELECT path, title FROM files ORDER BY path",
			"links":      "SELECT from_path, to_path, kind FROM links ORDER BY from_path, to_path, kind",
			"properties": "SELECT f.path, p.key, p.value FROM file_properties p JOIN files f ON f.id=p.file_id ORDER BY f.path,p.key,p.value",
		} {
			state[key] = [][]string{}
			rows, err := db.Query(query)
			if err != nil {
				return err
			}
			cols, err := rows.Columns()
			if err != nil {
				_ = rows.Close()
				return err
			}
			for rows.Next() {
				row := make([]string, len(cols))
				ptrs := make([]any, len(cols))
				for i := range row {
					ptrs[i] = &row[i]
				}
				if err := rows.Scan(ptrs...); err != nil {
					_ = rows.Close()
					return err
				}
				for i := range row {
					row[i] = normalizeRetryText(row[i], filepath.Dir(vault))
				}
				state[key] = append(state[key], row)
			}
			err = rows.Err()
			closeErr := rows.Close()
			if err != nil {
				return err
			}
			if closeErr != nil {
				return closeErr
			}
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	sort.Slice(state["vault"], func(i, j int) bool { return state["vault"][i][0] < state["vault"][j][0] })
	return state
}
