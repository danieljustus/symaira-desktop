package paperlessimport

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/paperless"
	"github.com/danieljustus/symaira-desktop/internal/ingest/internal/writer"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

func TestPortPaperlessMappingContract(t *testing.T) {
	raw := []string{
		`{"id":1,"title":"Invoice","tags":[7,{"id":8,"name":"Embedded"}],"correspondent":3,"document_type":4,"storage_path":5,"mime_type":"application/pdf","original_file_name":"original.txt","archived_file_name":"archive.PDF","created":"2024-02-03T04:05:06Z","created_date":"2023-01-01"}`,
		`{"id":2,"title":"Missing","tags":[91,91,0],"correspondent":92,"document_type":93,"storage_path":94,"file_type":"weird","mime_type":"application/unknown"}`,
		`{"id":3,"tags":[],"correspondent":{"id":0},"storage_path":{"name":"Embedded/path"},"mime_type":"image/png; charset=utf-8","created_date":"2024-03-04"}`,
		`{"id":4,"tags":[10],"file_type":".csv","mime_type":"text/csv","original_file_name":"unknown.custom"}`,
	}
	lu := &lookups{tags: map[int]string{7: "Tax", 10: ""}, correspondents: map[int]string{3: "Sender"}, documentTypes: map[int]string{4: "Invoice"}, storagePaths: map[int]string{5: "Finance/{{created_year}}"}}
	docs := []paperless.Document{}
	cases := []any{}
	for _, input := range raw {
		var doc paperless.Document
		if e := json.Unmarshal([]byte(input), &doc); e != nil {
			t.Fatal(e)
		}
		docs = append(docs, doc)
		var value any
		if e := json.Unmarshal([]byte(input), &value); e != nil {
			t.Fatal(e)
		}
		cases = append(cases, map[string]any{"input": value, "resolved": resolveDocMeta(doc, lu), "planned": plannedDocumentResult(doc, "fixed-run", "planned"), "created": paperlessCreated(doc).UTC().Format(time.RFC3339)})
	}
	extensions := []any{}
	for _, input := range []string{"", "PDF", ".PDF", " .csv ", "archive.TAR.GZ", "a/b.JPG", `a\b.PNG`, ".", "../file", "name.", "text/plain"} {
		extensions = append(extensions, map[string]any{"input": input, "output": normalizeExtension(input)})
	}
	download := []any{}
	for _, meta := range []paperless.DownloadMetadata{{Filename: "response.DOCX"}, {Filename: "", ContentType: "image/png; charset=utf-8"}, {Filename: "no-extension"}, {Filename: "", ContentType: " TEXT/PLAIN ; charset=utf-8 "}} {
		for _, doc := range docs {
			download = append(download, map[string]any{"doc_id": doc.ID, "metadata": meta, "extension": paperlessDownloadExtensionWithMetadata(doc, meta)})
		}
	}
	paths := []any{}
	for _, input := range []string{"", "Finance/2024", " ../outside/./ . /<x>:?* ", `A\B\C`, "..././..", " / absolute//file ", "a\x01b"} {
		paths = append(paths, map[string]any{"input": input, "output": filepath.ToSlash(writer.SanitizeStoragePath(input))})
	}
	stages := []any{}
	for _, input := range []string{"download timeout", "detect source type: unknown", "extract text failed", "archive file failed", "write note failed", "record document failed", "set vault path failed", "complete job failed", "other", "download and write note", " multi\n line	 message ", strings.Repeat("x", 1030)} {
		err := errors.New(input)
		stages = append(stages, map[string]any{"input": input, "stage": stageFromError(err), "bounded": boundedErrorString(err), "explicit_stage": stageFromError(stageError("custom", err))})
	}
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Dir(file)
	hashes := map[string]string{}
	for _, name := range []string{"import.go", "audit.go", "port_contract_test.go"} {
		//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
		b, e := os.ReadFile(filepath.Join(dir, name))
		if e != nil {
			t.Fatal(e)
		}
		hashes[name] = fmt.Sprintf("%x", sha256.Sum256(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))))
	}
	b, e := json.MarshalIndent(map[string]any{"schema_version": 1, "oracle": fixtureoracle.Current(), "source_hashes": hashes, "cases": cases, "lookups": map[string]any{"tags": lu.tags, "correspondents": lu.correspondents, "document_types": lu.documentTypes, "storage_paths": lu.storagePaths}, "audit": buildAuditReport(docs, lu), "empty_audit": buildAuditReport(nil, lu), "extensions": extensions, "downloads": download, "storage_paths": paths, "stages": stages}, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	b = append(b, '\n')
	out := filepath.Join(dir, "../../../../testdata/port/ingest/paperless-mapping.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if e := os.WriteFile(out, b, 0600); e != nil {
			t.Fatal(e)
		}
	} else {
		//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
		old, e := os.ReadFile(out)
		if e != nil {
			t.Fatal(e)
		}
		if !bytes.Equal(old, b) {
			t.Fatal("Paperless mapping fixture drift; regenerate deliberately")
		}
	}
	t.Logf("replayed %d metadata mappings, %d extension choices, %d download choices, %d storage paths, %d errors", len(cases), len(extensions), len(download), len(paths), len(stages))
}
