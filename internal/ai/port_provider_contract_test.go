package ai

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/config"
	"github.com/danieljustus/symaira-desktop/scripts/rust-port/fixtureoracle"
)

type portProviderRequest struct {
	Method  string              `json:"method"`
	Path    string              `json:"path"`
	Headers map[string][]string `json:"headers"`
	Body    string              `json:"body"`
}
type portProviderCase struct {
	ID                        string                `json:"id"`
	Provider                  string                `json:"provider"`
	URL                       string                `json:"url"`
	EnvURL                    string                `json:"env_url"`
	Model                     string                `json:"model"`
	EnvModel                  string                `json:"env_model"`
	MaxTokens                 int                   `json:"max_tokens"`
	Prompt                    string                `json:"prompt"`
	Status                    int                   `json:"status"`
	Response                  string                `json:"response"`
	Chunks                    []string              `json:"chunks"`
	Error                     string                `json:"error"`
	GoErrorContainsCredential bool                  `json:"go_error_contains_credential"`
	OneShot                   string                `json:"one_shot"`
	OneShotError              string                `json:"one_shot_error"`
	Requests                  []portProviderRequest `json:"requests"`
}

// TestPortAIProviderContract records production streamLLM against loopback-only
// servers. Credentials are injected synthetic canaries, never ambient secrets.
// Transport defaults (Host, Content-Length, Accept-Encoding, User-Agent) are
// omitted: they belong to net/http rather than the provider request contract.
// API-key header presence is retained, but its value is always [REDACTED].
func TestPortAIProviderContract(t *testing.T) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	commit := fixtureoracle.Current().Commit
	hashes := map[string]string{}
	for _, name := range []string{"internal/ai/ai.go", "internal/ai/anthropic.go"} {
		//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
		data, err := os.ReadFile(filepath.Join(repo, name))
		if err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(data)
		hashes[name] = hex.EncodeToString(sum[:])
	}
	// All keys consulted by streamLLM/llmkit are fixed before requests. Neither
	// secret resolution fallback nor the real keychain is reached by these cases.
	for _, key := range []string{"SYMDESK_OLLAMA_URL", "SYMDESK_OLLAMA_MODEL", "SYMDESK_ANTHROPIC_URL", "ANTHROPIC_API_KEY", "OLLAMA_API_KEY", "HTTP_PROXY", "HTTPS_PROXY", "ALL_PROXY"} {
		t.Setenv(key, "")
	}
	t.Setenv("NO_PROXY", "*")
	const canary = "port-only-synthetic-provider-canary"
	success := "{\"response\":\" first \"}\n{\"response\":\"\"}\n{\"response\":\"second<&>é\"}\n{\"done\":true}\n"
	sse := "event: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\"first<&>é\"}}\n\nevent: content_block_delta\ndata: {\"type\":\"content_block_delta\",\"delta\":{\"type\":\"text_delta\",\"text\":\" second \"}}\n\nevent: message_delta\ndata: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"end_turn\"}}\n\nevent: message_stop\ndata: {\"type\":\"message_stop\"}\n\n"
	cases := []portProviderCase{
		{ID: "missing-default"}, {ID: "missing-unknown", Provider: "other"},
		{ID: "ollama-default", URL: "{server}", Response: success},
		{ID: "ollama-native-root", Provider: "ollama", URL: "{server}/v1/models///", Response: success},
		{ID: "unknown-falls-back-to-ollama", Provider: "other", URL: "{server}/nested", Response: success},
		{ID: "ollama-env-url-wins", URL: "http://127.0.0.1:1", EnvURL: "{server}/v1///", Response: success},
		{ID: "ollama-configured-model-ignored", URL: "{server}", Model: "ignored-config-model", Response: success},
		{ID: "ollama-env-model", URL: "{server}", EnvModel: "local-test-model", Response: success},
		{ID: "ollama-empty-stream", URL: "{server}"},
		{ID: "ollama-malformed-chunk", URL: "{server}", Response: "{broken}\n"},
		{ID: "ollama-partial-then-malformed", URL: "{server}", Response: "{\"response\":\"partial\"}\n{broken}\n"},
		{ID: "ollama-error-chunk", URL: "{server}", Response: "{\"error\":\"model missing\"}\n"},
		{ID: "ollama-unauthorized", URL: "{server}", Status: 401, Response: "denied"},
		{ID: "ollama-rate-limit", URL: "{server}", Status: 429, Response: "slow down"},
		{ID: "ollama-server-error", URL: "{server}", Status: 500, Response: "unavailable"},
		{ID: "ollama-null-scalars", URL: "{server}", Response: "null\n{\"model\":null,\"response\":null,\"done\":null}\n"},
		{ID: "ollama-crlf-blank-lines", URL: "{server}", Response: "\r\n{\"response\":\"CRLF\"}\r\n\r\n"},
		{ID: "ollama-final-line-no-newline", URL: "{server}", Response: "{\"response\":\"tail\"}"},
		{ID: "ollama-output-after-done", URL: "{server}", Response: "{\"done\":true}\n{\"response\":\"late\"}\n"},
		{ID: "ollama-forbidden", URL: "{server}", Status: 403, Response: "forbidden"},
		{ID: "ollama-model-not-found", URL: "{server}", Status: 404, Response: "model missing"},
		{ID: "ollama-context-overflow", URL: "{server}", Status: 400, Response: "maximum context length"},
		{ID: "anthropic-default", Provider: "anthropic", Response: sse},
		{ID: "anthropic-custom-model-options", Provider: "anthropic", Model: "synthetic-model", MaxTokens: 321, Response: sse},
		{ID: "anthropic-negative-max-default", Provider: "anthropic", MaxTokens: -1, Response: sse},
		{ID: "anthropic-wide-max-tokens", Provider: "anthropic", MaxTokens: 4294967296, Response: sse},
		{ID: "anthropic-i64-max-tokens", Provider: "anthropic", MaxTokens: 9223372036854775807, Response: sse},
		{ID: "anthropic-truncated", Provider: "anthropic", Response: strings.ReplaceAll(sse, "end_turn", "max_tokens")},
		{ID: "anthropic-empty-stream", Provider: "anthropic"},
		{ID: "anthropic-malformed-event", Provider: "anthropic", Response: "event: content_block_delta\ndata: {broken}\n\n"},
		{ID: "anthropic-unauthorized-redacted", Provider: "anthropic", Status: 401, Response: "denied " + canary},
		{ID: "anthropic-rate-limit", Provider: "anthropic", Status: 429, Response: "slow down"},
		{ID: "anthropic-server-error", Provider: "anthropic", Status: 500, Response: "unavailable"},
	}
	for i := range cases {
		c := &cases[i]
		c.Prompt = "Prompt <&> é\u2028\n"
		c.Chunks = []string{}
		c.Requests = []portProviderRequest{}
		status := c.Status
		if status == 0 {
			status = 200
			c.Status = status
		}
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Error(err)
			}
			headers := map[string][]string{}
			for key, values := range r.Header {
				switch strings.ToLower(key) {
				case "accept-encoding", "user-agent", "content-length":
					continue
				case "x-api-key", "authorization":
					headers[strings.ToLower(key)] = []string{"[REDACTED]"}
				default:
					headers[strings.ToLower(key)] = values
				}
			}
			c.Requests = append(c.Requests, portProviderRequest{r.Method, r.URL.RequestURI(), headers, string(body)})
			w.Header().Set("Content-Type", "text/event-stream")
			w.WriteHeader(status)
			_, _ = w.Write([]byte(c.Response))
		}))
		func() {
			defer srv.Close()
			expand := func(s string) string { return strings.ReplaceAll(s, "{server}", srv.URL) }
			// No case can direct a request anywhere except srv: the invalid config URL
			// above is shadowed by the asserted loopback environment override.
			t.Setenv("SYMDESK_OLLAMA_URL", expand(c.EnvURL))
			t.Setenv("SYMDESK_OLLAMA_MODEL", c.EnvModel)
			t.Setenv("SYMDESK_ANTHROPIC_URL", srv.URL)
			cfg := &config.Config{LLMProvider: c.Provider, OllamaURL: expand(c.URL), LLMModel: c.Model, MaxTokens: c.MaxTokens, LLMAPIKey: canary}
			out := make(chan AskChunk, 32)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			streamErr := streamLLM(ctx, cfg, c.Prompt, out)
			close(out)
			for chunk := range out {
				c.Chunks = append(c.Chunks, chunk.Chunk)
			}
			if streamErr != nil {
				c.Error = streamErr.Error()
			}
			oneShot, oneShotErr := PromptOneReal(cfg, c.Prompt)
			c.OneShot = oneShot
			if oneShotErr != nil {
				c.OneShotError = strings.ReplaceAll(oneShotErr.Error(), canary, "[REDACTED]")
			}
		}()
		c.GoErrorContainsCredential = strings.Contains(c.Error, canary)
		if c.GoErrorContainsCredential && c.ID != "anthropic-unauthorized-redacted" {
			t.Fatalf("%s: unexpected credential leak", c.ID)
		}
		// SEC-002 known Go defect: preserve the measured fact, never publish the
		// credential-bearing diagnostic. Rust must redact this exact failure.
		c.Error = strings.ReplaceAll(c.Error, canary, "[REDACTED]")
	}
	document := struct {
		SchemaVersion int                `json:"schema_version"`
		OracleCommit  string             `json:"oracle_commit"`
		SourceHashes  map[string]string  `json:"source_hashes"`
		Cases         []portProviderCase `json:"cases"`
	}{1, commit, hashes, cases}
	data, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	path := filepath.Join(repo, "testdata/port/ai/providers.json")
	if override := os.Getenv("PORT_FIXTURE_PATH"); override != "" {
		path = override
	}
	if os.Getenv("PORT_GENERATE") == "1" {
		//nolint:gosec // path is the repository-local fixture output selected by this test.
		if err := os.WriteFile(path, data, 0o600); err != nil {
			t.Fatal(err)
		}
	} else {
		//nolint:gosec // path is derived from repository fixtures or the test-owned temporary root, not external input.
		expected, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(data, expected) {
			t.Fatal("AI provider fixture drift; regenerate deliberately")
		}
	}
	t.Logf("executed %d real Go provider cases, loopback only", len(cases))
}
