package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/danieljustus/symaira-seek/internal/db"
)

const (
	// symfetchTimeout is the maximum time to wait for symfetch to complete.
	symfetchTimeout = 60 * time.Second
	// httpFallbackTimeout is the timeout for the HTTP fallback client.
	httpFallbackTimeout = 30 * time.Second
	// maxHTTPResponseSize limits the response body to 10 MB.
	maxHTTPResponseSize = 10 << 20
)

// Precompiled regex patterns for HTML-to-text conversion.
// Compiled once at package initialization to prevent ReDoS and improve performance.
var (
	htmlScriptRE  = regexp.MustCompile(`(?i)<script[^>]*>[\s\S]*?</script>`)
	htmlStyleRE   = regexp.MustCompile(`(?i)<style[^>]*>[\s\S]*?</style>`)
	htmlBRRE      = regexp.MustCompile(`(?i)<br\s*/?>`)
	htmlBlockRE   = regexp.MustCompile(`(?i)<(?:hr|p|div|h[1-6]|li|tr)[^>]*>`)
	htmlTagRE     = regexp.MustCompile(`<[^>]+>`)
	htmlNewlineRE = regexp.MustCompile(`\n{3,}`)
)

// IndexURL fetches content from a URL and indexes it.
// It first attempts to use symfetch if available, falling back to a simple
// HTTP GET with minimal HTML-to-text conversion if symfetch is not found.
func IndexURL(dbClient db.Store, embedder Embedder, url string) error {
	content, err := fetchURLContent(url)
	if err != nil {
		return fmt.Errorf("failed to fetch URL content: %w", err)
	}

	return indexContent(dbClient, embedder, url, content)
}

// IndexStdin reads content from a reader and indexes it with the given source.
func IndexStdin(dbClient db.Store, embedder Embedder, reader io.Reader, source string) error {
	data, err := io.ReadAll(reader)
	if err != nil {
		return fmt.Errorf("failed to read from stdin: %w", err)
	}

	content := string(data)
	if content == "" {
		return fmt.Errorf("no content provided via stdin")
	}

	return indexContent(dbClient, embedder, source, content)
}

// userFriendlyError wraps an error with a user-friendly message and suggestion.
func userFriendlyError(err error, context, suggestion string) error {
	return fmt.Errorf("%s: %w\nHint: %s", context, err, suggestion)
}

func validatePublicURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported URL scheme %q (only http and https are allowed)", u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return "", fmt.Errorf("URL has no host")
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return "", fmt.Errorf("cannot resolve host %q: %w", host, err)
	}
	for _, ip := range ips {
		if !isDisallowedIP(ip) {
			return ip.String(), nil
		}
	}
	return "", fmt.Errorf("refusing to fetch %q: host resolves to non-public address", raw)
}

func validatePublicURLString(raw string) error {
	_, err := validatePublicURL(raw)
	return err
}

// isDisallowedIP reports whether ip is an address that must not be reachable via
// user/agent-supplied URLs (SSRF protection). Blocking private/loopback ranges
// is the default; setting SEEK_ALLOW_PRIVATE_URLS=1 (or true) opts in to
// indexing local/internal endpoints (e.g. a localhost doc server).
func isDisallowedIP(ip net.IP) bool {
	if allowPrivateURLs() {
		return false
	}
	return ip.IsLoopback() ||
		ip.IsPrivate() ||
		ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() ||
		ip.IsUnspecified() ||
		ip.IsMulticast()
}

func allowPrivateURLs() bool {
	v := os.Getenv("SEEK_ALLOW_PRIVATE_URLS")
	return v == "1" || strings.EqualFold(v, "true")
}

