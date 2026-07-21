package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

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

// MeetingImport detects symmeet, validates artifact-schema compatibility,
// and imports one reviewed meeting into the vault as a contract-v2
// meeting note. It never mutates the raw SymMeet artifact.
func (s *Service) MeetingImport(meetingID string) (string, error) {
	meetingID = strings.TrimSpace(meetingID)
	if meetingID == "" {
		return "", fmt.Errorf("meeting id is required")
	}

	if ok, _ := compose.HasSymmeet(); !ok {
		return "", ErrSymmeetUnavailable
	}
	caps, err := compose.GetSymmeetCapabilities()
	if err != nil {
		return "", fmt.Errorf("symmeet capabilities check failed: %w", err)
	}
	if !caps.SupportsArtifactSchema() {
		return "", fmt.Errorf("symmeet %s only supports artifact schema versions %v; symdesk needs %d", caps.Version, caps.ArtifactSchemaVersions, compose.SymmeetSchemaVersion)
	}

	manifest, err := compose.ShowMeeting(meetingID)
	if err != nil {
		return "", fmt.Errorf("failed to load meeting %s: %w", meetingID, err)
	}

	var participants []MeetingParticipant
	if speakers, speakerErr := compose.ListSpeakers(meetingID); speakerErr == nil {
		for _, sp := range speakers {
			participants = append(participants, MeetingParticipant{
				Label:      sp.Label,
				SpeakerIDs: []string{sp.SpeakerID},
			})
		}
	}

	transcript, err := compose.ExportMeetingMarkdown(meetingID)
	if err != nil {
		return "", fmt.Errorf("failed to export meeting %s: %w", meetingID, err)
	}

	startedAt := manifest.CreatedAt.Time
	endedAt := startedAt
	var durationMS int64
	if !manifest.UpdatedAt.IsZero() && manifest.UpdatedAt.After(startedAt) {
		endedAt = manifest.UpdatedAt.Time
		durationMS = endedAt.Sub(startedAt).Milliseconds()
	}

	fm := meetingFrontmatter{
		Type:         "meeting",
		Title:        "Meeting " + startedAt.Format("2006-01-02 15:04"),
		Created:      time.Now().UTC().Format(time.RFC3339),
		Tags:         []string{"meeting"},
		MeetingID:    manifest.MeetingID,
		StartedAt:    startedAt.Format(time.RFC3339),
		EndedAt:      endedAt.Format(time.RFC3339),
		DurationMS:   durationMS,
		Language:     manifest.Language,
		Participants: participants,
		SymmeetSource: MeetingSourceInfo{
			ArtifactSchemaVersion: manifest.SchemaVersion,
			ReviewState:           "unreviewed",
		},
	}

	fmBytes, err := yaml.Marshal(fm)
	if err != nil {
		return "", fmt.Errorf("failed to encode meeting frontmatter: %w", err)
	}

	fullContent := "---\n" + string(fmBytes) + "---\n\n" + wrapTranscript(transcript)

	relPath := meetingNotePath(manifest.MeetingID)
	absPath, err := vault.SecurePath(s.VaultRoot, relPath)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(absPath), 0750); err != nil {
		return "", fmt.Errorf("failed to create meetings directory: %w", err)
	}

	s.snapshotBefore(absPath)
	// Meeting transcripts can contain sensitive conversation content, so
	// notes are written owner-only rather than matching the vault's usual
	// group/other-readable note permissions.
	if err := os.WriteFile(absPath, []byte(fullContent), 0600); err != nil {
		return "", fmt.Errorf("failed to write meeting note: %w", err)
	}

	doc, err := vault.ParseFile(absPath)
	if err != nil {
		return relPath, err
	}
	hash := sha256.Sum256([]byte(fullContent))
	doc.SHA256 = hex.EncodeToString(hash[:])
	if err := s.IndexDocument(doc); err != nil {
		return relPath, fmt.Errorf("failed to index meeting note: %w", err)
	}

	return relPath, nil
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
	var results []MeetingNoteSummary
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

// AvailableMeetingSummary is one entry returned by AvailableMeetings: a raw
// SymMeet meeting that has not yet been imported into the vault.
type AvailableMeetingSummary struct {
	MeetingID string `json:"meeting_id"`
	Source    string `json:"source"`
}

