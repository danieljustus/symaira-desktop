package service

import (
	"fmt"
	"path/filepath"

	"github.com/danieljustus/symaira-desktop/internal/contacts"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// MeetingParticipant is one reviewed participant entry in a meeting note's
// frontmatter. EntityID is left empty on import; it is populated only by
// an explicit, separately reviewed participant-confirmation step.
// ContactRef is equally optional and reviewed-only: an opaque reference to
// the authoritative symrelate contact, never a copy of contact data (see
// VAULT.md section 8). Extras preserves unknown fields written by newer
// contract versions so an edit never strips them.
type MeetingParticipant struct {
	Label      string                 `yaml:"label"`
	SpeakerIDs []string               `yaml:"speaker_ids"`
	EntityID   string                 `yaml:"entity_id,omitempty"`
	ContactRef *contacts.Ref          `yaml:"contact_ref,omitempty"`
	Extras     map[string]interface{} `yaml:",inline"`
}

// MeetingSourceInfo records SymMeet artifact provenance on a meeting note.
type MeetingSourceInfo struct {
	ArtifactSchemaVersion int    `yaml:"artifact_schema_version"`
	ReviewState           string `yaml:"review_state"`
}

// meetingFrontmatter is the additive contract-v2 frontmatter shape written
// for meeting notes; see VAULT.md section 8. Extras preserves unknown
// top-level fields (written by newer contract versions) across edits.
type meetingFrontmatter struct {
	Type          string                 `yaml:"type"`
	Title         string                 `yaml:"title"`
	Created       string                 `yaml:"created"`
	Tags          []string               `yaml:"tags"`
	MeetingID     string                 `yaml:"meeting_id"`
	StartedAt     string                 `yaml:"started_at"`
	EndedAt       string                 `yaml:"ended_at,omitempty"`
	DurationMS    int64                  `yaml:"duration_ms,omitempty"`
	Language      string                 `yaml:"language,omitempty"`
	Participants  []MeetingParticipant   `yaml:"participants,omitempty"`
	SymmeetSource MeetingSourceInfo      `yaml:"symmeet_source"`
	Extras        map[string]interface{} `yaml:",inline"`
}

// MeetingNoteSummary is one entry returned by MeetingList.
type MeetingNoteSummary struct {
	Path        string `json:"path"`
	Title       string `json:"title"`
	MeetingID   string `json:"meeting_id"`
	StartedAt   string `json:"started_at"`
	DurationMS  int64  `json:"duration_ms"`
	Language    string `json:"language"`
	ReviewState string `json:"review_state"`
}

// MeetingListFailure identifies a vault file that could not be decoded while
// building the meeting list. It is intentionally non-fatal: one corrupt note
// must not hide every other meeting, but the UI must name the file so the user
// can reveal or skip it instead of silently losing it.
type MeetingListFailure struct {
	Path    string `json:"path"`
	Message string `json:"message"`
}

// MeetingListResult is the detailed meeting-list response used by the native
// client. The legacy MeetingList method below remains array-shaped for callers
// that only need the successfully parsed notes.
type MeetingListResult struct {
	Meetings []MeetingNoteSummary `json:"meetings"`
	Failures []MeetingListFailure `json:"failures"`
}

// MeetingList returns every successfully parsed vault note whose frontmatter
// marks it as an imported meeting. Parse failures are intentionally omitted for
// backwards compatibility; MeetingListDetailed exposes them to actionable UI.
func (s *Service) MeetingList() ([]MeetingNoteSummary, error) {
	result, err := s.MeetingListDetailed()
	if err != nil {
		return nil, err
	}
	return result.Meetings, nil
}

// MeetingListDetailed returns parsed meeting notes plus per-file failures.
func (s *Service) MeetingListDetailed() (*MeetingListResult, error) {
	// Start with explicit empty slices, not nil ones: encoding/json marshals a
	// nil slice as `null`, which Swift cannot decode as an array.
	result := &MeetingListResult{
		Meetings: []MeetingNoteSummary{},
		Failures: []MeetingListFailure{},
	}
	err := vault.Walk(s.VaultRoot, func(path string) error {
		rel, relErr := filepath.Rel(s.VaultRoot, path)
		if relErr != nil {
			rel = path
		}

		doc, err := vault.ParseFile(path)
		if err != nil {
			result.Failures = append(result.Failures, MeetingListFailure{
				Path:    rel,
				Message: err.Error(),
			})
			return nil
		}
		if t, _ := doc.Frontmatter["type"].(string); t != "meeting" {
			return nil
		}

		summary := MeetingNoteSummary{Path: rel, Title: doc.Title}
		summary.MeetingID, _ = doc.Frontmatter["meeting_id"].(string)
		summary.StartedAt, _ = doc.Frontmatter["started_at"].(string)
		summary.Language, _ = doc.Frontmatter["language"].(string)
		switch v := doc.Frontmatter["duration_ms"].(type) {
		case int:
			summary.DurationMS = int64(v)
		case int64:
			summary.DurationMS = v
		case float64:
			summary.DurationMS = int64(v)
		}
		if src, ok := doc.Frontmatter["symmeet_source"].(map[string]interface{}); ok {
			summary.ReviewState, _ = src["review_state"].(string)
		}

		result.Meetings = append(result.Meetings, summary)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return result, nil
}

// MeetingShow loads one meeting note by its vault-relative path.
func (s *Service) MeetingShow(notePath string) (*vault.Document, error) {
	absPath, err := vault.SecurePath(s.VaultRoot, notePath)
	if err != nil {
		return nil, err
	}
	doc, err := vault.ParseFile(absPath)
	if err != nil {
		return nil, err
	}
	if t, _ := doc.Frontmatter["type"].(string); t != "meeting" {
		return nil, fmt.Errorf("%s is not a meeting note (frontmatter type != \"meeting\")", notePath)
	}
	return doc, nil
}

// MeetingRefreshResult reports what a refresh found (and, when apply=true,
// what it wrote).
type MeetingRefreshResult struct {
	Path      string   `json:"path"`
	Changed   bool     `json:"changed"`
	DiffLines []string `json:"diff_lines,omitempty"`
	Applied   bool     `json:"applied"`
}

