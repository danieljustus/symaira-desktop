package engine

import (
	"bytes"
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/singleflight"

	"github.com/danieljustus/symaira-corekit/ollamakit"
)

const (
	maxEmbeddingCacheSize = 10000

	defaultOllamaTimeout    = 120 * time.Second
	defaultOllamaRetries    = 2
	defaultOllamaBackoff    = 500 * time.Millisecond
	defaultOllamaMaxBackoff = 8 * time.Second
)

// EmbeddingsGenerator manages Ollama queries and the pure-Go hashing fallback.
type EmbeddingsGenerator struct {
	OllamaURL    string
	Model        string
	Timeout      time.Duration
	RetryCount   int
	RetryBackoff time.Duration
	configDim    int                 // from config; 0 means auto-detect from first response
	sleepFn      func(time.Duration) // injectable for tests; defaults to time.Sleep
	ollama       *ollamakit.Client
	cache        map[string]*list.Element
	cacheOrder   *list.List
	cacheMu      sync.Mutex
	dim          int // cached dimension from first successful Ollama response
	dimOnce      sync.Once
	sf           singleflight.Group
}

type cacheEntry struct {
	key   string
	value []float32
	model string
}

func newEmbeddingsGenerator() *EmbeddingsGenerator {
	eg := &EmbeddingsGenerator{
		OllamaURL:    "http://localhost:11434/api/embeddings",
		Model:        "qwen3-embedding:0.6b",
		configDim:    defaultEmbeddingDim, // Matryoshka pin: keep 768 dims (issue #302)
		Timeout:      defaultOllamaTimeout,
		RetryCount:   defaultOllamaRetries,
		RetryBackoff: defaultOllamaBackoff,
		sleepFn:      time.Sleep,
		cache:        make(map[string]*list.Element),
		cacheOrder:   list.New(),
	}
	eg.rebuildOllamaClient()
	return eg
}

// rebuildOllamaClient (re)constructs the shared ollamakit client from the
// generator's current OllamaURL/Model/Timeout. Must be called whenever any
// of those fields change after construction.
func (eg *EmbeddingsGenerator) rebuildOllamaClient() {
	eg.ollama = ollamakit.New(ollamakit.Config{
		BaseURL: ollamaBaseURL(eg.OllamaURL),
		Model:   eg.Model,
		Timeout: eg.Timeout,
	})
}

// ollamaBaseURL strips a configured Ollama endpoint path (e.g.
// "http://localhost:11434/api/embeddings" or ".../api/embed") down to the
// scheme+host root ollamakit.Config.BaseURL expects. Malformed input is
// passed through unchanged so ollamakit's own defaulting takes over.
func ollamaBaseURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return raw
	}
	return u.Scheme + "://" + u.Host
}

const defaultEmbeddingDim = 768

// localHashModelName is the embedding_model value stored for chunks that were
// embedded with the deterministic local hash fallback instead of the configured
// Ollama model. Keeping it distinct lets DetectMixedEmbeddingSpaces flag the
// mixed-space corruption.
const localHashModelName = "local-hash"

// Dim returns the cached embedding dimension. If no Ollama response has been
// received yet and no config-driven dimension was set, it returns the legacy
// default of 768 for backwards compatibility.
func (eg *EmbeddingsGenerator) Dim() int {
	if eg.configDim > 0 {
		return eg.configDim
	}
	if eg.dim > 0 {
		return eg.dim
	}
	return defaultEmbeddingDim
}

func (eg *EmbeddingsGenerator) effectiveDim() int {
	return eg.Dim()
}

func (eg *EmbeddingsGenerator) ModelName() string {
	return eg.Model
}

// NewEmbeddingsGenerator sets up the standard engine configuration.
func NewEmbeddingsGenerator() *EmbeddingsGenerator {
	return newEmbeddingsGenerator()
}

// EmbeddingResult pairs a vector with the name of the model that produced it.
// When the local hash fallback is used the Model field reports a distinct
// name (e.g. "local-hash") so callers can detect mixed embedding spaces.
type EmbeddingResult struct {
	Vector []float32
	Model  string
}

