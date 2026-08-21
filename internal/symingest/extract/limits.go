package extract

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
)

// Fixed limits for ZIP-based structured extractors. These are intentionally
// not configurable: they protect the daemon from crafted archives while
// staying far above the decompressed size of legitimate documents. Validate
// against the largest real corpus before changing them.
const (
	// maxZipEntryBytes caps the decompressed size of a single archive part.
	maxZipEntryBytes = 32 << 20 // 32 MiB
	// maxZipTotalBytes caps the total decompressed bytes read from one
	// archive, so multi-part loops (e.g. XLSX sheets) cannot amplify memory.
	maxZipTotalBytes = 128 << 20 // 128 MiB
	// maxZipEntries caps the number of entries a structured archive may
	// contain and is enforced at open, before any decompression.
	maxZipEntries = 4096
)

// ErrArchiveLimit is the typed error returned for every limit violation
// (entry count at open, per-entry size, archive total). The pipeline can
// distinguish it from a parse failure with errors.Is and quarantine the
// document instead of aborting the batch.
var ErrArchiveLimit = errors.New("extract: archive limit exceeded")

// zipArchive is a ZIP reader bound by the fixed limits above. Every part read
// goes through readPart so per-entry and whole-archive budgets are enforced
// in one place.
type zipArchive struct {
	rc    *zip.ReadCloser
	total int64
}

// openZip opens path and rejects archives above the entry-count cap at open,
// before any decompression takes place.
func openZip(path string) (*zipArchive, error) {
	rc, err := zip.OpenReader(path)
	if err != nil {
		return nil, fmt.Errorf("open archive: %w", err)
	}
	a := &zipArchive{rc: rc}
	if len(a.rc.File) > maxZipEntries {
		_ = rc.Close()
		return nil, fmt.Errorf("%w: %d entries exceed the cap of %d", ErrArchiveLimit, len(a.rc.File), maxZipEntries)
	}
	return a, nil
}

// closeZip releases the underlying archive.
func closeZip(a *zipArchive) error {
	if a == nil || a.rc == nil {
		return nil
	}
	return a.rc.Close()
}

// find returns the entry with the exact name, or nil.
func (a *zipArchive) find(name string) *zip.File {
	for _, f := range a.rc.File {
		if f.Name == name {
			return f
		}
	}
	return nil
}

// readPart reads one entry through the per-entry byte cap, charging its
// decompressed size against the shared archive total. A missing part returns
// (nil, nil) so callers can distinguish absence from a limit violation.
func (a *zipArchive) readPart(name string) ([]byte, error) {
	f := a.find(name)
	if f == nil {
		return nil, nil
	}
	rc, err := f.Open()
	if err != nil {
		return nil, fmt.Errorf("open part %q: %w", name, err)
	}
	defer rc.Close()
	// Read one byte past the cap so an overrun errors instead of truncating.
	data, err := io.ReadAll(io.LimitReader(rc, maxZipEntryBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read part %q: %w", name, err)
	}
	if int64(len(data)) > maxZipEntryBytes {
		return nil, fmt.Errorf("%w: part %q exceeds the per-entry cap of %d bytes", ErrArchiveLimit, name, maxZipEntryBytes)
	}
	a.total += int64(len(data))
	if a.total > maxZipTotalBytes {
		return nil, fmt.Errorf("%w: archive total exceeds the cap of %d bytes", ErrArchiveLimit, maxZipTotalBytes)
	}
	return data, nil
}
