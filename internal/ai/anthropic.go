package ai

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/danieljustus/symaira-corekit/llmkit"
	"github.com/danieljustus/symaira-desktop/internal/config"
)

// streamAnthropic streams an Anthropic-wire chat completion through the
// shared llmkit transport (issue #617). The SYMDESK_ANTHROPIC_URL env
// override keeps working as a base-URL override for self-hosted gateways;
// the credential is either injected directly (secrets.ResolveKey result)
// or resolved by llmkit from ANTHROPIC_API_KEY.
func streamAnthropic(ctx context.Context, cfg *config.Config, apiKey, model, prompt string, out chan<- AskChunk) error {
	desc, ok := llmkit.Lookup("anthropic")
	if !ok {
		return fmt.Errorf("anthropic descriptor missing from embedded registry")
	}
	if model == "" {
		model = config.DefaultAnthropicModel
	}
	maxTokens := cfg.MaxTokens
	if maxTokens <= 0 {
		maxTokens = 8192
	}

	opts := []llmkit.Option{llmkit.WithTimeout(5 * time.Minute)}
	if apiKey != "" {
		opts = append(opts, llmkit.WithAPIKey(apiKey))
	}
	if apiURL := os.Getenv("SYMDESK_ANTHROPIC_URL"); apiURL != "" {
		opts = append(opts, llmkit.WithBaseURL(apiURL))
	}
	cl, err := llmkit.NewClient(desc, "", opts...)
	if err != nil {
		return err
	}

	err = cl.StreamChat(ctx, model, []llmkit.Message{{Role: "user", Content: prompt}},
		&llmkit.ChatOptions{MaxTokens: maxTokens},
		func(delta string) error {
			sendChunk(ctx, out, AskChunk{Chunk: delta})
			return nil
		},
		llmkit.WithStreamFinished(func(reason string) {
			if reason == "max_tokens" {
				sendChunk(ctx, out, AskChunk{Chunk: "\n\n⚠️ **[Output truncated due to token limit]**"})
			}
		}))
	if err != nil {
		return fmt.Errorf("anthropic: %w", err)
	}
	return nil
}