// Embedder is the public surface of an embedding generator. Callers depend
// on the contract, not on the concrete *EmbeddingsGenerator struct, so the
// HTTP client / cache / mutex plumbing stays encapsulated.
type Embedder interface {
	GenerateVector(text string) []float32
	GenerateVectors(texts []string) [][]float32
	// GenerateVectorsWithModel returns one result per input text, preserving the
	// actual embedding provenance. This lets the indexer record the real model
	// for each chunk, including the local hash fallback name.
	GenerateVectorsWithModel(texts []string) []EmbeddingResult
	// GenerateVectorNoRetry produces an embedding vector for the given text
	// without retrying on Ollama failures. If Ollama is unreachable or
	// returns an error, the local hash fallback is used immediately. This
	// is intended for interactive search paths where latency matters more
	// than embedding quality (issue #162).
	GenerateVectorNoRetry(text string) []float32
	// GenerateVectorNoRetryWithModel is the provenance-aware variant of
	// GenerateVectorNoRetry. It returns the actual model used for the query
	// vector so the search path can warn when the query fell back while the
	// index was built with an Ollama model (issue #270).
	GenerateVectorNoRetryWithModel(text string) EmbeddingResult
	Dim() int
	ModelName() string
}

// Compile-time check that *EmbeddingsGenerator satisfies Embedder.
var _ Embedder = (*EmbeddingsGenerator)(nil)

// OllamaConfig bundles the user-tunable knobs for the embeddings
// generator. Zero values fall back to package defaults.
type OllamaConfig struct {
	URL          string
	Model        string
	Dim          int
	Timeout      time.Duration
	RetryCount   int
	RetryBackoff time.Duration
}

func (c OllamaConfig) applyDefaults() OllamaConfig {
	if c.URL == "" {
		c.URL = "http://localhost:11434/api/embeddings"
	}
	if c.Model == "" {
		c.Model = "qwen3-embedding:0.6b"
	}
	if c.Timeout <= 0 {
		c.Timeout = defaultOllamaTimeout
	}
	if c.RetryCount <= 0 {
		c.RetryCount = defaultOllamaRetries
	}
	if c.RetryBackoff <= 0 {
		c.RetryBackoff = defaultOllamaBackoff
	}
	return c
}

// NewEmbeddingsGeneratorWithConfig builds an EmbeddingsGenerator pre-configured
// with the given Ollama endpoint and model name. It performs the same internal
// initialization as NewEmbeddingsGenerator (HTTP client, LRU cache, mutex) so
// callers can construct one without depending on the unexported fields.
func NewEmbeddingsGeneratorWithConfig(ollamaURL, model string) *EmbeddingsGenerator {
	eg := newEmbeddingsGenerator()
	eg.OllamaURL = ollamaURL
	eg.Model = model
	eg.rebuildOllamaClient()
	return eg
}

// NewEmbeddingsGeneratorWithOllamaConfig builds an EmbeddingsGenerator
// from a fully-populated OllamaConfig. Zero values fall back to package
// defaults; negative retry counts are clamped to zero.
func NewEmbeddingsGeneratorWithOllamaConfig(cfg OllamaConfig) *EmbeddingsGenerator {
	cfg = cfg.applyDefaults()
	eg := newEmbeddingsGenerator()
	eg.OllamaURL = cfg.URL
	eg.Model = cfg.Model
	eg.configDim = cfg.Dim
	eg.Timeout = cfg.Timeout
	eg.RetryCount = cfg.RetryCount
	eg.RetryBackoff = cfg.RetryBackoff
	eg.rebuildOllamaClient()
	return eg
}

// GenerateVector produces a 768-dimensional normalized embedding vector.
// Uses an LRU in-memory cache to avoid recomputing embeddings for repeated text.
// Queries Ollama first with the configured retry/backoff, falling back to
// local deterministic hashing if offline.
func (eg *EmbeddingsGenerator) GenerateVector(text string) []float32 {
	return eg.generateVectorImpl(text, eg.RetryCount).Vector
}

// GenerateVectorNoRetry produces a 768-dimensional embedding vector without
// retrying on Ollama failures. If Ollama is unreachable or returns an error,
// the local hash fallback is used immediately. Designed for interactive search
// paths where latency matters more than embedding quality (issue #162).
func (eg *EmbeddingsGenerator) GenerateVectorNoRetry(text string) []float32 {
	return eg.generateVectorImpl(text, 0).Vector
}

// GenerateVectorNoRetryWithModel is the provenance-aware variant of
// GenerateVectorNoRetry. It returns the actual model used for the query vector.
func (eg *EmbeddingsGenerator) GenerateVectorNoRetryWithModel(text string) EmbeddingResult {
	return eg.generateVectorImpl(text, 0)
}

