package engine

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestNewEmbeddingsGeneratorWithOllamaConfig_AppliesConfig(t *testing.T) {
	cfg := OllamaConfig{
		URL:          "http://example.test/api/embeddings",
		Model:        "test-model",
		Timeout:      5 * time.Second,
		RetryCount:   3,
		RetryBackoff: 100 * time.Millisecond,
	}
	eg := NewEmbeddingsGeneratorWithOllamaConfig(cfg)
	eg.sleepFn = func(time.Duration) {}
	if eg.OllamaURL != cfg.URL {
		t.Errorf("expected URL %q, got %q", cfg.URL, eg.OllamaURL)
	}
	if eg.Model != cfg.Model {
		t.Errorf("expected Model %q, got %q", cfg.Model, eg.Model)
	}
	if eg.Timeout != cfg.Timeout {
		t.Errorf("expected Timeout %v, got %v", cfg.Timeout, eg.Timeout)
	}
	if eg.RetryCount != cfg.RetryCount {
		t.Errorf("expected RetryCount %d, got %d", cfg.RetryCount, eg.RetryCount)
	}
	if eg.RetryBackoff != cfg.RetryBackoff {
		t.Errorf("expected RetryBackoff %v, got %v", cfg.RetryBackoff, eg.RetryBackoff)
	}
	if eg.ollama == nil {
		t.Fatal("expected the ollamakit client to be initialized")
	}
	if eg.ollama.BaseURL() != "http://example.test" {
		t.Errorf("expected ollamakit base URL %q, got %q", "http://example.test", eg.ollama.BaseURL())
	}
}

func TestNewEmbeddingsGeneratorWithOllamaConfig_ZeroValuesGetDefaults(t *testing.T) {
	eg := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{})
	if eg.OllamaURL == "" {
		t.Error("expected default OllamaURL")
	}
	if eg.Model == "" {
		t.Error("expected default Model")
	}
	if eg.Timeout != defaultOllamaTimeout {
		t.Errorf("expected default Timeout %v, got %v", defaultOllamaTimeout, eg.Timeout)
	}
	if eg.RetryCount != defaultOllamaRetries {
		t.Errorf("expected default RetryCount %d, got %d", defaultOllamaRetries, eg.RetryCount)
	}
	if eg.RetryBackoff != defaultOllamaBackoff {
		t.Errorf("expected default RetryBackoff %v, got %v", defaultOllamaBackoff, eg.RetryBackoff)
	}
}

func TestNewEmbeddingsGeneratorWithOllamaConfig_NegativeRetryClampedToDefault(t *testing.T) {
	eg := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{RetryCount: -5})
	if eg.RetryCount != defaultOllamaRetries {
		t.Errorf("expected negative RetryCount clamped to default %d, got %d", defaultOllamaRetries, eg.RetryCount)
	}
}

func TestNewEmbeddingsGeneratorWithOllamaConfig_PositiveRetryCountHonored(t *testing.T) {
	eg := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{RetryCount: 5})
	if eg.RetryCount != 5 {
		t.Errorf("expected RetryCount 5, got %d", eg.RetryCount)
	}
}

