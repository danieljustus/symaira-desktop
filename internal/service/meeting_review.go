package service

// MeetingSpeaker is one speaker of a meeting note's source artifact, with
// its current display label from the symmeet edit layer.
type MeetingSpeaker struct {
	SpeakerID string `json:"speaker_id"`
	Label     string `json:"label"`
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
