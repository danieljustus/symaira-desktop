package store

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql/driver"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
	sqlite "modernc.org/sqlite"
)

var portPaperlessState = flag.Bool("port-paperless-state", false, "enable isolated Paperless state clock oracle")

type portStateOp struct {
	Op      string `json:"op"`
	Base    string `json:"base"`
	Vault   string `json:"vault"`
	Archive string `json:"archive"`
	ID      int    `json:"id"`
	Status  string `json:"status"`
	Error   string `json:"error"`
	Note    string `json:"note"`
	File    string `json:"file"`
	Hash    string `json:"hash"`
}

func TestPortPaperlessStateContract(t *testing.T) {
	if !*portPaperlessState {
		t.Skip("requires isolated clock; pass -args -port-paperless-state")
	}
	now := "2000-01-01 00:00:00"
	if e := sqlite.RegisterScalarFunction("current_timestamp", 0, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) { return now, nil }); e != nil {
		t.Fatal(e)
	}
	root := t.TempDir()
	t.Setenv("HOME", root)
	t.Setenv("USERPROFILE", root)
	path := filepath.Join(root, "state.db")
	s, e := Open(path)
	if e != nil {
		t.Fatal(e)
	}
	defer func() {
		if e := s.Close(); e != nil {
			t.Error(e)
		}
	}()
	ctx := context.Background()
	base := "https://paperless.example.test"
	upsert := func(vault, archive string, id int, status, note, file, hash string) portStateOp {
		return portStateOp{Op: "upsert", Base: base, Vault: vault, Archive: archive, ID: id, Status: status, Note: note, File: file, Hash: hash}
	}
	ops := []portStateOp{
		{Op: "status", Base: base, ID: 7}, {Op: "get", Base: base, ID: 7}, {Op: "list", Base: base},
		upsert("/vault/a", "/archive/a", 7, "imported", "a.md", "a.pdf", "hash-a"),
		upsert("/vault/b", "/archive/a", 7, "failed", "", "", ""),
		{Op: "status", Base: base, ID: 7}, {Op: "target-status", Base: base, Vault: "/vault/a", Archive: "/archive/a", ID: 7},
		{Op: "target-get", Base: base, Vault: "/vault/a", Archive: "/archive/a", ID: 7},
		{Op: "target-get", Base: base, Vault: "/vault/a", Archive: "/archive/b", ID: 7},
		{Op: "advance", Status: "2000-01-01 01:00:00"},
		upsert("/vault/a", "/archive/a", 7, "skipped", "moved.md", "moved.pdf", "hash-a"),
		{Op: "status", Base: base, ID: 7}, {Op: "get", Base: base, ID: 7},
		upsert("/vault/a", "/archive/b", 7, "pending", "", "", ""),
		upsert("/vault/a", "/archive/a", 3, "failed", "", "", ""),
		{Op: "upsert", Base: base, Vault: "/vault/a", Archive: "/archive/a", ID: 3, Status: "failed", Error: "download: failed"},
		{Op: "legacy-upsert", Base: base, ID: 7, Status: "imported"},
		{Op: "list", Base: base}, {Op: "list", Base: base, Status: "failed"}, {Op: "list", Base: base, Status: "invalid"},
		{Op: "reopen"}, {Op: "list", Base: base},
		{Op: "target-status", Base: base, Vault: "/missing", Archive: "/archive/a", ID: 7},
		{Op: "upsert", Base: base, ID: 7, Status: "invalid\n\"status"},
		{Op: "upsert", Base: base + "/", ID: 7, Status: "imported"},
		{Op: "list", Base: base + "/"}, {Op: "list", Base: base},
	}
	steps := []any{}
	snapshot := func() any {
		rows, e := s.db.Query(`SELECT id,base_url,target_vault,target_archive,paperless_document_id,status,last_error,vault_path,archive_path,sha256,CAST(created_at AS TEXT) created_at,CAST(updated_at AS TEXT) updated_at FROM paperless_import_state ORDER BY id`)
		if e != nil {
			t.Fatal(e)
		}
		defer rows.Close()
		cols, e := rows.Columns()
		if e != nil {
			t.Fatal(e)
		}
		out := []any{}
		for rows.Next() {
			values := make([]any, len(cols))
			ptr := make([]any, len(cols))
			for i := range values {
				ptr[i] = &values[i]
			}
			if e := rows.Scan(ptr...); e != nil {
				t.Fatal(e)
			}
			m := map[string]any{}
			for i, k := range cols {
				m[k] = values[i]
			}
			out = append(out, m)
		}
		if e := rows.Err(); e != nil {
			t.Fatal(e)
		}
		return out
	}
	for _, op := range ops {
		var result any
		var err error
		switch op.Op {
		case "upsert":
			err = s.UpsertPaperlessImportStateForTarget(ctx, op.Base, op.Vault, op.Archive, op.ID, op.Status, op.Error, op.Note, op.File, op.Hash)
		case "legacy-upsert":
			err = s.UpsertPaperlessImportState(ctx, op.Base, op.ID, op.Status, op.Error)
		case "status", "target-status":
			var status string
			var found bool
			if op.Op == "status" {
				status, found, err = s.PaperlessImportStatus(ctx, op.Base, op.ID)
			} else {
				status, found, err = s.PaperlessImportStatusForTarget(ctx, op.Base, op.Vault, op.Archive, op.ID)
			}
			result = map[string]any{"status": status, "found": found}
		case "get":
			result, err = s.PaperlessImportStateByID(ctx, op.Base, op.ID)
		case "target-get":
			result, err = s.PaperlessImportStateForTarget(ctx, op.Base, op.Vault, op.Archive, op.ID)
		case "list":
			result, err = s.ListPaperlessImportState(ctx, op.Base, op.Status)
		case "advance":
			now = op.Status
		case "reopen":
			if e := s.Close(); e != nil {
				t.Fatal(e)
			}
			s, err = Open(path)
		}
		message := ""
		if err != nil {
			message = err.Error()
		}
		var sequence int64
		if e := s.db.QueryRow("SELECT COALESCE(MAX(seq),0) FROM sqlite_sequence WHERE name='paperless_import_state'").Scan(&sequence); e != nil {
			t.Fatal(e)
		}
		steps = append(steps, map[string]any{"input": op, "result": result, "error": message, "rows": snapshot(), "sequence": sequence})
	}
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Dir(file)
	hashes := map[string]string{}
	for _, name := range []string{"store.go", "paperless_state_port_test.go"} {
		b, e := os.ReadFile(filepath.Join(dir, name))
		if e != nil {
			t.Fatal(e)
		}
		hashes[name] = fmt.Sprintf("%x", sha256.Sum256(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))))
	}
	b, e := json.MarshalIndent(map[string]any{"schema_version": 1, "oracle": fixtureoracle.Current(), "source_hashes": hashes, "steps": steps, "clock": "test-only SQLite current_timestamp override; target roots are opaque keys, never opened"}, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	b = append(b, '\n')
	out := filepath.Join(dir, "../../../../testdata/port/ingest/paperless-state.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if e := os.WriteFile(out, b, 0600); e != nil {
			t.Fatal(e)
		}
	} else {
		old, e := os.ReadFile(out)
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(old, b) {
			t.Fatal("Paperless state fixture drift; regenerate deliberately")
		}
	}
	t.Logf("replayed %d Paperless target-state transitions", len(steps))
}
