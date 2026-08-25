package desk

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"
)

func TestSymdeskAbsentGracefulDegradation(t *testing.T) {
	// Isolate PATH so symdesk is not present
	t.Setenv("PATH", t.TempDir())

	if IsAvailable() {
		t.Errorf("expected IsAvailable() to be false when PATH is empty")
	}

	_, err := InspectPath(context.Background(), "some/file.txt")
	if err != ErrSymdeskNotFound {
		t.Errorf("expected ErrSymdeskNotFound, got %v", err)
	}
}

func TestSymdeskFakeExecutable(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping bash fake exec test on windows")
	}

	tempDir := t.TempDir()
	fakeSymdesk := filepath.Join(tempDir, "symdesk")

	script := `#!/bin/sh
if [ "$1" = "inspect" ]; then
    echo '{"document_id":"doc_12345","vault_name":"my_vault","valid":true}'
    exit 0
fi
exit 1
`
	if err := os.WriteFile(fakeSymdesk, []byte(script), 0755); err != nil {
		t.Fatalf("failed to write fake symdesk script: %v", err)
	}

	t.Setenv("PATH", tempDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	if !IsAvailable() {
		t.Fatalf("expected fake symdesk to be detected")
	}

	res, err := InspectPath(context.Background(), "/path/to/doc")
	if err != nil {
		t.Fatalf("InspectPath with fake executable failed: %v", err)
	}
	if res.DocumentID != "doc_12345" || res.VaultName != "my_vault" {
		t.Errorf("unexpected inspect result: %+v", res)
	}
}

func TestSymdeskTimeout(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("skipping bash fake exec test on windows")
	}

	tempDir := t.TempDir()
	fakeSymdesk := filepath.Join(tempDir, "symdesk")

	script := `#!/bin/sh
sleep 1.2
echo '{"document_id":"slow"}'
exit 0
`
	if err := os.WriteFile(fakeSymdesk, []byte(script), 0755); err != nil {
		t.Fatalf("failed to write fake slow symdesk script: %v", err)
	}

	t.Setenv("PATH", tempDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	_, err := InspectPath(ctx, "/path/to/doc")
	if err == nil {
		t.Fatalf("expected timeout error, got nil")
	}
}
