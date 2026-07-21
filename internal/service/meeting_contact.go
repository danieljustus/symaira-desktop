package service

import (
	"errors"
	"fmt"

	"github.com/danieljustus/symaira-desktop/internal/compose"
)

// ErrSymrelateUnavailable is returned by Relate-backed meeting operations
// when symrelate is not installed on PATH. SymDesk must remain fully
// usable in that case, mirroring ErrSymmeetUnavailable and
// ErrSymmemoryUnavailable.
var ErrSymrelateUnavailable = errors.New("symrelate not found on PATH")

// participantIndexBySpeaker returns the index of the participant covering
// speakerID, or -1 when no participant does.
func participantIndexBySpeaker(fm *meetingFrontmatter, speakerID string) int {
	for i := range fm.Participants {
		for _, id := range fm.Participants[i].SpeakerIDs {
			if id == speakerID {
				return i
			}
		}
	}
	return -1
}

// ResolveMeetingContactRef is the review step of the contact-linking flow:
// it resolves a symrelate contact ID to its reference-only shape so the
// reviewer can see who they are about to link. Nothing is written — no
// contact is created, matched, or stored by this call.
func (s *Service) ResolveMeetingContactRef(contactID string) (*compose.ContactRef, error) {
	if ok, _ := compose.HasSymrelate(); !ok {
		return nil, ErrSymrelateUnavailable
	}
	return compose.ResolveContactRef(contactID)
}

// LinkParticipantContact stores an opaque, reviewed symrelate contact
// reference on a meeting participant, alongside any confirmed EntityID
// (VAULT.md section 8). The reference is resolved and schema-checked at
// link time; linking never creates a contact in symrelate and never copies
// contact points, notes, or paths into the vault.
func (s *Service) LinkParticipantContact(notePath, speakerID, contactID string) (*compose.ContactRef, error) {
	ref, err := s.ResolveMeetingContactRef(contactID)
	if err != nil {
		return nil, err
	}

	doc, fm, err := s.loadMeetingFrontmatter(notePath)
	if err != nil {
		return nil, err
	}
	idx := participantIndexBySpeaker(&fm, speakerID)
	if idx < 0 {
		return nil, fmt.Errorf("no participant with speaker id %q in %s", speakerID, notePath)
	}
	fm.Participants[idx].ContactRef = ref

	if err := s.writeMeetingFrontmatter(notePath, doc, fm); err != nil {
		return nil, err
	}
	return ref, nil
}

// UnlinkParticipantContact removes a participant's contact reference. The
// confirmed EntityID and every other participant field stay untouched.
func (s *Service) UnlinkParticipantContact(notePath, speakerID string) error {
	doc, fm, err := s.loadMeetingFrontmatter(notePath)
	if err != nil {
		return err
	}
	idx := participantIndexBySpeaker(&fm, speakerID)
	if idx < 0 {
		return fmt.Errorf("no participant with speaker id %q in %s", speakerID, notePath)
	}
	fm.Participants[idx].ContactRef = nil

	return s.writeMeetingFrontmatter(notePath, doc, fm)
}
