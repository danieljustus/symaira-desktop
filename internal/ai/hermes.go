package ai

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// hermesBinary is the Hermes CLI used by the optional hermes provider. It is
// resolved at call time so the feature degrades honestly when Hermes is not
// installed (issue #559).
const hermesBinary = "hermes"

// hermesTimeout bounds a single one-shot Hermes call. Hermes answers appear
// without token streaming in Stage 1, so a generous but finite bound keeps
// the vault shell responsive.
const hermesTimeout = 5 * time.Minute

// streamHermes streams a one-shot answer from the user's Hermes session
// (issue #559). Stage 1 uses `hermes -z "<prompt>" --resume <session>`: the
// prompt is answered by the user's existing agent session instead of a
// second, isolated agent loop. The response is delivered as a single chunk
// because the CLI contract is one final response, no streaming events.
//
// Hermes becomes an optional backend: when the binary is missing the caller
// degrades to the built-in path (streamLLM returns ErrNotConfigured).
func streamHermes(ctx context.Context, cfg HermesConfig, prompt string, out chan<- AskChunk) error {
	if _, err := exec.LookPath(hermesBinary); err != nil {
		return ErrNotConfigured
	}

	args := []string{"-z", prompt}
	if cfg.Session != "" {
		args = append(args, "--resume", cfg.Session)
	}

	cmdCtx, cancel := context.WithTimeout(ctx, hermesTimeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, hermesBinary, args...) //nolint:gosec // G204: hermesBinary is a fixed constant resolved from PATH, args are the caller's own prompt
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		if cmdCtx.Err() != nil {
			return fmt.Errorf("hermes backend timed out after %s", hermesTimeout)
		}
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("hermes backend failed: %s", msg)
	}

	answer := strings.TrimSpace(stdout.String())
	if answer == "" {
		return fmt.Errorf("hermes backend returned an empty answer")
	}

	sendChunk(ctx, out, AskChunk{Chunk: answer})
	return nil
}

// HermesConfig carries the hermes-provider settings used by streamHermes.
type HermesConfig struct {
	// Session is the Hermes session id or title to resume. Empty lets Hermes
	// pick the most recent session.
	Session string
}
