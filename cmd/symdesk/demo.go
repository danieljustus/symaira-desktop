package main

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/danieljustus/symaira-desktop/internal/demo"
)

func newDemoCmd() *cobra.Command {
	demoCmd := &cobra.Command{
		Use:   "demo",
		Short: "Demo vault management",
	}

	demoInitCmd := &cobra.Command{
		Use:   "init [dir]",
		Short: "Materialise the built-in demo vault into a directory",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			dir := "symdesk-demo"
			if len(args) > 0 {
				dir = args[0]
			}

			size, _ := cmd.Flags().GetString("size")
			if err := demo.InitSize(dir, size); err != nil {
				return err
			}

			absDir, _ := filepath.Abs(dir)
			if jsonFlag {
				out := map[string]string{
					"status": "ok",
					"path":   absDir,
				}
				b, _ := json.Marshal(out)
				fmt.Println(string(b))
			} else {
				fmt.Printf("Demo vault created at %s\n", absDir)
				fmt.Println("Next steps:")
				fmt.Printf("  SYMDESK_VAULT=%s symdesk index\n", absDir)
				fmt.Printf("  SYMDESK_VAULT=%s symdesk docs list\n", absDir)
			}
			return nil
		},
	}
	demoInitCmd.Flags().String("size", "small", "demo size: small or large")
	demoCmd.AddCommand(demoInitCmd)

	return demoCmd
}
