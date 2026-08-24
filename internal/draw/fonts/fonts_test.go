package fonts

import (
	"testing"
)

func TestEmbeddedFonts(t *testing.T) {
	reg := Regular()
	if len(reg) == 0 {
		t.Fatal("expected non-empty Inter-Regular.ttf")
	}
	bold := Bold()
	if len(bold) == 0 {
		t.Fatal("expected non-empty Inter-Bold.ttf")
	}

	key1 := VersionKey()
	key2 := VersionKey()
	if key1 == "" || key1 != key2 {
		t.Fatalf("expected deterministic non-empty VersionKey, got %q vs %q", key1, key2)
	}
	if len(key1) != 64 {
		t.Fatalf("expected 64-char sha256 hex string, got %d chars: %q", len(key1), key1)
	}
}
