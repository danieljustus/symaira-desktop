package service

import (
	"errors"
	"fmt"

	"github.com/danieljustus/symaira-desktop/internal/contacts"
)

// ErrContactStoreUnavailable is returned by contact-backed meeting
// operations when the local contact store cannot be opened. The store is
// in-process since the repo consolidation, so this no longer means "a tool
// is missing" — it means the store itself is unreadable. SymDesk must stay
// fully usable in that case.
var ErrContactStoreUnavailable = errors.New("symdesk contact store unavailable")

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
// it resolves a contact ID to its reference-only shape so the reviewer can
// see who they are about to link. Nothing is written — no contact is
// created, matched, or stored by this call.
func (s *Service) ResolveMeetingContactRef(contactID string) (*contacts.Ref, error) {
	if !contacts.Available() {
		return nil, ErrContactStoreUnavailable
	}
	return contacts.ResolveRef(contactID)
}

// LinkParticipantContact stores an opaque, reviewed contact reference on a
// meeting participant, alongside any confirmed EntityID (VAULT.md section
// 8). The reference is resolved at link time; linking never creates a
// contact and never copies contact points, notes, or paths into the vault.
func (s *Service) LinkParticipantContact(notePath, speakerID, contactID string) (*contacts.Ref, error) {
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
