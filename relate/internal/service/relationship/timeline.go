package relationship

import (
	"context"
	"sort"

	"github.com/danieljustus/symaira-relate/internal/domain/relationship"
	"github.com/danieljustus/symaira-relate/internal/errs"
)

// PersonTimeline returns a person's interactions and follow-ups combined
// into one feed, most recent first. Ties (equal timestamps) break on
// created_at/id order already applied by the underlying list queries, so
// the merge stays a stable sort and the result is deterministic across
// calls.
func (s *Service) PersonTimeline(ctx context.Context, personID string) ([]relationship.TimelineEntry, error) {
	interactions, err := s.ListPersonInteractions(ctx, personID)
	if err != nil {
		return nil, err
	}
	followUps, err := s.ListPersonFollowUps(ctx, personID, FollowUpFilterAll)
	if err != nil {
		return nil, err
	}
	return mergeTimeline(interactions, followUps), nil
}

// OrganizationTimeline is the organization equivalent of PersonTimeline.
func (s *Service) OrganizationTimeline(ctx context.Context, organizationID string) ([]relationship.TimelineEntry, error) {
	interactions, err := s.ListOrganizationInteractions(ctx, organizationID)
	if err != nil {
		return nil, err
	}
	followUps, err := s.ListOrganizationFollowUps(ctx, organizationID, FollowUpFilterAll)
	if err != nil {
		return nil, errs.Internal("relationship.OrganizationTimeline", "failed to list follow-ups", err)
	}
	return mergeTimeline(interactions, followUps), nil
}

func mergeTimeline(interactions []relationship.Interaction, followUps []relationship.FollowUp) []relationship.TimelineEntry {
	entries := make([]relationship.TimelineEntry, 0, len(interactions)+len(followUps))
	for i := range interactions {
		entries = append(entries, relationship.TimelineEntry{
			Kind: relationship.TimelineInteraction, At: interactions[i].OccurredAt, Interaction: &interactions[i],
		})
	}
	for i := range followUps {
		entries = append(entries, relationship.TimelineEntry{
			Kind: relationship.TimelineFollowUp, At: followUps[i].DueAt, FollowUp: &followUps[i],
		})
	}

	sort.SliceStable(entries, func(i, j int) bool {
		return entries[i].At.After(entries[j].At)
	})
	return entries
}
