package secrets

import (
	"os"
	"testing"
)

func TestResolveKey(t *testing.T) {
	// Set an env var
	os.Setenv("SYMDESK_LLM_API_KEY", "test-env-key")
	defer os.Unsetenv("SYMDESK_LLM_API_KEY")

	// Empty ref should fallback to env var
	key := ResolveKey("")
	if key != "test-env-key" {
		t.Errorf("Expected test-env-key, got %s", key)
	}

	// Raw string should just return the string
	key = ResolveKey("raw-key")
	if key != "raw-key" {
		t.Errorf("Expected raw-key, got %s", key)
	}
}

func TestSource(t *testing.T) {
	os.Setenv("SYMDESK_LLM_API_KEY", "test-env-key")
	defer os.Unsetenv("SYMDESK_LLM_API_KEY")

	src := Source("")
	if src != "config/env" {
		t.Errorf("Expected config/env, got %s", src)
	}

	src = Source("op://vault/item/key")
	if src != "symvault" {
		t.Errorf("Expected symvault, got %s", src)
	}
}
