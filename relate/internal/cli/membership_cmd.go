package cli

import (
	"context"
	"flag"
	"fmt"

	"github.com/danieljustus/symaira-relate/internal/app"
	"github.com/danieljustus/symaira-relate/internal/domain/contact"
)

func init() {
	Register(&Command{
		Name:  "membership",
		Short: "Link a person to an organization with a role over time",
		Run:   runMembership,
	})
}

func runMembership(ctx context.Context, iostreams IO, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: symrelate membership <add|list-person|list-org> ...")
	}
	verb, rest := args[0], args[1:]

	a, err := app.Open(ctx)
	if err != nil {
		return err
	}
	defer a.Close()

	switch verb {
	case "add":
		return membershipAdd(ctx, iostreams, a, rest)
	case "list-person":
		return membershipListPerson(ctx, iostreams, a, rest)
	case "list-org":
		return membershipListOrg(ctx, iostreams, a, rest)
	default:
		return fmt.Errorf("symrelate membership: unknown subcommand %q", verb)
	}
}

func membershipAdd(ctx context.Context, iostreams IO, a *app.App, args []string) error {
	fs := flag.NewFlagSet("membership add", flag.ContinueOnError)
	fs.SetOutput(iostreams.Stderr)
	person := fs.String("person", "", "person id (required)")
	org := fs.String("org", "", "organization id (required)")
	role := fs.String("role", "", "role, e.g. member/vendor")
	title := fs.String("title", "", "job title")
	human := fs.Bool("human", false, "print a human-readable summary instead of JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *person == "" || *org == "" {
		return fmt.Errorf("usage: symrelate membership add --person <id> --org <id> [--role ...] [--title ...]")
	}

	m, err := a.Contacts.AddMembership(ctx, *person, *org, contact.MembershipInput{Role: *role, Title: *title})
	if err != nil {
		return err
	}
	return printResult(iostreams, *human, m)
}

func membershipListPerson(ctx context.Context, iostreams IO, a *app.App, args []string) error {
	fs := flag.NewFlagSet("membership list-person", flag.ContinueOnError)
	fs.SetOutput(iostreams.Stderr)
	human := fs.Bool("human", false, "print a human-readable summary instead of JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: symrelate membership list-person [--human] <person-id>")
	}
	list, err := a.Contacts.ListMembershipsByPerson(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	return printResult(iostreams, *human, list)
}

func membershipListOrg(ctx context.Context, iostreams IO, a *app.App, args []string) error {
	fs := flag.NewFlagSet("membership list-org", flag.ContinueOnError)
	fs.SetOutput(iostreams.Stderr)
	human := fs.Bool("human", false, "print a human-readable summary instead of JSON")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return fmt.Errorf("usage: symrelate membership list-org [--human] <org-id>")
	}
	list, err := a.Contacts.ListMembershipsByOrganization(ctx, fs.Arg(0))
	if err != nil {
		return err
	}
	return printResult(iostreams, *human, list)
}
