//! Indexed document metadata queries (Go internal/sidecar/db.go).
use crate::{Sidecar, SidecarError};
use rusqlite::{params_from_iter, types::Value};
use serde::Serialize;

#[derive(Default)]
pub struct DocsFilter {
    pub document_type: String,
    pub file_type: String,
    pub status: String,
    pub person: String,
    pub correspondent: String,
    pub year: String,
    pub due_before: String,
    pub min_confidence: Option<i64>,
    pub max_confidence: Option<i64>,
    pub asn: Option<i64>,
}

#[derive(Debug, Serialize)]
pub struct DocsResult {
    pub path: String,
    pub title: String,
    #[serde(rename = "type", skip_serializing_if = "String::is_empty")]
    pub file_type: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub document_date: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub person: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub status: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub due_date: String,
    #[serde(skip_serializing_if = "is_zero")]
    pub confidence: i64,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub correspondent: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub document_type: String,
    #[serde(skip_serializing_if = "is_zero")]
    pub asn: i64,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub tags: Vec<String>,
}
fn is_zero(value: &i64) -> bool {
    *value == 0
}

#[derive(Debug, Serialize)]
pub struct ReviewResult {
    pub path: String,
    pub title: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub status: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub document_type: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub document_date: String,
    pub confidence: i64,
    pub reasons: Vec<String>,
}

impl Sidecar {
    /// Queries only the derived index; does not refresh it from Markdown.
    pub fn docs_list(&self, filter: &DocsFilter) -> Result<Vec<DocsResult>, SidecarError> {
        let mut sql = String::from(
            "SELECT f.path,f.title,f.type,COALESCE(f.document_date,''),COALESCE(f.person,''),COALESCE(f.status,''),COALESCE(f.due_date,''),COALESCE(f.confidence,0),COALESCE(c.value,''),COALESCE(t.value,''),COALESCE(f.asn,0),COALESCE((SELECT value FROM file_properties WHERE file_id=f.id AND key='tags'),'') FROM files f LEFT JOIN file_properties c ON c.file_id=f.id AND c.key='correspondent' LEFT JOIN file_properties t ON t.file_id=f.id AND t.key='document_type' WHERE 1=1",
        );
        let mut args: Vec<Value> = Vec::new();
        for (column, value) in [
            ("f.type", &filter.file_type),
            ("f.status", &filter.status),
            ("f.person", &filter.person),
            ("c.value", &filter.correspondent),
            ("t.value", &filter.document_type),
        ] {
            if !value.is_empty() {
                sql.push_str(&format!(" AND {column} = ?"));
                args.push(Value::Text(value.clone()));
            }
        }
        if !filter.year.is_empty() {
            sql.push_str(" AND f.document_date LIKE ?");
            args.push(Value::Text(format!("{}%", filter.year)));
        }
        if !filter.due_before.is_empty() {
            sql.push_str(" AND f.due_date != '' AND f.due_date <= ?");
            args.push(Value::Text(filter.due_before.clone()));
        }
        for (expression, value) in [
            ("f.confidence >= ?", filter.min_confidence),
            ("f.confidence <= ?", filter.max_confidence),
            ("f.asn = ?", filter.asn),
        ] {
            if let Some(value) = value {
                sql.push_str(&format!(" AND {expression}"));
                args.push(Value::Integer(value));
            }
        }
        sql.push_str(" ORDER BY f.path ASC");
        let mut stmt = self.connection.prepare(&sql)?;
        let rows = stmt.query_map(params_from_iter(args), |r| {
            let raw: String = r.get(11)?;
            Ok(DocsResult {
                path: r.get(0)?,
                title: r.get(1)?,
                file_type: r.get(2)?,
                document_date: r.get(3)?,
                person: r.get(4)?,
                status: r.get(5)?,
                due_date: r.get(6)?,
                confidence: r.get(7)?,
                correspondent: r.get(8)?,
                document_type: r.get(9)?,
                asn: r.get(10)?,
                tags: parse_tags(&raw),
            })
        })?;
        rows.collect::<Result<Vec<_>, _>>().map_err(Into::into)
    }

    /// Returns the review queue in stored absolute-path order.
    pub fn docs_review(&self, threshold: i64) -> Result<Vec<ReviewResult>, SidecarError> {
        let mut stmt = self.connection.prepare("SELECT f.path,f.title,COALESCE(f.status,''),COALESCE(t.value,''),COALESCE(f.document_date,''),COALESCE(f.confidence,0) FROM files f LEFT JOIN file_properties t ON t.file_id=f.id AND t.key='document_type' LEFT JOIN file_properties i ON i.file_id=f.id AND i.key='review_ignored' WHERE (f.confidence < ? OR t.value IS NULL OR t.value='' OR f.document_date IS NULL OR f.document_date='') AND (i.value IS NULL OR i.value != 'true') ORDER BY f.path ASC")?;
        let rows = stmt.query_map([threshold], |r| {
            let mut row = ReviewResult {
                path: r.get(0)?,
                title: r.get(1)?,
                status: r.get(2)?,
                document_type: r.get(3)?,
                document_date: r.get(4)?,
                confidence: r.get(5)?,
                reasons: Vec::new(),
            };
            if row.confidence < threshold {
                row.reasons
                    .push(format!("confidence {} < {threshold}", row.confidence));
            }
            if row.document_type.is_empty() {
                row.reasons.push("missing document_type".into());
            }
            if row.document_date.is_empty() {
                row.reasons.push("missing document_date".into());
            }
            Ok(row)
        })?;
        rows.collect::<Result<Vec<_>, _>>().map_err(Into::into)
    }
}

fn parse_tags(raw: &str) -> Vec<String> {
    let raw = raw.trim();
    let raw = raw.strip_suffix(']').unwrap_or(raw);
    let raw = raw.strip_prefix('[').unwrap_or(raw);
    raw.split(|c: char| c == ',' || c.is_whitespace())
        .map(|s| s.trim_matches(['\'', '"']))
        .filter(|s| !s.is_empty())
        .map(str::to_owned)
        .collect()
}
