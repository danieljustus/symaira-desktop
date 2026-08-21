package cli

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/danieljustus/symaira-relate/internal/app"
	"github.com/danieljustus/symaira-relate/internal/integration/symmeet"
	relationshipsvc "github.com/danieljustus/symaira-relate/internal/service/relationship"
)

func init() {
	Register(&Command{
		Name:  "meeting",
		Short: "Import a reviewed SymMeet meeting as a contact interaction — see docs/integrations/SYMMEET.md",
		Run:   runMeeting,
	})
}

func runMeeting(ctx context.Context, iostreams IO, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: symrelate meeting <status|import> ...")
	}
	verb, rest := args[0], args[1:]

	switch verb {
	case "status":
		return meetingStatus(ctx, iostreams)
	case "import":
		return meetingImport(ctx, iostreams, rest)
	default:
		return fmt.Errorf("symrelate meeting: unknown subcommand %q", verb)
	}
}

// meetingStatus reports whether a compatible SymMeet installation was
// found — never an error, since SymMeet is always optional.
func meetingStatus(ctx context.Context, iostreams IO) error {
	caps, err := symmeet.Discover(ctx)
	if err != nil {
		return printJSON(iostreams, map[string]any{"available": false, "reason": err.Error()})
	}
	return printJSON(iostreams, map[string]any{"available": true, "tool": caps.Tool, "version": caps.Version, "schema_version": caps.SchemaVersion})
}

// symMeetManifest is the subset of a SymMeet meeting manifest.json this
// package reads — see docs/integrations/SYMMEET.md and symaira-meet's
// published v1 contract (symaira-meet#19). It intentionally leaves every
// other documented field (audio_tracks, language, job, ...) unparsed:
// Relate never touches transcript, audio, or recording data, only the
// opaque meeting id plus the source/consent/retention status fields
// needed for display.
type symMeetManifest struct {
	SchemaVersion int    `json:"schema_version"`
	MeetingID     string `json:"meeting_id"`
	Source        string `json:"source"`
	CreatedAt     string `json:"created_at"`
	Consent       struct {
		Status string `json:"status"`
	} `json:"consent"`
	Retention struct {
		Policy string `json:"policy"`
	} `json:"retention"`
}

// symMeetSupportedManifestSchema is the only manifest schema_version this
// package understands, matching MeetingManifest.supportedSchemaVersion in
// symaira-meet's own contract. A mismatch is rejected rather than parsed
// best-effort, since a future major version may repurpose field meanings.
const symMeetSupportedManifestSchema = 1

// meetingFixtureStatus is source/consent/retention status read from a
// SymMeet manifest for display only — see the parent issue's "source/
// consent/retention status display" acceptance criterion. None of this is
// persisted; MeetingImportInput only ever stores the opaque meeting id.
type meetingFixtureStatus struct {
	Source          string `json:"source,omitempty"`
	ConsentStatus   string `json:"consent_status,omitempty"`
	RetentionPolicy string `json:"retention_policy,omitempty"`
}

