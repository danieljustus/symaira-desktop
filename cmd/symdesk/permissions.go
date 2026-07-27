package main

import (
	"encoding/json"
	"fmt"

	"github.com/danieljustus/symaira-desktop/internal/permissions"
	"github.com/danieljustus/symaira-desktop/internal/vault"
	"github.com/spf13/cobra"
)

func newPermissionsCmd() *cobra.Command {
	permCmd := &cobra.Command{
		Use:   "perm",
		Short: "Manage users, groups, and document permissions",
	}
	permCmd.AddCommand(newUserCmd())
	permCmd.AddCommand(newGroupCmd())
	return permCmd
}

// --- user subcommand ----------------------------------------------------------

func newUserCmd() *cobra.Command {
	userCmd := &cobra.Command{
		Use:   "user",
		Short: "Manage named user accounts",
	}
	userCmd.AddCommand(newUserAddCmd())
	userCmd.AddCommand(newUserListCmd())
	userCmd.AddCommand(newUserRemoveCmd())
	userCmd.AddCommand(newUserTokenCmd())
	return userCmd
}

func newUserAddCmd() *cobra.Command {
	var group string
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Create a new user and print their token",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			perm, err := openPermissions()
			if err != nil {
				return err
			}
			token, err := perm.UserAdd(args[0], "user")
			if err != nil {
				return err
			}
			if token == "" {
				fmt.Printf("User %q already exists.\n", args[0])
				return nil
			}
			if group != "" {
				if err := perm.GroupAddMember(group, args[0]); err != nil {
					return fmt.Errorf("user created but failed to add to group %q: %w", group, err)
				}
			}
			if jsonFlag {
				out := map[string]string{"status": "created", "name": args[0], "token": token}
				b, _ := json.Marshal(out)
				fmt.Println(string(b))
			} else {
				fmt.Printf("User %q created.\nToken: %s\n", args[0], token)
				if group != "" {
					fmt.Printf("Added to group %q.\n", group)
				}
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&group, "group", "", "assign user to a group")
	return cmd
}

func newUserListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all registered users",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			perm, err := openPermissions()
			if err != nil {
				return err
			}
			users, err := perm.UserList()
			if err != nil {
				return err
			}
			return outputResult(users)
		},
	}
	return cmd
}

func newUserRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Delete a user and remove them from all groups",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			perm, err := openPermissions()
			if err != nil {
				return err
			}
			if err := perm.UserRemove(args[0]); err != nil {
				return err
			}
			if jsonFlag {
				out := map[string]string{"status": "removed", "name": args[0]}
				b, _ := json.Marshal(out)
				fmt.Println(string(b))
			} else {
				fmt.Printf("User %q removed.\n", args[0])
			}
			return nil
		},
	}
	return cmd
}

func newUserTokenCmd() *cobra.Command {
	var generate bool
	cmd := &cobra.Command{
		Use:   "token <name>",
		Short: "Generate a new token for a user (invalidates the old one)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if !generate {
				return fmt.Errorf("use --generate to confirm token rotation")
			}
			perm, err := openPermissions()
			if err != nil {
				return err
			}
			token, err := perm.UserGenerateToken(args[0])
			if err != nil {
				return err
			}
			if jsonFlag {
				out := map[string]string{"status": "rotated", "name": args[0], "token": token}
				b, _ := json.Marshal(out)
				fmt.Println(string(b))
			} else {
				fmt.Printf("New token for %q: %s\n", args[0], token)
			}
			return nil
		},
	}
	cmd.Flags().BoolVar(&generate, "generate", false, "confirm token rotation")
	return cmd
}

// --- group subcommand ---------------------------------------------------------

func newGroupCmd() *cobra.Command {
	groupCmd := &cobra.Command{
		Use:   "group",
		Short: "Manage named groups",
	}
	groupCmd.AddCommand(newGroupAddCmd())
	groupCmd.AddCommand(newGroupListCmd())
	groupCmd.AddCommand(newGroupRemoveCmd())
	return groupCmd
}

func newGroupAddCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "add <name>",
		Short: "Create a new empty group",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			perm, err := openPermissions()
			if err != nil {
				return err
			}
			if err := perm.GroupAdd(args[0]); err != nil {
				return err
			}
			if jsonFlag {
				out := map[string]string{"status": "created", "name": args[0]}
				b, _ := json.Marshal(out)
				fmt.Println(string(b))
			} else {
				fmt.Printf("Group %q created.\n", args[0])
			}
			return nil
		},
	}
	return cmd
}

func newGroupListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all groups",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			perm, err := openPermissions()
			if err != nil {
				return err
			}
			groups, err := perm.GroupList()
			if err != nil {
				return err
			}
			return outputResult(groups)
		},
	}
	return cmd
}

func newGroupRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove <name>",
		Short: "Delete a group (does not remove users)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			perm, err := openPermissions()
			if err != nil {
				return err
			}
			if err := perm.GroupRemove(args[0]); err != nil {
				return err
			}
			if jsonFlag {
				out := map[string]string{"status": "removed", "name": args[0]}
				b, _ := json.Marshal(out)
				fmt.Println(string(b))
			} else {
				fmt.Printf("Group %q removed.\n", args[0])
			}
			return nil
		},
	}
	return cmd
}

// --- helpers ------------------------------------------------------------------

func openPermissions() (*permissions.Manager, error) {
	vRoot, err := vault.ResolveVaultRoot("", cfg)
	if err != nil {
		return nil, err
	}
	configDir := vRoot + "/.symdesk"
	return permissions.NewManager(configDir)
}
