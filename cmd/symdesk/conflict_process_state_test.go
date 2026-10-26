package main

import (
	"database/sql"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
)

func conflictProcessState(t *testing.T, vault, home string) map[string][][]string {
	t.Helper()
	state := linkProcessState(t, vault, home)
	err := filepath.WalkDir(home, func(path string, e fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if e.IsDir() || e.Name() != "retrieval.db" {
			return nil
		}
		prefix := "retrieval-standalone/"
		if strings.Contains(filepath.ToSlash(path), "/vaults/") {
			prefix = "retrieval-vault/"
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
			"documents":  "SELECT path,hash FROM documents ORDER BY path",
			"chunks":     "SELECT document_path,CAST(chunk_index AS TEXT),content,hash,hex(embedding) FROM chunks ORDER BY document_path,chunk_index",
			"generation": "SELECT key,CAST(value AS TEXT) FROM index_meta ORDER BY key",
		} {
			state[prefix+key] = [][]string{}
			rows, err := db.Query(query)
			if err != nil {
				return err
			}
			columns, err := rows.Columns()
			if err != nil {
				_ = rows.Close()
				return err
			}
			for rows.Next() {
				row := make([]string, len(columns))
				ptrs := make([]any, len(columns))
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
				state[prefix+key] = append(state[prefix+key], row)
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
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}
