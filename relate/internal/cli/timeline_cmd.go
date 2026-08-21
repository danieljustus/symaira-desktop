package cli

import (
	"context"
	"flag"
	"fmt"

	"github.com/danieljustus/symaira-relate/internal/app"
	"github.com/danieljustus/symaira-relate/internal/domain/relationship"
)

func init() {
	Register(&Command{
		Name:  "timeline",
		Short: "Show a contact's combined interaction and follow-up timeline",
		Run:   runTimeline,
	})
}

func runTimeline(ctx context.Context, iostreams IO, args []string) error {
	fs := flag.NewFlagSet("timeline", flag.ContinueOnError)
	fs.SetOutput(iostreams.Stderr)
	person := fs.String("person", "", "person id")
	org := fs.String("org", "", "organization id")
	human := fs.Bool("human", false, "print a human-readable summary instead of JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*person == "") == (*org == "") {
		return fmt.Errorf("usage: symrelate timeline (--person <id> | --org <id>) [--human]")
	}

	a, err := app.Open(ctx)
	if err != nil {
		return err
	}
	defer a.Close()

	var timeline []relationship.TimelineEntry
	if *person != "" {
		timeline, err = a.Relationships.PersonTimeline(ctx, *person)
	} else {
		timeline, err = a.Relationships.OrganizationTimeline(ctx, *org)
	}
	if err != nil {
		return err
	}
	return printResult(iostreams, *human, timeline)
}
