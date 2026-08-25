package annotate

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/danieljustus/symaira-corekit/fsutil"
)

// SidecarDirName is the directory name under the vault for extraction sidecars.
const SidecarDirName = ".symaira/extractions"

// SidecarPath returns the vault-relative path for a document's extraction sidecar.
func SidecarPath(vault, docSHA256 string) string {
	return filepath.Join(vault, SidecarDirName, docSHA256+".jsonl")
}

// WriteSidecar writes extractions as JSONL lines to the sidecar file atomically.
// The directory is created with 0700 and files are written with 0600.
func WriteSidecar(vault, docSHA256 string, extractions []Extraction) error {
	dir := filepath.Join(vault, SidecarDirName)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create sidecar directory: %w", err)
	}

	path := filepath.Join(dir, docSHA256+".jsonl")

	var buf bytes.Buffer
	for _, e := range extractions {
		line, err := json.Marshal(e)
		if err != nil {
			return fmt.Errorf("marshal extraction: %w", err)
		}
		line = append(line, '\n')
		if _, err := buf.Write(line); err != nil {
			return fmt.Errorf("write extraction: %w", err)
		}
	}

	if err := fsutil.AtomicWriteFile(path, buf.Bytes(), 0o600); err != nil {
		return fmt.Errorf("write sidecar: %w", err)
	}

	return nil
}

// ComputeSHA256 returns the hex-encoded SHA-256 of data.
func ComputeSHA256(data []byte) string {
	h := sha256.Sum256(data)
	return fmt.Sprintf("%x", h)
}
