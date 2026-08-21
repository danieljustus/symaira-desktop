package engine

import (
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/danieljustus/symaira-seek/internal/db"
)

// TestMain opts the engine test suite into indexing loopback URLs so the
// fetch-mechanics tests can use httptest servers. The SSRF guard's default
// behavior (rejecting private/loopback/bad-scheme URLs) is covered explicitly
// by TestValidatePublicURL_RejectsPrivateAndBadScheme.
func TestMain(m *testing.M) {
	os.Setenv("SEEK_ALLOW_PRIVATE_URLS", "1")
	os.Exit(m.Run())
}

func TestValidatePublicURL_RejectsPrivateAndBadScheme(t *testing.T) {
	t.Setenv("SEEK_ALLOW_PRIVATE_URLS", "0")
	cases := []string{
		"http://127.0.0.1/x",
		"http://localhost/x",
		"http://169.254.169.254/latest/meta-data/",
		"file:///etc/passwd",
		"ftp://example.com/x",
	}
	for _, u := range cases {
		if err := validatePublicURLString(u); err == nil {
			t.Errorf("expected rejection for %q", u)
		}
	}
}

func TestIndexURL_WithSymfetch(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-url-symfetch-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer dbClient.Close()

	embedder := &fakeEmbedder{dim: 768}

	binDir := filepath.Join(tempDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("failed to create bin dir: %v", err)
	}

	fakeSymfetch := filepath.Join(binDir, "symfetch")
	script := `#!/bin/sh
echo "# Test Document from symfetch"
echo "This content was fetched by the fake symfetch."
`
	if err := os.WriteFile(fakeSymfetch, []byte(script), 0755); err != nil {
		t.Fatalf("failed to write fake symfetch: %v", err)
	}

	originalPath := os.Getenv("PATH")
	t.Setenv("PATH", binDir+":"+originalPath)

	if err := IndexURL(dbClient, embedder, "https://example.com/test"); err != nil {
		t.Fatalf("IndexURL failed: %v", err)
	}

	doc, err := dbClient.GetDocument("https://example.com/test")
	if err != nil {
		t.Fatalf("GetDocument failed: %v", err)
	}
	if doc == nil {
		t.Fatal("expected document to be indexed")
	}

	chunks, err := dbClient.GetChunksForDocument("https://example.com/test")
	if err != nil {
		t.Fatalf("GetChunksForDocument failed: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}

	if !strings.Contains(chunks[0].Content, "fake symfetch") {
		t.Errorf("expected chunk to contain symfetch content, got: %s", chunks[0].Content)
	}
}

func TestIndexURL_HTTPFallback(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-url-http-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer dbClient.Close()

	embedder := &fakeEmbedder{dim: 768}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintln(w, "# Test Document from HTTP")
		fmt.Fprintln(w, "This content was fetched via HTTP GET fallback.")
	}))
	defer server.Close()

	t.Setenv("PATH", "/nonexistent")

	if err := IndexURL(dbClient, embedder, server.URL+"/test"); err != nil {
		t.Fatalf("IndexURL failed: %v", err)
	}

	doc, err := dbClient.GetDocument(server.URL + "/test")
	if err != nil {
		t.Fatalf("GetDocument failed: %v", err)
	}
	if doc == nil {
		t.Fatal("expected document to be indexed")
	}

	chunks, err := dbClient.GetChunksForDocument(server.URL + "/test")
	if err != nil {
		t.Fatalf("GetChunksForDocument failed: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}

	if !strings.Contains(chunks[0].Content, "HTTP GET fallback") {
		t.Errorf("expected chunk to contain HTTP fallback content, got: %s", chunks[0].Content)
	}
}

func TestIndexURL_HTTPFallback_HTMLToText(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-url-html-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer dbClient.Close()

	embedder := &fakeEmbedder{dim: 768}

	htmlContent := `<!DOCTYPE html>
<html>
<head><title>Test Page</title></head>
<body>
<h1>Hello World</h1>
<p>This is a test paragraph.</p>
<script>console.log('should be removed');</script>
<style>.hidden { display: none; }</style>
<p>Another paragraph with <strong>bold</strong> text.</p>
</body>
</html>`

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		fmt.Fprint(w, htmlContent)
	}))
	defer server.Close()

	t.Setenv("PATH", "/nonexistent")

	if err := IndexURL(dbClient, embedder, server.URL+"/html-test"); err != nil {
		t.Fatalf("IndexURL failed: %v", err)
	}

	chunks, err := dbClient.GetChunksForDocument(server.URL + "/html-test")
	if err != nil {
		t.Fatalf("GetChunksForDocument failed: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}

	content := chunks[0].Content
	if strings.Contains(content, "console.log") {
		t.Error("expected script content to be removed")
	}
	if strings.Contains(content, "display: none") {
		t.Error("expected style content to be removed")
	}
	if !strings.Contains(content, "Hello World") {
		t.Error("expected visible text to be preserved")
	}
	if !strings.Contains(content, "Another paragraph") {
		t.Error("expected visible text to be preserved")
	}
}

func TestIndexStdin(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-stdin-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer dbClient.Close()

	embedder := &fakeEmbedder{dim: 768}

	content := "This is a test document from stdin with unique content."
	reader := strings.NewReader(content)

	if err := IndexStdin(dbClient, embedder, reader, "test://stdin-doc"); err != nil {
		t.Fatalf("IndexStdin failed: %v", err)
	}

	doc, err := dbClient.GetDocument("test://stdin-doc")
	if err != nil {
		t.Fatalf("GetDocument failed: %v", err)
	}
	if doc == nil {
		t.Fatal("expected document to be indexed")
	}

	chunks, err := dbClient.GetChunksForDocument("test://stdin-doc")
	if err != nil {
		t.Fatalf("GetChunksForDocument failed: %v", err)
	}
	if len(chunks) == 0 {
		t.Fatal("expected at least one chunk")
	}

	if !strings.Contains(chunks[0].Content, "test document from stdin") {
		t.Errorf("expected chunk to contain stdin content, got: %s", chunks[0].Content)
	}
}

func TestIndexStdin_EmptyContent(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-stdin-empty-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer dbClient.Close()

	embedder := &fakeEmbedder{dim: 768}

	reader := strings.NewReader("")

	err = IndexStdin(dbClient, embedder, reader, "test://empty")
	if err == nil {
		t.Fatal("expected error for empty content")
	}
	if !strings.Contains(err.Error(), "no content provided") {
		t.Errorf("expected 'no content provided' error, got: %v", err)
	}
}

func TestIndexStdin_DefaultSource(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-stdin-default-source-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer dbClient.Close()

	embedder := &fakeEmbedder{dim: 768}

	content := "Content with default source."
	reader := strings.NewReader(content)

	if err := IndexStdin(dbClient, embedder, reader, ""); err != nil {
		t.Fatalf("IndexStdin failed: %v", err)
	}

	doc, err := dbClient.GetDocument("")
	if err != nil {
		t.Fatalf("GetDocument failed: %v", err)
	}
	if doc == nil {
		t.Fatal("expected document to be indexed with empty source")
	}
}

func TestIndexContent_UnchangedSkip(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-content-skip-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer dbClient.Close()

	embedder := &fakeEmbedder{dim: 768}

	content := "Test content for skip check."

	if err := indexContent(dbClient, embedder, "test://skip", content); err != nil {
		t.Fatalf("first indexContent failed: %v", err)
	}

	stats1, _ := dbClient.GetStats()

	if err := indexContent(dbClient, embedder, "test://skip", content); err != nil {
		t.Fatalf("second indexContent failed: %v", err)
	}

	stats2, _ := dbClient.GetStats()
	if stats2.ChunkCount != stats1.ChunkCount {
		t.Errorf("expected chunk count to remain same after re-indexing unchanged content, got %d vs %d",
			stats2.ChunkCount, stats1.ChunkCount)
	}
}

func TestIndexContent_UpdatedContent(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-content-update-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer dbClient.Close()

	embedder := &fakeEmbedder{dim: 768}

	content1 := "Original content for update test."
	if err := indexContent(dbClient, embedder, "test://update", content1); err != nil {
		t.Fatalf("first indexContent failed: %v", err)
	}

	doc1, _ := dbClient.GetDocument("test://update")
	hash1 := doc1.Hash

	content2 := "Updated content for update test."
	if err := indexContent(dbClient, embedder, "test://update", content2); err != nil {
		t.Fatalf("second indexContent failed: %v", err)
	}

	doc2, _ := dbClient.GetDocument("test://update")
	if doc2.Hash == hash1 {
		t.Error("expected hash to change after updating content")
	}

	chunks, _ := dbClient.GetChunksForDocument("test://update")
	if len(chunks) == 0 {
		t.Fatal("expected chunks to be present")
	}
	if !strings.Contains(chunks[0].Content, "Updated content") {
		t.Errorf("expected updated content in chunks, got: %s", chunks[0].Content)
	}
}

func TestHTMLToText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "removes scripts",
			input:    "<p>Hello</p><script>alert('xss')</script><p>World</p>",
			expected: "Hello\nWorld",
		},
		{
			name:     "removes styles",
			input:    "<p>Hello</p><style>.hidden{display:none}</style><p>World</p>",
			expected: "Hello\nWorld",
		},
		{
			name:     "converts block elements",
			input:    "<div>Line1</div><br><p>Line2</p><h1>Header</h1>",
			expected: "Line1\n\nLine2\nHeader",
		},
		{
			name:     "decodes entities",
			input:    "<p>&amp; &lt; &gt; &quot; &#39; &nbsp;</p>",
			expected: "& < > \" '",
		},
		{
			name:     "collapses newlines",
			input:    "<p>A</p>\n\n\n<p>B</p>",
			expected: "A\n\nB",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := htmlToText(tt.input)
			if result != tt.expected {
				t.Errorf("htmlToText(%q) = %q, want %q", tt.input, result, tt.expected)
			}
		})
	}
}

