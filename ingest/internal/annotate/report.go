package annotate

import "time"

// Report is a body-safe review surface that lists extractions by document
// profile without embedding the full document body.
type Report struct {
	SchemaVersion int          `json:"schema_version"`
	DocSHA256     string       `json:"doc_sha256"`
	Profile       string       `json:"profile"`
	TotalFields   int          `json:"total_fields"`
	GeneratedAt   time.Time    `json:"generated_at"`
	Extractions   []Extraction `json:"extractions"`
}

// ReportFromExtractions creates a Report from a set of extractions.
func ReportFromExtractions(docSHA256, profile string, extractions []Extraction) *Report {
	return &Report{
		SchemaVersion: 1,
		DocSHA256:     docSHA256,
		Profile:       profile,
		TotalFields:   len(extractions),
		GeneratedAt:   time.Now().UTC(),
		Extractions:   extractions,
	}
}
