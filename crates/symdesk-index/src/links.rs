//! Indexed backlink lookup, matching Go sidecar.DB.GetBacklinks.
use std::{collections::BTreeSet, path::Path};

use rusqlite::{OptionalExtension, params, params_from_iter};

use crate::{Sidecar, SidecarError};

fn without_extension(value: &str) -> &str {
    Path::new(value)
        .extension()
        .and_then(|v| v.to_str())
        .and_then(|v| value.strip_suffix(&format!(".{v}")))
        .unwrap_or(value)
}

fn aliases(raw: &str) -> Vec<String> {
    let raw = raw.trim();
    if (raw.starts_with('[') || raw.starts_with('-'))
        && let Ok(list) = noyalib::from_str::<Vec<String>>(raw)
    {
        let list: Vec<_> = list
            .into_iter()
            .map(|v| v.trim().to_owned())
            .filter(|v| !v.is_empty())
            .collect();
        if !list.is_empty() {
            return list;
        }
    }
    if raw.contains(',') {
        return raw
            .trim_start_matches('[')
            .trim_end_matches(']')
            .split(',')
            .map(|v| v.trim().trim_matches(['\'', '"']).to_owned())
            .filter(|v| !v.is_empty())
            .collect();
    }
    if let Ok(single) = noyalib::from_str::<String>(raw)
        && !single.trim().is_empty()
        && !single.trim().starts_with('[')
    {
        return vec![single.trim().to_owned()];
    }
    let cleaned = raw
        .trim_start_matches('[')
        .trim_end_matches(']')
        .trim_matches(['\'', '"']);
    if cleaned.is_empty() {
        Vec::new()
    } else {
        vec![cleaned.to_owned()]
    }
}

impl Sidecar {
    /// Returns distinct source paths in the SQLite query's order.
    ///
    /// # Errors
    /// Returns database errors from backlink lookup.
    pub fn backlinks(&self, target: &str, contact: bool) -> Result<Vec<String>, SidecarError> {
        let mut targets = BTreeSet::new();
        let mut add = |value: &str| {
            let value = value.trim();
            if !value.is_empty() {
                targets.insert(value.to_owned());
                targets.insert(value.to_lowercase());
                let stem = without_extension(value);
                if !stem.is_empty() {
                    targets.insert(stem.to_owned());
                    targets.insert(stem.to_lowercase());
                }
            }
        };
        add(target);
        let base = Path::new(target)
            .file_name()
            .and_then(|v| v.to_str())
            .unwrap_or(target);
        if !contact {
            add(base);
            add(without_extension(base));
        }
        let row = self
            .connection
            .query_row(
                "SELECT id, title FROM files WHERE path = ?",
                [target],
                |r| Ok((r.get::<_, i64>(0)?, r.get::<_, String>(1)?)),
            )
            .optional()?;
        let row = match row {
            Some(row) => Some(row),
            None => self.connection.query_row("SELECT id, title FROM files WHERE title = ? COLLATE NOCASE OR path LIKE ? COLLATE NOCASE LIMIT 1", params![target, format!("%/{base}")], |r| Ok((r.get::<_, i64>(0)?, r.get::<_, String>(1)?))).optional()?,
        };
        if let Some((id, title)) = row {
            add(&title);
            let raw: Option<String> = self
                .connection
                .query_row(
                    "SELECT value FROM file_properties WHERE file_id = ? AND key = 'aliases'",
                    [id],
                    |r| r.get(0),
                )
                .optional()?;
            if let Some(raw) = raw {
                for alias in aliases(&raw) {
                    add(&alias);
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
        Ok(statement
            .query_map(params_from_iter(targets), |r| r.get(0))?
            .collect::<Result<Vec<String>, _>>()?)
    }
}
