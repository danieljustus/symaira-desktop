package main

import (
	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newAskCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "ask [query]",
		Short: "Ask the AI a question about the vault",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer db.Close()
			svc := service.New(vRoot, db)

			out := make(chan interface{})
			go svc.Ask(cmd.Context(), args[0], out)

			return outputStream(out)
		},
	}
}
