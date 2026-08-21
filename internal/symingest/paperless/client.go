package paperless

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	jsonRequestTimeout = 30 * time.Second
	maxErrorBodyBytes  = 512
)

type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

func NewClient(baseURL, token string) *Client {
	if baseURL == "" {
		baseURL = os.Getenv("PAPERLESS_URL")
	}
	if token == "" {
		token = os.Getenv("PAPERLESS_TOKEN")
	}
	return &Client{
		baseURL: baseURL,
		token:   token,
		httpClient: &http.Client{
			Transport: defaultTransport(),
		},
	}
}

func defaultTransport() *http.Transport {
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: (&net.Dialer{
			Timeout:   30 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 30 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
		IdleConnTimeout:       90 * time.Second,
	}
}

func (c *Client) doRequest(ctx context.Context, url string, result any) error {
	ctx, cancel := context.WithTimeout(ctx, jsonRequestTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Token "+c.token)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return apiError(resp)
	}

	if err := json.NewDecoder(resp.Body).Decode(result); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

func apiError(resp *http.Response) error {
	status := resp.Status
	if status == "" {
		status = fmt.Sprintf("%d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}
	preview := errorBodyPreview(resp.Body)
	if preview == "" {
		return fmt.Errorf("API error %s", status)
	}
	return fmt.Errorf("API error %s: %s", status, preview)
}

func errorBodyPreview(body io.Reader) string {
	data, _ := io.ReadAll(io.LimitReader(body, maxErrorBodyBytes+1))
	truncated := len(data) > maxErrorBodyBytes
	if truncated {
		data = data[:maxErrorBodyBytes]
	}
	preview := strings.Join(strings.Fields(string(data)), " ")
	if len(preview) > maxErrorBodyBytes {
		preview = preview[:maxErrorBodyBytes]
		truncated = true
	}
	if truncated {
		preview = strings.TrimSpace(preview) + "…"
	}
	return preview
}

// resolveNextURL normalizes a Paperless pagination "next" link against the
// configured base URL. Paperless-ngx has been observed to return absolute
// next links that drop the deployment port (e.g. the bare host without
// :8001), which would otherwise send the follow-up request to port 80 and
// fail. Relative links are resolved against the base URL; absolute links that
// point at the configured host but changed or dropped the port have the
// configured host:port restored. A genuinely different host is left as-is.
func (c *Client) resolveNextURL(next string) (string, error) {
	if next == "" {
		return "", nil
	}
	ref, err := url.Parse(next)
	if err != nil {
		return "", fmt.Errorf("parse next link %q: %w", next, err)
	}
	base, err := url.Parse(c.baseURL)
	if err != nil {
		// Without a parseable base we cannot normalize; follow the link as
		// given rather than dropping pagination entirely.
		return next, nil
	}
	resolved := base.ResolveReference(ref)
	if resolved.Hostname() == base.Hostname() && resolved.Port() != base.Port() {
		resolved.Host = base.Host
	}
	return resolved.String(), nil
}

func (c *Client) ListDocuments(ctx context.Context, since time.Time, maxResults int, filters ...string) ([]Document, error) {
	url := c.baseURL + "/api/documents/?format=json&ordering=-created_date"
	if !since.IsZero() {
		// created__date__gte (the Django date-transform lookup) is honored by
		// the deployed Paperless-ngx API; the plain created_date__gte field is
		// silently ignored and would return the entire archive unbounded.
		url += "&created__date__gte=" + since.Format("2006-01-02")
	}
	if maxResults > 0 {
		url += "&page_size=" + strconv.Itoa(maxResults)
	}
	for _, f := range filters {
		url += "&" + f
	}

	var all []Document
	for url != "" {
		var page listResponse[Document]
		if err := c.doRequest(ctx, url, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Results...)
		if maxResults > 0 && len(all) >= maxResults {
			return all[:maxResults], nil
		}
		next, err := c.resolveNextURL(page.Next)
		if err != nil {
			return nil, err
		}
		url = next
	}
	return all, nil
}

// DocumentURL returns the Paperless-ngx web UI link for a document, for use
// as an audit backlink to the original record.
func (c *Client) DocumentURL(id int) string {
	return c.baseURL + "/documents/" + strconv.Itoa(id)
}

func (c *Client) GetDocument(ctx context.Context, id int) (*Document, error) {
	url := c.baseURL + "/api/documents/" + strconv.Itoa(id) + "/?format=json"
	var doc Document
	if err := c.doRequest(ctx, url, &doc); err != nil {
		return nil, err
	}
	return &doc, nil
}

// DownloadMetadata captures response headers from a Paperless document
// download. It is intentionally small and content-free so migration reports can
// explain filename/MIME decisions without leaking document bodies.
type DownloadMetadata struct {
	ContentType        string `json:"content_type,omitempty"`
	ContentDisposition string `json:"content_disposition,omitempty"`
	Filename           string `json:"filename,omitempty"`
}

func (c *Client) DownloadDocument(ctx context.Context, id int, dst io.Writer) error {
	_, err := c.DownloadDocumentWithMetadata(ctx, id, dst)
	return err
}

func (c *Client) DownloadDocumentWithMetadata(ctx context.Context, id int, dst io.Writer) (DownloadMetadata, error) {
	url := c.baseURL + "/api/documents/" + strconv.Itoa(id) + "/download/"
	req, err := http.NewRequestWithContext(ctx, "GET", url, nil)
	if err != nil {
		return DownloadMetadata{}, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("Authorization", "Token "+c.token)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return DownloadMetadata{}, fmt.Errorf("execute request: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return DownloadMetadata{}, apiError(resp)
	}

	meta := DownloadMetadata{
		ContentType:        resp.Header.Get("Content-Type"),
		ContentDisposition: resp.Header.Get("Content-Disposition"),
		Filename:           contentDispositionFilename(resp.Header.Get("Content-Disposition")),
	}
	if _, err := io.Copy(dst, resp.Body); err != nil {
		return meta, err
	}
	return meta, nil
}

func contentDispositionFilename(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	_, params, err := mime.ParseMediaType(value)
	if err != nil {
		return ""
	}
	name := strings.TrimSpace(params["filename"])
	if name == "" {
		name = strings.TrimSpace(params["filename*"])
	}
	if name == "" {
		return ""
	}
	base := filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	if base == "." || base == string(filepath.Separator) {
		return ""
	}
	return base
}

func (c *Client) ListTags(ctx context.Context) ([]Tag, error) {
	var all []Tag
	url := c.baseURL + "/api/tags/?format=json"
	for url != "" {
		var page listResponse[Tag]
		if err := c.doRequest(ctx, url, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Results...)
		next, err := c.resolveNextURL(page.Next)
		if err != nil {
			return nil, err
		}
		url = next
	}
	return all, nil
}

func (c *Client) ListCorrespondents(ctx context.Context) ([]Correspondent, error) {
	var all []Correspondent
	url := c.baseURL + "/api/correspondents/?format=json"
	for url != "" {
		var page listResponse[Correspondent]
		if err := c.doRequest(ctx, url, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Results...)
		next, err := c.resolveNextURL(page.Next)
		if err != nil {
			return nil, err
		}
		url = next
	}
	return all, nil
}

func (c *Client) ListDocumentTypes(ctx context.Context) ([]DocumentType, error) {
	var all []DocumentType
	url := c.baseURL + "/api/document_types/?format=json"
	for url != "" {
		var page listResponse[DocumentType]
		if err := c.doRequest(ctx, url, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Results...)
		next, err := c.resolveNextURL(page.Next)
		if err != nil {
			return nil, err
		}
		url = next
	}
	return all, nil
}

func (c *Client) ListStoragePaths(ctx context.Context) ([]StoragePath, error) {
	var all []StoragePath
	url := c.baseURL + "/api/storage_paths/?format=json"
	for url != "" {
		var page listResponse[StoragePath]
		if err := c.doRequest(ctx, url, &page); err != nil {
			return nil, err
		}
		all = append(all, page.Results...)
		next, err := c.resolveNextURL(page.Next)
		if err != nil {
			return nil, err
		}
		url = next
	}
	return all, nil
}