func TestFetchURLContent_SymfetchNotFound(t *testing.T) {
	originalPath := os.Getenv("PATH")
	t.Setenv("PATH", "/nonexistent")
	defer t.Setenv("PATH", originalPath)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprintln(w, "Fallback content")
	}))
	defer server.Close()

	content, err := fetchURLContent(server.URL)
	if err != nil {
		t.Fatalf("fetchURLContent failed: %v", err)
	}

	if !strings.Contains(content, "Fallback content") {
		t.Errorf("expected fallback content, got: %s", content)
	}
}

func TestFetchWithHTTP_StatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer server.Close()

	_, err := fetchWithHTTP(server.URL, "127.0.0.1")
	if err == nil {
		t.Fatal("expected error for 404 status")
	}
	if !strings.Contains(err.Error(), "404") {
		t.Errorf("expected 404 in error, got: %v", err)
	}
}

func TestFetchWithSymfetch_CommandFails(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-symfetch-fail-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	binDir := filepath.Join(tempDir, "bin")
	if err := os.MkdirAll(binDir, 0755); err != nil {
		t.Fatalf("failed to create bin dir: %v", err)
	}

	fakeSymfetch := filepath.Join(binDir, "symfetch")
	script := `#!/bin/sh
exit 1
`
	if err := os.WriteFile(fakeSymfetch, []byte(script), 0755); err != nil {
		t.Fatalf("failed to write fake symfetch: %v", err)
	}

	_, err = fetchWithSymfetch(fakeSymfetch, "https://example.com")
	if err == nil {
		t.Fatal("expected error when symfetch fails")
	}
}

