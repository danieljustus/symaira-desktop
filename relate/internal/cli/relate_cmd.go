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
		Name:  "relate",
		Short: "Manage directed relationships between contacts",
		Run:   runRelate,
	})
}

func runRelate(ctx context.Context, iostreams IO, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: symrelate relate <add|outgoing|incoming-person|incoming-org> ...")
	}
	verb, rest := args[0], args[1:]

	a, err := app.Open(ctx)
	if err != nil {
		return err
	}
	defer a.Close()

	switch verb {
	case "add":
		return relateAdd(ctx, iostreams, a, rest)
	case "outgoing":
		return relateOutgoing(ctx, iostreams, a, rest)
	case "incoming-person":
		return relateIncomingPerson(ctx, iostreams, a, rest)
	case "incoming-org":
		return relateIncomingOrg(ctx, iostreams, a, rest)
	default:
		return fmt.Errorf("symrelate relate: unknown subcommand %q", verb)
	}
}

func relateAdd(ctx context.Context, iostreams IO, a *app.App, args []string) error {
	fs := flag.NewFlagSet("relate add", flag.ContinueOnError)
	fs.SetOutput(iostreams.Stderr)
	from := fs.String("from", "", "subject person id (required)")
	toPerson := fs.String("to-person", "", "target person id")
	toOrg := fs.String("to-org", "", "target organization id")
	relType := fs.String("type", "", "relationship type, e.g. colleague/friend/manager (required)")
	notes := fs.String("notes", "", "free-text notes")
	human := fs.Bool("human", false, "print a human-readable summary instead of JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}

	rel, err := a.Relationships.AddRelationship(ctx, relationship.RelationshipInput{
		FromPersonID: *from, ToPersonID: *toPerson, ToOrganizationID: *toOrg, Type: *relType, Notes: *notes,
	})
	if err != nil {
		return err
	}
	return printResult(iostreams, *human, rel)
}

func relateOutgoing(ctx context.Context, iostreams IO, a *app.App, args []string) error {
	fs := flag.NewFlagSet("relate outgoing", flag.ContinueOnError)
	fs.SetOutput(iostreams.Stderr)
	human := fs.Bool("human", false, "print a human-readable summary instead of JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: symrelate relate outgoing [--human] <person-id>")
	}
	list, err := a.Relationships.ListOutgoingFromPerson(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	return printResult(iostreams, *human, list)
}

func relateIncomingPerson(ctx context.Context, iostreams IO, a *app.App, args []string) error {
	fs := flag.NewFlagSet("relate incoming-person", flag.ContinueOnError)
	fs.SetOutput(iostreams.Stderr)
	human := fs.Bool("human", false, "print a human-readable summary instead of JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: symrelate relate incoming-person [--human] <person-id>")
	}
	list, err := a.Relationships.ListIncomingToPerson(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	return printResult(iostreams, *human, list)
}

func relateIncomingOrg(ctx context.Context, iostreams IO, a *app.App, args []string) error {
	fs := flag.NewFlagSet("relate incoming-org", flag.ContinueOnError)
	fs.SetOutput(iostreams.Stderr)
	human := fs.Bool("human", false, "print a human-readable summary instead of JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: symrelate relate incoming-org [--human] <org-id>")
	}
	list, err := a.Relationships.ListIncomingToOrganization(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	return printResult(iostreams, *human, list)
}