func (eg *EmbeddingsGenerator) generateVectorImpl(text string, maxRetries int) EmbeddingResult {
	key := hashKey(text)

	eg.cacheMu.Lock()
	if elem, ok := eg.cache[key]; ok {
		eg.cacheOrder.MoveToFront(elem)
		entry := elem.Value.(*cacheEntry)
		eg.cacheMu.Unlock()
		return EmbeddingResult{Vector: entry.value, Model: entry.model}
	}
	eg.cacheMu.Unlock()

	hasKnownDim := eg.configDim > 0 || eg.dim > 0
	expectedDim := eg.effectiveDim()

	vec, err := eg.queryOllamaWithRetries(text, maxRetries)
	if err == nil {
		if !hasKnownDim {
			eg.cacheDimOnce(len(vec))
			eg.cachePut(key, vec, eg.Model)
			return EmbeddingResult{Vector: vec, Model: eg.Model}
		}
		eg.cacheDimOnce(len(vec))
		if len(vec) == expectedDim {
			eg.cachePut(key, vec, eg.Model)
			return EmbeddingResult{Vector: vec, Model: eg.Model}
		}
		fmt.Fprintf(os.Stderr, "engine: embedding dimension mismatch: expected %d, got %d; falling back to local hash vector\n", expectedDim, len(vec))
	}

	fallback := GenerateLocalHashVector(text, expectedDim)
	eg.cachePut(key, fallback, localHashModelName)
	return EmbeddingResult{Vector: fallback, Model: localHashModelName}
}

func (eg *EmbeddingsGenerator) cachePut(key string, value []float32, model string) {
	eg.cacheMu.Lock()
	defer eg.cacheMu.Unlock()

	if _, exists := eg.cache[key]; exists {
		return
	}

	if eg.cacheOrder.Len() >= maxEmbeddingCacheSize {
		oldest := eg.cacheOrder.Back()
		if oldest != nil {
			entry := oldest.Value.(*cacheEntry)
			delete(eg.cache, entry.key)
			_ = eg.cacheOrder.Remove(oldest)
		}
	}

	elem := eg.cacheOrder.PushFront(&cacheEntry{key: key, value: value, model: model})
	eg.cache[key] = elem
}

func (eg *EmbeddingsGenerator) cacheDimOnce(actualDim int) {
	if actualDim <= 0 {
		return
	}
	eg.dimOnce.Do(func() {
		eg.dim = actualDim
	})
}

// GenerateVectors produces embeddings for a batch of texts.
// Sends them to Ollama in a single HTTP request when possible, falling back
// to individual queries and local hashing per text.
// Returns a slice with one embedding per input text, in the same order.
func (eg *EmbeddingsGenerator) GenerateVectors(texts []string) [][]float32 {
	results := eg.generateVectorsImpl(texts)
	vectors := make([][]float32, len(results))
	for i, r := range results {
		vectors[i] = r.Vector
	}
	return vectors
}

// GenerateVectorsWithModel returns embeddings together with the actual model
// that produced each vector. Fallback vectors are tagged with localHashModelName.
func (eg *EmbeddingsGenerator) GenerateVectorsWithModel(texts []string) []EmbeddingResult {
	return eg.generateVectorsImpl(texts)
}

func (eg *EmbeddingsGenerator) generateVectorsImpl(texts []string) []EmbeddingResult {
	if len(texts) == 0 {
		return nil
	}

	hasKnownDim := eg.configDim > 0 || eg.dim > 0
	expectedDim := eg.effectiveDim()
	results := make([]EmbeddingResult, len(texts))

	// Collect uncached texts and their indexes
	type uncached struct {
		idx  int
		text string
	}
	var uncachedList []uncached

	eg.cacheMu.Lock()
	for i, t := range texts {
		key := hashKey(t)
		if elem, ok := eg.cache[key]; ok {
			eg.cacheOrder.MoveToFront(elem)
			entry := elem.Value.(*cacheEntry)
			results[i] = EmbeddingResult{Vector: entry.value, Model: entry.model}
		} else {
			uncachedList = append(uncachedList, uncached{idx: i, text: t})
		}
	}
	eg.cacheMu.Unlock()

	if len(uncachedList) == 0 {
		return results
	}

	// Build uncached text slice for batch query
	uncachedTexts := make([]string, len(uncachedList))
	for i, u := range uncachedList {
		uncachedTexts[i] = u.text
	}

	var batchVectors [][]float32
	var batchErr error
	// Try batch Ollama query, deduplicated via singleflight so concurrent
	// goroutines requesting the same uncached texts make only one HTTP call.
	{
		batchKey := hashKey(strings.Join(uncachedTexts, "\x00"))
		res, sfErr, _ := eg.sf.Do(batchKey, func() (interface{}, error) {
			return eg.queryOllamaBatch(uncachedTexts)
		})
		if sfErr == nil && res != nil {
			batchVectors = res.([][]float32)
		}
		batchErr = sfErr
	}
	if batchErr == nil && len(batchVectors) == len(uncachedList) {
		for i, u := range uncachedList {
			vec := batchVectors[i]
			key := hashKey(u.text)
			if !hasKnownDim && i == 0 {
				eg.cacheDimOnce(len(vec))
				expectedDim = eg.effectiveDim()
			} else {
				eg.cacheDimOnce(len(vec))
			}
			if len(vec) != expectedDim {
				fmt.Fprintf(os.Stderr, "engine: batch embedding dimension mismatch for text %q: expected %d, got %d; using local hash fallback\n", u.text, expectedDim, len(vec))
				results[u.idx] = EmbeddingResult{
					Vector: GenerateLocalHashVector(u.text, expectedDim),
					Model:  localHashModelName,
				}
			} else {
				results[u.idx] = EmbeddingResult{Vector: vec, Model: eg.Model}
			}
			eg.cachePut(key, results[u.idx].Vector, results[u.idx].Model)
		}
		return results
	}

	// Fall back to individual queries with caching
	if batchErr != nil {
		fmt.Fprintf(os.Stderr, "engine: batch embedding failed (%v), falling back to per-text requests\n", batchErr)
	}
	for _, u := range uncachedList {
		results[u.idx] = eg.generateVectorImpl(u.text, eg.RetryCount)
	}
	return results
}

