// Command extrecord runs an external (non-Go) fixture recorder in --check mode
// from the repository root, so portgen can verify fixtures that Python
// recorders wrote against the real Go oracle (RUST-019 frozen contract).
//
// Usage: extrecord -- python3 scripts/rust-port/source_record.py --check
package main

import (
	"fmt"
	"os"
	"os/exec"
)

func main() {
	args := os.Args[1:]
	if len(args) > 0 && args[0] == "--" {
		args = args[1:]
	}
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "FAIL usage: extrecord -- COMMAND [ARGS...]")
		os.Exit(2)
	}
	command := exec.Command(args[0], args[1:]...) // #nosec G204 -- developer-supplied recorder command
	command.Stdout, command.Stderr, command.Stdin = os.Stdout, os.Stderr, nil
	if err := command.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "FAIL %v\n", err)
		os.Exit(1)
	}
}