func TestIndexURL_HTTPFallback_NotFound(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-url-notfound-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer dbClient.Close()

	embedder := &fakeEmbedder{dim: 768}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	t.Setenv("PATH", "/nonexistent")

	err = IndexURL(dbClient, embedder, server.URL)
	if err == nil {
		t.Fatal("expected error for 500 status")
	}
}

func TestIndexContent_IndexAndSearch(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-index-search-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer dbClient.Close()

	embedder := &fakeEmbedder{dim: 768}

	content := "This is a unique searchable document about quantum computing and machine learning."
	if err := indexContent(dbClient, embedder, "test://quantum", content); err != nil {
		t.Fatalf("indexContent failed: %v", err)
	}

	results, err := SearchHybrid(dbClient, dbClient, embedder, "quantum computing", 5)
	if err != nil {
		t.Fatalf("SearchHybrid failed: %v", err)
	}

	if len(results) == 0 {
		t.Fatal("expected search results")
	}

	found := false
	for _, r := range results {
		if r.Chunk.DocumentPath == "test://quantum" {
			found = true
			break
		}
	}

	if !found {
		t.Error("expected indexed document to be found in search results")
	}
}

func TestFetchWithHTTP_LargeResponse(t *testing.T) {
	largeContent := strings.Repeat("A", 11<<20)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, largeContent)
	}))
	defer server.Close()

	content, err := fetchWithHTTP(server.URL, "127.0.0.1")
	if err != nil {
		t.Fatalf("fetchWithHTTP failed: %v", err)
	}

	if len(content) > 10<<20 {
		t.Errorf("expected content to be truncated to 10MB, got %d bytes", len(content))
	}
}

