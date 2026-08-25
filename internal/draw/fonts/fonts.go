// Package fonts embeds the Inter brand font files (Regular and Bold) used for
// exact text measurement, glyph outline extraction, and raster rendering.
package fonts

import (
	"crypto/sha256"
	"embed"
	"encoding/hex"
	"fmt"
	"io/fs"
	"sync"
)

//go:embed *.ttf
var fontFS embed.FS

var (
	versionOnce sync.Once
	cachedKey   string
	cachedErr   error
)

// FS returns the embedded filesystem containing the font files.
func FS() embed.FS {
	return fontFS
}

// Regular returns the raw bytes of Inter-Regular.ttf.
func Regular() []byte {
	b, err := fontFS.ReadFile("Inter-Regular.ttf")
	if err != nil {
		panic(fmt.Sprintf("embedded Inter-Regular.ttf missing: %v", err))
	}
	return b
}

// Bold returns the raw bytes of Inter-Bold.ttf.
func Bold() []byte {
	b, err := fontFS.ReadFile("Inter-Bold.ttf")
	if err != nil {
		panic(fmt.Sprintf("embedded Inter-Bold.ttf missing: %v", err))
	}
	return b
}

// VersionKey returns a deterministic SHA-256 content hash over all embedded font files,
// covering file names and file contents in sorted order.
func VersionKey() string {
	versionOnce.Do(func() {
		h := sha256.New()
		err := fs.WalkDir(fontFS, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				return nil
			}
			b, err := fontFS.ReadFile(p)
			if err != nil {
				return err
			}
			h.Write([]byte(p + "\x00"))
			h.Write(b)
			return nil
		})
		if err != nil {
			cachedErr = fmt.Errorf("hash embedded fonts: %w", err)
			return
		}
		cachedKey = hex.EncodeToString(h.Sum(nil))
	})
	if cachedErr != nil {
		panic(cachedErr)
	}
	return cachedKey
}
