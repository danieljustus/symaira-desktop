package sidecar

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/vault"
)

type portSidecarFixture struct {
	SchemaVersion int                 `json:"schema_version"`
	Documents     []portDocumentInput `json:"documents"`
	UpdatedInput  portDocumentInput   `json:"updated_input"`
	BatchInputs   []portDocumentInput `json:"batch_inputs"`
	Pragmas       map[string]string   `json:"pragmas"`
	Migrations    []string            `json:"migrations"`
	Schema        []portSchemaObject  `json:"schema"`
	Initial       portSidecarState    `json:"initial"`
	Searches      []portSearchCase    `json:"searches"`
	Updated       portSidecarState    `json:"updated"`
	Deleted       portSidecarState    `json:"deleted"`
	BatchError    string              `json:"batch_error"`
	AfterError    portSidecarState    `json:"after_batch_error"`
}

type portDocumentInput struct {
	Path      string `json:"path"`
	Markdown  string `json:"markdown"`
	ModTimeNS int64  `json:"mtime_ns"`
}

type portSchemaObject struct {
	Type string `json:"type"`
	Name string `json:"name"`
	SQL  string `json:"sql"`
}

type portFileRow struct {
	ID           int64   `json:"id"`
	Path         string  `json:"path"`
	SHA256       string  `json:"sha256"`
	Title        string  `json:"title"`
	CreatedAt    string  `json:"created_at"`
	ModifiedAt   string  `json:"modified_at"`
	Type         string  `json:"type"`
	DocumentDate *string `json:"document_date"`
	Person       *string `json:"person"`
	Status       *string `json:"status"`
	DueDate      *string `json:"due_date"`
	Confidence   *int64  `json:"confidence"`
	OCRJSONPath  *string `json:"ocr_json_path"`
	Simhash      *string `json:"simhash"`
	ASN          *int64  `json:"asn"`
	Size         *int64  `json:"size"`
	MTimeNS      *int64  `json:"mtime_ns"`
}

type portPropertyRow struct {
	FileID    int64   `json:"file_id"`
	Key       string  `json:"key"`
	Value     *string `json:"value"`
	ValueType string  `json:"value_type"`
}

type portLinkRow struct {
	From string `json:"from"`
	To   string `json:"to"`
	Kind string `json:"kind"`
}

type portFTSRow struct {
	RowID int64  `json:"rowid"`
	Title string `json:"title,omitempty"`
	Body  string `json:"body,omitempty"`
	Norm  string `json:"norm,omitempty"`
}

type portSidecarState struct {
	Files      []portFileRow     `json:"files"`
	Properties []portPropertyRow `json:"properties"`
	Links      []portLinkRow     `json:"links"`
	FTSSearch  []portFTSRow      `json:"fts_search"`
	FTSNorm    []portFTSRow      `json:"fts_norm"`
	FTSTri     []portFTSRow      `json:"fts_tri"`
}

type portSearchHit struct {
	Path    string `json:"path"`
	Title   string `json:"title"`
	Snippet string `json:"snippet"`
}

type portSearchCase struct {
	Query   string          `json:"query"`
	Scoped  bool            `json:"scoped"`
	Allowed []string        `json:"allowed,omitempty"`
	Hits    []portSearchHit `json:"hits"`
}

