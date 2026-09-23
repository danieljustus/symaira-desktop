//! Daily vault activity records used by document mutations.

use std::{
    io::{self, Write},
    path::Path,
};

use cap_std::fs::{Dir, DirBuilder, OpenOptions};
use serde::Serialize;
use time::{OffsetDateTime, format_description::well_known::Rfc3339};

#[derive(Serialize)]
struct Entry<'a> {
    ts: String,
    event: &'a str,
    path: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    title: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    details: &'a str,
}

/// Appends one Go-compatible activity event; callers may ignore errors when
/// matching Go's best-effort document mutation behavior.
pub fn append_activity(
    vault_root: &Path,
    event: &str,
    path: &str,
    title: &str,
    details: &str,
) -> io::Result<()> {
    append_at(
        vault_root,
        OffsetDateTime::now_utc(),
        event,
        path,
        title,
        details,
    )
}

fn append_at(
    vault_root: &Path,
    at: OffsetDateTime,
    event: &str,
    path: &str,
    title: &str,
    details: &str,
) -> io::Result<()> {
    let root = Dir::open_ambient_dir(vault_root, cap_std::ambient_authority())?;
    let mut builder = DirBuilder::new();
    builder.recursive(true);
    #[cfg(unix)]
    {
        use cap_std::fs::DirBuilderExt;
        builder.mode(0o750);
    }
    root.create_dir_with(Path::new(".symdesk/journal"), &builder)?;
    let date = at.date().to_string();
    let relative = format!(".symdesk/journal/{date}.ndjson");
    let mut options = OpenOptions::new();
    options.create(true).append(true);
    #[cfg(unix)]
    {
        use cap_std::fs::OpenOptionsExt;
        options.mode(0o644);
    }
    let mut file = root.open_with(Path::new(&relative), &options)?;
    let ts = at
        .replace_nanosecond(0)
        .map_err(io::Error::other)?
        .format(&Rfc3339)
        .map_err(io::Error::other)?;
    let entry = Entry {
        ts,
        event,
        path,
        title,
        details,
    };
    let encoded = serde_json::to_string(&entry).map_err(io::Error::other)?;
    // Go encoding/json escapes HTML-sensitive characters and line separators.
    let encoded = encoded
        .replace('&', "\\u0026")
        .replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029");
    file.write_all(encoded.as_bytes())?;
    file.write_all(b"\n")
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn matches_go_daily_ndjson_encoding() {
        let dir = std::env::temp_dir().join(format!("symdesk-journal-{}", std::process::id()));
        std::fs::create_dir_all(&dir).unwrap();
        let at = OffsetDateTime::parse("2026-01-02T03:04:05.123Z", &Rfc3339).unwrap();
        append_at(
            &dir,
            at,
            "file_removed",
            "docs/a.md",
            "A <&>",
            "moved to trash",
        )
        .unwrap();
        let data = std::fs::read_to_string(dir.join(".symdesk/journal/2026-01-02.ndjson")).unwrap();
        assert_eq!(
            data,
            "{\"ts\":\"2026-01-02T03:04:05Z\",\"event\":\"file_removed\",\"path\":\"docs/a.md\",\"title\":\"A \\u003c\\u0026\\u003e\",\"details\":\"moved to trash\"}\n"
        );
        std::fs::remove_dir_all(dir).unwrap();
    }
}
