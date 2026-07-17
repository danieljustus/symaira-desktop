package service

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/danieljustus/symaira-desktop/internal/compose"
	"github.com/danieljustus/symaira-desktop/internal/vault"
)

// MeetingFact is one reviewed fact/decision/action item selected for
// publishing. Facts are authored by the reviewer in the meeting review
// workspace (#172); this package deliberately does not extract or generate
// them — generative extraction without review is explicitly out of scope.
type MeetingFact struct {
	Value string `json:"value"`
}

// MeetingPublishProposal is the reviewed set of Memory writes a user has
// selected for one meeting: relations for every confirmed participant
// (entity_id already set via ConfirmParticipant) plus freeform facts.
type MeetingPublishProposal struct {
	Facts []MeetingFact `json:"facts"`
}

// MeetingPublishResult reports what a publish actually wrote.
type MeetingPublishResult struct {
	MeetingEntityID  string   `json:"meeting_entity_id"`
	RelationsCreated int      `json:"relations_created"`
	FactsPublished   []string `json:"facts_published"` // memory IDs
	FactsSkipped     int      `json:"facts_skipped"`   // already published in a prior apply
}

// publishedFactsFrontmatterKey stores the content hashes of facts already
// published for a note, so re-applying the same proposal is idempotent.
const publishedFactsFrontmatterKey = "symmeet_published_facts"

func factHash(value string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(value)))
	return hex.EncodeToString(sum[:])
}

// PublishMeetingProposal applies a reviewed proposal for one meeting:
//   - ensures a Memory entity exists for the meeting itself
//   - creates a "person --attended--> meeting" relation for every confirmed
//     participant (entity relations are idempotent, so this is always safe
//     to repeat)
//   - saves every fact in the proposal not already published for this note
//
// `symmemory set` is NOT idempotent (two calls with identical content
// create two separate memories), so already-published fact content is
// tracked by hash in the note's frontmatter and skipped on a repeat apply —
// this is what makes re-running a partially-failed publish safe, and what
// satisfies "applying the same proposal twice does not duplicate ...
// memories."
func (s *Service) PublishMeetingProposal(notePath string, proposal MeetingPublishProposal) (*MeetingPublishResult, error) {
	if ok, _ := compose.HasSymmemory(); !ok {
		return nil, ErrSymmemoryUnavailable
	}

	doc, fm, err := s.loadMeetingFrontmatter(notePath)
	if err != nil {
		return nil, err
	}
	if fm.MeetingID == "" {
		return nil, fmt.Errorf("%s has no meeting_id in frontmatter", notePath)
	}

	meetingEntityName := "Meeting " + fm.MeetingID
	meetingEntity, err := compose.EnsureEntity(meetingEntityName, "other")
	if err != nil {
		return nil, fmt.Errorf("failed to ensure a Memory entity for %s: %w", meetingEntityName, err)
	}
	result := &MeetingPublishResult{MeetingEntityID: meetingEntity.ID}

	if len(fm.Participants) > 0 {
		entities, err := compose.ListEntities()
		if err != nil {
			return result, fmt.Errorf("failed to list Memory entities: %w", err)
		}
		entityByID := make(map[string]compose.MemoryEntity, len(entities))
		for _, e := range entities {
			entityByID[e.ID] = e
		}

		for _, p := range fm.Participants {
			if p.EntityID == "" {
				continue // only confirmed participants get published relations
			}
			entity, ok := entityByID[p.EntityID]
			if !ok {
				return result, fmt.Errorf("confirmed participant %q references unknown Memory entity id %q", p.Label, p.EntityID)
			}
			if err := compose.RelateEntities(entity.Name, "attended", meetingEntityName); err != nil {
				return result, fmt.Errorf("failed to relate %s to %s: %w", entity.Name, meetingEntityName, err)
			}
			result.RelationsCreated++
		}
	}

	alreadyPublished := map[string]bool{}
	if raw, ok := doc.Frontmatter[publishedFactsFrontmatterKey]; ok {
		if list, ok := raw.([]interface{}); ok {
			for _, v := range list {
				if hash, ok := v.(string); ok {
					alreadyPublished[hash] = true
				}
			}
		}
	}

	newHashes := make([]string, 0, len(alreadyPublished))
	for hash := range alreadyPublished {
		newHashes = append(newHashes, hash)
	}

	// flushLedger persists every fact hash published so far to the note's
	// frontmatter. symmemory `set` is not idempotent, so a fact already
	// written to Memory but not yet recorded in this note's ledger would be
	// resubmitted — and duplicated — by a retry. It is called immediately
	// after every individual successful fact below, not only once the
	// whole proposal has published: an interruption between two facts
	// (a returned error, but also a crash, force-quit, or anything else
	// that never returns control to this function) must not lose track of
	// facts that already succeeded.
	flushLedger := func() error {
		absPath, err := vault.SecurePath(s.VaultRoot, notePath)
		if err != nil {
			return err
		}
		if err := vault.SetFrontmatterValue(absPath, publishedFactsFrontmatterKey, newHashes); err != nil {
			return fmt.Errorf("published fact to Memory but failed to record it on the note (a repeat apply may duplicate it): %w", err)
		}
		return nil
	}

	for _, fact := range proposal.Facts {
		value := strings.TrimSpace(fact.Value)
		if value == "" {
			continue
		}
		hash := factHash(value)
		if alreadyPublished[hash] {
			result.FactsSkipped++
			continue
		}
		record, err := compose.SetMemory(value, "project", []string{meetingEntityName})
		if err != nil {
			return result, fmt.Errorf("failed to publish fact %q: %w", value, err)
		}
		result.FactsPublished = append(result.FactsPublished, record.ID)
		alreadyPublished[hash] = true
		newHashes = append(newHashes, hash)

		if err := flushLedger(); err != nil {
			return result, err
		}
	}

	return result, nil
}
