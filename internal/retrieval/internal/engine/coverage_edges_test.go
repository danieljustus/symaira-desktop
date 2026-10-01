package engine

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/danieljustus/symaira-corekit/llmkit"
)

func TestStatusProbeCanceledBeforeRequest(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	generator := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{
		URL:        server.URL,
		Model:      "test-model",
		RetryCount: 0,
	})
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	result, err := generator.GenerateVectorNoRetryWithModelContext(ctx, "status probe")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("probe error = %v, want context canceled", err)
	}
	if result.Vector != nil || result.Model != "" {
		t.Fatalf("canceled probe result = %#v, want empty result", result)
	}
	if got := atomic.LoadInt32(&requests); got != 0 {
		t.Fatalf("canceled probe sent %d requests, want none", got)
	}
}

func TestStatusProbeDimensionMismatchUsesUncachedFallback(t *testing.T) {
	var requests int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&requests, 1)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"data": []map[string]any{{"embedding": []float32{0.1, 0.2}}},
		})
	}))
	defer server.Close()

	generator := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{
		URL:        server.URL,
		Model:      "test-model",
		Dim:        3,
		RetryCount: 0,
	})
	for attempt := 0; attempt < 2; attempt++ {
		result, err := generator.GenerateVectorNoRetryWithModelContext(context.Background(), "status probe")
		if err != nil {
			t.Fatalf("probe %d error = %v", attempt+1, err)
		}
		if result.Model != LocalHashModelName || len(result.Vector) != 3 {
			t.Fatalf("probe %d result = model %q, %d dimensions; want local hash with 3 dimensions", attempt+1, result.Model, len(result.Vector))
		}
	}
	if got := atomic.LoadInt32(&requests); got != 2 {
		t.Fatalf("dimension-mismatch probes sent %d requests, want 2 uncached probes", got)
	}
}

func TestEmbeddingCacheEvictsLeastRecentlyUsedEntry(t *testing.T) {
	var requests int32
	server := embedServer(t, func(inputs []string) (int, [][]float32) {
		atomic.AddInt32(&requests, 1)
		vectors := make([][]float32, len(inputs))
		for i := range inputs {
			vectors[i] = []float32{float32(i + 1)}
		}
		return http.StatusOK, vectors
	})
	defer server.Close()

	generator := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{
		URL:        server.URL,
		Model:      "test-model",
		Dim:        1,
		RetryCount: 0,
	})
	texts := make([]string, maxEmbeddingCacheSize)
	for i := range texts {
		texts[i] = fmt.Sprintf("entry-%d", i)
	}
	// Keep each HTTP batch bounded while filling the cache through its public API.
	for start := 0; start < len(texts); start += 500 {
		end := start + 500
		if end > len(texts) {
			end = len(texts)
		}
		if got := len(generator.GenerateVectors(texts[start:end])); got != end-start {
			t.Fatalf("generated %d vectors for batch of %d", got, end-start)
		}
	}
	if got := atomic.LoadInt32(&requests); got != 20 {
		t.Fatalf("cache fill made %d requests, want 20", got)
	}

	// Reading entry 0 promotes it. Inserting one more distinct entry should
	// evict entry 1, which is now the least recently used.
	if got := generator.GenerateVectorNoRetryWithModel("entry-0").Model; got != "test-model" {
		t.Fatalf("cached entry 0 model = %q, want test-model", got)
	}
	if got := atomic.LoadInt32(&requests); got != 20 {
		t.Fatalf("cache hit made another request; total = %d", got)
	}
	if got := generator.GenerateVectorNoRetryWithModel("new-entry").Model; got != "test-model" {
		t.Fatalf("new entry model = %q, want test-model", got)
	}
	if got := atomic.LoadInt32(&requests); got != 21 {
		t.Fatalf("new entry made %d total requests, want 21", got)
	}
	if got := generator.GenerateVectorNoRetryWithModel("entry-0").Model; got != "test-model" {
		t.Fatalf("recently used entry 0 model = %q, want test-model", got)
	}
	if got := atomic.LoadInt32(&requests); got != 21 {
		t.Fatalf("entry 0 was evicted after an LRU refresh; total requests = %d", got)
	}
	if got := generator.GenerateVectorNoRetryWithModel("entry-1").Model; got != "test-model" {
		t.Fatalf("evicted entry 1 model = %q, want test-model", got)
	}
	if got := atomic.LoadInt32(&requests); got != 22 {
		t.Fatalf("evicted entry 1 made %d total requests, want 22", got)
	}
}

func TestIsTransientOllamaErrorClassificationEdges(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil, want: false},
		{name: "unavailable sentinel", err: fmt.Errorf("wrapped: %w", errOllamaUnavailable), want: true},
		{name: "transport code", err: &llmkit.Error{Code: llmkit.ErrCodeTransport}, want: true},
		{name: "server response", err: &llmkit.Error{Code: llmkit.ErrCodeProvider, StatusCode: http.StatusServiceUnavailable}, want: true},
		{name: "client response", err: &llmkit.Error{Code: llmkit.ErrCodeProvider, StatusCode: http.StatusBadRequest}, want: false},
		{name: "rate limit response", err: &llmkit.Error{Code: llmkit.ErrCodeRateLimited, StatusCode: http.StatusTooManyRequests}, want: false},
		{name: "ordinary error", err: errors.New("bad request"), want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := isTransientOllamaError(test.err); got != test.want {
				t.Fatalf("isTransientOllamaError(%v) = %t, want %t", test.err, got, test.want)
			}
		})
	}
}

func TestEmbedWithRetriesClampsNegativeRetriesAndBackoff(t *testing.T) {
	var calls int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	generator := NewEmbeddingsGeneratorWithOllamaConfig(OllamaConfig{
		URL:   server.URL,
		Model: "test-model",
	})
	generator.RetryBackoff = -time.Second
	var sleeps int32
	generator.sleepFn = func(delay time.Duration) {
		if delay != defaultOllamaBackoff {
			t.Errorf("retry delay = %v, want default backoff %v", delay, defaultOllamaBackoff)
		}
		atomic.AddInt32(&sleeps, 1)
	}

	_, err := generator.embedWithRetries([]string{"text"}, -1)
	if err == nil {
		t.Fatal("embedWithRetries succeeded after a server error")
	}
	if got := atomic.LoadInt32(&calls); got != 1 {
		t.Fatalf("negative retry count made %d calls, want one", got)
	}
	if got := atomic.LoadInt32(&sleeps); got != 0 {
		t.Fatalf("negative retry count slept %d times, want none", got)
	}
}
