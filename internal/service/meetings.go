package service

import (
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/compose"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// ErrSymmeetUnavailable is returned by meeting operations when symmeet is
// not installed on PATH. SymDesk must remain fully usable in that case;
// callers surface this as a clear, non-fatal message rather than a crash.
var ErrSymmeetUnavailable = errors.New("symmeet not found on PATH")

const (
	transcriptStartMarker = "<!-- symmeet-transcript:start -->"
	transcriptEndMarker   = "<!-- symmeet-transcript:end -->"
)

var meetingIDUnsafeChars = regexp.MustCompile(`[^a-zA-Z0-9_-]`)

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
	ContactRef *compose.ContactRef    `yaml:"contact_ref,omitempty"`
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

func wrapTranscript(transcript string) string {
	return transcriptStartMarker + "\n" + strings.TrimRight(transcript, "\n") + "\n" + transcriptEndMarker + "\n"
}

// findTranscriptMarkers returns the byte span of the content strictly
// between the transcript markers. Refresh refuses to proceed when the
// markers are missing rather than guessing where the transcript is, so a
// manually restructured note is never silently clobbered.
func findTranscriptMarkers(body string) (start, end int, err error) {
	startIdx := strings.Index(body, transcriptStartMarker)
	if startIdx == -1 {
		return 0, 0, fmt.Errorf("missing %s marker", transcriptStartMarker)
	}
	contentStart := startIdx + len(transcriptStartMarker)
	endIdx := strings.Index(body[contentStart:], transcriptEndMarker)
	if endIdx == -1 {
		return 0, 0, fmt.Errorf("missing %s marker", transcriptEndMarker)
	}
	return contentStart, contentStart + endIdx, nil
}

// meetingNotePath returns the vault-relative path a meeting note is
// imported to for a given meeting ID. Import and refresh both derive the
// path this way so re-importing the same meeting updates the same note.
func meetingNotePath(meetingID string) string {
	safe := meetingIDUnsafeChars.ReplaceAllString(meetingID, "_")
	return filepath.Join("meetings", "meeting-"+safe+".md")
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

// MeetingList returns every vault note whose frontmatter marks it as an
// imported meeting.
func (s *Service) MeetingList() ([]MeetingNoteSummary, error) {
	// Start with an explicit empty slice, not a nil one: encoding/json
	// marshals a nil slice as the JSON literal `null`, which the Swift
	// client cannot decode as a top-level array and surfaces as a raw
	// decode error on any vault with zero meeting notes.
	results := []MeetingNoteSummary{}
	err := vault.Walk(s.VaultRoot, func(path string) error {
		doc, err := vault.ParseFile(path)
		if err != nil {
			// A single unparsable note must not fail the whole listing.
			return nil
		}
		if t, _ := doc.Frontmatter["type"].(string); t != "meeting" {
			return nil
		}
		rel, err := filepath.Rel(s.VaultRoot, path)
		if err != nil {
			rel = path
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

		results = append(results, summary)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return results, nil
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

// maxDiffCells bounds the O(n*m) line-diff table so a pathologically large
// transcript cannot exhaust memory; beyond it, MeetingRefresh still reports
// whether the transcript changed, just without a line-level diff.
const maxDiffCells = 4_000_000

// diffLines returns a minimal unified-style line diff between old and new,
// prefixing unchanged lines with "  ", removed lines with "- " and added
// lines with "+ ". It is intended for moderate-length meeting transcripts;
// callers should bound its O(n*m) table for very large inputs.
