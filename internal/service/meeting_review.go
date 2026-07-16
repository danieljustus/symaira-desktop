package service

import (
	"fmt"

	"github.com/danieljustus/symaira-desktop/internal/compose"
)

// MeetingSpeaker is one speaker of a meeting note's source artifact, with
// its current display label from the symmeet edit layer.
type MeetingSpeaker struct {
	SpeakerID string `json:"speaker_id"`
	Label     string `json:"label"`
}

// meetingIDForNote resolves the source symmeet meeting ID for a vault
// meeting note, gated on symmeet being installed — every operation in this
// file talks to the live artifact, not just the imported note.
func (s *Service) meetingIDForNote(notePath string) (string, error) {
	if ok, _ := compose.HasSymmeet(); !ok {
		return "", ErrSymmeetUnavailable
	}
	_, fm, err := s.loadMeetingFrontmatter(notePath)
	if err != nil {
		return "", err
	}
	if fm.MeetingID == "" {
		return "", fmt.Errorf("%s has no meeting_id in frontmatter", notePath)
	}
	return fm.MeetingID, nil
}

// MeetingSegments returns the structured, time-coded transcript segments
// for a meeting note's source artifact. Missing segment data (symmeet
// absent, artifact gone) is an error the UI shows as "unavailable" — never
// note corruption.
func (s *Service) MeetingSegments(notePath string) ([]compose.MeetingSegment, error) {
	meetingID, err := s.meetingIDForNote(notePath)
	if err != nil {
		return nil, err
	}
	return compose.ExportMeetingSegments(meetingID)
}

// MeetingSpeakers lists the speakers of a meeting note's source artifact
// with their current labels.
func (s *Service) MeetingSpeakers(notePath string) ([]MeetingSpeaker, error) {
	meetingID, err := s.meetingIDForNote(notePath)
	if err != nil {
		return nil, err
	}
	infos, err := compose.ListSpeakers(meetingID)
	if err != nil {
		return nil, err
	}
	speakers := make([]MeetingSpeaker, 0, len(infos))
	for _, info := range infos {
		speakers = append(speakers, MeetingSpeaker{SpeakerID: info.SpeakerID, Label: info.Label})
	}
	return speakers, nil
}

// MeetingSpeakerLabel assigns a display label to an anonymous speaker in
// the source artifact's edit layer. The caller refreshes the note's
// transcript afterwards to pick the new label up.
func (s *Service) MeetingSpeakerLabel(notePath, speakerID, label string) error {
	meetingID, err := s.meetingIDForNote(notePath)
	if err != nil {
		return err
	}
	return compose.LabelSpeaker(meetingID, speakerID, label)
}

// MeetingSpeakerMerge merges one speaker into another in the source
// artifact's edit layer.
func (s *Service) MeetingSpeakerMerge(notePath, fromSpeakerID, toSpeakerID string) error {
	meetingID, err := s.meetingIDForNote(notePath)
	if err != nil {
		return err
	}
	return compose.MergeSpeakers(meetingID, fromSpeakerID, toSpeakerID)
}

// MeetingSpeakerSplit splits a segment away from its current speaker in
// the source artifact's edit layer.
func (s *Service) MeetingSpeakerSplit(notePath, speakerID, segmentID string) error {
	meetingID, err := s.meetingIDForNote(notePath)
	if err != nil {
		return err
	}
	return compose.SplitSpeaker(meetingID, speakerID, segmentID)
}

// MeetingSpeakerReset discards all speaker edits for the source artifact,
// restoring raw engine output.
func (s *Service) MeetingSpeakerReset(notePath string) error {
	meetingID, err := s.meetingIDForNote(notePath)
	if err != nil {
		return err
	}
	return compose.ResetSpeakers(meetingID)
}

// MeetingMarkReviewed marks a meeting note as reviewed. The write goes
// through writeMeetingFrontmatter, which snapshots the previous content to
// history first — so a review save is always recoverable — and leaves the
// body untouched. It never mutates the raw symmeet artifact. Unlike the
// artifact-backed operations above this works with symmeet absent: review
// state is SymDesk's own.
func (s *Service) MeetingMarkReviewed(notePath string) error {
	doc, fm, err := s.loadMeetingFrontmatter(notePath)
	if err != nil {
		return err
	}
	fm.SymmeetSource.ReviewState = "reviewed"
	return s.writeMeetingFrontmatter(notePath, doc, fm)
}
