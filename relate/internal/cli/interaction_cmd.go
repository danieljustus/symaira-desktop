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
		Name:  "interaction",
		Short: "Record and list contact interactions (note, call, email, meeting)",
		Run:   runInteraction,
	})
}

func runInteraction(ctx context.Context, iostreams IO, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: symrelate interaction <add|list|last> ...")
	}
	verb, rest := args[0], args[1:]

	a, err := app.Open(ctx)
	if err != nil {
		return err
	}
	defer a.Close()

	switch verb {
	case "add":
		return interactionAdd(ctx, iostreams, a, rest)
	case "list":
		return interactionList(ctx, iostreams, a, rest)
	case "last":
		return interactionLast(ctx, iostreams, a, rest)
	default:
		return fmt.Errorf("symrelate interaction: unknown subcommand %q", verb)
	}
}

func interactionAdd(ctx context.Context, iostreams IO, a *app.App, args []string) error {
	fs := flag.NewFlagSet("interaction add", flag.ContinueOnError)
	fs.SetOutput(iostreams.Stderr)
	person := fs.String("person", "", "person id")
	org := fs.String("org", "", "organization id")
	kind := fs.String("kind", "", "note|call|email|meeting|external_reference (required)")
	summary := fs.String("summary", "", "short summary (required)")
	occurred := fs.String("occurred", "", "RFC3339 timestamp; defaults to now")
	externalRef := fs.String("external-ref", "", "reference to an external artifact (required for external_reference)")
	human := fs.Bool("human", false, "print a human-readable summary instead of JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*person == "") == (*org == "") {
		return fmt.Errorf("exactly one of --person or --org is required")
	}

	in := relationship.InteractionInput{
		Kind: relationship.InteractionKind(*kind), Summary: *summary, ExternalRef: *externalRef,
	}
	if *occurred != "" {
		t, err := time.Parse(time.RFC3339, *occurred)
		if err != nil {
			return fmt.Errorf("--occurred: %w", err)
		}
		in.OccurredAt = t
	}

	var out *relationship.Interaction
	var err error
	if *person != "" {
		out, err = a.Relationships.AddPersonInteraction(ctx, *person, in)
	} else {
		out, err = a.Relationships.AddOrganizationInteraction(ctx, *org, in)
	}
	if err != nil {
		return err
	}
	return printResult(iostreams, *human, out)
}

func interactionList(ctx context.Context, iostreams IO, a *app.App, args []string) error {
	fs := flag.NewFlagSet("interaction list", flag.ContinueOnError)
	fs.SetOutput(iostreams.Stderr)
	person := fs.String("person", "", "person id")
	org := fs.String("org", "", "organization id")
	human := fs.Bool("human", false, "print a human-readable summary instead of JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*person == "") == (*org == "") {
		return fmt.Errorf("exactly one of --person or --org is required")
	}

	var list []relationship.Interaction
	var err error
	if *person != "" {
		list, err = a.Relationships.ListPersonInteractions(ctx, *person)
	} else {
		list, err = a.Relationships.ListOrganizationInteractions(ctx, *org)
	}
	if err != nil {
		return err
	}
	return printResult(iostreams, *human, list)
}

func interactionLast(ctx context.Context, iostreams IO, a *app.App, args []string) error {
	fs := flag.NewFlagSet("interaction last", flag.ContinueOnError)
	fs.SetOutput(iostreams.Stderr)
	person := fs.String("person", "", "person id")
	org := fs.String("org", "", "organization id")
	human := fs.Bool("human", false, "print a human-readable summary instead of JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if (*person == "") == (*org == "") {
		return fmt.Errorf("exactly one of --person or --org is required")
	}

	var last *relationship.Interaction
	var err error
	if *person != "" {
		last, err = a.Relationships.LastPersonInteraction(ctx, *person)
	} else {
		last, err = a.Relationships.LastOrganizationInteraction(ctx, *org)
	}
	if err != nil {
		return err
	}
	if last == nil {
		fmt.Fprintln(iostreams.Stdout, "null")
		return nil
	}
	return printResult(iostreams, *human, last)
}
