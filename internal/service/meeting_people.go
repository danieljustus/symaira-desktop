package service

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/danieljustus/symaira-desktop/internal/compose"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// ErrSymmemoryUnavailable is returned by Memory-backed meeting operations
// when symmemory is not installed on PATH. SymDesk must remain fully
// usable in that case, mirroring ErrSymmeetUnavailable.
var ErrSymmemoryUnavailable = errors.New("symmemory not found on PATH")

// ParticipantCandidate is one Memory entity Symaira Memory suggests for a
// meeting participant label, for the reviewed confirmation flow. Every
// candidate is a deterministic exact-name or alias match (see
// compose.ResolveCandidates) — never a guess.
type ParticipantCandidate struct {
	EntityID    string `json:"entity_id"`
	Name        string `json:"name"`
	MatchReason string `json:"match_reason"`
}

// ResolveParticipantCandidates returns deterministic Memory entity
// candidates for a meeting participant label.
func (s *Service) ResolveParticipantCandidates(participantLabel string) ([]ParticipantCandidate, error) {
	if ok, _ := compose.HasSymmemory(); !ok {
		return nil, ErrSymmemoryUnavailable
	}
	matches, err := compose.ResolveCandidates(participantLabel)
	if err != nil {
		return nil, fmt.Errorf("failed to resolve candidates for %q: %w", participantLabel, err)
	}
	candidates := make([]ParticipantCandidate, 0, len(matches))
	for _, m := range matches {
		candidates = append(candidates, ParticipantCandidate{
			EntityID:    m.Entity.ID,
			Name:        m.Entity.Name,
			MatchReason: m.MatchReason,
		})
	}
	return candidates, nil
}

// loadMeetingFrontmatter parses a meeting note and decodes its frontmatter
// into the typed meetingFrontmatter shape via a YAML round-trip (the parser
// only produces map[string]interface{}; re-marshaling that back to YAML and
// decoding into the typed struct is the safe way to get a mutable,
// structured view without hand-rolling a second decoder).
func (s *Service) loadMeetingFrontmatter(notePath string) (*vault.Document, meetingFrontmatter, error) {
	absPath, err := vault.SecurePath(s.VaultRoot, notePath)
	if err != nil {
		return nil, meetingFrontmatter{}, err
	}
	doc, err := vault.ParseFile(absPath)
	if err != nil {
		return nil, meetingFrontmatter{}, err
	}
	if t, _ := doc.Frontmatter["type"].(string); t != "meeting" {
		return nil, meetingFrontmatter{}, fmt.Errorf("%s is not a meeting note (frontmatter type != \"meeting\")", notePath)
	}

	fmBytes, err := yaml.Marshal(doc.Frontmatter)
	if err != nil {
		return nil, meetingFrontmatter{}, fmt.Errorf("failed to re-encode frontmatter: %w", err)
	}
	var fm meetingFrontmatter
	if err := yaml.Unmarshal(fmBytes, &fm); err != nil {
		return nil, meetingFrontmatter{}, fmt.Errorf("failed to decode meeting frontmatter: %w", err)
	}
	return doc, fm, nil
}

// writeMeetingFrontmatter replaces a meeting note's entire frontmatter
// block while leaving the body (transcript plus any manual notes) byte-for-
// byte untouched, refusing the write if the file changed on disk since doc
// was read (the same conflict guard MeetingRefresh uses).
func (s *Service) writeMeetingFrontmatter(notePath string, doc *vault.Document, fm meetingFrontmatter) error {
	absPath, err := vault.SecurePath(s.VaultRoot, notePath)
	if err != nil {
		return err
	}

	newFmBytes, err := yaml.Marshal(fm)
	if err != nil {
		return fmt.Errorf("failed to encode meeting frontmatter: %w", err)
	}

	raw, err := os.ReadFile(absPath) //nolint:gosec // absPath was already validated by vault.SecurePath above
	if err != nil {
		return fmt.Errorf("failed to re-read %s: %w", notePath, err)
	}
	rawStr := string(raw)
	if !strings.HasSuffix(rawStr, doc.Body) {
		return fmt.Errorf("%s changed on disk since it was read; re-run", notePath)
	}
	newContent := "---\n" + string(newFmBytes) + "---\n" + doc.Body

	s.snapshotBefore(absPath)
	if err := os.WriteFile(absPath, []byte(newContent), 0600); err != nil { //nolint:gosec // absPath was already validated by vault.SecurePath above
		return fmt.Errorf("failed to write %s: %w", notePath, err)
	}

	newDoc, err := vault.ParseFile(absPath)
	if err != nil {
		return err
	}
	hash := sha256.Sum256([]byte(newContent))
	newDoc.SHA256 = hex.EncodeToString(hash[:])
	if err := s.IndexDocument(newDoc); err != nil {
		return fmt.Errorf("failed to re-index %s: %w", notePath, err)
	}
	return nil
}

// ConfirmParticipant links a meeting-local speakerID to a confirmed Memory
// entityID, persisted separately from the anonymous speakerID (VAULT.md
// section 8, and see #173's participant-resolution flow). No entity is
// created or matched automatically here: entityID must come from an
// explicit, already-reviewed choice (e.g. one returned by
// ResolveParticipantCandidates, or a newly created entity the reviewer
// confirmed). Passing an empty entityID un-links the participant.
func (s *Service) ConfirmParticipant(notePath, speakerID, entityID string) error {
	doc, fm, err := s.loadMeetingFrontmatter(notePath)
	if err != nil {
		return err
	}

	found := false
	for i := range fm.Participants {
		for _, id := range fm.Participants[i].SpeakerIDs {
			if id == speakerID {
				fm.Participants[i].EntityID = entityID
				found = true
				break
			}
		}
		if found {
			break
		}
	}
	if !found {
		return fmt.Errorf("no participant with speaker id %q in %s", speakerID, notePath)
	}

	return s.writeMeetingFrontmatter(notePath, doc, fm)
}

// ConfirmParticipantNewPerson creates (or reuses, by exact name) a person
// entity in Memory and links the participant to it. It exists for the
// reviewed "create a new person" choice in the confirmation flow — the
// reviewer explicitly typed and confirmed the name, so this is never an
// automatic identity creation. Returns the entity ID that was linked.
func (s *Service) ConfirmParticipantNewPerson(notePath, speakerID, name string) (string, error) {
	if ok, _ := compose.HasSymmemory(); !ok {
		return "", ErrSymmemoryUnavailable
	}
	trimmed := strings.TrimSpace(name)
	if trimmed == "" {
		return "", fmt.Errorf("a person name is required")
	}
	entity, err := compose.EnsureEntity(trimmed, "person")
	if err != nil {
		return "", fmt.Errorf("failed to create Memory person %q: %w", trimmed, err)
	}
	if err := s.ConfirmParticipant(notePath, speakerID, entity.ID); err != nil {
		return "", err
	}
	return entity.ID, nil
}
