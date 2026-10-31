package store

// The oracle drives the real Store (rules, extractions, poll status/cursor,
// listings, claim ordering, reopen/migration idempotency) with a fake SQLite
// clock. The clock override is process-global, so the test only runs in its own
// opt-in process.
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
	"time"

	sqlite "modernc.org/sqlite"

	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/annotate"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

var portIngestStore = flag.Bool("port-ingest-store", false, "enable isolated whole-store oracle")

type storeOp struct {
	Op string   `json:"op"`
	ID int64    `json:"id"`
	A  string   `json:"a"`
	B  string   `json:"b"`
	C  string   `json:"c"`
	N  int64    `json:"n"`
	M  int64    `json:"m"`
	X  []string `json:"x,omitempty"`
}

type storeStep struct {
	Input  storeOp          `json:"input"`
	Result any              `json:"result"`
	Error  string           `json:"error"`
	Raw    string           `json:"result_json"`
	State  map[string]any   `json:"state"`
	Jobs   []map[string]any `json:"jobs"`
}

func TestPortIngestStoreContract(t *testing.T) {
	if !*portIngestStore {
		t.Skip("isolated SQLite fake-clock oracle; pass -args -port-ingest-store")
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
	path := filepath.Join(t.TempDir(), "nested", "whole.db")
	s, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := s.Close(); err != nil {
			t.Error(err)
		}
	}()

	long := make([]byte, 5000)
	for i := range long {
		long[i] = 'x'
	}
	ops := []storeOp{
		// Rules: validation, ordering, update, delete, unicode, duplicates.
		{Op: "rule_validate", A: "", B: "tag", C: "v"},
		{Op: "rule_validate", A: "p", B: "bogus", C: "v"},
		{Op: "rule_validate", A: "p", B: "", C: "v"},
		{Op: "rule_validate", A: "p", B: "category", C: ""},
		{Op: "rule_validate", A: "p", B: "Category", C: "v"},
		{Op: "rule_validate", A: "p", B: "document_type", C: "v"},
		{Op: "rule_list"},
		{Op: "rule_add", A: "", B: "tag", C: "v"},
		{Op: "rule_add", A: "invoice", B: "bogus", C: "v"},
		{Op: "rule_add", A: "invoice", B: "category", C: ""},
		{Op: "rule_add", A: "invoice", B: "category", C: "Finance"},
		{Op: "advance", A: "2000-01-01 00:00:05"},
		{Op: "rule_add", A: "rechnung", B: "tag", C: "billing"},
		{Op: "rule_add", A: "rechnung", B: "tag", C: "billing"},
		{Op: "rule_add", A: "日本語 ¿ü? <&>", B: "correspondent", C: "ACME \"quoted\" \\ back"},
		{Op: "rule_add", A: string(long), B: "document_type", C: "invoice\nline2\ttab"},
		{Op: "rule_list"},
		{Op: "advance", A: "2000-01-01 00:00:09"},
		{Op: "rule_update", ID: 2, A: "rechnung2", B: "document_type", C: "bill"},
		{Op: "rule_update", ID: 2, A: "", B: "document_type", C: "bill"},
		{Op: "rule_update", ID: 2, A: "x", B: "nope", C: "bill"},
		{Op: "rule_update", ID: 2, A: "x", B: "tag", C: ""},
		{Op: "rule_update", ID: 999, A: "x", B: "tag", C: "y"},
		{Op: "rule_update", ID: 0, A: "x", B: "tag", C: "y"},
		{Op: "rule_update", ID: -1, A: "x", B: "tag", C: "y"},
		{Op: "rule_list"},
		{Op: "rule_delete", ID: 3},
		{Op: "rule_delete", ID: 3},
		{Op: "rule_delete", ID: 999},
		{Op: "rule_add", A: "after-delete", B: "category", C: "autoincrement keeps counting"},
		{Op: "rule_list"},
		// Documents the rest of the store hangs off.
		{Op: "doc_create", A: "a.pdf", B: "h1", C: "application/pdf"},
		{Op: "doc_create", A: "b.pdf", B: "h2", C: "application/pdf", X: []string{"/vault/a"}},
		{Op: "doc_create", A: "c.pdf", B: "h1", C: "application/pdf"},
		{Op: "doc_create", A: "d.pdf", B: "h3", C: "image/png", X: []string{"/vault/b"}},
		// Claim ordering by created_at, then insertion order.
		{Op: "advance", A: "2000-01-01 00:01:00"},
		{Op: "enqueue", ID: 2, A: "ocr"},
		{Op: "advance", A: "2000-01-01 00:00:30"},
		{Op: "enqueue", ID: 1, A: "ocr"},
		{Op: "enqueue", ID: 3, A: "reocr"},
		{Op: "advance", A: "2000-01-01 00:02:00"},
		{Op: "claim"},
		{Op: "claim"},
		{Op: "claim"},
		{Op: "claim"},
		{Op: "fail", ID: 2, A: "boom"},
		{Op: "advance", A: "2000-01-01 00:02:09"},
		{Op: "claim"},
		{Op: "advance", A: "2000-01-01 00:02:10"},
		{Op: "claim"},
		{Op: "complete", ID: 1},
		// Listings.
		{Op: "jobs_list", N: 0},
		{Op: "jobs_list", N: 2},
		{Op: "jobs_list", N: -1},
		{Op: "jobs_page", A: "", N: 2, M: 1},
		{Op: "jobs_page", A: "", N: 2, M: 5},
		{Op: "jobs_page", A: "", N: 0, M: 3},
		{Op: "jobs_page", A: "/vault/a", N: 10, M: 0},
		{Op: "jobs_page", A: "/vault/b", N: 10, M: 0},
		{Op: "jobs_page", A: "/missing", N: 10, M: 0},
		{Op: "jobs_page", A: "", N: 10, M: -3},
		{Op: "jobs_vault", A: "/vault/a", N: 1, M: 0},
		{Op: "jobs_vault", A: "", N: 1, M: 0},
		// Extractions.
		{Op: "ext_list", ID: 1},
		{Op: "ext_record", ID: 1, A: "invoice", X: []string{"date|date|2024-01-02|3|13|on 2024-01-02", "total|amount|12,50 EUR|40|50|Total 12,50 EUR", "nospan|text|plain||", "zerosnip|text|v|0|0|snippet only", "off|text|v|0|4|"}},
		{Op: "ext_record", ID: 1, A: "contract", X: []string{"party|party|ACME & Söhne <GmbH>|10|30|zw\u00f6lf \"x\""}},
		{Op: "ext_list", ID: 1},
		{Op: "ext_list", ID: 2},
		{Op: "ext_record", ID: 999, A: "invoice", X: []string{"date|date|v|1|2|s"}},
		{Op: "ext_record", ID: 2, A: "invoice", X: []string{"ok|text|first|1|2|s", "bad", "later|text|second|1|2|s"}},
		{Op: "ext_list", ID: 2},
		{Op: "ext_record", ID: 2, A: "empty", X: []string{}},
		// Mail poll status and cursor.
		{Op: "status_get", A: "acct-1"},
		{Op: "status_set", A: "acct-1", B: "ok", C: "", N: 946684800},
		{Op: "status_get", A: "acct-1"},
		{Op: "status_set", A: "acct-1", B: "error", C: "network timeout", N: 946688400},
		{Op: "status_get", A: "acct-1"},
		{Op: "status_set", A: "acct-2", B: "ok", C: "", N: 946771200},
		{Op: "status_get", A: "acct-2"},
		{Op: "status_get", A: "missing"},
		{Op: "status_set_frac", A: "acct-3", B: "ok", C: "", N: 946684800, M: 123456789},
		{Op: "status_get", A: "acct-3"},
		{Op: "cursor_get", A: "acct-1"},
		{Op: "cursor_set", A: "acct-1", B: "INBOX", N: 7, M: 42},
		{Op: "cursor_get", A: "acct-1"},
		{Op: "cursor_set", A: "acct-1", B: "Archive/2024", N: 8, M: 0},
		{Op: "cursor_get", A: "acct-1"},
		{Op: "cursor_set", A: "acct-2", B: "INBOX", N: 4294967295, M: 4294967295},
		{Op: "cursor_get", A: "acct-2"},
		{Op: "cursor_set", A: "acct-3", B: "", N: 0, M: 0},
		{Op: "cursor_get", A: "acct-3"},
		{Op: "cursor_get", A: "missing"},
		// Mail dedupe.
		{Op: "mail_has", A: "<m1@example>"},
		{Op: "mail_track", A: "<m1@example>", B: "a@example"},
		{Op: "mail_track", A: "<m1@example>", B: "other@example"},
		{Op: "mail_has", A: "<m1@example>"},
		{Op: "mail_has", A: "<M1@example>"},
		{Op: "mail_has", A: ""},
		{Op: "mail_track", A: "", B: ""},
		{Op: "mail_has", A: ""},
		// Provenance + paths + lookups after the above.
		{Op: "doc_provenance", ID: 1, A: "<m1@example>", B: "Alice <a@example>"},
		{Op: "doc_paths", ID: 1, A: "/vault/a/n.md", B: "arch.pdf", C: "fin", X: []string{"x"}},
		{Op: "doc_id", ID: 1},
		{Op: "doc_list"},
		// Reopen: migrations are idempotent and the persisted state survives.
		{Op: "reopen"},
		{Op: "rule_list"},
		{Op: "status_get", A: "acct-1"},
		{Op: "cursor_get", A: "acct-1"},
		{Op: "doc_list"},
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
			out = append(out, m)
		}
		if e := rows.Err(); e != nil {
			t.Fatal(e)
		}
		return out
	}
	tables := []string{}
	for _, r := range snapshot("SELECT name FROM sqlite_master WHERE type='table' AND name NOT LIKE 'sqlite_%' ORDER BY name") {
		n := r["name"].(string)
		if n != "schema_migrations" {
			tables = append(tables, n)
		}
	}
	// The stored text of a time.Time parameter is recorded verbatim, so the
	// poll-status snapshot reads the column as text.
	textSnapshot := func(table string) []map[string]any {
		if table == "mail_poll_status" {
			return snapshot("SELECT account_id, CAST(last_polled_at AS TEXT) AS last_polled_at, typeof(last_polled_at) AS last_polled_type, status, last_error FROM mail_poll_status ORDER BY rowid")
		}
		return snapshot("SELECT * FROM " + table + " ORDER BY rowid")
	}

	steps := []storeStep{}
	for _, op := range ops {
		var result any
		var e error
		switch op.Op {
		case "advance":
			now = op.A
		case "rule_validate":
			e = ValidateClassificationRule(op.A, op.B, op.C)
		case "rule_add":
			result, e = s.AddRule(ctx, op.A, op.B, op.C)
		case "rule_list":
			result, e = s.ListRules(ctx)
		case "rule_update":
			result, e = s.UpdateRule(ctx, op.ID, op.A, op.B, op.C)
		case "rule_delete":
			e = s.DeleteRule(ctx, op.ID)
		case "doc_create":
			var d *Document
			var created bool
			d, created, e = s.CreateOrGet(ctx, op.A, op.B, op.C, op.X...)
			result = map[string]any{"document": d, "created": created}
		case "doc_provenance":
			e = s.SetProvenance(ctx, op.ID, op.A, op.B)
		case "doc_paths":
			e = s.SetVaultAndArchivePath(ctx, op.ID, op.A, op.B, op.C, op.X, "ACME", "invoice")
		case "doc_id":
			result, e = s.ByID(ctx, op.ID)
		case "doc_list":
			result, e = s.ListDocuments(ctx)
		case "enqueue":
			result, e = s.EnqueueJob(ctx, op.ID, op.A)
		case "claim":
			result, e = s.ClaimJob(ctx)
		case "fail":
			e = s.FailJob(ctx, op.ID, op.A)
		case "complete":
			e = s.CompleteJob(ctx, op.ID)
		case "jobs_list":
			result, e = s.ListJobs(ctx, int(op.N))
		case "jobs_page":
			var jobs []*Job
			var total int
			jobs, total, e = s.ListJobsPage(ctx, op.A, int(op.N), int(op.M))
			result = map[string]any{"jobs": jobs, "total": total}
		case "jobs_vault":
			var jobs []*Job
			var total int
			jobs, total, e = s.ListJobsInVault(ctx, op.A, int(op.N), int(op.M))
			result = map[string]any{"jobs": jobs, "total": total}
		case "ext_record":
			ex := []annotate.Extraction{}
			for _, spec := range op.X {
				var field, typ, value string
				var start, end int
				var snippet string
				parts := splitPipe(spec)
				if len(parts) < 3 {
					ex = append(ex, annotate.Extraction{Field: spec, Type: spec, Value: ""})
					continue
				}
				field, typ, value = parts[0], parts[1], parts[2]
				_ = typ
				x := annotate.Extraction{Field: field, Type: typ, Value: value, Matched: true}
				if len(parts) >= 6 {
					_, _ = fmt.Sscanf(parts[3], "%d", &start)
					_, _ = fmt.Sscanf(parts[4], "%d", &end)
					snippet = parts[5]
					if parts[3] != "" || parts[4] != "" || snippet != "" {
						x.Span = &annotate.Span{Start: start, End: end, Snippet: snippet}
					}
				}
				ex = append(ex, x)
			}
			e = s.RecordExtractions(ctx, op.ID, op.A, ex)
		case "ext_list":
			result, e = s.ListExtractions(ctx, op.ID)
		case "status_get":
			result, e = s.GetMailPollStatus(ctx, op.A)
		case "status_set":
			e = s.RecordMailPollStatus(ctx, op.A, time.Unix(op.N, 0).UTC(), op.B, op.C)
		case "status_set_frac":
			e = s.RecordMailPollStatus(ctx, op.A, time.Unix(op.N, op.M).UTC(), op.B, op.C)
		case "cursor_get":
			result, e = s.GetMailPollCursor(ctx, op.A)
		case "cursor_set":
			e = s.SetMailPollCursor(ctx, op.A, op.B, uint32(op.N), uint32(op.M))
		case "mail_has":
			result, e = s.HasMailMessage(ctx, op.A)
		case "mail_track":
			e = s.TrackMailMessage(ctx, op.A, op.B)
		case "reopen":
			if e = s.Close(); e != nil {
				t.Fatal(e)
			}
			s, e = Open(path)
		default:
			t.Fatal(op.Op)
		}
		text := ""
		if e != nil {
			text = e.Error()
		}
		raw, marshalErr := json.Marshal(result)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		state := map[string]any{}
		for _, table := range tables {
			state[table] = textSnapshot(table)
		}
		state["schema_migrations"] = snapshot("SELECT version FROM schema_migrations ORDER BY version")
		steps = append(steps, storeStep{Input: op, Result: result, Error: text, Raw: string(raw), State: state})
	}
	schema := snapshot("SELECT type, name, tbl_name, sql FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type,name")
	_, thisFile, _, _ := runtime.Caller(0)
	sourceDir := filepath.Dir(thisFile)
	hashes := map[string]string{}
	sources := []string{"store.go", "store_port_test.go"}
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
	content, e := json.MarshalIndent(map[string]any{
		"oracle":         fixtureoracle.Current(),
		"source_hashes":  hashes,
		"schema_version": 1,
		"clock":          "SQLite current_timestamp and datetime(now,-10 seconds) overridden in isolated Go test",
		"normalizations": []string{"none: poll-status timestamps are written from fixed UTC instants so the stored text is deterministic"},
		"schema":         schema,
		"steps":          steps,
	}, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	content = append(content, '\n')
	out := filepath.Join(sourceDir, "../../../../testdata/port/ingest/store.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if e := os.MkdirAll(filepath.Dir(out), 0o700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(out, content, 0o600); e != nil {
			t.Fatal(e)
		}
	} else {
		//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
		old, e := os.ReadFile(out)
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(old, content) {
			t.Fatal("ingest store fixture drift; regenerate deliberately")
		}
	}
	t.Logf("replayed %d store steps", len(steps))
}

func splitPipe(s string) []string {
	var parts []string
	cur := ""
	for _, r := range s {
		if r == '|' {
			parts = append(parts, cur)
			cur = ""
			continue
		}
		cur += string(r)
	}
	return append(parts, cur)
}
