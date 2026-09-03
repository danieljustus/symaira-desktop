package main

import (
	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newClipCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "clip <url>",
		Short: "Fetch a URL via symbrowse and save it as a note",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			urlStr := args[0]
			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer closeWithWarning("sidecar database", db.Close)
			svc := service.New(vRoot, db)

			path, err := svc.NoteClip(urlStr)
			if err != nil {
				return err
			}
			return outputResult(map[string]string{"status": "ok", "path": path})
		},
	}
}
