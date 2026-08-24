package vault

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAssetsFolderName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"", "assets"},
		{"   ", "assets"},
		{"assets", "assets"},
		{"attachments", "attachments"},
		{"custom/media", "custom/media"},
		{"media/", "media"},
		{"/absolute/path", "assets"},
		{"../traversal", "assets"},
		{"media/../escape", "assets"},
		{"/", "assets"},
		{"///", "assets"},
	}

	for _, tt := range tests {
		got := AssetsFolderName(tt.input)
		if got != tt.expected {
			t.Errorf("AssetsFolderName(%q) = %q; want %q", tt.input, got, tt.expected)
		}
	}
}

func TestSanitizeAssetName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"photo", "photo"},
		{"my photo 2026", "my photo 2026"},
		{"path/to\\file:name\nnewline\r\x00", "path-to-file-name-newline--"},
		{"", "pasted-image"},
		{"   ", "pasted-image"},
		{"  trimmed  ", "trimmed"},
	}

	for _, tt := range tests {
		got := SanitizeAssetName(tt.input)
		if got != tt.expected {
			t.Errorf("SanitizeAssetName(%q) = %q; want %q", tt.input, got, tt.expected)
		}
	}
}

func TestCollisionSafeAssetName(t *testing.T) {
	existing := map[string]bool{
		"diagram.png":   true,
		"diagram-2.png": true,
	}
	exists := func(candidate string) bool {
		return existing[candidate]
	}

	got := CollisionSafeAssetName("diagram", "png", exists)
	if got != "diagram-3.png" {
		t.Errorf("expected diagram-3.png, got %q", got)
	}

	gotNew := CollisionSafeAssetName("other", "png", exists)
	if gotNew != "other.png" {
		t.Errorf("expected other.png, got %q", gotNew)
	}

	gotExtDot := CollisionSafeAssetName("other", ".pdf", exists)
	if gotExtDot != "other.pdf" {
		t.Errorf("expected other.pdf, got %q", gotExtDot)
	}
}

func TestStoreAsset(t *testing.T) {
	vaultRoot := t.TempDir()
	data := []byte("binary-image-data-here")
	fixedTime := time.Date(2026, 8, 24, 14, 30, 0, 0, time.UTC)

	// 1. Store with preferred name
	rel1, err := StoreAsset(vaultRoot, data, "receipt.png", "png", "assets", fixedTime)
	if err != nil {
		t.Fatalf("StoreAsset 1 failed: %v", err)
	}
	if rel1 != "assets/receipt.png" {
		t.Errorf("expected assets/receipt.png, got %q", rel1)
	}

	content1, err := os.ReadFile(filepath.Join(vaultRoot, filepath.FromSlash(rel1)))
	if err != nil {
		t.Fatalf("failed to read stored asset: %v", err)
	}
	if string(content1) != string(data) {
		t.Errorf("stored content mismatch: got %q, want %q", string(content1), string(data))
	}

	// 2. Collision safe suffix
	rel2, err := StoreAsset(vaultRoot, data, "receipt.png", "png", "assets", fixedTime)
	if err != nil {
		t.Fatalf("StoreAsset 2 failed: %v", err)
	}
	if rel2 != "assets/receipt-2.png" {
		t.Errorf("expected assets/receipt-2.png, got %q", rel2)
	}

	// 3. Fallback timestamp name
	rel3, err := StoreAsset(vaultRoot, data, "", "png", "assets", fixedTime)
	if err != nil {
		t.Fatalf("StoreAsset 3 failed: %v", err)
	}
	if rel3 != "assets/pasted-2026-08-24-143000.png" {
		t.Errorf("expected assets/pasted-2026-08-24-143000.png, got %q", rel3)
	}

	// 4. Traversal protection in folder parameter
	rel4, err := StoreAsset(vaultRoot, data, "safe.png", "png", "../../../escape", fixedTime)
	if err != nil {
		t.Fatalf("StoreAsset 4 failed: %v", err)
	}
	if rel4 != "assets/safe.png" {
		t.Errorf("expected traversal to fallback to assets/safe.png, got %q", rel4)
	}
}

func TestAssetMarkdownLink(t *testing.T) {
	link1 := AssetMarkdownLink("assets/scan.png")
	if link1 != "![scan.png](assets/scan.png)" {
		t.Errorf("expected ![scan.png](assets/scan.png), got %q", link1)
	}

	link2 := AssetMarkdownLink("assets/my receipt 2026.png")
	if link2 != "![my receipt 2026.png](assets/my%20receipt%202026.png)" {
		t.Errorf("expected ![my receipt 2026.png](assets/my%%20receipt%%202026.png), got %q", link2)
	}
}
