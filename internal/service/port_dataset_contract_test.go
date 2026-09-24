package service

// Go-owned oracle for contract row DATA-001 (dataset sync).
//
// The fixture pins what Go's DatasetImport actually produces: the Markdown
// handle bytes written before any rows are projected, the raw asset path, the
// deduplicated sidecar projection read back through sidecar.DB.DatasetRows,
// and the wrapped errors for the rejected inputs. Rust replays these bytes
// read-only and must reproduce them exactly.
//
// Regenerate only with PORT_GENERATE=1.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/dataset"
	"github.com/danieljustus/symaira-desktop/internal/dbviews"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

const portDatasetFixturePath = "../../testdata/port/dataset/sync.json"

type portDatasetSidecarRow struct {
	DatasetSlug string `json:"dataset_slug"`
	RowKey      string `json:"row_key"`
	Identity    string `json:"identity"`
	ValuesJSON  string `json:"values_json"`
	SourcePath  string `json:"source_path"`
	RowNumber   int    `json:"row_number"`
}

type portDatasetFileEvidence struct {
	Path string `json:"path"`
	SHA  string `json:"sha256"`
	Mode string `json:"mode"`
}

type portDatasetCase struct {
	// Inputs
	Name          string `json:"name"`
	SourceName    string `json:"source_name"`
	CSV           string `json:"csv"`
	Title         string `json:"title"`
	Slug          string `json:"slug,omitempty"`
	IdentityField string `json:"identity_field,omitempty"`
	Sensitivity   string `json:"sensitivity,omitempty"`
	RetentionRule string `json:"retention_rule,omitempty"`
	Now           string `json:"now"`
	ImportTwice   bool   `json:"import_twice"`
	Rebuild       bool   `json:"rebuild"`

	// Recorded output. Result holds DatasetImportResult verbatim so the
	// recorded bytes keep Go's own JSON encoding.
	Result       *json.RawMessage        `json:"result,omitempty"`
	Second       *json.RawMessage        `json:"second_result,omitempty"`
	HandleBytes  string                  `json:"handle_bytes,omitempty"`
	Handle       portDatasetFileEvidence `json:"handle"`
	Raw          portDatasetFileEvidence `json:"raw"`
	SidecarRows  []portDatasetSidecarRow `json:"sidecar_rows"`
	SidecarCount int                     `json:"sidecar_count"`
}

type portDatasetCSVCase struct {
	Name          string                            `json:"name"`
	CSV           string                            `json:"csv"`
	IdentityField string                            `json:"identity_field,omitempty"`
	Declared      map[string]dbviews.PropertyConfig `json:"declared,omitempty"`
	Schema        map[string]dbviews.PropertyConfig `json:"schema"`
	SidecarRows   []portDatasetSidecarRow           `json:"sidecar_rows"`
}

type portDatasetCSVErrorCase struct {
	Name           string `json:"name"`
	CSV            string `json:"csv"`
	IdentityField  string `json:"identity_field,omitempty"`
	DeclaredType   string `json:"declared_type,omitempty"`
	DeclaredColumn string `json:"declared_column,omitempty"`
	Error          string `json:"error"`
}

type portDatasetProjectionErrorCase struct {
	Name            string                            `json:"name"`
	SourceName      string                            `json:"source_name"`
	CSV             string                            `json:"csv"`
	IdentityField   string                            `json:"identity_field"`
	Declared        map[string]dbviews.PropertyConfig `json:"declared"`
	ProjectionError string                            `json:"projection_error"`
	Error           string                            `json:"error"`
	Handle          portDatasetFileEvidence           `json:"handle"`
	Raw             portDatasetFileEvidence           `json:"raw"`
	SidecarCount    int                               `json:"sidecar_count"`
}

