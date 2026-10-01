package engine

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-corekit/llmkit"
)

type embeddingHTTPFixture struct {
	SchemaVersion int                        `json:"schema_version"`
	Cases         []embeddingHTTPFixtureCase `json:"cases"`
}

type embeddingHTTPFixtureCase struct {
	ID                  string          `json:"id"`
	Model               string          `json:"model"`
	Inputs              []string        `json:"inputs"`
	Dimensions          int             `json:"dimensions"`
	TimeoutMillis       int             `json:"timeout_millis"`
	ResponseStatus      int             `json:"response_status"`
	ResponseBody        string          `json:"response_body"`
	ResponseDelayMillis int             `json:"response_delay_millis,omitempty"`
	ExpectedVectors     [][]float32     `json:"expected_vectors,omitempty"`
	ExpectedErrorCode   string          `json:"expected_error_code,omitempty"`
	ExpectedErrorStatus int             `json:"expected_error_status,omitempty"`
	ExpectedErrorBody   string          `json:"expected_error_body,omitempty"`
	ExpectedFailureKind string          `json:"expected_failure_kind,omitempty"`
	ExpectedRetryable   *bool           `json:"expected_retryable,omitempty"`
	RequestMethod       string          `json:"request_method,omitempty"`
	RequestPath         string          `json:"request_path,omitempty"`
	RequestContentType  string          `json:"request_content_type,omitempty"`
	RequestAccept       string          `json:"request_accept,omitempty"`
	RequestBody         json.RawMessage `json:"request_body,omitempty"`
}