// AvailableMeetings lists SymMeet meetings that are not yet imported into
// the vault, for an "Import Existing SymMeet Meeting" picker. It never
// mutates state.
func (s *Service) AvailableMeetings() ([]AvailableMeetingSummary, error) {
	if ok, _ := compose.HasSymmeet(); !ok {
		return nil, ErrSymmeetUnavailable
	}
	all, err := compose.ListMeetings()
	if err != nil {
		return nil, fmt.Errorf("failed to list symmeet meetings: %w", err)
	}
	imported, err := s.MeetingList()
	if err != nil {
		return nil, fmt.Errorf("failed to list imported meeting notes: %w", err)
	}
	importedIDs := make(map[string]bool, len(imported))
	for _, m := range imported {
		importedIDs[m.MeetingID] = true
	}

	available := make([]AvailableMeetingSummary, 0, len(all))
	for _, m := range all {
		if importedIDs[m.MeetingID] {
			continue
		}
		available = append(available, AvailableMeetingSummary{MeetingID: m.MeetingID, Source: m.Source})
	}
	return available, nil
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

// MeetingRefresh re-exports the transcript for a previously imported
// meeting note and, by default, only previews the change: it does not
// touch the note's frontmatter (preserving any reviewed participant
// confirmations) and only ever replaces the content between the
// transcript markers, leaving any manual notes outside that block intact.
// Pass apply=true to write the refreshed transcript.
func (s *Service) MeetingRefresh(notePath string, apply bool) (*MeetingRefreshResult, error) {
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
	meetingID, _ := doc.Frontmatter["meeting_id"].(string)
	if meetingID == "" {
		return nil, fmt.Errorf("%s has no meeting_id in frontmatter", notePath)
	}

	start, end, err := findTranscriptMarkers(doc.Body)
	if err != nil {
		return nil, fmt.Errorf("cannot safely refresh %s: %w", notePath, err)
	}
	oldTranscript := doc.Body[start:end]

	if ok, _ := compose.HasSymmeet(); !ok {
		return nil, ErrSymmeetUnavailable
	}
	newTranscript, err := compose.ExportMeetingMarkdown(meetingID)
	if err != nil {
		return nil, fmt.Errorf("refresh export failed: %w", err)
	}
	newTranscriptWrapped := "\n" + strings.TrimRight(newTranscript, "\n") + "\n"

	changed := oldTranscript != newTranscriptWrapped
	result := &MeetingRefreshResult{Path: notePath, Changed: changed}
	if changed {
		if n, m := strings.Count(oldTranscript, "\n")+1, strings.Count(newTranscriptWrapped, "\n")+1; n*m <= maxDiffCells {
			result.DiffLines = diffLines(oldTranscript, newTranscriptWrapped)
		}
	}
	if !apply || !changed {
		return result, nil
	}

	raw, err := os.ReadFile(absPath) //nolint:gosec // absPath was already validated by vault.SecurePath above
	if err != nil {
		return nil, fmt.Errorf("failed to re-read %s: %w", notePath, err)
	}
	rawStr := string(raw)
	if !strings.HasSuffix(rawStr, doc.Body) {
		return nil, fmt.Errorf("%s changed on disk since it was read; re-run refresh", notePath)
	}
	frontmatterPart := strings.TrimSuffix(rawStr, doc.Body)
	newBody := doc.Body[:start] + newTranscriptWrapped + doc.Body[end:]
	newContent := frontmatterPart + newBody

	s.snapshotBefore(absPath)
	if err := os.WriteFile(absPath, []byte(newContent), 0600); err != nil { //nolint:gosec // absPath was already validated by vault.SecurePath above
		return nil, fmt.Errorf("failed to write refreshed meeting note: %w", err)
	}

	newDoc, err := vault.ParseFile(absPath)
	if err != nil {
		return nil, err
	}
	hash := sha256.Sum256([]byte(newContent))
	newDoc.SHA256 = hex.EncodeToString(hash[:])
	if err := s.IndexDocument(newDoc); err != nil {
		return nil, fmt.Errorf("failed to re-index refreshed meeting note: %w", err)
	}

	result.Applied = true
	return result, nil
}

// diffLines returns a minimal unified-style line diff between old and new,
// prefixing unchanged lines with "  ", removed lines with "- " and added
// lines with "+ ". It is intended for moderate-length meeting transcripts;
// callers should bound its O(n*m) table for very large inputs.
func diffLines(oldText, newText string) []string {
	oldLines := strings.Split(oldText, "\n")
	newLines := strings.Split(newText, "\n")
	n, m := len(oldLines), len(newLines)

	lcs := make([][]int, n+1)
	for i := range lcs {
		lcs[i] = make([]int, m+1)
	}
	for i := n - 1; i >= 0; i-- {
		for j := m - 1; j >= 0; j-- {
			switch {
			case oldLines[i] == newLines[j]:
				lcs[i][j] = lcs[i+1][j+1] + 1
			case lcs[i+1][j] >= lcs[i][j+1]:
				lcs[i][j] = lcs[i+1][j]
			default:
				lcs[i][j] = lcs[i][j+1]
			}
		}
	}

	var out []string
	i, j := 0, 0
	for i < n && j < m {
		switch {
		case oldLines[i] == newLines[j]:
			out = append(out, "  "+oldLines[i])
			i++
			j++
		case lcs[i+1][j] >= lcs[i][j+1]:
			out = append(out, "- "+oldLines[i])
			i++
		default:
			out = append(out, "+ "+newLines[j])
			j++
		}
	}
	for ; i < n; i++ {
		out = append(out, "- "+oldLines[i])
	}
	for ; j < m; j++ {
		out = append(out, "+ "+newLines[j])
	}
	return out
}
