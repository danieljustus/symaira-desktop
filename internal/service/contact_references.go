package service

import (
	"path/filepath"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/contacts"
	"github.com/danieljustus/symaira-desktop/internal/sidecar"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// ContactReferences is everything the vault holds for one name: the contact
// references that carry it (empty when the store knows nobody by that name),
// the documents filed under it as correspondent, and the meeting notes whose
// participants are linked to one of those references.
//
// Documents are matched by name because that is what a document carries;
// meetings are matched by reference ID because a linked participant carries a
// reviewed identity, not a string. Both are read-only views — resolving a
// name never creates, links, or modifies a contact (issue #516).
type ContactReferences struct {
	Name      string              `json:"name"`
	ContactID string              `json:"contact_id,omitempty"`
	Refs      []contacts.Ref      `json:"refs"`
	Documents []DocsListResult    `json:"documents"`
	Meetings  []ContactMeetingRef `json:"meetings"`
	// StoreAvailable is false when the contact store could not be opened. The
	// documents above are still valid then; only the identity half is
	// unknown, and the caller must not render that as "no such contact".
	StoreAvailable bool `json:"store_available"`
}

// ContactMeetingRef is one meeting note that references a contact.
type ContactMeetingRef struct {
	Path      string `json:"path"`
	Title     string `json:"title"`
	MeetingID string `json:"meeting_id,omitempty"`
	StartedAt string `json:"started_at,omitempty"`
	// Participant is the label of the participant carrying the reference.
	Participant string `json:"participant,omitempty"`
}

// ResolveContactReferences answers "who is this correspondent, and what else
// mentions them" for a name taken from a document.
//
// A name the store does not know is not an error: Refs comes back empty and
// the documents are still listed, which is exactly the pre-#516 behaviour
// plus an explicit "unresolved".
func (s *Service) ResolveContactReferences(name string) (*ContactReferences, error) {
	trimmed := strings.TrimSpace(name)
	out := &ContactReferences{
		Name:      trimmed,
		Refs:      []contacts.Ref{},
		Documents: []DocsListResult{},
		Meetings:  []ContactMeetingRef{},
	}
	if trimmed == "" {
		return out, nil
	}

	docs, err := s.DocsList(sidecar.DocsFilter{Correspondent: trimmed})
	if err != nil {
		return nil, err
	}
	out.Documents = docs

	out.StoreAvailable = contacts.Available()
	if !out.StoreAvailable {
		return out, nil
	}

	refs, err := contacts.FindRefsByName(trimmed)
	if err != nil {
		return nil, err
	}
	out.Refs = append(out.Refs, refs...)
	if len(refs) == 0 {
		return out, nil
	}

	wanted := make(map[string]bool, len(refs))
	for _, ref := range refs {
		wanted[ref.ID] = true
	}
	meetings, err := s.meetingsReferencing(wanted)
	if err != nil {
		return nil, err
	}
	out.Meetings = meetings
	return out, nil
}

// meetingsReferencing walks the vault for meeting notes whose participants
// carry one of the given contact reference IDs.
func (s *Service) meetingsReferencing(ids map[string]bool) ([]ContactMeetingRef, error) {
	found := []ContactMeetingRef{}
	err := vault.Walk(s.VaultRoot, func(path string) error {
		doc, err := vault.ParseFile(path)
		if err != nil {
			// One unparsable note must not fail the whole lookup.
			return nil
		}
		if t, _ := doc.Frontmatter["type"].(string); t != "meeting" {
			return nil
		}
		rel, err := filepath.Rel(s.VaultRoot, path)
		if err != nil {
			rel = path
		}
		_, fm, err := s.loadMeetingFrontmatter(rel)
		if err != nil {
			return nil
		}
		for _, participant := range fm.Participants {
			if participant.ContactRef == nil || !ids[participant.ContactRef.ID] {
				continue
			}
			entry := ContactMeetingRef{
				Path:        rel,
				Title:       doc.Title,
				MeetingID:   fm.MeetingID,
				StartedAt:   fm.StartedAt,
				Participant: participant.Label,
			}
			found = append(found, entry)
			break
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return found, nil
}