// queryOllama sends a single embedding request to Ollama with the
// configured retry count.
func (eg *EmbeddingsGenerator) queryOllama(text string) ([]float32, error) {
	return eg.queryOllamaWithRetries(text, eg.RetryCount)
}

// queryOllamaWithRetries sends a single embedding request to Ollama,
// retrying up to maxRetries times on transient failures.
func (eg *EmbeddingsGenerator) queryOllamaWithRetries(text string, maxRetries int) ([]float32, error) {
	vecs, err := eg.embedWithRetries([]string{text}, maxRetries)
	if err != nil {
		return nil, err
	}
	return vecs[0], nil
}

// queryOllamaBatch sends a batch embedding request to Ollama.
func (eg *EmbeddingsGenerator) queryOllamaBatch(texts []string) ([][]float32, error) {
	return eg.embedWithRetries(texts, eg.RetryCount)
}

// embedRequestWithDim mirrors ollamakit's embed request but additionally pins
// the output dimension (Matryoshka truncation) via Ollama's `dimensions`
// body parameter. ollamakit (corekit v0.8.0) does not expose that parameter
// yet; once it does, this local transport can be removed.
type embedRequestWithDim struct {
	Model      string   `json:"model"`
	Input      []string `json:"input"`
	Dimensions int      `json:"dimensions,omitempty"`
}

// embedResponse is the Ollama /api/embed response body.
type embedResponse struct {
	Embeddings [][]float32 `json:"embeddings"`
}

