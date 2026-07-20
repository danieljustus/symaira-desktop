package secrets

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/danieljustus/symaira-desktop/internal/compose"
)

// ResolveKey attempts to resolve an LLM API key.
// 1. Checks if symvault is available and key string looks like a symvault reference (e.g. op://).
// 2. Returns env var SYMDESK_LLM_API_KEY if present.
// 3. Fallback to macOS Keychain if nothing else is found and we are on macOS.
func ResolveKey(ref string) string {
	if ref == "" {
		ref = os.Getenv("SYMDESK_LLM_API_KEY")
	}

	if ref == "" {
		// Fallback to Keychain on macOS
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if b, err := exec.CommandContext(ctx, "security", "find-generic-password", "-s", "symaira-desktop", "-a", "llm-api-key", "-w").Output(); err == nil {
			return strings.TrimSpace(string(b))
		}
		return ""
	}

	if strings.HasPrefix(ref, "op://") { // supports 1Password references via symvault
		ok, _ := compose.HasTool("symvault")
		if !ok {
			// symvault is required to resolve this reference; never fall through
			// to returning the raw op:// string as if it were the actual key.
			return ""
		}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, "symvault", "get", ref)
		var out bytes.Buffer
		cmd.Stdout = &out
		if err := cmd.Run(); err == nil {
			return strings.TrimSpace(out.String())
		}
		// If symvault is on PATH but fails, we should not leak the reference, but we can't return the resolved key either.
		return ""
	}

	// If it's a raw key (not op://), we just return it.
	return ref
}

// Source returns a human-readable string of where the key was found.
func Source(ref string) string {
	if ref == "" {
		ref = os.Getenv("SYMDESK_LLM_API_KEY")
	}
	if ref == "" {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, err := exec.CommandContext(ctx, "security", "find-generic-password", "-s", "symaira-desktop", "-a", "llm-api-key", "-w").Output(); err == nil {
			return "keychain"
		}
		return "none"
	}
	if strings.HasPrefix(ref, "op://") {
		if ok, _ := compose.HasTool("symvault"); ok {
			return "symvault"
		}
		return "symvault (missing)"
	}
	return "config/env"
}
