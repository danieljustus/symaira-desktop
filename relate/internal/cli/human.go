package cli

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/danieljustus/symaira-relate/internal/domain/contact"
	"github.com/danieljustus/symaira-relate/internal/domain/page"
	"github.com/danieljustus/symaira-relate/internal/domain/relationship"
)

// printResult writes v to Stdout as either JSON (the machine-readable
// contract shape, see docs/CLI_CONTRACT.md) or a short human-readable
// rendering, selected by the --human flag most commands expose. Every
// value humanize does not have a dedicated renderer for still gets a
// readable rendering — indented JSON — so --human never fails, it just
// degrades to a more verbose but still legible shape for composite/report
// payloads (import plans, audit events, ...).
func printResult(iostreams IO, human bool, v any) error {
	if !human {
		return printJSON(iostreams, v)
	}
	fmt.Fprintln(iostreams.Stdout, humanize(v))
	return nil
}

func humanize(v any) string {
	switch x := v.(type) {
	case *contact.Person:
		return humanPerson(x)
	case *contact.Ref:
		return fmt.Sprintf("%s (%s) [ref %s:%s schema v%d]", x.DisplayName, x.Kind, x.Provider, x.ID, x.SchemaVersion)
	case *contact.Organization:
		return humanOrganization(x)
	case *contact.ContactPoint:
		return humanContactPoint(*x)
	case *contact.Membership:
		return humanMembership(*x)
	case []contact.Membership:
		return humanList(len(x), "membership", "memberships", func(i int) string { return humanMembership(x[i]) })
	case page.Result[contact.Person]:
		return humanList(len(x.Items), "person", "persons", func(i int) string { return humanPersonLine(x.Items[i]) }) + humanMore(x.HasMore)
	case page.Result[contact.Organization]:
		return humanList(len(x.Items), "organization", "organizations", func(i int) string { return humanOrganizationLine(x.Items[i]) }) + humanMore(x.HasMore)
	case *relationship.Relationship:
		return humanRelationship(*x)
	case []relationship.Relationship:
		return humanList(len(x), "relationship", "relationships", func(i int) string { return humanRelationship(x[i]) })
	case *relationship.Interaction:
		return humanInteraction(*x)
	case []relationship.Interaction:
		return humanList(len(x), "interaction", "interactions", func(i int) string { return humanInteraction(x[i]) })
	case *relationship.FollowUp:
		return humanFollowUp(*x)
	case []relationship.FollowUp:
		return humanList(len(x), "follow-up", "follow-ups", func(i int) string { return humanFollowUp(x[i]) })
	case []relationship.TimelineEntry:
		return humanList(len(x), "entry", "entries", func(i int) string { return humanTimelineEntry(x[i]) })
	default:
		b, err := json.MarshalIndent(v, "", "  ")
		if err != nil {
			return fmt.Sprintf("%+v", v)
		}
		return string(b)
	}
}

func humanList(n int, singular, plural string, line func(i int) string) string {
	if n == 0 {
		return "no " + plural
	}
	word := plural
	if n == 1 {
		word = singular
	}
	lines := make([]string, n)
	for i := 0; i < n; i++ {
		lines[i] = line(i)
	}
	return fmt.Sprintf("%d %s:\n%s", n, word, strings.Join(lines, "\n"))
}

func humanMore(hasMore bool) string {
	if hasMore {
		return "\n(more results available — raise --limit or --offset)"
	}
	return ""
}

func humanPerson(p *contact.Person) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s)\n", p.DisplayName, p.ID)
	if len(p.Classifications) > 0 {
		fmt.Fprintf(&b, "  classification: %s\n", joinClassifications(p.Classifications))
	}
	if len(p.Tags) > 0 {
		fmt.Fprintf(&b, "  tags: %s\n", strings.Join(p.Tags, ", "))
	}
	for _, cp := range p.ContactPoints {
		fmt.Fprintf(&b, "  %s\n", humanContactPointLine(cp))
	}
	if p.Notes != "" {
		fmt.Fprintf(&b, "  notes: %s\n", p.Notes)
	}
	return strings.TrimRight(b.String(), "\n")
}

