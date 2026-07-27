package paperless

import (
	"encoding/json"
	"fmt"
	"os"
)

// ManifestEntry represents a single document from a Paperless-ngx export manifest.
type ManifestEntry struct {
	ID                  int            `json:"id"`
	Correspondent       *string        `json:"correspondent"`
	DocumentType        *string        `json:"document_type"`
	Title               string         `json:"title"`
	Content             string         `json:"content"`
	Tags                []string       `json:"tags"`
	Created             *string        `json:"created"`
	Added               *string        `json:"added"`
	Modified            *string        `json:"modified"`
	FileSize            *int64         `json:"file_size"`
	Checksum            *string        `json:"checksum"`
	ArchiveSerialNumber *int           `json:"archive_serial_number"`
	OriginalFileName    *string        `json:"original_file_name"`
	ArchivedFileName    *string        `json:"archived_file_name"`
	Notes               []ManifestNote `json:"notes"`
	Extra               map[string]any `json:"-"`
}

// ManifestNote represents a note attached to a document in the export.
type ManifestNote struct {
	Note    string  `json:"note"`
	Created *string `json:"created"`
	User    *int    `json:"user"`
}

// ParseManifest reads and parses the manifest.json file from a Paperless-ngx
// export directory. The file must be a JSON array of document entries.
func ParseManifest(path string) ([]ManifestEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read manifest file: %w", err)
	}

	var entries []ManifestEntry
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse manifest: %w", err)
	}

	// Also parse extra fields we didn't explicitly define
	var raw []map[string]any
	if err := json.Unmarshal(data, &raw); err == nil {
		for i, e := range entries {
			if i < len(raw) {
				for k, v := range raw[i] {
					if !isKnownField(k) {
						if e.Extra == nil {
							e.Extra = make(map[string]any)
						}
						e.Extra[k] = v
						entries[i] = e
					}
				}
			}
		}
	}

	return entries, nil
}

// IsManifestFile returns true if the named file is likely a Paperless manifest.
func IsManifestFile(name string) bool {
	return name == "manifest.json"
}

// knownManifestFields lists the fields we explicitly handle.
var knownManifestFields = map[string]bool{
	"id":                    true,
	"correspondent":         true,
	"document_type":         true,
	"title":                 true,
	"content":               true,
	"tags":                  true,
	"created":               true,
	"added":                 true,
	"modified":              true,
	"file_size":             true,
	"checksum":              true,
	"archive_serial_number": true,
	"original_file_name":    true,
	"archived_file_name":    true,
	"notes":                 true,
}

func isKnownField(k string) bool {
	return knownManifestFields[k]
}
