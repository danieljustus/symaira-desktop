package cli

import (
	"context"
	"flag"
	"fmt"
	"time"

	"github.com/danieljustus/symaira-relate/internal/app"
	"github.com/danieljustus/symaira-relate/internal/domain/relationship"
)

func init() {
	Register(&Command{
		Name:  "followup",
		Short: "Manage follow-up reminders (add, list, complete, cancel)",
		Run:   runFollowUp,
	})
}

func runFollowUp(ctx context.Context, iostreams IO, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: symrelate followup <add|list|complete|cancel> ...")
	}
	verb, rest := args[0], args[1:]

	a, err := app.Open(ctx)
	if err != nil {
		return err
	}
	defer a.Close()

	switch verb {
	case "add":
		return followUpAdd(ctx, iostreams, a, rest)
	case "list":
		return followUpList(ctx, iostreams, a, rest)
	case "complete":
		return followUpComplete(ctx, iostreams, a, rest)
	case "cancel":
		return followUpCancel(ctx, iostreams, a, rest)
	default:
		return fmt.Errorf("symrelate followup: unknown subcommand %q", verb)
	}
}

func followUpAdd(ctx context.Context, iostreams IO, a *app.App, args []string) error {
	fs := flag.NewFlagSet("followup add", flag.ContinueOnError)
	fs.SetOutput(iostreams.Stderr)
	person := fs.String("person", "", "person id")
	org := fs.String("org", "", "organization id")
	due := fs.String("due", "", "RFC3339 due date (required)")
	notes := fs.String("notes", "", "free-text notes")
	human := fs.Bool("human", false, "print a human-readable summary instead of JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*person == "") == (*org == "") {
		return fmt.Errorf("exactly one of --person or --org is required")
	}
	dueAt, err := time.Parse(time.RFC3339, *due)
	if err != nil {
		return fmt.Errorf("--due: %w", err)
	}

	in := relationship.FollowUpInput{DueAt: dueAt, Notes: *notes}
	var out *relationship.FollowUp
	if *person != "" {
		out, err = a.Relationships.AddPersonFollowUp(ctx, *person, in)
	} else {
		out, err = a.Relationships.AddOrganizationFollowUp(ctx, *org, in)
	}
	if err != nil {
		return err
	}
	return printResult(iostreams, *human, out)
}

func followUpList(ctx context.Context, iostreams IO, a *app.App, args []string) error {
	fs := flag.NewFlagSet("followup list", flag.ContinueOnError)
	fs.SetOutput(iostreams.Stderr)
	person := fs.String("person", "", "person id")
	org := fs.String("org", "", "organization id")
	filter := fs.String("filter", "all", "all|open|overdue|upcoming")
	human := fs.Bool("human", false, "print a human-readable summary instead of JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*person == "") == (*org == "") {
		return fmt.Errorf("exactly one of --person or --org is required")
	}

	var list []relationship.FollowUp
	var err error
	if *person != "" {
		list, err = a.Relationships.ListPersonFollowUps(ctx, *person, app.FollowUpFilter(*filter))
	} else {
		list, err = a.Relationships.ListOrganizationFollowUps(ctx, *org, app.FollowUpFilter(*filter))
	}
	if err != nil {
		return err
	}
	return printResult(iostreams, *human, list)
}

func followUpComplete(ctx context.Context, iostreams IO, a *app.App, args []string) error {
	fs := flag.NewFlagSet("followup complete", flag.ContinueOnError)
	fs.SetOutput(iostreams.Stderr)
	human := fs.Bool("human", false, "print a human-readable summary instead of JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: symrelate followup complete [--human] <id>")
	}
	fu, err := a.Relationships.CompleteFollowUp(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	return printResult(iostreams, *human, fu)
}

func followUpCancel(ctx context.Context, iostreams IO, a *app.App, args []string) error {
	fs := flag.NewFlagSet("followup cancel", flag.ContinueOnError)
	fs.SetOutput(iostreams.Stderr)
	human := fs.Bool("human", false, "print a human-readable summary instead of JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: symrelate followup cancel [--human] <id>")
	}
	fu, err := a.Relationships.CancelFollowUp(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	return printResult(iostreams, *human, fu)
}