// embedServer returns an httptest server that answers Ollama's /api/embed
// endpoint. handler receives the decoded input texts and returns the
// vectors to send back (one per input, in order).
func embedServer(t *testing.T, handler func(inputs []string) (status int, vecs [][]float32)) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string   `json:"model"`
			Input []string `json:"input"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		status, vecs := handler(req.Input)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		if status >= 200 && status < 300 {
			_ = json.NewEncoder(w).Encode(map[string]interface{}{"embeddings": vecs})
		}
	}))
}

func TestQueryOllama_SuccessOnFirstTry(t *testing.T) {
	var calls int32
	srv := embedServer(t, func(inputs []string) (int, [][]float32) {
		atomic.AddInt32(&calls, 1)
		return http.StatusOK, [][]float32{{0.1, 0.2, 0.3}}
	})
	defer srv.Close()

	eg := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{
		URL:          srv.URL,
		Model:        "test",
		RetryCount:   2,
		RetryBackoff: 10 * time.Millisecond,
	})
	vec, err := eg.queryOllama("hello")
	if err != nil {
		t.Fatalf("expected success, got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("expected 1 call, got %d", got)
	}
	if len(vec) != 3 {
		t.Errorf("expected 3-dim embedding, got %d", len(vec))
	}
}

func TestQueryOllama_Retries5xx(t *testing.T) {
	var calls int32
	srv := embedServer(t, func(inputs []string) (int, [][]float32) {
		if atomic.AddInt32(&calls, 1) < 3 {
			return http.StatusBadGateway, nil
		}
		return http.StatusOK, [][]float32{{0.5}}
	})
	defer srv.Close()

	eg := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{
		URL:          srv.URL,
		Model:        "test",
		RetryCount:   2,
		RetryBackoff: 1 * time.Millisecond,
	})
	vec, err := eg.queryOllama("hello")
	if err != nil {
		t.Fatalf("expected eventual success, got %v", err)
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("expected 3 calls (2 retries), got %d", got)
	}
	if len(vec) != 1 {
		t.Errorf("expected 1-dim embedding, got %d", len(vec))
	}
}

func TestQueryOllama_GivesUpAfterMaxRetries(t *testing.T) {
	var calls int32
	srv := embedServer(t, func(inputs []string) (int, [][]float32) {
		atomic.AddInt32(&calls, 1)
		return http.StatusInternalServerError, nil
	})
	defer srv.Close()

	eg := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{
		URL:          srv.URL,
		Model:        "test",
		RetryCount:   2,
		RetryBackoff: 1 * time.Millisecond,
	})
	if _, err := eg.queryOllama("hello"); err == nil {
		t.Fatal("expected exhaustion error")
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("expected 3 calls (1 initial + 2 retries), got %d", got)
	}
}

func TestQueryOllama_DoesNotRetryModelNotFound(t *testing.T) {
	var calls int32
	srv := embedServer(t, func(inputs []string) (int, [][]float32) {
		atomic.AddInt32(&calls, 1)
		return http.StatusNotFound, nil
	})
	defer srv.Close()

	eg := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{
		URL:          srv.URL,
		Model:        "test",
		RetryCount:   3,
		RetryBackoff: 1 * time.Millisecond,
	})
	if _, err := eg.queryOllama("hello"); err == nil {
		t.Fatal("expected error for missing model")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("expected exactly 1 call (no retry on model-not-found), got %d", got)
	}
}

func TestQueryOllama_RetriesOnNetworkError(t *testing.T) {
	eg := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{
		URL:          "http://127.0.0.1:1/api/embeddings",
		Model:        "test",
		RetryCount:   1,
		RetryBackoff: 1 * time.Millisecond,
		Timeout:      200 * time.Millisecond,
	})
	if _, err := eg.queryOllama("hello"); err == nil {
		t.Fatal("expected connection error")
	}
}

// TestGenerateVectors_BatchSendsOneRequest verifies that a multi-text batch
// is sent to Ollama's /api/embed endpoint as a single request, not one
// request per text (issue #67).
func TestGenerateVectors_BatchSendsOneRequest(t *testing.T) {
	var batchHits int32
	vec768 := make([]float32, 768)
	vec768b := make([]float32, 768)

	srv := embedServer(t, func(inputs []string) (int, [][]float32) {
		atomic.AddInt32(&batchHits, 1)
		if len(inputs) != 2 {
			t.Errorf("expected batch of 2 inputs, got %d", len(inputs))
		}
		return http.StatusOK, [][]float32{vec768, vec768b}
	})
	defer srv.Close()

	eg := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{
		URL:   srv.URL,
		Model: "test",
	})

	vecs := eg.GenerateVectors([]string{"alpha", "beta"})
	if len(vecs) != 2 {
		t.Fatalf("expected 2 vectors, got %d", len(vecs))
	}
	for i, v := range vecs {
		if len(v) != 768 {
			t.Errorf("vector %d: expected 768 dims, got %d", i, len(v))
		}
	}
	if got := atomic.LoadInt32(&batchHits); got != 1 {
		t.Errorf("expected 1 batch request, got %d", got)
	}
}

// TestRedirectNotFollowed verifies that the embedder does not follow
// a 301 redirect (issue #68) — enforced by ollamakit itself.
func TestRedirectNotFollowed(t *testing.T) {
	var redirectHits int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&redirectHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer target.Close()

	var redirectorHits int32
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&redirectorHits, 1)
		http.Redirect(w, r, target.URL, http.StatusMovedPermanently)
	}))
	defer redirector.Close()

	eg := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{
		URL:          redirector.URL + "/api/embeddings",
		Model:        "test",
		RetryCount:   0,
		RetryBackoff: 1 * time.Millisecond,
	})

	_, err := eg.queryOllama("test")
	if err == nil {
		t.Fatal("expected error from redirect response")
	}

	if got := atomic.LoadInt32(&redirectorHits); got != 1 {
		t.Errorf("expected 1 hit on redirector, got %d", got)
	}
	if got := atomic.LoadInt32(&redirectHits); got != 0 {
		t.Errorf("expected 0 hits on redirect target (redirect was followed!), got %d", got)
	}
}

func TestIsStopWordUsesPackageMap(t *testing.T) {
	stopSamples := []string{
		"and", "the", "a", "an", "of", "to", "in", "is", "it", "that",
		"und", "der", "die", "das", "ein", "eine", "ist", "es", "dass",
		"von", "zu", "mit", "auf", "für", "den", "dem", "des", "im", "am",
	}
	for _, w := range stopSamples {
		if !isStopWord(w) {
			t.Errorf("expected %q to be classified as a stop word", w)
		}
	}

	nonStop := []string{"falcon", "database", "golang", "symaira", "search"}
	for _, w := range nonStop {
		if isStopWord(w) {
			t.Errorf("expected %q to NOT be classified as a stop word", w)
		}
	}
}

// TestGenerateVectorNoRetry_SkipsRetries verifies that GenerateVectorNoRetry
// makes only a single Ollama attempt (no retry/backoff) when the server
// returns 5xx, then falls back to the local hash vector (issue #162).
func TestGenerateVectorNoRetry_SkipsRetries(t *testing.T) {
	var calls int32
	srv := embedServer(t, func(inputs []string) (int, [][]float32) {
		atomic.AddInt32(&calls, 1)
		return http.StatusInternalServerError, nil
	})
	defer srv.Close()

	var sleepCount int32
	eg := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{
		URL:          srv.URL,
		Model:        "test",
		RetryCount:   2,
		RetryBackoff: 500 * time.Millisecond,
	})
	eg.sleepFn = func(d time.Duration) {
		atomic.AddInt32(&sleepCount, 1)
	}

	vec := eg.GenerateVectorNoRetry("hello")
	if len(vec) != 768 {
		t.Fatalf("expected 768-dim hash fallback vector, got %d", len(vec))
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("expected exactly 1 call (no retries), got %d", got)
	}
	if got := atomic.LoadInt32(&sleepCount); got != 0 {
		t.Errorf("expected 0 sleeps, got %d", got)
	}
}

// TestGenerateVectorNoRetry_FastReturnWhenUnreachable verifies that
// GenerateVectorNoRetry returns quickly (well under the retry penalty)
// when Ollama is unreachable on the network (issue #162).
func TestGenerateVectorNoRetry_FastReturnWhenUnreachable(t *testing.T) {
	eg := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{
		URL:          "http://127.0.0.1:1/api/embeddings",
		Model:        "test",
		RetryCount:   2,
		RetryBackoff: 1 * time.Second,
		Timeout:      100 * time.Millisecond,
	})

	start := time.Now()
	vec := eg.GenerateVectorNoRetry("hello")
	elapsed := time.Since(start)

	if len(vec) != 768 {
		t.Fatalf("expected 768-dim hash fallback vector, got %d", len(vec))
	}
	if elapsed > 500*time.Millisecond {
		t.Errorf("GenerateVectorNoRetry took %v; expected well under the ~1.5s retry penalty", elapsed)
	}
}

// TestGenerateVector_StillRetriesOnFailure verifies that the normal
// GenerateVector path still retries with backoff when Ollama returns 5xx,
// preserving indexing-time retry behavior (issue #162).
func TestGenerateVector_StillRetriesOnFailure(t *testing.T) {
	var calls int32
	srv := embedServer(t, func(inputs []string) (int, [][]float32) {
		atomic.AddInt32(&calls, 1)
		return http.StatusInternalServerError, nil
	})
	defer srv.Close()

	var sleepCount int32
	eg := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{
		URL:          srv.URL,
		Model:        "test",
		RetryCount:   2,
		RetryBackoff: 1 * time.Millisecond,
	})
	eg.sleepFn = func(d time.Duration) {
		atomic.AddInt32(&sleepCount, 1)
	}

	vec := eg.GenerateVector("hello")
	if len(vec) != 768 {
		t.Fatalf("expected 768-dim hash fallback vector, got %d", len(vec))
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("expected 3 calls (1 initial + 2 retries), got %d", got)
	}
	if got := atomic.LoadInt32(&sleepCount); got != 2 {
		t.Errorf("expected 2 sleeps for 2 retries, got %d", got)
	}
}

// TestEmbedWithRetries_ZeroRetriesMakesSingleAttempt verifies that calling
// embedWithRetries with maxRetries=0 makes exactly one HTTP request with no
// sleep (issue #162).
func TestEmbedWithRetries_ZeroRetriesMakesSingleAttempt(t *testing.T) {
	var calls int32
	srv := embedServer(t, func(inputs []string) (int, [][]float32) {
		atomic.AddInt32(&calls, 1)
		return http.StatusInternalServerError, nil
	})
	defer srv.Close()

	eg := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{
		URL:          srv.URL,
		Model:        "test",
		RetryCount:   5,
		RetryBackoff: 1 * time.Second,
	})
	var sleepCount int32
	eg.sleepFn = func(d time.Duration) {
		atomic.AddInt32(&sleepCount, 1)
	}

	if _, err := eg.embedWithRetries([]string{"hello"}, 0); err == nil {
		t.Fatal("expected error from 500 response")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Errorf("expected exactly 1 call, got %d", got)
	}
	if got := atomic.LoadInt32(&sleepCount); got != 0 {
		t.Errorf("expected 0 sleeps, got %d", got)
	}
}

func TestDim_CachesFirstOllamaResponse(t *testing.T) {
	srv := embedServer(t, func(inputs []string) (int, [][]float32) {
		return http.StatusOK, [][]float32{make([]float32, 384)}
	})
	defer srv.Close()

	eg := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{
		URL:          srv.URL,
		Model:        "test",
		RetryCount:   0,
		RetryBackoff: time.Millisecond,
	})

	vec := eg.GenerateVector("hello")
	if len(vec) != 384 {
		t.Fatalf("expected 384-dim vector, got %d", len(vec))
	}
	if got := eg.Dim(); got != 384 {
		t.Errorf("expected Dim() = 384 after caching, got %d", got)
	}

	vec2 := eg.GenerateVector("world")
	if len(vec2) != 384 {
		t.Fatalf("expected 384-dim vector for second call, got %d", len(vec2))
	}
}

func TestDim_DifferentDimensionTriggersFallback(t *testing.T) {
	var callCount int32
	srv := embedServer(t, func(inputs []string) (int, [][]float32) {
		n := atomic.AddInt32(&callCount, 1)
		if n <= 2 {
			return http.StatusOK, [][]float32{make([]float32, 384)}
		}
		return http.StatusOK, [][]float32{make([]float32, 512)}
	})
	defer srv.Close()

	eg := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{
		URL:          srv.URL,
		Model:        "test",
		RetryCount:   0,
		RetryBackoff: time.Millisecond,
	})

	vec1 := eg.GenerateVector("first")
	if len(vec1) != 384 {
		t.Fatalf("expected 384-dim vector, got %d", len(vec1))
	}
	if got := eg.Dim(); got != 384 {
		t.Errorf("expected Dim() = 384 after first response, got %d", got)
	}

	vec3 := eg.GenerateVector("third")
	if len(vec3) != 384 {
		t.Fatalf("expected fallback to 384-dim hash vector, got %d", len(vec3))
	}
}

func TestDim_ConfigDimOverridesAutoDetect(t *testing.T) {
	eg := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{
		URL:   "http://localhost:99999/api/embeddings",
		Model: "test",
		Dim:   512,
	})
	if got := eg.Dim(); got != 512 {
		t.Errorf("expected Dim() = 512 from config, got %d", got)
	}
}

func TestDim_DefaultFallbackWhenNoResponse(t *testing.T) {
	eg := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{})
	if got := eg.Dim(); got != defaultEmbeddingDim {
		t.Errorf("expected Dim() = %d (default), got %d", defaultEmbeddingDim, got)
	}
}

func TestModelName_ReturnsConfiguredModel(t *testing.T) {
	eg := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{Model: "mxbai-embed-large"})
	if got := eg.ModelName(); got != "mxbai-embed-large" {
		t.Errorf("expected ModelName() = %q, got %q", "mxbai-embed-large", got)
	}
}

// TestGenerateVectorsWithModel_ReportsFallbackProvenance verifies that the
// model reported for each vector reflects the actual provenance, with fallback
// vectors carrying localHashModelName (issue #268).
func TestGenerateVectorsWithModel_ReportsFallbackProvenance(t *testing.T) {
	vec768 := make([]float32, 768)
	vec384 := make([]float32, 384)

	srv := embedServer(t, func(inputs []string) (int, [][]float32) {
		if len(inputs) != 2 {
			t.Errorf("expected batch of 2 inputs, got %d", len(inputs))
		}
		return http.StatusOK, [][]float32{vec768, vec384}
	})
	defer srv.Close()

	eg := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{
		URL:   srv.URL,
		Model: "test-model",
		Dim:   768,
	})

	results := eg.GenerateVectorsWithModel([]string{"alpha", "beta"})
	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	if results[0].Model != "test-model" {
		t.Errorf("expected first result model %q, got %q", "test-model", results[0].Model)
	}
	if results[1].Model != localHashModelName {
		t.Errorf("expected second result model %q, got %q", localHashModelName, results[1].Model)
	}
	if len(results[1].Vector) != 768 {
		t.Errorf("expected fallback vector dim 768, got %d", len(results[1].Vector))
	}
}

// TestConfiguredDim_SendsDimensionsParameter verifies that a configured
// embedding_dim is sent as Ollama's `dimensions` body parameter so the
// Matryoshka-truncated output matches the configured dimension (issue #302).
func TestConfiguredDim_SendsDimensionsParameter(t *testing.T) {
	var gotDim int
	var gotPath string
	vec768 := make([]float32, 768)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		var req struct {
			Dimensions int `json:"dimensions"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		gotDim = req.Dimensions
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{vec768}})
	}))
	defer srv.Close()

	eg := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{
		URL:   srv.URL,
		Model: "qwen3-embedding:0.6b",
		Dim:   768,
	})

	vec := eg.GenerateVector("Hallo Welt")
	if len(vec) != 768 {
		t.Errorf("expected 768-dim vector, got %d", len(vec))
	}
	if gotDim != 768 {
		t.Errorf("expected dimensions=768 in request, got %d", gotDim)
	}
	if gotPath != "/api/embed" {
		t.Errorf("expected request to /api/embed, got %q", gotPath)
	}
}