// embedWithDim posts to Ollama's /api/embed with a pinned output dimension.
// Errors are wrapped in the same shapes ollamakit produces (ErrUnreachable /
// ResponseError) so the shared transient-error classification keeps working.
func (eg *EmbeddingsGenerator) embedWithDim(ctx context.Context, texts []string, dim int) ([][]float32, error) {
	u := strings.TrimSuffix(ollamaBaseURL(eg.OllamaURL), "/") + "/api/embed"
	body, err := json.Marshal(embedRequestWithDim{Model: eg.Model, Input: texts, Dimensions: dim})
	if err != nil {
		return nil, fmt.Errorf("engine: encode embed request: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("engine: build embed request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := &http.Client{Timeout: eg.Timeout}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("ollamakit: %w: %v", ollamakit.ErrUnreachable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, ollamakit.ErrModelNotFound
	}
	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return nil, &ollamakit.ResponseError{StatusCode: resp.StatusCode, Body: strings.TrimSpace(string(b))}
	}

	var er embedResponse
	if err := json.NewDecoder(resp.Body).Decode(&er); err != nil {
		return nil, fmt.Errorf("engine: decode embed response: %w", err)
	}
	if len(er.Embeddings) != len(texts) {
		return nil, fmt.Errorf("engine: expected %d embeddings, got %d", len(texts), len(er.Embeddings))
	}
	return er.Embeddings, nil
}

// embedWithRetries is the core transport for Ollama embedding requests via
// ollamakit. It retries up to maxRetries times on unreachable-host or
// unexpected-response errors with exponential backoff; a missing model is
// not transient and returns immediately. A maxRetries of 0 means a single
// attempt with no sleep.
func (eg *EmbeddingsGenerator) embedWithRetries(texts []string, maxRetries int) ([][]float32, error) {
	backoff := eg.RetryBackoff
	if backoff <= 0 {
		backoff = defaultOllamaBackoff
	}
	maxBackoff := defaultOllamaMaxBackoff
	retries := maxRetries
	if retries < 0 {
		retries = 0
	}

	var lastErr error
	for attempt := 0; attempt <= retries; attempt++ {
		if attempt > 0 {
			fmt.Fprintf(os.Stderr, "engine: ollama retry %d/%d after %v (last error: %v)\n", attempt, retries, backoff, lastErr)
			eg.sleepFn(backoff)
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
		}

		var vecs [][]float32
		var err error
		if eg.configDim > 0 {
			// A configured embedding_dim pins the output dimension
			// (Matryoshka truncation) via the dimensions body parameter.
			vecs, err = eg.embedWithDim(context.Background(), texts, eg.configDim)
		} else {
			vecs, err = eg.ollama.Embed(context.Background(), eg.Model, texts)
		}
		if err == nil {
			return vecs, nil
		}
		if !isTransientOllamaError(err) {
			return nil, err
		}
		lastErr = err
	}
	return nil, fmt.Errorf("ollama: exhausted %d retries: %w", retries, lastErr)
}

// isTransientOllamaError reports whether err is worth retrying: the host
// was unreachable, or Ollama returned a 5xx. A missing model or any other
// non-5xx response (redirect, malformed request, ...) will not succeed on
// retry, so those return immediately.
func isTransientOllamaError(err error) bool {
	if errors.Is(err, ollamakit.ErrUnreachable) {
		return true
	}
	var respErr *ollamakit.ResponseError
	if errors.As(err, &respErr) {
		return respErr.StatusCode >= 500
	}
	return false
}

// GenerateLocalHashVector utilizes the "Hashing Trick" to produce a normalized
// 768-dimensional float32 vector based on word hashes.
func GenerateLocalHashVector(text string, dimensions int) []float32 {
	vec := make([]float32, dimensions)

	// Clean and normalize text
	cleaned := strings.ToLower(text)
	for _, char := range []string{".", ",", "!", "?", ";", ":", "-", "_", "(", ")", "[", "]", "{", "}"} {
		cleaned = strings.ReplaceAll(cleaned, char, " ")
	}

	words := strings.Fields(cleaned)
	if len(words) == 0 {
		// Non-empty fallback vector to prevent division by zero
		vec[0] = 1.0
		return vec
	}

	// Calculate a secondary hash signature for the entire block to seed weights
	textHash := sha256.Sum256([]byte(text))

	for i, word := range words {
		if isStopWord(word) {
			continue
		}

		h := fnv.New32a()
		h.Write([]byte(word))
		hashVal := h.Sum32()

		idx := int(hashVal) % dimensions

		// Use a combination of word hash and context sequence weight
		weight := float32(1.0)
		if i < len(textHash) {
			weight += float32(textHash[i]) / 255.0
		}
		vec[idx] += weight
	}

	// Normalize vector (L2 norm) so cosine similarity behave correctly
	var sumSquares float64
	for _, val := range vec {
		sumSquares += float64(val * val)
	}

	if sumSquares > 0 {
		norm := float32(math.Sqrt(sumSquares))
		for i := range vec {
			vec[i] /= norm
		}
	} else {
		vec[0] = 1.0
	}

	return vec
}

// stopWords is the multilingual stop-word set consulted by
// isStopWord. It is intentionally read-only after package init so
// the lookup is a single hash-map probe with no per-call allocation
// (issue #47).
var stopWords = map[string]struct{}{
	"and": {}, "the": {}, "a": {}, "an": {}, "of": {}, "to": {}, "in": {}, "is": {}, "it": {}, "that": {},
	"und": {}, "der": {}, "die": {}, "das": {}, "ein": {}, "eine": {}, "ist": {}, "es": {}, "dass": {},
	"von": {}, "zu": {}, "mit": {}, "auf": {}, "für": {}, "den": {}, "dem": {}, "des": {}, "im": {}, "am": {},
}

func isStopWord(w string) bool {
	_, ok := stopWords[w]
	return ok
}

// hashKey returns a hex-encoded SHA-256 hash of the input text, truncated
// to 32 hex chars (128 bits of entropy). Used as a cache key to avoid
// storing large raw text strings in memory while keeping collision risk
// negligible well past the 10K-entry cache ceiling.
func hashKey(text string) string {
	sum := sha256.Sum256([]byte(text))
	return fmt.Sprintf("%x", sum[:16])
}
