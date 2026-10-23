//! Text expansion shared by the Go-recorded CLI replays
//! (`flag_grammar_commands`, `io_error_commands`).
//!
//! A recorded stream holds two kinds of `/`:
//!
//! * a plain `/` is a host path separator produced by Go's `filepath`
//!   functions (`open nope/room.toml`, `.symroom/index.sqlite` after
//!   `index rebuild`); Windows prints it as `\`;
//! * a `/` inside a Go source string literal (doctor's `create/select`,
//!   `rebuild .symroom/index.sqlite`) is printed unchanged on every host. The
//!   Go generator records it as [`SLASH_TOKEN`] (`ioErrorSlashLiterals` in
//!   `cmd/symroom/port_ioerror_cli_contract_test.go`).

/// Recorded form of a literal `/` that is not a path separator.
pub const SLASH_TOKEN: &str = "<slash>";

/// The separator Go's `filepath` prints on this host.
pub const HOST_SEPARATOR: &str = if cfg!(windows) { "\\" } else { "/" };

/// Rewrites host path separators to `separator` and restores literal slashes.
///
/// Order matters: the token contains no `/`, so the separator pass cannot
/// touch it, and it is resolved last.
pub fn expand_separators(text: &str, separator: &str) -> String {
    text.replace('/', separator).replace(SLASH_TOKEN, "/")
}