// TestAutoDetectDim_OmitsDimensionsParameter verifies that without a
// configured embedding_dim the request carries no dimensions parameter and
// the dimension is auto-detected from the response (default behaviour).
func TestAutoDetectDim_OmitsDimensionsParameter(t *testing.T) {
	var sawDim bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		_ = json.NewDecoder(r.Body).Decode(&req)
		_, sawDim = req["dimensions"]
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{{0.1, 0.2}}})
	}))
	defer srv.Close()

	eg := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{
		URL:   srv.URL,
		Model: "nomic-embed-text",
	})

	vec := eg.GenerateVector("Hallo Welt")
	if len(vec) != 2 {
		t.Errorf("expected 2-dim auto-detected vector, got %d", len(vec))
	}
	if sawDim {
		t.Error("expected no dimensions parameter without configured embedding_dim")
	}
	if eg.Dim() != 2 {
		t.Errorf("expected auto-detected Dim()=2, got %d", eg.Dim())
	}
}

// TestConfiguredDim_RetriesOn5xx verifies the dimensions transport honours
// the retry contract for transient failures.
func TestConfiguredDim_RetriesOn5xx(t *testing.T) {
	var calls int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if atomic.AddInt32(&calls, 1) < 3 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"embeddings": [][]float32{{0.1, 0.2, 0.3}}})
	}))
	defer srv.Close()

	eg := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{
		URL:          srv.URL,
		Model:        "qwen3-embedding:0.6b",
		Dim:          3,
		RetryCount:   2,
		RetryBackoff: 10 * time.Millisecond,
	})

	vec, err := eg.queryOllamaWithRetries("test", 2)
	if err != nil {
		t.Fatalf("expected success after retries, got %v", err)
	}
	if len(vec) != 3 {
		t.Errorf("expected 3-dim vector, got %d", len(vec))
	}
	if got := atomic.LoadInt32(&calls); got != 3 {
		t.Errorf("expected 3 calls, got %d", got)
	}
}