func meetingImport(ctx context.Context, iostreams IO, args []string) error {
	fs := flag.NewFlagSet("meeting import", flag.ContinueOnError)
	fs.SetOutput(iostreams.Stderr)
	id := fs.String("id", "", "opaque SymMeet meeting id (required unless --fixture supplies one)")
	title := fs.String("title", "", "meeting title")
	summary := fs.String("summary", "", "short summary")
	occurred := fs.String("occurred", "", "RFC3339 timestamp; defaults to now")
	personsFlag := fs.String("person", "", "comma-separated person ids to attach this meeting to")
	orgsFlag := fs.String("org", "", "comma-separated organization ids to attach this meeting to")
	fixture := fs.String("fixture", "", "path to a SymMeet meeting artifact directory containing manifest.json (the published fixture format, e.g. symaira-meet's Tests/Fixtures/integration/meeting-complete/); supplies meeting_id and occurred_at, both overridable by the explicit flags above")
	if err := fs.Parse(args); err != nil {
		return err
	}

	in := relationshipsvc.MeetingImportInput{MeetingID: *id, Title: *title, Summary: *summary}
	var fixtureStatus *meetingFixtureStatus
	if *fixture != "" {
		status, err := applyMeetingFixture(*fixture, &in)
		if err != nil {
			return err
		}
		fixtureStatus = status
	}
	if in.MeetingID == "" {
		return fmt.Errorf("usage: symrelate meeting import --id <meeting-id> (--person <id>[,...] | --org <id>[,...]) [--fixture <path>]")
	}

	if *occurred != "" {
		t, err := time.Parse(time.RFC3339, *occurred)
		if err != nil {
			return fmt.Errorf("--occurred: %w", err)
		}
		in.OccurredAt = t
	} else if in.OccurredAt.IsZero() {
		in.OccurredAt = time.Now().UTC()
	}

	persons := splitNonEmpty(*personsFlag)
	orgs := splitNonEmpty(*orgsFlag)
	if len(persons) == 0 && len(orgs) == 0 {
		return fmt.Errorf("at least one of --person or --org is required — contact association is always manual and reviewed, never automatic")
	}

	a, err := app.Open(ctx)
	if err != nil {
		return err
	}
	defer a.Close()

	type outcome struct {
		PersonID       string `json:"person_id,omitempty"`
		OrganizationID string `json:"organization_id,omitempty"`
		InteractionID  string `json:"interaction_id"`
		Created        bool   `json:"created"`
	}
	var results []outcome
	for _, personID := range persons {
		interaction, created, err := a.Relationships.ImportPersonMeeting(ctx, personID, in)
		if err != nil {
			return err
		}
		results = append(results, outcome{PersonID: personID, InteractionID: interaction.ID, Created: created})
	}
	for _, orgID := range orgs {
		interaction, created, err := a.Relationships.ImportOrganizationMeeting(ctx, orgID, in)
		if err != nil {
			return err
		}
		results = append(results, outcome{OrganizationID: orgID, InteractionID: interaction.ID, Created: created})
	}

	// Source/consent/retention status display (parent issue acceptance
	// criterion) — informational only, on Stderr alongside other
	// diagnostics; never part of the Stdout JSON result contract and never
	// stored (see MeetingImportInput).
	if fixtureStatus != nil {
		fmt.Fprintf(iostreams.Stderr, "SymMeet meeting %s: source=%s consent=%s retention=%s\n",
			in.MeetingID, orNA(fixtureStatus.Source), orNA(fixtureStatus.ConsentStatus), orNA(fixtureStatus.RetentionPolicy))
	}

	return printJSON(iostreams, results)
}

func orNA(s string) string {
	if s == "" {
		return "n/a"
	}
	return s
}

// applyMeetingFixture reads a SymMeet meeting artifact directory's
// manifest.json — the published v1 contract (symaira-meet#19) — and fills
// in MeetingID/OccurredAt on in where not already set by an explicit
// flag. It returns the manifest's source/consent/retention status for
// display; see meetingFixtureStatus.
func applyMeetingFixture(dir string, in *relationshipsvc.MeetingImportInput) (*meetingFixtureStatus, error) {
	data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
	if err != nil {
		return nil, fmt.Errorf("failed to read SymMeet fixture manifest.json: %w", err)
	}
	var m symMeetManifest
	if err := json.Unmarshal(data, &m); err != nil {
		return nil, fmt.Errorf("--fixture: invalid manifest.json: %w", err)
	}
	if m.SchemaVersion != symMeetSupportedManifestSchema {
		return nil, fmt.Errorf("--fixture: unsupported manifest schema_version %d (symrelate supports %d)", m.SchemaVersion, symMeetSupportedManifestSchema)
	}
	if in.MeetingID == "" {
		in.MeetingID = m.MeetingID
	}
	if m.CreatedAt != "" && in.OccurredAt.IsZero() {
		t, err := time.Parse(time.RFC3339, m.CreatedAt)
		if err != nil {
			return nil, fmt.Errorf("--fixture: created_at: %w", err)
		}
		in.OccurredAt = t
	}
	return &meetingFixtureStatus{Source: m.Source, ConsentStatus: m.Consent.Status, RetentionPolicy: m.Retention.Policy}, nil
}

func splitNonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	var out []string
	for _, part := range strings.Split(s, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			out = append(out, trimmed)
		}
	}
	return out
}
