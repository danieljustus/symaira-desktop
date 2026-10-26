//! Indexed backlink lookup, including title/alias and opaque contact targets.
use std::{collections::BTreeSet, path::Path};

use rusqlite::{OptionalExtension, params_from_iter};

use crate::{Sidecar, SidecarError};

/// Recognizes the opaque contact graph namespace, never a filesystem path.
#[must_use]
pub fn is_contact_reference(target: &str) -> bool {
    target.strip_prefix("contact:").is_some_and(|reference| {
        let parts: Vec<_> = reference.split(':').collect();
        parts.len() == 3 && parts.iter().all(|part| !part.is_empty())
    })
}

fn add_target(targets: &mut BTreeSet<String>, value: &str) {
    let value = value.trim();
    if value.is_empty() {
        return;
    }
    targets.insert(value.to_owned());
    targets.insert(symdesk_vault::go_lowercase(value));
    let stem = without_extension(value);
    if !stem.is_empty() {
        targets.insert(stem.to_owned());
        targets.insert(symdesk_vault::go_lowercase(stem));
    }
}

fn without_extension(path: &str) -> &str {
    let base = path
        .rsplit(std::path::MAIN_SEPARATOR)
        .next()
        .unwrap_or(path);
    match base.rfind('.') {
        Some(dot) => &path[..path.len() - base.len() + dot],
        None => path,
    }
}

fn aliases(raw: &str) -> Vec<String> {
    let raw = raw.trim();
    if raw.is_empty() {
        return Vec::new();
    }
    if (raw.starts_with('[') || raw.starts_with('-'))
        && let Ok(values) = noyalib::from_str::<Vec<String>>(raw)
    {
        let values: Vec<_> = values
            .into_iter()
            .map(|value| value.trim().to_owned())
            .filter(|value| !value.is_empty())
            .collect();
        if !values.is_empty() {
            return values;
        }
    }
    if raw.contains(',') {
        let values: Vec<_> = raw
            .trim_end_matches(']')
            .trim_start_matches('[')
            .split(',')
            .map(|value| value.trim().trim_matches(['\"', '\'']).to_owned())
            .filter(|value| !value.is_empty())
            .collect();
        if !values.is_empty() {
            return values;
        }
    }
    if let Ok(value) = noyalib::from_str::<String>(raw) {
        let value = value.trim();
        if !value.is_empty() && !value.starts_with('[') {
            return vec![value.to_owned()];
        }
    }
    let value = raw
        .trim_end_matches(']')
        .trim_start_matches('[')
        .trim_matches(['\"', '\'']);
    if value.is_empty() {
        Vec::new()
    } else {
        vec![value.to_owned()]
    }
}

impl Sidecar {
    /// Returns distinct indexed sources linking to a path, title, or alias.
    ///
    /// # Errors
    /// Returns an error when the link query cannot read the derived database.
    pub fn get_backlinks(&self, path: &str) -> Result<Vec<String>, SidecarError> {
        let mut targets = BTreeSet::new();
        add_target(&mut targets, path);
        let base = Path::new(path)
            .file_name()
            .map_or(path, |base| base.to_str().unwrap_or(path));
        if !is_contact_reference(path) {
            add_target(&mut targets, base);
            add_target(&mut targets, without_extension(base));
        }
        let exact: Option<(i64, String)> = self
            .connection
            .query_row(
                "SELECT id, title FROM files WHERE path = ?",
                [path],
                |row| Ok((row.get(0)?, row.get(1)?)),
            )
            .optional()
            .unwrap_or_default();
        let document = exact.or_else(|| self.connection.query_row(
            "SELECT id, title FROM files WHERE title = ? COLLATE NOCASE OR path LIKE ? COLLATE NOCASE LIMIT 1",
            [path, &format!("%/{base}")], |row| Ok((row.get::<_,i64>(0)?, row.get::<_,String>(1)?)),
        ).ok());
        if let Some((file_id, title)) = document {
            add_target(&mut targets, &title);
            let aliases_raw: Option<String> = self
                .connection
                .query_row(
                    "SELECT value FROM file_properties WHERE file_id = ? AND key = 'aliases'",
                    [file_id],
                    |row| row.get(0),
                )
                .optional()
                .unwrap_or_default()
                .flatten();
            if let Some(raw) = aliases_raw {
                for alias in aliases(&raw) {
                    add_target(&mut targets, &alias);
                }
            }
        }
        if targets.is_empty() {
            return Ok(Vec::new());
        }
        let placeholders = vec!["?"; targets.len()].join(",");
        let mut statement = self.connection.prepare(&format!(
            "SELECT DISTINCT from_path FROM links WHERE to_path IN ({placeholders})"
        ))?;
        let rows = statement.query_map(params_from_iter(targets), |row| row.get(0))?;
        rows.collect::<Result<Vec<String>, _>>().map_err(Into::into)
    }
}
