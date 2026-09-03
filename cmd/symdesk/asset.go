package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/service"
)

func newAssetCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "asset",
		Short: "Store and manage binary assets in the vault",
	}
	cmd.AddCommand(newAssetStoreCmd())
	return cmd
}

func newAssetStoreCmd() *cobra.Command {
	var preferredName string
	cmd := &cobra.Command{
		Use:   "store <file>",
		Short: "Store a binary file into the vault assets folder",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			src := args[0]
			var data []byte
			var err error
			if src == "-" {
				data, err = io.ReadAll(os.Stdin)
			} else {
				// The CLI intentionally accepts an explicit source path from the user.
				//nolint:gosec // this is the command's documented input path
				data, err = os.ReadFile(src)
			}
			if err != nil {
				return fmt.Errorf("read asset data: %w", err)
			}

			name := preferredName
			if name == "" && src != "-" {
				name = filepath.Base(src)
			}

			vRoot, db, err := initServiceDeps()
			if err != nil {
				return err
			}
			defer closeWithWarning("sidecar database", db.Close)
			svc := service.New(vRoot, db)

			ext := filepath.Ext(name)
			relPath, mdLink, err := svc.StoreAssetWithLink(data, name, ext)
			if err != nil {
				return err
			}
			return outputResult(map[string]string{
				"status":        "ok",
				"path":          relPath,
				"markdown_link": mdLink,
			})
		},
	}
	cmd.Flags().StringVar(&preferredName, "name", "", "preferred file name for the stored asset")
	return cmd
}
