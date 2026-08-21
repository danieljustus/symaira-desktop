package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"sort"
	"text/tabwriter"

	"github.com/danieljustus/symaira-corekit/exitcodes"
	"github.com/danieljustus/symaira-room/internal/identity"
	"github.com/danieljustus/symaira-room/internal/members"
	"github.com/danieljustus/symaira-room/internal/room"
)

// runMember implements the "member" subcommand.
func runMember(args []string, stdout, stderr io.Writer) int {
	if len(args) < 3 {
		_, _ = fmt.Fprintln(stdout, "Usage: symroom member <add|list|remove|role> [flags] [args]")
		return int(exitcodes.ExitOK)
	}
	sub := args[2]
	switch sub {
	case "--help", "-h":
		_, _ = fmt.Fprintln(stdout, "Usage: symroom member <add|list|remove|role> [flags] [args]")
		return int(exitcodes.ExitOK)
	case "add":
		fs := flag.NewFlagSet("member add", flag.ExitOnError)
		pubFlag := fs.String("pubkey", "", "Member public key (hex)")
		nameFlag := fs.String("name", "", "Member display name")
		roleFlag := fs.String("role", "member", "Member role (owner|member|agent|observer)")
		kindFlag := fs.String("kind", "human", "Member kind (human|agent)")
		idFlag := fs.String("identity", "", "Caller identity name (must be the room owner)")
		if err := fs.Parse(args[3:]); err != nil {
			return int(exitcodes.ExitNoInput)
		}
		memberName, pubKeyHex := *nameFlag, *pubFlag
		if fs.NArg() >= 2 {
			memberName, pubKeyHex = fs.Arg(0), fs.Arg(1)
		}
		if pubKeyHex == "" || memberName == "" {
			_, _ = fmt.Fprintln(stderr, "Usage: symroom member add [--identity <name>] <name> <pubkey> [--role <role>] [--kind <kind>]")
			_, _ = fmt.Fprintln(stderr, "       symroom member add --pubkey <hex> --name <name> [--role <role>] [--kind <kind>] [--identity <name>]")
			return int(exitcodes.ExitNoInput)
		}
		id := resolveIdentity(*idFlag)
		ev, err := room.AddMember(roomDir(), memberName, pubKeyHex, members.Role(*roleFlag), members.MemberKind(*kindFlag), id)
		if err != nil {
			exitMemberError(err)
		}
		pubBytes, err := hex.DecodeString(pubKeyHex)
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Invalid public key hex: %v\n", err)
			return int(exitcodes.ExitNoInput)
		}
		memID := identity.ComputeMemberID(pubBytes)
		_, _ = fmt.Fprintf(stdout, "Member added: %s (%s, role: %s, kind: %s, event: %s)\n", memberName, memID, *roleFlag, *kindFlag, ev.ID)
		return int(exitcodes.ExitOK)
	case "list":
		fs := flag.NewFlagSet("member list", flag.ExitOnError)
		jsonFlag := fs.Bool("json", false, "Output members as JSON")
		if err := fs.Parse(args[3:]); err != nil {
			return int(exitcodes.ExitNoInput)
		}
		state, err := room.ListMembers(roomDir())
		if err != nil {
			_, _ = fmt.Fprintf(stderr, "Error listing members: %v\n", err)
			return int(exitcodes.ExitGeneric)
		}
		if len(state.Members) == 0 {
			if *jsonFlag {
				_, _ = fmt.Fprintln(stdout, "[]")
			} else {
				_, _ = fmt.Fprintln(stdout, "No members")
			}
			return int(exitcodes.ExitOK)
		}
		ids := make([]string, 0, len(state.Members))
		for mid := range state.Members {
			ids = append(ids, mid)
		}
		sort.Strings(ids)
		if *jsonFlag {
			type memberJSON struct {
				ID   string `json:"id"`
				Name string `json:"name"`
				Role string `json:"role"`
				Kind string `json:"kind"`
			}
			out := make([]memberJSON, 0, len(ids))
			for _, mid := range ids {
				m := state.Members[mid]
				out = append(out, memberJSON{ID: m.ID, Name: m.Name, Role: string(m.Role), Kind: string(m.Kind)})
			}
			data, _ := json.MarshalIndent(out, "", "  ")
			_, _ = fmt.Fprintln(stdout, string(data))
			return int(exitcodes.ExitOK)
		}
		w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
		_, _ = fmt.Fprintln(w, "ID\tNAME\tROLE\tKIND")
		for _, mid := range ids {
			m := state.Members[mid]
			_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", m.ID, m.Name, m.Role, m.Kind)
		}
		_ = w.Flush()
		return int(exitcodes.ExitOK)
	case "remove":
		fs := flag.NewFlagSet("member remove", flag.ExitOnError)
		idFlag := fs.String("identity", "", "Caller identity name (must be the room owner)")
		if err := fs.Parse(args[3:]); err != nil {
			return int(exitcodes.ExitNoInput)
		}
		if fs.NArg() < 1 {
			_, _ = fmt.Fprintln(stderr, "Usage: symroom member remove [--identity <name>] <member_id>")
			return int(exitcodes.ExitNoInput)
		}
		id := resolveIdentity(*idFlag)
		memberID := fs.Arg(0)
		ev, err := room.RemoveMember(roomDir(), memberID, id)
		if err != nil {
			exitMemberError(err)
		}
		_, _ = fmt.Fprintf(stdout, "Member removed: %s (event: %s)\n", memberID, ev.ID)
		return int(exitcodes.ExitOK)
	case "role":
		fs := flag.NewFlagSet("member role", flag.ExitOnError)
		idFlag := fs.String("identity", "", "Caller identity name (must be the room owner)")
		if err := fs.Parse(args[3:]); err != nil {
			return int(exitcodes.ExitNoInput)
		}
		if fs.NArg() < 2 {
			_, _ = fmt.Fprintln(stderr, "Usage: symroom member role [--identity <name>] <member_id> <role>")
			return int(exitcodes.ExitNoInput)
		}
		id := resolveIdentity(*idFlag)
		memberID, roleStr := fs.Arg(0), fs.Arg(1)
		ev, err := room.SetMemberRole(roomDir(), memberID, members.Role(roleStr), id)
		if err != nil {
			exitMemberError(err)
		}
		_, _ = fmt.Fprintf(stdout, "Updated role for %s to %s (event: %s)\n", memberID, roleStr, ev.ID)
		return int(exitcodes.ExitOK)
	default:
		_, _ = fmt.Fprintf(stderr, "Unknown member action: %s\n", sub)
		return int(exitcodes.ExitNoInput)
	}
}