func TestFetchWithHTTP_NonHTMLContentType(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"key": "value"}`)
	}))
	defer server.Close()

	content, err := fetchWithHTTP(server.URL, "127.0.0.1")
	if err != nil {
		t.Fatalf("fetchWithHTTP failed: %v", err)
	}

	if !strings.Contains(content, `"key": "value"`) {
		t.Errorf("expected JSON content to be preserved, got: %s", content)
	}
}

func TestIndexStdin_LargeContent(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-stdin-large-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer dbClient.Close()

	embedder := &fakeEmbedder{dim: 768}

	largeContent := strings.Repeat("This is line number X of the large document. ", 1000)
	reader := strings.NewReader(largeContent)

	if err := IndexStdin(dbClient, embedder, reader, "test://large"); err != nil {
		t.Fatalf("IndexStdin failed: %v", err)
	}

	chunks, err := dbClient.GetChunksForDocument("test://large")
	if err != nil {
		t.Fatalf("GetChunksForDocument failed: %v", err)
	}

	if len(chunks) <= 1 {
		t.Errorf("expected multiple chunks for large content, got %d", len(chunks))
	}
}

func TestIndexContent_MultipleSources(t *testing.T) {
	tempDir, err := os.MkdirTemp("", "seek-multi-source-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tempDir)

	t.Setenv("HOME", tempDir)

	dbClient, err := db.Open()
	if err != nil {
		t.Fatalf("failed to open database: %v", err)
	}
	defer dbClient.Close()

	embedder := &fakeEmbedder{dim: 768}

	sources := []struct {
		source  string
		content string
	}{
		{"test://doc1", "Content for document one."},
		{"test://doc2", "Content for document two."},
		{"test://doc3", "Content for document three."},
	}

	for _, s := range sources {
		if err := indexContent(dbClient, embedder, s.source, s.content); err != nil {
			t.Fatalf("indexContent(%s) failed: %v", s.source, err)
		}
	}

	stats, _ := dbClient.GetStats()
	if stats.DocumentCount != 3 {
		t.Errorf("expected 3 documents, got %d", stats.DocumentCount)
	}
}

// TestUserFriendlyError verifies that userFriendlyError wraps the underlying
// error with context and a hint line. Covers index.go:69-71.
func TestUserFriendlyError(t *testing.T) {
	inner := fmt.Errorf("dial tcp 127.0.0.1:443: connect: connection refused")
	err := userFriendlyError(inner, "HTTP request failed",
		"Check your internet connection and verify the URL is correct")

	if err == nil {
		t.Fatal("expected non-nil error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "HTTP request failed") {
		t.Errorf("expected context message, got: %s", msg)
	}
	if !strings.Contains(msg, "connection refused") {
		t.Errorf("expected inner error to be wrapped, got: %s", msg)
	}
	if !strings.Contains(msg, "Hint: Check your internet") {
		t.Errorf("expected hint line, got: %s", msg)
	}
}

// TestFetchWithHTTP_RedirectLoop verifies that fetchWithHTTP returns an error
// when the server redirects more than 10 times. Covers index.go:184-186.
func TestFetchWithHTTP_RedirectLoop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, r.URL.Path, http.StatusFound)
	}))
	defer server.Close()

	_, err := fetchWithHTTP(server.URL+"/loop", "127.0.0.1")
	if err == nil {
		t.Fatal("expected error for redirect loop")
	}
	if !strings.Contains(err.Error(), "redirect") {
		t.Errorf("expected redirect-related error, got: %v", err)
	}
}

// TestFetchWithHTTP_RedirectToPrivateIP verifies that the CheckRedirect
// callback rejects redirects to private/loopback IPs when SSRF protection
// is active. Covers index.go:188.
func TestFetchWithHTTP_RedirectToPrivateIP(t *testing.T) {
	t.Setenv("SEEK_ALLOW_PRIVATE_URLS", "0")

	var serverURL string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, serverURL+"/target", http.StatusFound)
	}))
	defer server.Close()
	serverURL = server.URL

	_, err := fetchWithHTTP(server.URL+"/start", "127.0.0.1")
	if err == nil {
		t.Fatal("expected error when redirecting to private/loopback IP")
	}
	if !strings.Contains(err.Error(), "HTTP request failed") {
		t.Errorf("expected HTTP request failed wrapper, got: %v", err)
	}
}

// TestFetchWithHTTP_ConnectionError verifies that a connection-level failure
// is wrapped by userFriendlyError with a user-friendly hint. Covers
// index.go:199-202 and index.go:69-71.
func TestFetchWithHTTP_ConnectionError(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			conn.Close()
		}
	}()

	_, err = fetchWithHTTP("http://"+ln.Addr().String()+"/fail", "127.0.0.1")
	if err == nil {
		t.Fatal("expected error for connection failure")
	}
	if !strings.Contains(err.Error(), "HTTP request failed") {
		t.Errorf("expected 'HTTP request failed' in error, got: %v", err)
	}
	if !strings.Contains(err.Error(), "Hint:") {
		t.Errorf("expected hint in error message, got: %v", err)
	}
}

// TestFetchWithHTTP_StatusErrors is a table-driven test for non-200 HTTP
// status codes. Covers index.go:205-207.
func TestFetchWithHTTP_StatusErrors(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
	}{
		{"404 Not Found", http.StatusNotFound},
		{"500 Internal Server Error", http.StatusInternalServerError},
		{"301 Moved Permanently (without following)", http.StatusMovedPermanently},
		{"403 Forbidden", http.StatusForbidden},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.statusCode)
				fmt.Fprint(w, "error page")
			}))
			defer server.Close()

			_, err := fetchWithHTTP(server.URL, "127.0.0.1")
			if err == nil {
				t.Fatalf("expected error for status %d", tc.statusCode)
			}
			if !strings.Contains(err.Error(), fmt.Sprintf("%d", tc.statusCode)) {
				t.Errorf("expected status %d in error, got: %v", tc.statusCode, err)
			}
		})
	}
}

// TestFetchWithHTTP_LargeResponseTruncation verifies that responses larger
// than maxHTTPResponseSize are truncated. Covers index.go:209-217.
func TestFetchWithHTTP_LargeResponseTruncation(t *testing.T) {
	oversized := strings.Repeat("X", int(maxHTTPResponseSize)+1024)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, oversized)
	}))
	defer server.Close()

	content, err := fetchWithHTTP(server.URL, "127.0.0.1")
	if err != nil {
		t.Fatalf("fetchWithHTTP failed: %v", err)
	}
	if int64(len(content)) > maxHTTPResponseSize {
		t.Errorf("expected content truncated to %d bytes, got %d", maxHTTPResponseSize, len(content))
	}
	if len(content) == 0 {
		t.Error("expected non-empty truncated content")
	}
}

// TestFetchWithHTTP_ContentTypeBranches is a table-driven test for the
// content-type branching in fetchWithHTTP: text/html triggers htmlToText,
// everything else is returned verbatim. (covers lines 219-224)
func TestFetchWithHTTP_ContentTypeBranches(t *testing.T) {
	cases := []struct {
		name        string
		contentType string
		body        string
		wantHTML    bool
	}{
		{
			name:        "text/html",
			contentType: "text/html; charset=utf-8",
			body:        "<p>Hello <b>world</b></p>",
			wantHTML:    true,
		},
		{
			name:        "application/json",
			contentType: "application/json",
			body:        `{"key": "value"}`,
			wantHTML:    false,
		},
		{
			name:        "text/plain",
			contentType: "text/plain",
			body:        "plain text content",
			wantHTML:    false,
		},
		{
			name:        "empty content-type",
			contentType: "",
			body:        "fallback content",
			wantHTML:    false,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tc.contentType != "" {
					w.Header().Set("Content-Type", tc.contentType)
				}
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()

			content, err := fetchWithHTTP(server.URL, "127.0.0.1")
			if err != nil {
				t.Fatalf("fetchWithHTTP failed: %v", err)
			}
			if tc.wantHTML {
				if strings.Contains(content, "<p>") || strings.Contains(content, "<b>") {
					t.Errorf("expected HTML tags to be stripped, got: %s", content)
				}
				if !strings.Contains(content, "Hello") {
					t.Errorf("expected visible text preserved, got: %s", content)
				}
			} else {
				if !strings.Contains(content, tc.body) {
					t.Errorf("expected body preserved verbatim, got: %s", content)
				}
			}
		})
	}
}

// TestFetchWithHTTP_BadScheme verifies fetchWithHTTP rejects ftp:// URLs.
func TestFetchWithHTTP_BadScheme(t *testing.T) {
	_, err := fetchWithHTTP("ftp://example.com/file", "93.184.216.34")
	if err == nil {
		t.Fatal("expected error for ftp scheme")
	}
}

// TestValidatePublicURL_AllBranches exercises every branch of
// validatePublicURL and isDisallowedIP beyond the existing rejection test.
func TestValidatePublicURL_AllBranches(t *testing.T) {
	t.Run("unsupported scheme ftp", func(t *testing.T) {
		_, err := validatePublicURL("ftp://example.com/file")
		if err == nil {
			t.Fatal("expected error for ftp scheme")
		}
		if !strings.Contains(err.Error(), "unsupported URL scheme") {
			t.Errorf("expected unsupported scheme error, got: %v", err)
		}
	})

	t.Run("unsupported scheme file", func(t *testing.T) {
		_, err := validatePublicURL("file:///etc/passwd")
		if err == nil {
			t.Fatal("expected error for file scheme")
		}
	})

	t.Run("no host", func(t *testing.T) {
		_, err := validatePublicURL("http:///path")
		if err == nil {
			t.Fatal("expected error for URL with no host")
		}
		if !strings.Contains(err.Error(), "no host") {
			t.Errorf("expected 'no host' error, got: %v", err)
		}
	})

	t.Run("unresolvable host", func(t *testing.T) {
		_, err := validatePublicURL("http://this-host-does-not-exist-xyzzy.example./path")
		if err == nil {
			t.Fatal("expected error for unresolvable host")
		}
		if !strings.Contains(err.Error(), "cannot resolve host") {
			t.Errorf("expected 'cannot resolve host' error, got: %v", err)
		}
	})

	t.Run("all IPs private triggers refusal", func(t *testing.T) {
		t.Setenv("SEEK_ALLOW_PRIVATE_URLS", "0")
		_, err := validatePublicURL("http://127.0.0.1/loopback")
		if err == nil {
			t.Fatal("expected refusal for loopback address")
		}
		if !strings.Contains(err.Error(), "non-public address") {
			t.Errorf("expected 'non-public address' error, got: %v", err)
		}
	})

	t.Run("private URLs allowed bypasses block", func(t *testing.T) {
		t.Setenv("SEEK_ALLOW_PRIVATE_URLS", "1")
		ip, err := validatePublicURL("http://127.0.0.1/loopback")
		if err != nil {
			t.Fatalf("expected no error when private URLs allowed, got: %v", err)
		}
		if ip != "127.0.0.1" {
			t.Errorf("expected resolved IP 127.0.0.1, got: %s", ip)
		}
	})
}

// TestFetchWithHTTP_RedirectToPublicHost verifies that a redirect to a
// resolvable public host is followed when SSRF protection allows it.
func TestFetchWithHTTP_RedirectToPublicHost(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		fmt.Fprint(w, "redirect target content")
	}))
	defer target.Close()

	targetURL := target.URL

	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, targetURL, http.StatusFound)
	}))
	defer source.Close()

	content, err := fetchWithHTTP(source.URL+"/start", "127.0.0.1")
	if err != nil {
		t.Fatalf("fetchWithHTTP failed: %v", err)
	}
	if !strings.Contains(content, "redirect target content") {
		t.Errorf("expected redirected content, got: %s", content)
	}
}