type portDatasetErrorCase struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`
	Error  string `json:"error"`
}

type portDatasetFixture struct {
	SchemaVersion        int                              `json:"schema_version"`
	Cases                []portDatasetCase                `json:"cases"`
	CSVCases             []portDatasetCSVCase             `json:"csv_cases"`
	CSVErrorCases        []portDatasetCSVErrorCase        `json:"csv_error_cases"`
	ProjectionErrorCases []portDatasetProjectionErrorCase `json:"projection_error_cases"`
	ErrorCases           []portDatasetErrorCase           `json:"error_cases"`
}

func TestPortDatasetSyncContract(t *testing.T) {
	// Every recorded handle/raw carries a POSIX mode, and the only way to obtain
	// one is os.Stat().Perm(), which Windows does not report as 0600/0644. The
	// sidecar metadata oracle skips for the same reason; the modes themselves are
	// still verified by the native macOS/Linux runs, and the Rust replay compares
	// the projection only, which carries no mode.
	if runtime.GOOS == "windows" {
		t.Skip("POSIX file modes are part of this oracle's records and are not observable on Windows")
	}
	got := buildPortDatasetFixture(t)
	encoded, err := json.MarshalIndent(got, "", "  ")
	if err != nil {
		t.Fatalf("encode dataset fixture: %v", err)
	}
	encoded = append(encoded, '\n')

	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(portDatasetFixturePath), 0o700); err != nil {
			t.Fatalf("create fixture directory: %v", err)
		}
		if err := os.WriteFile(portDatasetFixturePath, encoded, 0o600); err != nil {
			t.Fatalf("write dataset fixture: %v", err)
		}
		t.Logf("wrote %s (%d bytes)", portDatasetFixturePath, len(encoded))
		return
	}

	want, err := os.ReadFile(portDatasetFixturePath)
	if err != nil {
		t.Fatalf("fixture is missing (regenerate with PORT_GENERATE=1): %v", err)
	}
	if !bytes.Equal(want, encoded) {
		t.Fatalf("fixture is stale: regenerate with PORT_GENERATE=1 on the pinned Go oracle, then re-run make dataset-sync-differential")
	}
}

// TestPortDatasetSyncModes keeps the file modes asserted on every platform as
// recorded strings. The POSIX bits themselves are only observable on Unix, so
// the real filesystem comparison stays where Go can see it.
func TestPortDatasetSyncModes(t *testing.T) {
	got := buildPortDatasetFixture(t)
	for _, tc := range got.Cases {
		if tc.Result == nil {
			continue
		}
		if tc.Handle.Mode == "" || tc.Raw.Mode == "" {
			t.Fatalf("case %q did not record file modes", tc.Name)
		}
	}
	want, err := os.ReadFile(portDatasetFixturePath)
	if err != nil {
		t.Fatalf("fixture is missing: %v", err)
	}
	var wantFixture portDatasetFixture
	if err := json.Unmarshal(want, &wantFixture); err != nil {
		t.Fatalf("decode fixture: %v", err)
	}
	for _, tc := range wantFixture.Cases {
		if tc.Result == nil {
			continue
		}
		if tc.Handle.Mode == "" || tc.Raw.Mode == "" {
			t.Fatalf("recorded case %q lacks modes", tc.Name)
		}
	}
}

func buildPortDatasetFixture(t *testing.T) portDatasetFixture {
	t.Helper()

	now := time.Date(2026, 1, 4, 5, 6, 7, 0, time.UTC)
	nowStr := now.Format(time.RFC3339)

	inputs := []portDatasetCase{
		{
			Name:          "typed-import",
			SourceName:    "orders.csv",
			CSV:           "id,date,amount\norder-1,2026-01-02,12.50\norder-2,2026-01-03,8\n",
			Title:         "Orders",
			IdentityField: "id",
			Now:           nowStr,
		},
		{
			Name:          "second-import-same-day",
			SourceName:    "orders.csv",
			CSV:           "id,date,amount\norder-1,2026-01-02,12.50\norder-2,2026-01-03,8\n",
			Title:         "Orders",
			IdentityField: "id",
			Now:           nowStr,
			ImportTwice:   true,
		},
		{
			Name:          "duplicate-identity",
			SourceName:    "dups.csv",
			CSV:           "id,amount\nrow-1,5\nrow-1,6\nrow-2,7\n",
			Title:         "Duplicates",
			IdentityField: "id",
			Now:           nowStr,
		},
		{
			Name:          "rebuild-equals-import",
			SourceName:    "orders.csv",
			CSV:           "id,date,amount\norder-1,2026-01-02,12.50\norder-2,2026-01-03,8\n",
			Title:         "Rebuildable",
			IdentityField: "id",
			Now:           nowStr,
			Rebuild:       true,
		},
		{
			Name:          "explicit-slug-and-policy",
			SourceName:    "inventory.csv",
			CSV:           "sku,qty\nA-1,3\nA-2,0\n",
			Title:         "Inventory",
			Slug:          "inventory",
			IdentityField: "sku",
			Now:           nowStr,
			Rebuild:       true,
		},
		{
			Name:       "no-identity-field",
			SourceName: "events.csv",
			CSV:        "name,value\nalpha,1\nbeta,2\n",
			Title:      "Events",
			Now:        nowStr,
		},
	}

	cases := make([]portDatasetCase, 0, len(inputs))
	for _, input := range inputs {
		cases = append(cases, runPortDatasetCase(t, input))
	}

	return portDatasetFixture{
		SchemaVersion:        2,
		Cases:                cases,
		CSVCases:             runPortDatasetCSVCases(t),
		CSVErrorCases:        runPortDatasetCSVErrorCases(t),
		ProjectionErrorCases: runPortDatasetProjectionErrorCases(t, now),
		ErrorCases:           runPortDatasetErrorCases(t),
	}
}

// Successful parser/projection cases are kept separate from full DatasetImport
// so the Rust helper can replay the exact ParseCSV + replaceDatasetRows seam.
func runPortDatasetCSVCases(t *testing.T) []portDatasetCSVCase {
	t.Helper()

	inputs := []portDatasetCSVCase{
		{
			Name:          "unicode-blank-lines-and-quoted-crlf",
			CSV:           "id,note,detail\r\n\r\nu-1,Grüße,\"line one\r\nline two\"\r\n\r\nu-2,東京,plain\r\n",
			IdentityField: "id",
		},
		{
			Name:          "go-number-literals-and-json-thresholds",
			CSV:           "id,value\nhex,0x1p+2\nunderscore,1_000\nsmall,1e-7\nfixed-edge,1e-6\nlarge-fixed,1e20\nlarge-exp,1e21\nnegative-zero,-0\n",
			IdentityField: "id",
			Declared: map[string]dbviews.PropertyConfig{
				"value": {Type: "number"},
			},
		},
		{
			Name:          "all-go-date-layouts",
			CSV:           "id,when\ndate,2026-01-02\nrfc3339,2026-01-02T15:04:05Z\nseconds,2026-01-02 15:04:05\nminutes,2026-01-02 15:04\n",
			IdentityField: "id",
			Declared: map[string]dbviews.PropertyConfig{
				"when": {Type: "date"},
			},
		},
		{
			Name:          "go-boolean-spellings",
			CSV:           "id,enabled\none,1\nt,T\nupper,TRUE\ntitle,True\nzero,0\nf,F\nfalse-upper,FALSE\nfalse-title,False\n",
			IdentityField: "id",
			Declared: map[string]dbviews.PropertyConfig{
				"enabled": {Type: "checkbox"},
			},
		},
	}

	out := make([]portDatasetCSVCase, 0, len(inputs))
	for _, input := range inputs {
		rows, schema, err := dataset.ParseCSV(strings.NewReader(input.CSV), input.Declared, input.IdentityField)
		if err != nil {
			t.Fatalf("csv case %q: ParseCSV: %v", input.Name, err)
		}
		sourcePath := "datasets/oracle/source.csv"
		for i := range rows {
			rows[i].SourcePath = sourcePath
		}
		db, err := sidecar.Open(filepath.Join(t.TempDir(), "sidecar.db"))
		if err != nil {
			t.Fatalf("csv case %q: open sidecar: %v", input.Name, err)
		}
		svc := New(t.TempDir(), db)
		if err := svc.replaceDatasetRows("oracle", rows); err != nil {
			_ = db.Close()
			t.Fatalf("csv case %q: project rows: %v", input.Name, err)
		}
		projection, err := db.DatasetRows("oracle")
		if err != nil {
			_ = db.Close()
			t.Fatalf("csv case %q: read sidecar rows: %v", input.Name, err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("csv case %q: close sidecar: %v", input.Name, err)
		}
		record := input
		record.Schema = schema
		record.SidecarRows = make([]portDatasetSidecarRow, 0, len(projection))
		for _, row := range projection {
			record.SidecarRows = append(record.SidecarRows, portDatasetSidecarRow{
				DatasetSlug: row.DatasetSlug,
				RowKey:      row.RowKey,
				Identity:    row.Identity,
				ValuesJSON:  row.ValuesJSON,
				SourcePath:  row.SourcePath,
				RowNumber:   row.RowNumber,
			})
		}
		out = append(out, record)
	}
	return out
}

// Projection failures are run through DatasetImport so the fixture records the
// existing non-atomic service behavior: raw CSV and handle bytes are already on
// disk when encoding/json rejects NaN or infinity, while sidecar replacement has
// not happened. Rust replays the helper error but must not claim these writes are
// implemented by the helper-only port.
func runPortDatasetProjectionErrorCases(t *testing.T, now time.Time) []portDatasetProjectionErrorCase {
	t.Helper()

	inputs := []portDatasetProjectionErrorCase{
		{
			Name:          "nan-is-not-json",
			SourceName:    "nan.csv",
			CSV:           "id,value\nnan,NaN\n",
			IdentityField: "id",
			Declared: map[string]dbviews.PropertyConfig{
				"value": {Type: "number"},
			},
		},
		{
			Name:          "positive-infinity-is-not-json",
			SourceName:    "infinity.csv",
			CSV:           "id,value\ninf,+Inf\n",
			IdentityField: "id",
			Declared: map[string]dbviews.PropertyConfig{
				"value": {Type: "number"},
			},
		},
	}

	out := make([]portDatasetProjectionErrorCase, 0, len(inputs))
	for _, input := range inputs {
		vaultRoot := t.TempDir()
		sourcePath := filepath.Join(t.TempDir(), input.SourceName)
		if err := os.WriteFile(sourcePath, []byte(input.CSV), 0o600); err != nil {
			t.Fatalf("projection error case %q: write source: %v", input.Name, err)
		}
		db, err := sidecar.Open(filepath.Join(t.TempDir(), "sidecar.db"))
		if err != nil {
			t.Fatalf("projection error case %q: open sidecar: %v", input.Name, err)
		}
		slug := strings.TrimSuffix(input.SourceName, filepath.Ext(input.SourceName))
		parsed, _, err := dataset.ParseCSV(strings.NewReader(input.CSV), input.Declared, input.IdentityField)
		if err != nil {
			_ = db.Close()
			t.Fatalf("projection error case %q: ParseCSV: %v", input.Name, err)
		}
		directErr := New(t.TempDir(), db).replaceDatasetRows(slug, parsed)
		if directErr == nil {
			_ = db.Close()
			t.Fatalf("projection error case %q: direct projection did not fail", input.Name)
		}
		_, importErr := New(vaultRoot, db).DatasetImport(sourcePath, DatasetImportOptions{
			Title:         slug,
			Slug:          slug,
			IdentityField: input.IdentityField,
			Schema:        input.Declared,
			Now:           now,
		})
		if importErr == nil {
			_ = db.Close()
			t.Fatalf("projection error case %q did not fail", input.Name)
		}
		handleRel := filepath.ToSlash(filepath.Join(dataset.RawDir, slug+".md"))
		rawRel := filepath.ToSlash(filepath.Join(dataset.RawDir, slug, now.UTC().Format(time.DateOnly)+".csv"))
		handleAbs, err := vault.SecurePath(vaultRoot, handleRel)
		if err != nil {
			_ = db.Close()
			t.Fatalf("projection error case %q: handle path: %v", input.Name, err)
		}
		rawAbs, err := vault.SecurePath(vaultRoot, rawRel)
		if err != nil {
			_ = db.Close()
			t.Fatalf("projection error case %q: raw path: %v", input.Name, err)
		}
		handleBytes, err := os.ReadFile(handleAbs) //nolint:gosec // path is confined by vault.SecurePath
		if err != nil {
			_ = db.Close()
			t.Fatalf("projection error case %q: expected partial handle write: %v", input.Name, err)
		}
		rawBytes, err := os.ReadFile(rawAbs) //nolint:gosec // path is confined by vault.SecurePath
		if err != nil {
			_ = db.Close()
			t.Fatalf("projection error case %q: expected partial raw write: %v", input.Name, err)
		}
		rows, err := db.DatasetRows(slug)
		if err != nil {
			_ = db.Close()
			t.Fatalf("projection error case %q: read sidecar: %v", input.Name, err)
		}
		if err := db.Close(); err != nil {
			t.Fatalf("projection error case %q: close sidecar: %v", input.Name, err)
		}
		record := input
		record.ProjectionError = directErr.Error()
		record.Error = importErr.Error()
		record.Handle = portDatasetFileEvidence{Path: handleRel, SHA: sha256HexBytes(handleBytes), Mode: fileModeString(t, handleAbs)}
		record.Raw = portDatasetFileEvidence{Path: rawRel, SHA: sha256HexBytes(rawBytes), Mode: fileModeString(t, rawAbs)}
		record.SidecarCount = len(rows)
		out = append(out, record)
	}
	return out
}

// The CSV layer is fully owned by dataset.ParseCSV, so its rejections are
// replayable by Rust even though the service-level rejections above are not.
func runPortDatasetCSVErrorCases(t *testing.T) []portDatasetCSVErrorCase {
	t.Helper()

	inputs := []portDatasetCSVErrorCase{
		{Name: "csv-is-empty", CSV: ""},
		{Name: "empty-column-name", CSV: "id,,amount\n1,2,3\n"},
		{Name: "duplicate-column", CSV: "id,ID\n1,2\n"},
		{Name: "missing-identity-field", CSV: "name\nx\n", IdentityField: "id"},
		{Name: "field-count-mismatch", CSV: "id,amount\n1\n"},
		{Name: "physical-line-field-count", CSV: "id,note\r\n\r\n1,\"line one\r\nline two\"\r\n\r\n2\r\n"},
		{Name: "unterminated-quoted-field", CSV: "id,note\n1,\"unterminated\n"},
		{Name: "bare-quote-in-unquoted-field", CSV: "id,note\n1,bad\"quote\n"},
		{Name: "multiline-quoted-field-error-location", CSV: "id,note\n1,\"line one\nline two\"x\n"},
		{Name: "invalid-number", CSV: "id,amount\n1,abc\n", DeclaredColumn: "amount", DeclaredType: "number"},
		{Name: "invalid-boolean", CSV: "id,enabled\n1,perhaps\n", DeclaredColumn: "enabled", DeclaredType: "checkbox"},
		{Name: "invalid-date", CSV: "id,when\n1,2026/01/02\n", DeclaredColumn: "when", DeclaredType: "date"},
		{Name: "date-trailing-junk", CSV: "id,when\n1,2026-01-02 15:04:05junk\n", DeclaredColumn: "when", DeclaredType: "date"},
		{Name: "date-invalid-offset", CSV: "id,when\n1,2026-01-02T15:04:05+99:99\n", DeclaredColumn: "when", DeclaredType: "date"},
		{Name: "date-leap-second", CSV: "id,when\n1,2026-01-02T15:04:60Z\n", DeclaredColumn: "when", DeclaredType: "date"},
		{Name: "date-nonascii-byte-boundary", CSV: "id,when\n1,2026-01-02T1é:04:05Z\n", DeclaredColumn: "when", DeclaredType: "date"},
	}

	out := make([]portDatasetCSVErrorCase, 0, len(inputs))
	for _, input := range inputs {
		var declared map[string]dbviews.PropertyConfig
		if input.DeclaredColumn != "" {
			declared = map[string]dbviews.PropertyConfig{
				input.DeclaredColumn: {Type: input.DeclaredType},
			}
		}
		_, _, err := dataset.ParseCSV(strings.NewReader(input.CSV), declared, input.IdentityField)
		if err == nil {
			t.Fatalf("csv error case %q did not fail", input.Name)
		}
		input.Error = err.Error()
		out = append(out, input)
	}
	return out
}

func runPortDatasetCase(t *testing.T, input portDatasetCase) portDatasetCase {
	t.Helper()

	now, err := time.Parse(time.RFC3339, input.Now)
	if err != nil {
		t.Fatalf("case %q: parse now: %v", input.Name, err)
	}

	vaultRoot := t.TempDir()
	csvPath := filepath.Join(t.TempDir(), input.SourceName)
	if err := os.WriteFile(csvPath, []byte(input.CSV), 0o600); err != nil {
		t.Fatalf("case %q: write source: %v", input.Name, err)
	}
	dbPath := filepath.Join(t.TempDir(), "sidecar.db")
	db, err := sidecar.Open(dbPath)
	if err != nil {
		t.Fatalf("case %q: open sidecar: %v", input.Name, err)
	}
	defer db.Close() //nolint:errcheck // test handle

	svc := New(vaultRoot, db)
	opts := DatasetImportOptions{
		Title:         input.Title,
		Slug:          input.Slug,
		IdentityField: input.IdentityField,
		Sensitivity:   input.Sensitivity,
		RetentionRule: input.RetentionRule,
		Now:           now,
	}

	first, err := svc.DatasetImport(csvPath, opts)
	if err != nil {
		t.Fatalf("case %q: import: %v", input.Name, err)
	}
	record := input
	record.Result = rawResult(t, first)

	if input.ImportTwice {
		second, err := svc.DatasetImport(csvPath, opts)
		if err != nil {
			t.Fatalf("case %q: second import: %v", input.Name, err)
		}
		record.Second = rawResult(t, second)
	}

	if input.Rebuild {
		if err := svc.RebuildDatasets(); err != nil {
			t.Fatalf("case %q: rebuild: %v", input.Name, err)
		}
	}

	handleAbs, err := vault.SecurePath(vaultRoot, first.HandlePath)
	if err != nil {
		t.Fatalf("case %q: handle path: %v", input.Name, err)
	}
	handleBytes, err := os.ReadFile(handleAbs) //nolint:gosec // path comes from vault.SecurePath over the fixture's recorded handle path
	if err != nil {
		t.Fatalf("case %q: read handle: %v", input.Name, err)
	}
	rawAbs, err := vault.SecurePath(vaultRoot, first.RawPath)
	if err != nil {
		t.Fatalf("case %q: raw path: %v", input.Name, err)
	}
	rawBytes, err := os.ReadFile(rawAbs) //nolint:gosec // path comes from vault.SecurePath over the fixture's recorded raw path
	if err != nil {
		t.Fatalf("case %q: read raw: %v", input.Name, err)
	}

	record.Handle = portDatasetFileEvidence{
		Path: first.HandlePath,
		SHA:  sha256HexBytes(handleBytes),
		Mode: fileModeString(t, handleAbs),
	}
	record.Raw = portDatasetFileEvidence{
		Path: first.RawPath,
		SHA:  sha256HexBytes(rawBytes),
		Mode: fileModeString(t, rawAbs),
	}
	// The handle bytes themselves are pinned too: Rust must reproduce the
	// YAML frontmatter Go emitted, escapes included. Storing them as a JSON
	// string keeps Go's own encoding/json escaping in the fixture.
	record.HandleBytes = string(handleBytes)

	projection, err := db.DatasetRows(first.Slug)
	if err != nil {
		t.Fatalf("case %q: read sidecar rows: %v", input.Name, err)
	}
	rows := make([]portDatasetSidecarRow, 0, len(projection))
	for _, row := range projection {
		rows = append(rows, portDatasetSidecarRow{
			DatasetSlug: row.DatasetSlug,
			RowKey:      row.RowKey,
			Identity:    row.Identity,
			ValuesJSON:  row.ValuesJSON,
			SourcePath:  row.SourcePath,
			RowNumber:   row.RowNumber,
		})
	}
	record.SidecarRows = rows
	record.SidecarCount = len(rows)

	return record
}

func runPortDatasetErrorCases(t *testing.T) []portDatasetErrorCase {
	t.Helper()

	vaultRoot := t.TempDir()
	csvPath := filepath.Join(t.TempDir(), "source.csv")
	if err := os.WriteFile(csvPath, []byte("id,a\n1,2\n"), 0o600); err != nil {
		t.Fatalf("write source: %v", err)
	}
	notCSV := filepath.Join(t.TempDir(), "source.txt")
	if err := os.WriteFile(notCSV, []byte("id,a\n1,2\n"), 0o600); err != nil {
		t.Fatalf("write non-csv source: %v", err)
	}
	// The recorded error carries an absolute path, which would make the
	// fixture differ on every run. Rust applies the same <tmp> substitution
	// before comparing.
	missingPath := filepath.Join(t.TempDir(), "absent.csv")
	missingRoot := filepath.Dir(missingPath)

	type attempt struct {
		name   string
		detail string
		run    func() error
	}
	now := time.Date(2026, 1, 4, 5, 6, 7, 0, time.UTC)
	opts := DatasetImportOptions{Title: "Orders", IdentityField: "id", Now: now}

	attempts := []attempt{
		{
			name:   "missing-vault",
			detail: "Service without a vault root",
			run: func() error {
				_, err := (&Service{DB: &sidecar.DB{}}).DatasetImport(csvPath, opts)
				return err
			},
		},
		{
			name:   "missing-sidecar",
			detail: "Service without a sidecar handle",
			run: func() error {
				_, err := (&Service{VaultRoot: vaultRoot}).DatasetImport(csvPath, opts)
				return err
			},
		},
		{
			name:   "non-csv-source",
			detail: "Importing a .txt file",
			run: func() error {
				db, err := sidecar.Open(filepath.Join(t.TempDir(), "sidecar.db"))
				if err != nil {
					return err
				}
				defer db.Close() //nolint:errcheck // test handle
				_, err = New(vaultRoot, db).DatasetImport(notCSV, opts)
				return err
			},
		},
		{
			name:   "slug-not-filesystem-safe",
			detail: "Explicit slug with a path separator",
			run: func() error {
				db, err := sidecar.Open(filepath.Join(t.TempDir(), "sidecar.db"))
				if err != nil {
					return err
				}
				defer db.Close() //nolint:errcheck // test handle
				bad := opts
				bad.Slug = "../escape"
				_, err = New(vaultRoot, db).DatasetImport(csvPath, bad)
				return err
			},
		},
		{
			name:   "missing-source-file",
			detail: "Source path that does not exist",
			run: func() error {
				db, err := sidecar.Open(filepath.Join(t.TempDir(), "sidecar.db"))
				if err != nil {
					return err
				}
				defer db.Close() //nolint:errcheck // test handle
				_, err = New(vaultRoot, db).DatasetImport(missingPath, opts)
				return err
			},
		},
	}

	out := make([]portDatasetErrorCase, 0, len(attempts))
	for _, a := range attempts {
		err := a.run()
		if err == nil {
			t.Fatalf("error case %q did not fail", a.name)
		}
		msg := strings.ReplaceAll(err.Error(), missingRoot, "<tmp>")
		out = append(out, portDatasetErrorCase{
			Name:   a.name,
			Detail: a.detail,
			Error:  msg,
		})
	}
	return out
}

func rawResult(t *testing.T, result *DatasetImportResult) *json.RawMessage {
	t.Helper()
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatalf("encode import result: %v", err)
	}
	raw := json.RawMessage(encoded)
	return &raw
}

func sha256HexBytes(input []byte) string {
	sum := sha256.Sum256(input)
	return hex.EncodeToString(sum[:])
}

func fileModeString(t *testing.T, path string) string {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return fmt.Sprintf("%04o", info.Mode().Perm())
}
