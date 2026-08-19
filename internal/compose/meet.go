package compose

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

// SymmeetSchemaVersion is the meeting artifact schema version this build understands.
const SymmeetSchemaVersion = 1

// HasSymmeet is a shorthand helper for symmeet.
func HasSymmeet() (bool, string) {
	return HasTool("symmeet")
}

// SymmeetError wraps a non-zero symmeet exit with its documented meaning
// (see the SymDesk integration contract's error-handling table): exit 1 is
// a possibly-transient runtime failure, exit 2 is invalid input such as an
// unknown meeting ID, and exit 3 is permission denied (e.g. a trashed
// meeting accessed without --include-trashed).
type SymmeetError struct {
	Op       string
	ExitCode int
	Stderr   string
}

func (e *SymmeetError) Error() string {
	return fmt.Sprintf("symmeet %s failed (exit %d): %s", e.Op, e.ExitCode, e.Stderr)
}

// IsNotFound reports whether the failure was symmeet's "invalid input /
// unknown meeting ID" exit code.
func (e *SymmeetError) IsNotFound() bool { return e.ExitCode == 2 }

// IsPermissionDenied reports whether the failure was symmeet's "meeting in
// trash without --include-trashed" exit code.
func (e *SymmeetError) IsPermissionDenied() bool { return e.ExitCode == 3 }

// IsTransient reports whether the failure was symmeet's generic runtime
// exit code, which the contract documents as potentially transient (disk
// full, lock contention) and safe to retry.
func (e *SymmeetError) IsTransient() bool { return e.ExitCode == 1 }

func runSymmeet(ctx context.Context, op string, args []string) ([]byte, error) {
	bin, err := Resolve("symmeet")
	if err != nil {
		return nil, fmt.Errorf("symmeet not found: %w", err)
	}
	cmd := exec.CommandContext(ctx, bin, args...) //nolint:gosec // resolved via compose.Resolve; args are CLI flags/IDs, not shell-interpreted
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("symmeet %s timed out: %w", op, ctx.Err())
		}
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			return nil, &SymmeetError{Op: op, ExitCode: exitErr.ExitCode(), Stderr: strings.TrimSpace(stderr.String())}
		}
		return nil, fmt.Errorf("symmeet %s failed: %w (stderr: %s)", op, err, stderr.String())
	}
	return out.Bytes(), nil
}

func runSymmeetJSON(ctx context.Context, op string, args []string, dest any) error {
	data, err := runSymmeet(ctx, op, args)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, dest); err != nil {
		return fmt.Errorf("failed to unmarshal symmeet %s output: %w", op, err)
	}
	return nil
}

// SymmeetCapabilities is the machine-readable capability document from
// `symmeet capabilities --json`. Only non-sensitive capability data is
// ever decoded here: no private paths, participant names or transcript
// content are part of this contract.
type SymmeetCapabilities struct {
	Tool                   string   `json:"tool"`
	Version                string   `json:"version"`
	SchemaVersion          int      `json:"schema_version"`
	ArtifactSchemaVersions []int    `json:"artifact_schema_versions"`
	ExportFormats          []string `json:"export_formats"`
}

// SupportsArtifactSchema reports whether SymmeetSchemaVersion is one of the
// meeting artifact schema versions this symmeet build can produce.
func (c *SymmeetCapabilities) SupportsArtifactSchema() bool {
	for _, v := range c.ArtifactSchemaVersions {
		if v == SymmeetSchemaVersion {
			return true
		}
	}
	return false
}

// GetSymmeetCapabilities probes symmeet's capabilities with a short bounded
// timeout, so a hung or misbehaving sibling binary never blocks SymDesk.
func GetSymmeetCapabilities() (*SymmeetCapabilities, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var caps SymmeetCapabilities
	if err := runSymmeetJSON(ctx, "capabilities", []string{"capabilities", "--json"}, &caps); err != nil {
		return nil, err
	}
	return &caps, nil
}

// MeetingSummary is one entry from `symmeet meeting list --json`.
type MeetingSummary struct {
	MeetingID string `json:"meeting_id"`
	Source    string `json:"source"`
}

// ListMeetings calls `symmeet meeting list --json` (5s timeout, matching
// the documented SymDesk integration contract).
func ListMeetings() ([]MeetingSummary, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var result struct {
		Meetings []MeetingSummary `json:"meetings"`
	}
	if err := runSymmeetJSON(ctx, "meeting list", []string{"meeting", "list", "--json"}, &result); err != nil {
		return nil, err
	}
	return result.Meetings, nil
}

// flexibleTime decodes a symmeet timestamp that may be an ISO-8601 string
// (the on-disk manifest format) or a bare Unix-epoch number (what some
// symmeet CLI surfaces fall back to). Meeting import must not fail just
// because a sibling tool's timestamp encoding is inconsistent.
type flexibleTime struct {
	time.Time
}

func (f *flexibleTime) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err == nil {
		parsed, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return fmt.Errorf("invalid timestamp %q: %w", s, err)
		}
		f.Time = parsed
		return nil
	}

	var seconds float64
	if err := json.Unmarshal(data, &seconds); err != nil {
		return fmt.Errorf("timestamp is neither an ISO-8601 string nor a number: %s", string(data))
	}
	f.Time = time.Unix(0, int64(seconds*float64(time.Second))).UTC()
	return nil
}

// AudioTrack describes one recorded track of a meeting artifact.
type AudioTrack struct {
	TrackID      string `json:"track_id"`
	Kind         string `json:"kind"`
	RelativePath string `json:"relative_path"`
}