func TestPortSidecarContract(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "sidecar.db")
	db, err := Open(dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()

	fixed := time.Date(2026, 1, 15, 12, 0, 0, 123456789, time.UTC)
	initialInputs := []portDocumentInput{
		{Path: "vault/a.md", ModTimeNS: fixed.UnixNano(), Markdown: `---
title: Alpha Note
created: "2026-01-01T10:00:00Z"
tags: [alpha, project]
status: open
---
Alpha body contains a unique orchard marker.
`},
		{Path: "vault/rechnung.md", ModTimeNS: fixed.Add(time.Second).UnixNano(), Markdown: `---
title: Rechnung Müller
created: "2026-01-02T10:00:00Z"
document_date: "2026-01-02"
person: Jörg
asn: 42
aliases: [Faktura]
---
Die Krankenversicherungsbeiträge und Rechnungen sind überfällig.
`},
		{Path: "vault/link.md", ModTimeNS: fixed.Add(2 * time.Second).UnixNano(), Markdown: `---
title: Link Note
created: "2026-01-03T10:00:00Z"
confidence: 91
---
See [[Alpha Note]] and the needle phrase.
`},
	}
	documents := make([]*vault.Document, 0, len(initialInputs))
	for _, input := range initialInputs {
		documents = append(documents, portDocument(t, input))
	}
	if err := db.IndexDocuments(documents); err != nil {
		t.Fatal(err)
	}
	fixture := portSidecarFixture{SchemaVersion: 1, Documents: initialInputs}
	fixture.Pragmas = portPragmas(t, db.conn)
	fixture.Migrations = portMigrations(t, db.conn)
	fixture.Schema = portSchema(t, db.conn)
	fixture.Initial = portSnapshot(t, db.conn)
	for _, spec := range []struct {
		query   string
		allowed []string
	}{
		{"alpha", nil},
		{"Rechnungen", nil},
		{"versicherungs", nil},
		{"ASN 42", nil},
		{"needle phrase", nil},
		{"missing", nil},
		{"alpha", []string{"vault/link.md"}},
		{"alpha", []string{"vault/a.md"}},
		{"alpha", []string{}},
	} {
		var hits []*vault.Document
		if spec.allowed == nil {
			hits, err = db.Search(spec.query)
		} else {
			hits, err = db.SearchScoped(spec.query, spec.allowed)
		}
		if err != nil {
			t.Fatal(err)
		}
		entry := portSearchCase{Query: spec.query, Scoped: spec.allowed != nil, Allowed: spec.allowed, Hits: make([]portSearchHit, 0, len(hits))}
		for _, hit := range hits {
			entry.Hits = append(entry.Hits, portSearchHit{Path: hit.Path, Title: hit.Title, Snippet: hit.Body})
		}
		fixture.Searches = append(fixture.Searches, entry)
	}

	updatedInput := portDocumentInput{Path: "vault/a.md", ModTimeNS: fixed.Add(3 * time.Second).UnixNano(), Markdown: `---
title: Alpha Note Updated
created: "2026-01-01T10:00:00Z"
tags: [alpha, changed]
status: closed
---
Updated body contains a unique vineyard marker.
`}
	fixture.UpdatedInput = updatedInput
	updated := portDocument(t, updatedInput)
	if err := db.IndexDocument(updated); err != nil {
		t.Fatal(err)
	}
	fixture.Updated = portSnapshot(t, db.conn)
	if err := db.DeleteDocument("vault/link.md"); err != nil {
		t.Fatal(err)
	}
	fixture.Deleted = portSnapshot(t, db.conn)

	batchInputs := []portDocumentInput{
		{Path: "vault/batch-before.md", Markdown: "---\ntitle: Batch Before\n---\nbefore\n", ModTimeNS: fixed.Add(4 * time.Second).UnixNano()},
		{Path: "vault/batch-invalid.md", Markdown: "---\ntitle: Batch Invalid\n---\ninvalid\n", ModTimeNS: fixed.Add(5 * time.Second).UnixNano()},
		{Path: "vault/batch-after.md", Markdown: "---\ntitle: Batch After\n---\nafter\n", ModTimeNS: fixed.Add(6 * time.Second).UnixNano()},
	}
	fixture.BatchInputs = batchInputs
	before := portDocument(t, batchInputs[0])
	invalid := portDocument(t, batchInputs[1])
	zero := 0
	invalid.ASN = &zero
	after := portDocument(t, batchInputs[2])
	if err := db.IndexDocuments([]*vault.Document{before, invalid, after}); err != nil {
		fixture.BatchError = err.Error()
	} else {
		t.Fatal("expected invalid ASN batch error")
	}
	fixture.AfterError = portSnapshot(t, db.conn)

	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	fixturePath := filepath.Join("..", "..", "testdata", "port", "sidecar", "contracts.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(fixturePath), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fixturePath, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	//nolint:gosec // fixturePath is fixed relative to the repository
	current, err := os.ReadFile(fixturePath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded) {
		t.Fatal("sidecar contract fixture is stale; run make sidecar-fixtures-generate")
	}
}

func portDocument(t *testing.T, input portDocumentInput) *vault.Document {
	t.Helper()
	doc, err := vault.ParseBytes(input.Path, []byte(input.Markdown))
	if err != nil {
		t.Fatal(err)
	}
	doc.ModTime = time.Unix(0, input.ModTimeNS).UTC()
	return doc
}

func portPragmas(t *testing.T, conn *sql.DB) map[string]string {
	t.Helper()
	result := map[string]string{}
	for _, name := range []string{"journal_mode", "foreign_keys", "busy_timeout", "integrity_check"} {
		var value string
		if err := conn.QueryRow("PRAGMA " + name).Scan(&value); err != nil {
			t.Fatal(err)
		}
		result[name] = value
	}
	return result
}

func portMigrations(t *testing.T, conn *sql.DB) []string {
	t.Helper()
	rows, err := conn.Query("SELECT version FROM schema_migrations ORDER BY version")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var result []string
	for rows.Next() {
		var value string
		if err := rows.Scan(&value); err != nil {
			t.Fatal(err)
		}
		result = append(result, value)
	}
	return result
}

func portSchema(t *testing.T, conn *sql.DB) []portSchemaObject {
	t.Helper()
	rows, err := conn.Query(`SELECT type, name, COALESCE(sql, '') FROM sqlite_master WHERE name NOT LIKE 'sqlite_%' ORDER BY type, name`)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	var result []portSchemaObject
	for rows.Next() {
		var item portSchemaObject
		if err := rows.Scan(&item.Type, &item.Name, &item.SQL); err != nil {
			t.Fatal(err)
		}
		result = append(result, item)
	}
	return result
}

func portSnapshot(t *testing.T, conn *sql.DB) portSidecarState {
	t.Helper()
	state := portSidecarState{
		Files: []portFileRow{}, Properties: []portPropertyRow{}, Links: []portLinkRow{},
		FTSSearch: []portFTSRow{}, FTSNorm: []portFTSRow{}, FTSTri: []portFTSRow{},
	}
	rows, err := conn.Query(`SELECT id,path,sha256,title,created_at,modified_at,"type",document_date,person,status,due_date,confidence,ocr_json_path,simhash,asn,size,mtime_ns FROM files ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var row portFileRow
		if err := rows.Scan(&row.ID, &row.Path, &row.SHA256, &row.Title, &row.CreatedAt, &row.ModifiedAt, &row.Type, &row.DocumentDate, &row.Person, &row.Status, &row.DueDate, &row.Confidence, &row.OCRJSONPath, &row.Simhash, &row.ASN, &row.Size, &row.MTimeNS); err != nil {
			t.Fatal(err)
		}
		state.Files = append(state.Files, row)
	}
	_ = rows.Close()

	rows, err = conn.Query(`SELECT file_id,key,value,value_type FROM file_properties ORDER BY file_id,key,value`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var row portPropertyRow
		if err := rows.Scan(&row.FileID, &row.Key, &row.Value, &row.ValueType); err != nil {
			t.Fatal(err)
		}
		state.Properties = append(state.Properties, row)
	}
	_ = rows.Close()

	rows, err = conn.Query(`SELECT from_path,to_path,kind FROM links ORDER BY from_path,to_path,kind`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var row portLinkRow
		if err := rows.Scan(&row.From, &row.To, &row.Kind); err != nil {
			t.Fatal(err)
		}
		state.Links = append(state.Links, row)
	}
	_ = rows.Close()

	state.FTSSearch = portFTSRows(t, conn, `SELECT rowid,title,body,'' FROM fts_search ORDER BY rowid`)
	state.FTSNorm = portFTSRows(t, conn, `SELECT rowid,'','',norm FROM fts_norm ORDER BY rowid`)
	state.FTSTri = portFTSRows(t, conn, `SELECT rowid,'',body,'' FROM fts_tri ORDER BY rowid`)
	return state
}

func portFTSRows(t *testing.T, conn *sql.DB, query string) []portFTSRow {
	t.Helper()
	rows, err := conn.Query(query)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = rows.Close() }()
	result := []portFTSRow{}
	for rows.Next() {
		var row portFTSRow
		if err := rows.Scan(&row.RowID, &row.Title, &row.Body, &row.Norm); err != nil {
			t.Fatal(err)
		}
		result = append(result, row)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].RowID < result[j].RowID })
	return result
}
