package paperless

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// flexDateLayouts lists the date/time formats Paperless-ngx has been
// observed to emit for the same logical field, depending on endpoint and
// version: a full RFC3339 timestamp or a bare YYYY-MM-DD date.
var flexDateLayouts = []string{
	time.RFC3339,
	time.RFC3339Nano,
	"2006-01-02",
}

// FlexDate decodes a Paperless date/time field that may arrive as either a
// full timestamp or a date-only string.
type FlexDate struct {
	time.Time
}

func (d *FlexDate) UnmarshalJSON(data []byte) error {
	s := strings.Trim(string(data), `"`)
	if s == "" || s == "null" {
		d.Time = time.Time{}
		return nil
	}
	var lastErr error
	for _, layout := range flexDateLayouts {
		t, err := time.Parse(layout, s)
		if err == nil {
			d.Time = t
			return nil
		}
		lastErr = err
	}
	return fmt.Errorf("parse paperless date %q: %w", s, lastErr)
}

func (d FlexDate) MarshalJSON() ([]byte, error) {
	if d.IsZero() {
		return []byte("null"), nil
	}
	return json.Marshal(d.Format(time.RFC3339))
}

// Ref represents a Paperless related-entity reference (tag, correspondent,
// document type, storage path). Paperless emits these either as a bare
// integer ID or as an embedded object carrying id/name, depending on the
// endpoint and version. Name resolution from a bare ID is out of scope here
// and handled by lookup maps elsewhere.
type Ref struct {
	ID   int
	Name string
}

func (r *Ref) UnmarshalJSON(data []byte) error {
	trimmed := bytes.TrimSpace(data)
	if bytes.Equal(trimmed, []byte("null")) {
		*r = Ref{}
		return nil
	}
	if len(trimmed) > 0 && trimmed[0] != '{' {
		var id int
		if err := json.Unmarshal(trimmed, &id); err != nil {
			return fmt.Errorf("decode paperless ref id: %w", err)
		}
		*r = Ref{ID: id}
		return nil
	}
	var obj struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(trimmed, &obj); err != nil {
		return fmt.Errorf("decode paperless ref object: %w", err)
	}
	*r = Ref{ID: obj.ID, Name: obj.Name}
	return nil
}

type Document struct {
	ID               int      `json:"id"`
	Title            string   `json:"title"`
	Content          string   `json:"content"`
	CreatedDate      FlexDate `json:"created_date"`
	Created          FlexDate `json:"created"`
	Added            FlexDate `json:"added"`
	Modified         FlexDate `json:"modified"`
	Correspondent    *Ref     `json:"correspondent"`
	Tags             []Ref    `json:"tags"`
	DocumentType     *Ref     `json:"document_type"`
	StoragePath      *Ref     `json:"storage_path"`
	FileType         string   `json:"file_type"`
	MimeType         string   `json:"mime_type"`
	OriginalFileName string   `json:"original_file_name"`
	ArchivedFileName string   `json:"archived_file_name"`
	PageCount        int      `json:"page_count"`
}

type Tag struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type Correspondent struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type DocumentType struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type StoragePath struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
	Slug string `json:"slug"`
}

type listResponse[T any] struct {
	Count   int    `json:"count"`
	Results []T    `json:"results"`
	Next    string `json:"next"`
}
