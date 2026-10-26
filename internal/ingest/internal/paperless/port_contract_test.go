package paperless

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

type portResponse struct {
	Status  int               `json:"status"`
	Body    string            `json:"body"`
	Headers map[string]string `json:"headers"`
}
type portHTTPCase struct {
	ID        string         `json:"id"`
	Op        string         `json:"op"`
	Since     string         `json:"since"`
	Limit     int            `json:"limit"`
	Filters   []string       `json:"filters"`
	Responses []portResponse `json:"responses"`
}

func TestPortPaperlessHTTPContract(t *testing.T) {
	cases := []portHTTPCase{
		{ID: "documents-pagination", Op: "documents", Since: "2024-02-03", Filters: []string{"tags__id=7", "custom=x%20y"}, Responses: []portResponse{{200, `{"results":[{"id":1,"title":"First","created":"2024-02-03","tags":[7,{"id":8,"name":"embedded"}]}],"next":"http://127.0.0.1/api/documents/?page=2"}`, nil}, {200, `{"results":[{"id":2,"title":"Second","correspondent":{"id":3,"name":"Sender"}}],"next":null}`, nil}}},
		{ID: "documents-limit", Op: "documents", Limit: 1, Responses: []portResponse{{200, `{"results":[{"id":1},{"id":2}],"next":"/never"}`, nil}}},
		{ID: "documents-empty", Op: "documents", Responses: []portResponse{{200, `{"results":[],"next":null}`, nil}}},
		{ID: "get", Op: "get", Responses: []portResponse{{200, `{"id":42,"title":"One","created_date":"2024-01-01","created":"2024-02-03T04:05:06.123Z","document_type":9,"storage_path":{"id":1,"name":"Folder"},"mime_type":"application/pdf"}`, nil}}},
		{ID: "get-malformed", Op: "get", Responses: []portResponse{{200, `{`, nil}}},
		{ID: "get-empty", Op: "get", Responses: []portResponse{{200, ``, nil}}},
		{ID: "get-forbidden", Op: "get", Responses: []portResponse{{403, "  forbidden \n\t please login ", nil}}},
		{ID: "get-truncated-error", Op: "get", Responses: []portResponse{{500, strings.Repeat("x", 530), nil}}},
		{ID: "get-blank-error", Op: "get", Responses: []portResponse{{404, " \n\t", nil}}},
		{ID: "tags", Op: "tags", Responses: []portResponse{{200, `{"results":[{"id":1,"name":"Tag","slug":"tag"}],"next":"/api/tags/?page=2"}`, nil}, {200, `{"results":[{"id":2,"name":"Other"}]}`, nil}}},
		{ID: "correspondents", Op: "correspondents", Responses: []portResponse{{200, `{"results":[{"id":3,"name":"Sender"}]}`, nil}}},
		{ID: "document-types", Op: "document_types", Responses: []portResponse{{200, `{"results":[{"id":4,"name":"Invoice"}]}`, nil}}},
		{ID: "storage-paths", Op: "storage_paths", Responses: []portResponse{{200, `{"results":[{"id":5,"name":"Folder"}]}`, nil}}},
		{ID: "download", Op: "download", Responses: []portResponse{{200, "synthetic-pdf", map[string]string{"Content-Type": "application/pdf", "Content-Disposition": "attachment; filename*=UTF-8''folder%2FInvoice%20%C3%A4.pdf"}}}},
		{ID: "download-invalid-filename", Op: "download", Responses: []portResponse{{200, "data", map[string]string{"Content-Type": "application/octet-stream", "Content-Disposition": "broken; filename=\"unterminated"}}}},
		{ID: "download-error", Op: "download", Responses: []portResponse{{503, "server\n unavailable", nil}}},
	}
	output := []any{}
	for _, c := range cases {
		t.Run(c.ID, func(t *testing.T) {
			requests := []any{}
			index := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Token synthetic-test-only" {
					t.Error("authorization mismatch")
				}
				headers := map[string][]string{}
				for k, v := range r.Header {
					if strings.EqualFold(k, "Authorization") {
						continue
					}
					headers[strings.ToLower(k)] = v
				}
				b, e := io.ReadAll(r.Body)
				if e != nil {
					t.Error(e)
				}
				requests = append(requests, map[string]any{"method": r.Method, "path": r.URL.Path, "query": r.URL.RawQuery, "headers": headers, "body": string(b)})
				if index >= len(c.Responses) {
					t.Error("unexpected request")
					w.WriteHeader(500)
					return
				}
				response := c.Responses[index]
				index++
				w.Header().Set("Content-Type", "application/json")
				for k, v := range response.Headers {
					w.Header().Set(k, v)
				}
				w.WriteHeader(response.Status)
				_, _ = io.WriteString(w, response.Body)
			}))
			defer server.Close()
			client := NewClient(server.URL, "synthetic-test-only")
			var result any
			var err error
			ctx := context.Background()
			switch c.Op {
			case "documents":
				var since time.Time
				if c.Since != "" {
					since, _ = time.Parse("2006-01-02", c.Since)
				}
				result, err = client.ListDocuments(ctx, since, c.Limit, c.Filters...)
			case "get":
				result, err = client.GetDocument(ctx, 42)
			case "tags":
				result, err = client.ListTags(ctx)
			case "correspondents":
				result, err = client.ListCorrespondents(ctx)
			case "document_types":
				result, err = client.ListDocumentTypes(ctx)
			case "storage_paths":
				result, err = client.ListStoragePaths(ctx)
			case "download":
				var dst bytes.Buffer
				var meta DownloadMetadata
				meta, err = client.DownloadDocumentWithMetadata(ctx, 42, &dst)
				result = map[string]any{"meta": meta, "body": dst.String()}
			}
			message := ""
			if err != nil {
				message = err.Error()
			}
			if index != len(c.Responses) {
				t.Fatalf("executed %d/%d scripted responses", index, len(c.Responses))
			}
			output = append(output, map[string]any{"input": c, "result": result, "error": message, "requests": requests})
		})
	}
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Dir(file)
	hashes := map[string]string{}
	for _, name := range []string{"client.go", "types.go", "port_contract_test.go"} {
		b, e := os.ReadFile(filepath.Join(dir, name))
		if e != nil {
			t.Fatal(e)
		}
		hashes[name] = fmt.Sprintf("%x", sha256.Sum256(bytes.ReplaceAll(b, []byte("\r\n"), []byte("\n"))))
	}
	b, e := json.MarshalIndent(map[string]any{"schema_version": 1, "oracle": fixtureoracle.Current(), "source_hashes": hashes, "cases": output, "normalizations": []string{"loopback host/port omitted from transcript; method, path, raw query and all non-auth headers retained", "synthetic auth token asserted by server, never recorded"}}, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	b = append(b, '\n')
	out := filepath.Join(dir, "../../../../testdata/port/ingest/paperless-http.json")
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
			t.Fatal("Paperless HTTP fixture drift; regenerate deliberately")
		}
	}
	t.Logf("replayed %d loopback HTTP scenarios", len(cases))
}
