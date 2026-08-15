package main

// jsonReportedError marks an error whose diagnostic the command already wrote
// to stdout as a complete JSON document.
//
// With `--json`, `main` normally prints a `{"error": ...}` envelope for any
// error returned from a command. When the command has already emitted a full
// report — `doctor --json` writes one carrying `"overall":"error"` — that
// envelope lands as a *second* JSON document on the same stream, and any
// strict decoder fails on the trailing bytes and discards the diagnosis
// (issue #438).
//
// Wrapping the error keeps the exit code intact (the wrapped error still
// unwraps to its `*exitcodes.CLIError`) while telling `main` to skip the
// envelope.
type jsonReportedError struct {
	err error
}

func (e jsonReportedError) Error() string { return e.err.Error() }

func (e jsonReportedError) Unwrap() error { return e.err }
