package api

import (
	"testing"

	"github.com/danieljustus/symaira-seek/internal/engine"
)

// BackendAvailable is the difference between "search works" and "search
// silently ranks worse", so the fallback model name must never be reported as
// an available backend.
func TestLocalHashModelIsNotAnAvailableBackend(t *testing.T) {
	if engine.LocalHashModelName == "" {
		t.Fatal("LocalHashModelName is empty; the availability check would treat every model as a fallback")
	}
	if engine.LocalHashModelName == "qwen3-embedding:0.6b" {
		t.Fatal("the fallback model name collides with a real embedding model")
	}
}