func humanPersonLine(p contact.Person) string {
	return fmt.Sprintf("  - %s (%s)", p.DisplayName, p.ID)
}

func humanOrganization(o *contact.Organization) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s (%s)\n", o.Name, o.ID)
	if len(o.Classifications) > 0 {
		fmt.Fprintf(&b, "  classification: %s\n", joinClassifications(o.Classifications))
	}
	if len(o.Tags) > 0 {
		fmt.Fprintf(&b, "  tags: %s\n", strings.Join(o.Tags, ", "))
	}
	for _, cp := range o.ContactPoints {
		fmt.Fprintf(&b, "  %s\n", humanContactPointLine(cp))
	}
	if o.Notes != "" {
		fmt.Fprintf(&b, "  notes: %s\n", o.Notes)
	}
	return strings.TrimRight(b.String(), "\n")
}

func humanOrganizationLine(o contact.Organization) string {
	return fmt.Sprintf("  - %s (%s)", o.Name, o.ID)
}

func joinClassifications(cs []contact.Classification) string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = string(c)
	}
	return strings.Join(out, ", ")
}

func humanContactPoint(cp contact.ContactPoint) string {
	return humanContactPointLine(cp) + fmt.Sprintf(" (%s)", cp.ID)
}

func humanContactPointLine(cp contact.ContactPoint) string {
	pref := ""
	if cp.IsPreferred {
		pref = ", preferred"
	}
	label := ""
	if cp.Label != "" {
		label = " [" + cp.Label + "]"
	}
	return fmt.Sprintf("%s: %s%s%s", cp.Kind, cp.RawValue, label, pref)
}

func humanMembership(m contact.Membership) string {
	period := "ongoing"
	switch {
	case m.ValidFrom != nil && m.ValidTo != nil:
		period = m.ValidFrom.Format("2006-01-02") + " to " + m.ValidTo.Format("2006-01-02")
	case m.ValidFrom != nil:
		period = "since " + m.ValidFrom.Format("2006-01-02")
	case m.ValidTo != nil:
		period = "until " + m.ValidTo.Format("2006-01-02")
	}
	role := m.Role
	if m.Title != "" {
		role = role + " (" + m.Title + ")"
	}
	return fmt.Sprintf("  - person %s at org %s as %s, %s [%s]", m.PersonID, m.OrganizationID, role, period, m.ID)
}

func humanRelationship(r relationship.Relationship) string {
	target := r.ToPersonID
	targetKind := "person"
	if r.ToOrganizationID != "" {
		target, targetKind = r.ToOrganizationID, "organization"
	}
	return fmt.Sprintf("  - %s --[%s]--> %s %s [%s]", r.FromPersonID, r.Type, targetKind, target, r.ID)
}

func humanInteraction(i relationship.Interaction) string {
	ref := ""
	if i.ExternalRef != "" {
		ref = " ref:" + i.ExternalRef
	}
	return fmt.Sprintf("  - [%s] %s: %s%s [%s]", i.OccurredAt.Format("2006-01-02 15:04"), i.Kind, i.Summary, ref, i.ID)
}

func humanFollowUp(f relationship.FollowUp) string {
	return fmt.Sprintf("  - due %s [%s]: %s (%s)", f.DueAt.Format("2006-01-02"), f.Status, f.Notes, f.ID)
}

func humanTimelineEntry(e relationship.TimelineEntry) string {
	switch e.Kind {
	case relationship.TimelineInteraction:
		return humanInteraction(*e.Interaction)
	case relationship.TimelineFollowUp:
		return humanFollowUp(*e.FollowUp)
	default:
		return fmt.Sprintf("  - unknown timeline entry at %s", e.At.Format("2006-01-02"))
	}
}
