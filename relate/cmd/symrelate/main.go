// Command symrelate is the standalone CLI for Symaira Relate, a local-first
// contact and relationship manager. It has no compile-time dependency on
// any other Symaira binary.
package main

import (
	"context"
	"os"

	"github.com/danieljustus/symaira-relate/internal/cli"
)

func main() {
	ctx := context.Background()
	io := cli.IO{Stdout: os.Stdout, Stderr: os.Stderr, Stdin: os.Stdin}
	os.Exit(cli.Run(ctx, io, os.Args[1:]))
}