// fetchURLContent attempts to use symfetch, falling back to HTTP GET.
func fetchURLContent(url string) (string, error) {
	pinnedIP, err := validatePublicURL(url)
	if err != nil {
		return "", err
	}

	if symfetchPath, err := exec.LookPath("symfetch"); err == nil {
		if allowPrivateURLs() {
			content, err := fetchWithSymfetch(symfetchPath, url)
			if err == nil {
				return content, nil
			}
			fmt.Fprintf(os.Stderr, "symfetch failed: %v, falling back to HTTP GET\n", err)
		} else {
			fmt.Fprintf(os.Stderr, "symfetch skipped: external fetcher cannot pin resolved IP; using HTTP GET\n")
		}
	} else {
		fmt.Fprintf(os.Stderr, "symfetch not found in PATH, falling back to HTTP GET\n")
	}

	return fetchWithHTTP(url, pinnedIP)
}

func fetchWithSymfetch(symfetchPath, url string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), symfetchTimeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, symfetchPath, "get", url, "--format", "md")
	cmd.Stderr = os.Stderr

	output, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("symfetch execution failed: %w", err)
	}

	return string(output), nil
}

func fetchWithHTTP(rawURL, pinnedIP string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL: %w", err)
	}

	dialer := &net.Dialer{Timeout: httpFallbackTimeout}
	client := &http.Client{
		Timeout: httpFallbackTimeout,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
				_, port, err := net.SplitHostPort(addr)
				if err != nil {
					port = "80"
					if u.Scheme == "https" {
						port = "443"
					}
				}
				return dialer.DialContext(ctx, network, net.JoinHostPort(pinnedIP, port))
			},
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return fmt.Errorf("stopped after 10 redirects")
			}
			return validatePublicURLString(req.URL.String())
		},
	}

	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Host = u.Host

	resp, err := client.Do(req)
	if err != nil {
		return "", userFriendlyError(err, "HTTP request failed",
			"Check your internet connection and verify the URL is correct")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP GET returned status %d", resp.StatusCode)
	}

	limitedReader := io.LimitReader(resp.Body, maxHTTPResponseSize+1)
	data, err := io.ReadAll(limitedReader)
	if err != nil {
		return "", fmt.Errorf("failed to read HTTP response: %w", err)
	}

	if int64(len(data)) > maxHTTPResponseSize {
		data = data[:maxHTTPResponseSize]
	}

	contentType := resp.Header.Get("Content-Type")
	if strings.Contains(contentType, "text/html") {
		return htmlToText(string(data)), nil
	}

	return string(data), nil
}

// htmlToText performs minimal HTML-to-text conversion.
func htmlToText(html string) string {
	html = htmlScriptRE.ReplaceAllString(html, "")
	html = htmlStyleRE.ReplaceAllString(html, "")

	html = htmlBRRE.ReplaceAllString(html, "\n")
	html = htmlBlockRE.ReplaceAllString(html, "\n")

	html = htmlTagRE.ReplaceAllString(html, "")

	html = strings.ReplaceAll(html, "&amp;", "&")
	html = strings.ReplaceAll(html, "&lt;", "<")
	html = strings.ReplaceAll(html, "&gt;", ">")
	html = strings.ReplaceAll(html, "&quot;", "\"")
	html = strings.ReplaceAll(html, "&#39;", "'")
	html = strings.ReplaceAll(html, "&nbsp;", " ")

	html = htmlNewlineRE.ReplaceAllString(html, "\n\n")

	return strings.TrimSpace(html)
}

// indexContent indexes the given content with the source as document path.
func indexContent(dbClient db.Store, embedder Embedder, source, content string) error {
	// Compute hash of content
	hashSum := sha256.Sum256([]byte(content))
	currentHash := hex.EncodeToString(hashSum[:])

	// Check if document already exists with same hash
	existing, err := dbClient.GetDocument(source)
	if err != nil {
		return fmt.Errorf("failed to check existing document: %w", err)
	}
	if existing != nil && existing.Hash == currentHash {
		fmt.Fprintf(os.Stderr, "Document unchanged, skipping: %s\n", source)
		return nil
	}

	// Build chunks and persist via the shared commit path used by file and
	// directory indexing, so the chunk pipeline lives in exactly one place.
	chunks := buildChunks(embedder, source, content)
	doc := &db.Document{
		Path:      source,
		Hash:      currentHash,
		UpdatedAt: time.Now(),
	}
	return commitIndex(dbClient, source, chunks, doc, existing, "")
}
