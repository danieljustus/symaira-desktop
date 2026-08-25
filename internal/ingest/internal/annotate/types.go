// Package annotate provides deterministic regex/rule-based document annotation
// that produces grounded extraction sidecars for review and downstream indexing.
package annotate

// Span records the byte offsets and a quoted text snippet from the original
// document that supports an extraction. Downstream consumers can verify
// extractions against the source text using these offsets.
type Span struct {
	Start   int    `json:"start"`
	End     int    `json:"end"`
	Snippet string `json:"snippet"`
}

// Field describes one extraction target that a profile defines.
type Field struct {
	Name        string `json:"name"`
	Type        string `json:"type"`        // "date", "amount", "email", "url", "iban", "id", "party", "deadline", "text"
	Description string `json:"description"` // human-readable label
}

// Extraction is a single grounded extraction from a document. Each extraction
// includes a span with byte offsets and a quoted snippet from the original text.
type Extraction struct {
	Field   string `json:"field"`
	Type    string `json:"type"`
	Value   string `json:"value"`
	Span    *Span  `json:"span,omitempty"`
	Matched bool   `json:"matched"`
}

// Profile defines a set of fields to extract from a document.
type Profile interface {
	Name() string
	Fields() []Field
}
