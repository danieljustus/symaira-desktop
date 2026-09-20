//! Conflict-copy path convention, ported from the Go CLI's `deriveOriginalPath`
//! (`cmd/symdesk/conflict.go:99`).
//!
//! The same rule is what `symdesk doctor` uses to recognise conflicted copies
//! (`cmd/symdesk/doctor.go:406`), so the CLI and the Rust port must agree on it.
//!
//! Go splits the base name at the final `.` — which is why `.env` keeps its whole
//! name as the "extension" — strips **one** trailing ` conflicted copy`, or
//! failing that **one** trailing ` 2`, from the name part, and rejoins the
//! directory. Every value in `tests/conflict_contracts.rs` was produced by
//! running the Go function itself.
//!
//! Degenerate inputs are deliberately out of contract: Go maps `""` to `"."` and
//! `"vault/"` to `"vault/vault"`, because `filepath.Dir`/`Base`/`Join` clean those
//! paths. Neither can occur for a real conflict file, so this port returns the
//! input unchanged instead of reproducing the quirk.

/// The suffix Syncthing-style conflict copies carry. Note the leading space:
/// `deriveOriginalPath` strips it as a **suffix** (`cmd/symdesk/conflict.go:105`).
pub const CONFLICT_COPY_SUFFIX: &str = " conflicted copy";

/// The substring `symdesk doctor` looks for. Deliberately a different literal
/// from [`CONFLICT_COPY_SUFFIX`]: the Go doctor check has no leading space
/// (`cmd/symdesk/doctor.go:405`), so `"noteconflicted copy.md"` is reported
/// there even though the derivation above would leave it alone.
pub const SYNC_CONFLICT_MARKER: &str = "conflicted copy";

/// Returns the path a conflict copy belongs to, mirroring the Go
/// `deriveOriginalPath` for every well-formed vault path.
#[must_use]
pub fn derive_original_path(conflict_path: &str) -> String {
    let (directory, base) = match conflict_path.rfind('/') {
        Some(index) => (&conflict_path[..index], &conflict_path[index + 1..]),
        None => (".", conflict_path),
    };
    if base.is_empty() {
        // Out of contract; see the module documentation.
        return conflict_path.to_owned();
    }
    let (name, extension) = match base.rfind('.') {
        Some(index) => (&base[..index], &base[index..]),
        None => (base, ""),
    };
    let stem = name
        .strip_suffix(CONFLICT_COPY_SUFFIX)
        .or_else(|| name.strip_suffix(" 2"))
        .unwrap_or(name);
    let file_name = format!("{stem}{extension}");
    if directory == "." {
        file_name
    } else {
        format!("{directory}/{file_name}")
    }
}

/// Whether a file's base name is reported as an iCloud sync conflict by
/// `symdesk doctor`, mirroring the Go check verbatim
/// (`cmd/symdesk/doctor.go:404`):
///
/// ```text
/// strings.Contains(base, " 2.md") || strings.Contains(base, "conflicted copy")
/// ```
///
/// The Go rule deliberately only recognises the `.md` form of the numbered
/// suffix; this port keeps that behaviour instead of widening it.
#[must_use]
pub fn is_sync_conflict_base_name(base_name: &str) -> bool {
    base_name.contains(" 2.md") || base_name.contains(SYNC_CONFLICT_MARKER)
}
