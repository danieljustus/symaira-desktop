//! Indexed backlink lookup, matching Go sidecar.DB.GetBacklinks.
use std::{collections::BTreeSet, path::Path};

use rusqlite::{OptionalExtension, params, params_from_iter};

use crate::{Sidecar, SidecarError};

#[derive(Clone, Debug, serde::Serialize)]
pub struct InverseRelation {
    pub source: String,
    pub title: String,
    pub property: String,
}

fn references(value: &str, target: &str) -> bool {
    if value.trim().to_lowercase() == target.to_lowercase() {
        return true;
    }
    let mut rest = value;
    while let Some((_, after)) = rest.split_once("[[") {
        let Some((link, after)) = after.split_once("]]") else {
            return false;
        };
        if link
            .split('|')
            .next()
            .unwrap_or_default()
            .trim()
            .to_lowercase()
            == target.to_lowercase()
        {
            return true;
        }
        rest = after;
    }
    false
}

/// Indexed documents and raw edges, retaining SQLite's production query order.
pub type GraphInputs = (Vec<symdesk_vault::ResolveDocument>, Vec<(String, String)>);

impl Sidecar {
    /// Read the local graph without re-parsing potentially changed vault files.
    ///
    /// # Errors
    /// Returns errors reading the derived index.
    pub fn graph_inputs(&self) -> Result<GraphInputs, SidecarError> {
        let mut statement = self
            .connection
            .prepare("SELECT path, title FROM files ORDER BY path ASC")?;
        let rows = statement.query_map([], |row| {
            Ok((row.get::<_, String>(0)?, row.get::<_, String>(1)?))
        })?;
        let mut documents = Vec::new();
        for row in rows {
            let (path, title) = row?;
            let aliases = self
                .link_properties(&path)
                .ok()
                .and_then(|p| p.get("aliases").map(|v| aliases(v)))
                .unwrap_or_default();
            documents.push(symdesk_vault::ResolveDocument {
                path,
                title,
                aliases,
                links: Vec::new(),
            });
        }
        let mut statement = self
            .connection
            .prepare("SELECT from_path, to_path FROM links")?;
        let edges = statement
            .query_map([], |row| Ok((row.get(0)?, row.get(1)?)))?
            .collect::<Result<Vec<_>, _>>()?;
        Ok((documents, edges))
    }

    fn link_properties(
        &self,
        path: &str,
    ) -> Result<std::collections::BTreeMap<String, String>, SidecarError> {
        let mut stmt = self.connection.prepare("SELECT p.key, p.value FROM file_properties p JOIN files f ON f.id = p.file_id WHERE f.path = ?")?;
        Ok(stmt
            .query_map([path], |row| Ok((row.get(0)?, row.get(1)?)))?
            .collect::<Result<_, _>>()?)
    }

    /// Typed frontmatter references followed by body backlinks, in Go order.
    ///
    /// # Errors
    /// Returns database errors while reading indexed notes.
    pub fn inverse_relations(
        &self,
        root: &Path,
        target: &str,
    ) -> Result<Vec<InverseRelation>, SidecarError> {
        let key = super::strip_verbatim_prefix(target);
        let target = key.as_str();
        let root = super::absolute_non_verbatim(root)?;
        let root = root.as_path();
        let base = Path::new(target)
            .file_name()
            .and_then(|v| v.to_str())
            .unwrap_or(target);
        let mut targets = vec![without_extension(base).to_owned(), base.to_owned()];
        if let Ok(props) = self.link_properties(target) {
            if let Some(title) = props.get("title").filter(|v| !v.is_empty()) {
                targets.push(title.clone());
            }
            if let Some(raw) = props.get("aliases") {
                if let Ok(list) = noyalib::from_str::<Vec<String>>(raw)
                    && !list.is_empty()
                {
                    targets.extend(
                        list.into_iter()
                            .map(|v| v.trim().to_owned())
                            .filter(|v| !v.is_empty()),
                    );
                } else {
                    let trimmed = raw.strip_suffix(']').unwrap_or(raw);
                    let trimmed = trimmed.strip_prefix('[').unwrap_or(trimmed);
                    targets.extend(
                        trimmed
                            .split(',')
                            .map(|v| v.trim().trim_matches(['\'', '"']).to_owned())
                            .filter(|v| !v.is_empty()),
                    );
                }
            }
        }
        let mut stmt = self
            .connection
            .prepare("SELECT path, title FROM files ORDER BY path ASC")?;
        let docs = stmt
            .query_map([], |row| {
                Ok((row.get::<_, String>(0)?, row.get::<_, String>(1)?))
            })?
            .collect::<Result<Vec<_>, _>>()?;
        let mut seen = BTreeSet::new();
        let mut results = Vec::new();
        let mut add = |source: &str, title: &str, property: &str| {
            let rel = Path::new(source)
                .strip_prefix(root)
                .map(|v| v.to_string_lossy().replace(std::path::MAIN_SEPARATOR, "/"))
                .unwrap_or_else(|_| source.to_owned());
            if seen.insert((rel.clone(), property.to_owned())) {
                results.push(InverseRelation {
                    source: rel,
                    title: title.to_owned(),
                    property: property.to_owned(),
                });
            }
        };
        for (path, title) in &docs {
            if path == target {
                continue;
            }
            if let Ok(props) = self.link_properties(path) {
                for (key, value) in props {
                    if targets.iter().any(|target| references(&value, target)) {
                        add(path, title, &key);
                    }
                }
            }
        }
        let mut backlinks = self.backlinks(target, false)?;
        backlinks.sort();
        for path in backlinks {
            if path == target {
                continue;
            }
            let title = docs
                .iter()
                .find(|(p, _)| p == &path)
                .map_or("", |(_, title)| title.as_str());
            add(&path, title, "_link");
        }
        Ok(results)
    }
}

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
            None => self.connection.query_row("SELECT id, title FROM files WHERE title = ? COLLATE NOCASE OR path LIKE ? COLLATE NOCASE OR path LIKE ? COLLATE NOCASE LIMIT 1", params![target, format!("%/{base}"), format!("%{}{base}", std::path::MAIN_SEPARATOR)], |r| Ok((r.get::<_, i64>(0)?, r.get::<_, String>(1)?))).optional()?,
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
