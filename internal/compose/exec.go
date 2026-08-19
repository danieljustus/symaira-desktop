package compose

import (
	"bytes"
	"context"
	"io"
	"os/exec"
	"time"
)

// toolOpts configures an individual runTool invocation. The zero value runs
// the tool with no stdin and no WaitDelay override.
type toolOpts struct {
	// Stdin, if non-nil, is piped to the subprocess's standard input.
	Stdin io.Reader
	// WaitDelay, if non-zero, bounds how long cmd.Wait blocks after the
	// process is killed for context cancellation/timeout — see
	// exec.Cmd.WaitDelay. This matters for tools that may spawn a
	// grandchild process: without it, a hung grandchild can keep the
	// pipes open and block Wait past the context's own deadline.
	WaitDelay time.Duration
}

// runTool is the single audited subprocess-execution entry point for
// internal/compose. Every sibling-tool invocation in this package goes
// through it, so the exec.Command gosec suppression lives in one reviewed
// place instead of at every call site. bin must already be a resolved path
// (from ResolveFunc/Resolve, which checks $SYMAIRA_BIN, the managed-runtime
// directory, and finally PATH) — never an unresolved bare name subject to
// its own PATH search inside this function. args are always CLI
// flags/values assembled by this package's callers, never a shell command
// line, so there is no shell-injection surface here.
//
//nolint:gosec // G204: bin is a pre-resolved path from ResolveFunc/Resolve, not unsanitized user/network input; args are a fixed argv, never shell-interpreted. Single audited exec site for internal/compose.
func runTool(ctx context.Context, bin string, opts toolOpts, args ...string) (stdout, stderr *bytes.Buffer, err error) {
	cmd := exec.CommandContext(ctx, bin, args...)
	if opts.Stdin != nil {
		cmd.Stdin = opts.Stdin
	}
	if opts.WaitDelay > 0 {
		cmd.WaitDelay = opts.WaitDelay
	}
	var out, errBuf bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &errBuf
	err = cmd.Run()
	return &out, &errBuf, err
}
