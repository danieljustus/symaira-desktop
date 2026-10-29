package store

// The oracle uses the real Store methods and migrations. SQLite clock functions
// are overridden only in this opt-in, isolated test process, never in production.
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
	"strings"
	"testing"
	"time"

	sqlite "modernc.org/sqlite"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

type queueOp struct {
	Op   string `json:"op"`
	ID   int64  `json:"id"`
	Text string `json:"text"`
}
type queueStep struct {
	Input      queueOp          `json:"input"`
	Result     any              `json:"result"`
	Error      string           `json:"error"`
	Jobs       []map[string]any `json:"jobs"`
	Documents  []map[string]any `json:"documents"`
	ResultJSON string           `json:"result_json"`
	State      map[string]any   `json:"state"`
}

var portIngestQueue = flag.Bool("port-ingest-queue", false, "enable isolated ingest queue oracle")

func TestPortIngestQueueContract(t *testing.T) {
	if !*portIngestQueue {
		t.Skip("isolated SQLite fake-clock oracle; pass -args -port-ingest-queue")
	}
	now := "2000-01-01 00:00:00"
	if err := sqlite.RegisterScalarFunction("current_timestamp", 0, func(_ *sqlite.FunctionContext, _ []driver.Value) (driver.Value, error) { return now, nil }); err != nil {
		t.Fatal(err)
	}
	if err := sqlite.RegisterScalarFunction("datetime", -1, func(_ *sqlite.FunctionContext, args []driver.Value) (driver.Value, error) {
		if len(args) == 2 && args[0] == "now" && args[1] == "-10 seconds" {
			v, e := time.Parse("2006-01-02 15:04:05", now)
			if e != nil {
				return nil, e
			}
			return v.Add(-10 * time.Second).Format("2006-01-02 15:04:05"), nil
		}
		return nil, fmt.Errorf("unexpected datetime arguments: %v", args)
	}); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "queue.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()
	for _, v := range []string{"/vault/a", "/vault/b"} {
		if _, _, err := s.CreateOrGet(ctx, "input.pdf", "hash", "application/pdf", v); err != nil {
			t.Fatal(err)
		}
	}
	ops := []queueOp{
		{"enqueue", 1, "ocr"}, {"enqueue", 1, "ocr"}, {"reocr", 1, ""}, {"reocr", 1, ""},
		{"claim_id", 1, ""}, {"claim_id", 1, ""}, {"advance", 0, "2000-01-01 01:00:00"}, {"claim_id", 1, ""},
		{"fail", 1, "first"}, {"claim_id", 1, ""}, {"advance", 0, "2000-01-01 01:00:09"}, {"claim_id", 1, ""},
		{"advance", 0, "2000-01-01 01:00:10"}, {"claim_id", 1, ""}, {"fail", 1, "second"},
		{"advance", 0, "2000-01-01 01:00:20"}, {"claim_id", 1, ""}, {"fail", 1, "terminal"},
		{"advance", 0, "2000-01-01 01:00:30"}, {"claim_id", 1, ""}, {"retry", 1, ""}, {"claim_id", 1, ""},
		{"restart", 0, ""}, {"claim_id", 1, ""}, {"complete", 1, ""}, {"claim_id", 1, ""},
		{"fail", 1, "completed can fail"}, {"retry", 1, ""}, {"complete", 1, ""},
		{"claim", 0, ""}, {"complete", 2, ""}, {"claim", 0, ""}, {"reocr", 1, ""}, {"complete", 3, ""}, {"reocr", 1, ""},
		{"skipped", 2, "duplicate"}, {"claim_id", 5, ""}, {"retry", 5, ""}, {"claim_id", 5, ""}, {"reset", 0, ""},
		{"complete", 999, ""}, {"fail", 999, "missing"}, {"retry", 999, ""}, {"claim_id", 999, ""},
		{"list", 0, ""}, {"list", 1, "/vault/a"}, {"list", 1, "/vault/b"}, {"list", -1, "/missing"},
		{"claim_id", 4, ""}, {"complete", 4, ""}, {"claim_id", 5, ""}, {"complete", 5, ""}, {"claim", 0, ""},
		{"doc_create", 0, "/vault/a"}, {"doc_create", 0, "/vault/c"}, {"doc_create", 0, ""},
		{"doc_paths", 4, "/vault/legacy/note.md"}, {"doc_create", 0, "/vault/legacy"},
		{"doc_provenance", 1, ""}, {"doc_id", 1, ""}, {"doc_hash", 0, ""}, {"doc_vault", 0, "/vault/a"},
		{"doc_archive", 0, "archive.pdf"}, {"doc_list", 0, ""}, {"doc_paths", 999, "missing.md"},
		{"doc_id", 999, ""}, {"doc_vault", 0, "/missing"}, {"doc_archive", 0, "missing.pdf"},
		{"enqueue", 999, "ocr"}, {"reocr", 999, ""}, {"skipped", 999, "bad"},
		{"mail_has", 0, "message-id"}, {"mail_track", 0, "message-id"}, {"mail_track", 0, "message-id"}, {"mail_has", 0, "message-id"},
	}
	snapshot := func(query string) []map[string]any {
		rows, e := s.db.Query(query)
		if e != nil {
			t.Fatal(e)
		}
		defer func() {
			if err := rows.Close(); err != nil {
				t.Fatal(err)
			}
		}()
		cols, e := rows.Columns()
		if e != nil {
			t.Fatal(e)
		}
		out := []map[string]any{}
		for rows.Next() {
			vals := make([]any, len(cols))
			ptrs := make([]any, len(cols))
			for i := range vals {
				ptrs[i] = &vals[i]
			}
			if e := rows.Scan(ptrs...); e != nil {
				t.Fatal(e)
			}
			m := map[string]any{}
			for i, k := range cols {
				switch v := vals[i].(type) {
				case time.Time:
					m[k] = v.Format("2006-01-02 15:04:05")
				case []byte:
					m[k] = string(v)
				default:
					m[k] = v
				}
			}
			if p, ok := m["vault_path"].(string); ok {
				m["vault_path"] = strings.ReplaceAll(p, "\\", "/")
			}
			out = append(out, m)
		}
		if e := rows.Err(); e != nil {
			t.Fatal(e)
		}
		return out
	}
	tables := []string{}
	for _, r := range snapshot("SELECT name FROM sqlite_master WHERE type='table' ORDER BY name") {
		tables = append(tables, r["name"].(string))
	}
	steps := []queueStep{}
	for _, op := range ops {
		var result any
		var e error
		switch op.Op {
		case "doc_create":
			var d *Document
			var created bool
			d, created, e = s.CreateOrGet(ctx, "other.pdf", "hash", "changed/mime", op.Text)
			result = map[string]any{"document": d, "created": created}
		case "doc_paths":
			path := op.Text
			if path == "/vault/legacy/note.md" {
				path = "/vault/legacy" + string(filepath.Separator) + "note.md"
			}
			e = s.SetVaultAndArchivePath(ctx, op.ID, path, "archive.pdf", "finance", []string{"a&b", "<tag>", "日本語"}, "ACME", "invoice")
		case "doc_provenance":
			e = s.SetProvenance(ctx, op.ID, "mail-id", "sender")
		case "doc_id":
			result, e = s.ByID(ctx, op.ID)
		case "doc_hash":
			result, e = s.ByHash(ctx, "hash")
		case "doc_vault":
			result, e = s.ByHashInVault(ctx, "hash", op.Text)
		case "doc_archive":
			result, e = s.ByArchivePath(ctx, op.Text)
		case "doc_list":
			result, e = s.ListDocuments(ctx)
		case "mail_has":
			result, e = s.HasMailMessage(ctx, op.Text)
		case "mail_track":
			e = s.TrackMailMessage(ctx, op.Text, "INBOX")
		case "advance":
			now = op.Text
		case "enqueue":
			result, e = s.EnqueueJob(ctx, op.ID, op.Text)
		case "skipped":
			result, e = s.EnqueueSkippedJob(ctx, op.ID, "ocr", op.Text)
		case "reocr":
			var j *Job
			var created bool
			j, created, e = s.EnqueueReprocessJob(ctx, op.ID)
			result = map[string]any{"job": j, "created": created}
		case "claim_id":
			result, e = s.ClaimJobByID(ctx, op.ID)
		case "claim":
			result, e = s.ClaimJob(ctx)
		case "complete":
			e = s.CompleteJob(ctx, op.ID)
		case "fail":
			e = s.FailJob(ctx, op.ID, op.Text)
		case "retry":
			e = s.RetryJob(ctx, op.ID)
		case "reset":
			e = s.ResetRunningJobs(ctx)
		case "restart":
			if e = s.Close(); e != nil {
				t.Fatal(e)
			}
			s, e = Open(path)
			if e == nil {
				e = s.ResetRunningJobs(ctx)
			}
		case "list":
			var jobs []*Job
			var total int
			jobs, total, e = s.ListJobsPage(ctx, op.Text, int(op.ID), 1)
			result = map[string]any{"jobs": jobs, "total": total}
		default:
			t.Fatal(op.Op)
		}
		text := ""
		if e != nil {
			text = e.Error()
		}
		normalizeDocument := func(d *Document) {
			if d != nil && d.VaultPath != nil {
				*d.VaultPath = strings.ReplaceAll(*d.VaultPath, "\\", "/")
			}
		}
		switch r := result.(type) {
		case *Document:
			normalizeDocument(r)
		case []*Document:
			for _, d := range r {
				normalizeDocument(d)
			}
		case map[string]any:
			if d, ok := r["document"].(*Document); ok {
				normalizeDocument(d)
			}
		}
		raw, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		state := map[string]any{}
		for _, table := range tables {
			state[table] = snapshot("SELECT * FROM " + table + " ORDER BY rowid")
		}
		steps = append(steps, queueStep{op, result, text, snapshot("SELECT * FROM jobs ORDER BY id"), snapshot("SELECT * FROM documents ORDER BY id"), string(raw), state})
	}
	schema := snapshot("SELECT type, name, tbl_name, sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name")
	_, thisFile, _, _ := runtime.Caller(0)
	sourceDir := filepath.Dir(thisFile)
	hashes := map[string]string{}
	sources := []string{"store.go", "queue_port_test.go"}
	migrationFiles, e := filepath.Glob(filepath.Join(sourceDir, "migrations/*.sql"))
	if e != nil {
		t.Fatal(e)
	}
	for _, f := range migrationFiles {
		sources = append(sources, "migrations/"+filepath.Base(f))
	}
	for _, name := range sources {
		//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
		b, e := os.ReadFile(filepath.Join(sourceDir, name))
		if e != nil {
			t.Fatal(e)
		}
		hashes[name] = fmt.Sprintf("%x", sha256.Sum256(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))))
	}
	content, e := json.MarshalIndent(map[string]any{"normalizations": []string{"only vault_path path separators canonicalized to slash; synthetic vault roots remain opaque keys"}, "oracle": fixtureoracle.Current(), "source_hashes": hashes, "schema_version": 1, "clock": "SQLite current_timestamp and datetime(now,-10 seconds) overridden in isolated Go test", "schema": schema, "steps": steps}, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	content = append(content, '\n')
	_, file, _, _ := runtime.Caller(0)
	out := filepath.Join(filepath.Dir(file), "../../../../testdata/port/ingest/queue.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if e := os.MkdirAll(filepath.Dir(out), 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(out, content, 0600); e != nil {
			t.Fatal(e)
		}
	} else {
		//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
		old, e := os.ReadFile(out)
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(old, content) {
			t.Fatal("ingest queue fixture drift; regenerate deliberately")
		}
	}
	t.Logf("replayed %d state-machine steps", len(steps))
}
