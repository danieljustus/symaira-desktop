package cli

import (
	"context"
	"fmt"

	"github.com/danieljustus/symaira-relate/internal/app"
)

func init() {
	Register(&Command{
		Name:  "audit",
		Short: "Show the audit trail for sensitive mutations",
		Run:   runAudit,
	})
}

func runAudit(ctx context.Context, iostreams IO, args []string) error {
	if len(args) != 1 || args[0] != "list" {
		return fmt.Errorf("usage: symrelate audit list")
	}

	a, err := app.Open(ctx)
	if err != nil {
		return err
	}
	defer a.Close()

	events, err := a.Security.ListAuditEvents(ctx)
	if err != nil {
		return err
	}
	return printJSON(iostreams, events)
}