// TestEmbeddingHTTPPortFixture records the actual Go llmkit Embed wire call
// used by EmbeddingsGenerator, including its request and failure behavior.
func TestEmbeddingHTTPPortFixture(t *testing.T) {
	cases := []embeddingHTTPFixtureCase{
		{
			ID: "batch-with-pinned-dimensions", Model: "qwen3-embedding:0.6b",
			Inputs: []string{"first note", "second 🧪 note"}, Dimensions: 3, TimeoutMillis: 2000,
			ResponseStatus:  http.StatusOK,
			ResponseBody:    `{"data":[{"embedding":[0.25,-0.5,1]},{"embedding":[2,3.5,-4]}]}`,
			ExpectedVectors: [][]float32{{0.25, -0.5, 1}, {2, 3.5, -4}},
		},
		{
			ID: "single-with-provider-dimension", Model: "nomic-embed-text", Inputs: []string{"one"},
			TimeoutMillis: 2000, ResponseStatus: http.StatusOK,
			ResponseBody:    `{"data":[{"embedding":[0.125,0.875]}]}`,
			ExpectedVectors: [][]float32{{0.125, 0.875}},
		},
		{
			ID: "case-insensitive-response-fields", Model: "case-model", Inputs: []string{"case"},
			TimeoutMillis: 2000, ResponseStatus: http.StatusOK,
			ResponseBody:    `{"DaTa":[{"eMbEdDiNg":[0.75,-0.25]}]}`,
			ExpectedVectors: [][]float32{{0.75, -0.25}},
		},
		{
			ID: "duplicate-case-variant-data-last-wins", Model: "case-model", Inputs: []string{"case"},
			TimeoutMillis: 2000, ResponseStatus: http.StatusOK,
			ResponseBody: `{"data":[{"embedding":[0.25]}],"DATA":[{"embedding":[0.75]}]}`,
		},
		{
			ID: "duplicate-case-variant-embedding-last-wins", Model: "case-model", Inputs: []string{"case"},
			TimeoutMillis: 2000, ResponseStatus: http.StatusOK,
			ResponseBody: `{"data":[{"embedding":[0.25],"EMBEDDING":[0.75]}]}`,
		},
		{
			ID: "null-vector-is-empty-vector", Model: "null-model", Inputs: []string{"null"},
			TimeoutMillis: 2000, ResponseStatus: http.StatusOK,
			ResponseBody: `{"data":[{"embedding":null}]}`,
		},
		{
			ID: "http-server-error", Model: "test-model", Inputs: []string{"retry is caller policy"},
			TimeoutMillis: 2000, ResponseStatus: http.StatusServiceUnavailable,
			ResponseBody:      `{"error":"local test failure"}`,
			ExpectedErrorCode: string(llmkit.ErrCodeProvider), ExpectedErrorStatus: http.StatusServiceUnavailable,
		},
		{
			ID: "missing-model-is-not-retryable", Model: "missing-model", Inputs: []string{"do not retry"},
			TimeoutMillis: 2000, ResponseStatus: http.StatusNotFound,
			ResponseBody:      `{"error":"model not found"}`,
			ExpectedErrorCode: string(llmkit.ErrCodeModelNotFound), ExpectedErrorStatus: http.StatusNotFound,
		},
		{
			ID: "unauthorized-is-auth-failure", Model: "test-model", Inputs: []string{"bad credentials"},
			TimeoutMillis: 2000, ResponseStatus: http.StatusUnauthorized,
			ResponseBody:      `{"error":"unauthorized"}`,
			ExpectedErrorCode: string(llmkit.ErrCodeAuth), ExpectedErrorStatus: http.StatusUnauthorized,
		},
		{
			ID: "rate-limit-is-not-transport-retry", Model: "test-model", Inputs: []string{"rate limited"},
			TimeoutMillis: 2000, ResponseStatus: http.StatusTooManyRequests,
			ResponseBody:      `{"error":"too many requests"}`,
			ExpectedErrorCode: string(llmkit.ErrCodeRateLimited), ExpectedErrorStatus: http.StatusTooManyRequests,
		},
		{
			ID: "context-overflow-marker", Model: "test-model", Inputs: []string{"too long"},
			TimeoutMillis: 2000, ResponseStatus: http.StatusBadRequest,
			ResponseBody:      `{"error":"context_length_exceeded"}`,
			ExpectedErrorCode: string(llmkit.ErrCodeContextOverflow), ExpectedErrorStatus: http.StatusBadRequest,
		},
		{
			ID: "context-overflow-marker-after-display-excerpt", Model: "test-model", Inputs: []string{"too long"},
			TimeoutMillis: 2000, ResponseStatus: http.StatusBadRequest,
			ResponseBody:      `{"error":"` + strings.Repeat("x", 600) + ` context_length_exceeded"}`,
			ExpectedErrorCode: string(llmkit.ErrCodeContextOverflow), ExpectedErrorStatus: http.StatusBadRequest,
		},
		{
			ID: "malformed-success-body", Model: "test-model", Inputs: []string{"decode this"},
			TimeoutMillis: 2000, ResponseStatus: http.StatusOK, ResponseBody: `{"data":[`,
			ExpectedErrorCode: string(llmkit.ErrCodeProvider),
		},
		{
			ID: "wrong-response-count", Model: "test-model", Inputs: []string{"first", "second"},
			TimeoutMillis: 2000, ResponseStatus: http.StatusOK,
			ResponseBody:      `{"data":[{"embedding":[1,0]}]}`,
			ExpectedErrorCode: string(llmkit.ErrCodeProvider),
		},
		{
			ID: "empty-inputs-rejected-before-request", Model: "test-model", Inputs: []string{},
			TimeoutMillis: 2000, ResponseStatus: http.StatusOK,
			ResponseBody: `{"data":[]}`, ExpectedErrorCode: "input_validation",
		},
		{
			ID: "request-timeout", Model: "test-model", Inputs: []string{"wait"},
			TimeoutMillis: 40, ResponseStatus: http.StatusOK,
			ResponseBody: `{"data":[{"embedding":[1]}]}`, ResponseDelayMillis: 250,
			ExpectedErrorCode: string(llmkit.ErrCodeTransport),
		},
	}

	fixture := embeddingHTTPFixture{SchemaVersion: 1}
	for _, testCase := range cases {
		t.Run(testCase.ID, func(t *testing.T) {
			type capturedRequest struct {
				method      string
				path        string
				contentType string
				accept      string
				body        json.RawMessage
			}
			requests := make(chan capturedRequest, 4)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					t.Errorf("read request body: %v", err)
				}
				requests <- capturedRequest{
					method: r.Method, path: r.URL.RequestURI(), contentType: r.Header.Get("Content-Type"),
					accept: r.Header.Get("Accept"), body: append(json.RawMessage(nil), body...),
				}
				if testCase.ResponseDelayMillis > 0 {
					time.Sleep(time.Duration(testCase.ResponseDelayMillis) * time.Millisecond)
				}
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(testCase.ResponseStatus)
				_, _ = w.Write([]byte(testCase.ResponseBody))
			}))
			defer server.Close()

			generator := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{
				URL:          server.URL + "/api/embeddings?ignored=1",
				Model:        testCase.Model,
				Dim:          testCase.Dimensions,
				Timeout:      time.Duration(testCase.TimeoutMillis) * time.Millisecond,
				RetryCount:   0,
				RetryBackoff: time.Millisecond,
			})
			// The constructor maps zero to the production retry default; pin the
			// transport oracle to one request so retry policy remains outside the
			// llmkit Embed wire contract captured here.
			generator.RetryCount = 0
			generator.sleepFn = func(time.Duration) {}
			vectors, err := generator.queryOllamaBatch(testCase.Inputs)
			if testCase.ExpectedErrorCode == "" {
				if err != nil {
					t.Fatalf("query embeddings: %v", err)
				}
				testCase.ExpectedVectors = vectors
			} else {
				if err == nil {
					t.Fatal("expected embedding request failure")
				}
				if testCase.ExpectedErrorCode == "input_validation" {
					if !strings.Contains(err.Error(), "embed inputs must not be empty") {
						t.Fatalf("error = %v, want empty-input validation error", err)
					}
					testCase.ExpectedFailureKind = "empty_inputs"
					retryable := isTransientOllamaError(err)
					testCase.ExpectedRetryable = &retryable
				} else {
					expectedCode := testCase.ExpectedErrorCode
					expectedStatus := testCase.ExpectedErrorStatus
					var providerErr *llmkit.Error
					if !errors.As(err, &providerErr) {
						t.Fatalf("error %T (%v), want llmkit.Error", err, err)
					}
					if expectedCode != "" && string(providerErr.Code) != expectedCode {
						t.Fatalf("error code = %q, want %q", providerErr.Code, expectedCode)
					}
					if expectedStatus != 0 && providerErr.StatusCode != expectedStatus {
						t.Fatalf("status = %d, want %d", providerErr.StatusCode, expectedStatus)
					}
					testCase.ExpectedErrorCode = string(providerErr.Code)
					testCase.ExpectedErrorStatus = providerErr.StatusCode
					testCase.ExpectedErrorBody = providerErr.Body
					retryable := isTransientOllamaError(err)
					testCase.ExpectedRetryable = &retryable
					switch {
					case providerErr.StatusCode != 0:
						testCase.ExpectedFailureKind = "http_status"
					case providerErr.Code == llmkit.ErrCodeTransport:
						testCase.ExpectedFailureKind = "transport"
					case providerErr.Err != nil && strings.Contains(providerErr.Err.Error(), "expected ") && strings.Contains(providerErr.Err.Error(), " embeddings, got "):
						testCase.ExpectedFailureKind = "embedding_count"
					default:
						testCase.ExpectedFailureKind = "invalid_response"
					}
				}
			}
			if len(testCase.Inputs) == 0 {
				select {
				case request := <-requests:
					t.Fatalf("empty inputs unexpectedly sent request: %+v", request)
				default:
				}
			} else {
				select {
				case request := <-requests:
					testCase.RequestMethod = request.method
					testCase.RequestPath = request.path
					testCase.RequestContentType = request.contentType
					testCase.RequestAccept = request.accept
					testCase.RequestBody = request.body
				case <-time.After(time.Second):
					t.Fatal("request was not observed by the local HTTP server")
				}
			}
			fixture.Cases = append(fixture.Cases, testCase)
		})
	}

	encoded, err := json.MarshalIndent(fixture, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	encoded = append(encoded, '\n')
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("test source path unavailable")
	}
	path := filepath.Join(filepath.Dir(source), "../../../../testdata/port/retrieval/embedding-http.json")
	if os.Getenv("PORT_GENERATE") == "1" {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, encoded, 0o600); err != nil {
			t.Fatal(err)
		}
		return
	}
	//nolint:gosec // path is fixed from this test file to its committed fixture.
	current, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(current, encoded) {
		t.Fatal("embedding HTTP fixture is stale; regenerate explicitly with PORT_GENERATE=1 go test ./internal/retrieval/internal/engine -run '^TestEmbeddingHTTPPortFixture$'")
	}
}
