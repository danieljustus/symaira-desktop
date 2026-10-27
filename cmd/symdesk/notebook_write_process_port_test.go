package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-corekit/sqlitekit"
)

// Only generated wall-clock fields are normalized. Existing notebook creation
// values are the literal "fixed"; source bodies contain no timestamps.
var notebookClock = regexp.MustCompile(`\d{4}-\d\d-\d\dT\d\d:\d\d:\d\d(?:\.\d+)?Z`)

var notebookQueries = map[string]string{
	"files":      "SELECT path,title FROM files ORDER BY path",
	"properties": "SELECT f.path,p.key,COALESCE(p.value,''),p.value_type FROM file_properties p JOIN files f ON f.id=p.file_id ORDER BY f.path,p.key",
	"links":      "SELECT from_path,to_path,kind FROM links ORDER BY from_path,to_path,kind",
	"fts":        "SELECT title,body FROM fts_search ORDER BY title,body",
}

func notebookWriteState(t *testing.T, root string) viewsPortState {
	state := observeViewsState(t, root)
	for name, encoded := range state.Files {
		data, err := hex.DecodeString(encoded)
		if err != nil {
			t.Fatal(err)
		}
		state.Files[name] = hex.EncodeToString(notebookClock.ReplaceAll(data, []byte("$$TIME")))
	}
	state.Tables = map[string][][]string{}
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".db") {
			return nil
		}
		db, err := sqlitekit.Open(path)
		if err != nil {
			return err
		}
		defer func() {
			if err := db.Close(); err != nil {
				t.Error(err)
			}
		}()
		// Assert each indexed hash matches the authoritative raw vault bytes before
		// normalizing clocks; a matching row count alone is not write parity.
		rows, err := db.Query("SELECT path,sha256 FROM files ORDER BY path")
		if err != nil {
			return err
		}
		for rows.Next() {
			var file, hash string
			if err := rows.Scan(&file, &hash); err != nil {
				return err
			}
			//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
			data, err := os.ReadFile(file)
			if err != nil {
				return err
			}
			sum := sha256.Sum256(data)
			if fmt.Sprintf("%x", sum) != hash {
				t.Errorf("indexed content hash mismatch for %s", file)
			}
		}
		if err := rows.Err(); err != nil {
			return err
		}
		if err := rows.Close(); err != nil {
			return err
		}
		for key, query := range notebookQueries {
			rows, err := db.Query(query)
			if err != nil {
				return err
			}
			columns, err := rows.Columns()
			if err != nil {
				return err
			}
			records := [][]string{}
			for rows.Next() {
				record := make([]string, len(columns))
				dest := make([]interface{}, len(columns))
				for i := range record {
					dest[i] = &record[i]
				}
				if err := rows.Scan(dest...); err != nil {
					return err
				}
				for i := range record {
					record[i] = notebookClock.ReplaceAllString(normalizeRetryText(record[i], root), "$$TIME")
				}
				records = append(records, record)
			}
			if err := rows.Err(); err != nil {
				return err
			}
			if err := rows.Close(); err != nil {
				return err
			}
			state.Tables[key] = records
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func notebookWriteCases() []duplicatesPortCase {
	step := func(args ...string) duplicatesPortStep { return duplicatesPortStep{Args: args} }
	seed := "---\ntype: notebook\nnotebook_id: work\ntitle: Work & notes\ncreated: fixed\nsources: [notes/a.md]\n---\n# Custom body\n"
	var edits []duplicatesPortStep
	for _, args := range [][]string{
		{"add-source", "work", "notes/b.md"}, {"add-source", "work", "notes/b.md"},
		{"remove-source", "work", "notes/a.md"}, {"remove-source", "work", "absent.md"},
		{"add-source", "work", "notebooks/work.md"}, {"add-source", "work", ""},
		{"remove-source", "work", "notebooks/work.md"}, {"remove-source", "work", ""},
		{"add-source", "work", "../outside.md"}, {"remove-source", "work", "../outside.md"},
		{"add-source", "work", " notes/b.md "}, {"remove-source", "work", " notes/b.md "},
		{"add-source", "missing", "a.md"}, {"remove-source", "missing", "a.md"},
		{"add-source"}, {"remove-source", "work"}, {"new"}, {"new", ""},
	} {
		edits = append(edits, step(append([]string{"--json", "notebook"}, args...)...))
	}
	edits = append(edits, step("notebook", "add-source", "work", "notes/a.md"), step("notebook", "remove-source", "work", "notes/b.md"))
	return []duplicatesPortCase{
		{Name: "edits", SkipIndex: true, Files: map[string]string{"notebooks/work.md": seed, "notes/a.md": "# Alpha\n", "notes/b.md": "# Beta\n"}, Steps: edits},
		{Name: "create", SkipIndex: true, Steps: []duplicatesPortStep{
			step("--json", "notebook", "new", " New & Research ", "--description", "Purpose <test>"),
			step("notebook", "new", "New & Research"),
			step("--json", "notebook", "new", "!!!"),
			step("notebook", "new", "Title", "--output", "json"),
		}},
	}
}

func TestPortNotebookWriteProcessContract(t *testing.T) {
	binary := buildIndexRetrievalBinary(t, t.TempDir())
	observed := viewsPortFixture{SchemaVersion: 1}
	for _, c := range notebookWriteCases() {
		result := observeViewsCaseWithState(t, binary, c, notebookWriteState)
		for i := range result.Steps {
			result.Steps[i].Stdout = normalizeNotebookPaths(notebookClock.ReplaceAllString(result.Steps[i].Stdout, "$$TIME"))
		}
		observed.Cases = append(observed.Cases, result)
	}
	encoded, err := json.MarshalIndent(observed, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("source path unavailable")
	}
	path := filepath.Join(filepath.Dir(source), "../../testdata/port/cli/notebook-write-process.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded) {
		t.Fatal("notebook write process fixture drift; regenerate deliberately")
	}
}