// MeetingJob describes the transcription job state of a meeting artifact.
type MeetingJob struct {
	JobID string `json:"job_id"`
	State string `json:"state"`
}

// MeetingManifest is the subset of `symmeet meeting show --json` this
// integration consumes. Unknown fields are ignored, per the artifact
// contract's additive-schema guarantee.
type MeetingManifest struct {
	SchemaVersion int          `json:"schema_version"`
	MeetingID     string       `json:"meeting_id"`
	Source        string       `json:"source"`
	CreatedAt     flexibleTime `json:"created_at"`
	UpdatedAt     flexibleTime `json:"updated_at"`
	AudioTracks   []AudioTrack `json:"audio_tracks"`
	Language      string       `json:"language"`
	Job           *MeetingJob  `json:"job"`
}

// ShowMeeting calls `symmeet meeting show <id> --json` (5s timeout,
// matching the documented SymDesk integration contract) and validates the
// artifact schema version before returning.
func ShowMeeting(meetingID string) (*MeetingManifest, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var manifest MeetingManifest
	if err := runSymmeetJSON(ctx, "meeting show", []string{"meeting", "show", meetingID, "--json"}, &manifest); err != nil {
		return nil, err
	}
	if manifest.SchemaVersion != SymmeetSchemaVersion {
		return nil, fmt.Errorf("unsupported meeting artifact schema version %d (symdesk supports %d)", manifest.SchemaVersion, SymmeetSchemaVersion)
	}
	return &manifest, nil
}

// SpeakerInfo is one speaker entry from `symmeet speaker list --json`.
type SpeakerInfo struct {
	SpeakerID string
	Label     string
}

// ListSpeakers calls `symmeet speaker list <id> --json` (5s timeout) and
// returns each known speaker with its display label, falling back to the
// raw anonymous speaker ID when no label has been assigned yet.
func ListSpeakers(meetingID string) ([]SpeakerInfo, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var result struct {
		Speakers []string          `json:"speakers"`
		Labels   map[string]string `json:"labels"`
	}
	if err := runSymmeetJSON(ctx, "speaker list", []string{"speaker", "list", meetingID, "--json"}, &result); err != nil {
		return nil, err
	}

	speakers := make([]SpeakerInfo, 0, len(result.Speakers))
	for _, id := range result.Speakers {
		label := result.Labels[id]
		if label == "" {
			label = id
		}
		speakers = append(speakers, SpeakerInfo{SpeakerID: id, Label: label})
	}
	return speakers, nil
}

// ExportMeetingMarkdown calls `symmeet export <id> --format markdown
// --output -` (30s timeout, matching the documented SymDesk integration
// contract) and returns the rendered transcript. Export prefers
// user-edited segments over raw engine output whenever an edited version
// exists, so a refresh after speaker/text corrections picks them up.
func ExportMeetingMarkdown(meetingID string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	data, err := runSymmeet(ctx, "export", []string{"export", meetingID, "--format", "markdown", "--output", "-"})
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// MeetingSegment is one time-coded transcript segment from
// `symmeet export <id> --format json`. EditedText, when non-empty, is the
// user-corrected text that supersedes EngineText.
type MeetingSegment struct {
	SegmentID  string `json:"segment_id"`
	SpeakerID  string `json:"speaker_id"`
	StartMS    int64  `json:"start_ms"`
	EndMS      int64  `json:"end_ms"`
	EngineText string `json:"engine_text"`
	EditedText string `json:"edited_text,omitempty"`
	Revision   string `json:"revision"`
}

// ExportMeetingSegments calls `symmeet export <id> --format json --output -`
// (30s timeout, same as the markdown export) and returns the structured,
// time-coded segments the synchronized review timeline needs. Like the
// markdown export, symmeet prefers user-edited segments over raw engine
// output.
func ExportMeetingSegments(meetingID string) ([]MeetingSegment, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	var envelope struct {
		Segments []MeetingSegment `json:"segments"`
	}
	if err := runSymmeetJSON(ctx, "export json", []string{"export", meetingID, "--format", "json", "--output", "-"}, &envelope); err != nil {
		return nil, err
	}
	return envelope.Segments, nil
}

// LabelSpeaker calls `symmeet speaker label <id> <speaker> <label> --json`
// (5s timeout) to assign a display label to an anonymous speaker. The edit
// lives in the symmeet artifact's edit layer; raw engine output is never
// mutated.
func LabelSpeaker(meetingID, speakerID, label string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := runSymmeet(ctx, "speaker label", []string{"speaker", "label", meetingID, speakerID, label, "--json"})
	return err
}

// MergeSpeakers calls `symmeet speaker merge <id> <from> <to> --json`
// (5s timeout) to merge one anonymous speaker into another.
func MergeSpeakers(meetingID, fromSpeakerID, toSpeakerID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := runSymmeet(ctx, "speaker merge", []string{"speaker", "merge", meetingID, fromSpeakerID, toSpeakerID, "--json"})
	return err
}

// SplitSpeaker calls `symmeet speaker split <id> <speaker> --segment <seg>
// --json` (5s timeout) to split one segment away from its current speaker.
func SplitSpeaker(meetingID, speakerID, segmentID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := runSymmeet(ctx, "speaker split", []string{"speaker", "split", meetingID, speakerID, "--segment", segmentID, "--json"})
	return err
}

// ResetSpeakers calls `symmeet speaker reset <id> --json` (5s timeout) to
// discard all speaker edits for a meeting, restoring raw engine output.
func ResetSpeakers(meetingID string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := runSymmeet(ctx, "speaker reset", []string{"speaker", "reset", meetingID, "--json"})
	return err
}
