//! Read-only duplicate planning and explicit transactional import application.
use crate::import::{ImportRow, ValidationIssue};
use rusqlite::{Connection, OptionalExtension, Transaction};
use serde::{Deserialize, Serialize};

#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "PascalCase")]
pub struct DuplicateCandidate {
    pub row_number: i64,
    #[serde(rename = "ExistingPersonID")]
    pub existing_person_id: String,
    pub existing_name: String,
    pub matched_on: String,
    pub exact_source_rematch: bool,
}
#[derive(Clone, Debug, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "PascalCase")]
pub struct ImportPlan {
    pub source: String,
    pub rows: Option<Vec<ImportRow>>,
    pub issues: Option<Vec<ValidationIssue>>,
    pub duplicates: Option<Vec<DuplicateCandidate>>,
}
#[derive(Clone, Debug, Serialize, Deserialize)]
#[serde(rename_all = "PascalCase")]
pub struct RowResolution {
    pub row_number: i64,
    pub resolution: String,
    #[serde(rename = "MergePersonID")]
    pub merge_person_id: String,
}
#[derive(Clone, Debug, Default, Serialize, Deserialize, PartialEq, Eq)]
#[serde(rename_all = "PascalCase")]
pub struct RunResult {
    pub source: String,
    pub created: i64,
    pub merged: i64,
    pub skipped: i64,
    pub failed: i64,
    pub errors: Option<Vec<ValidationIssue>>,
}

pub(crate) fn go_lower(s: &str) -> String {
    s.chars()
        .map(|c| c.to_lowercase().next().unwrap_or(c))
        .collect()
}

pub fn normalize(kind: &str, raw: &str) -> String {
    let s = raw.trim();
    match kind {
        "phone" => s
            .chars()
            .enumerate()
            .filter(|(i, c)| (*i == 0 && *c == '+') || c.is_numeric() && is_decimal_digit(*c))
            .map(|(_, c)| c)
            .collect(),
        "url" => go_lower(s.trim_end_matches('/')),
        "handle" => {
            let lower = go_lower(s);
            lower.strip_prefix('@').unwrap_or(&lower).to_owned()
        }
        "address" => go_lower(&s.split_whitespace().collect::<Vec<_>>().join(" ")),
        _ => go_lower(s),
    }
}
// unicode.IsDigit is Nd, not Rust's broader is_numeric (which includes Ⅳ).
fn is_decimal_digit(c: char) -> bool {
    unicode_general_category::get_general_category(c)
        == unicode_general_category::GeneralCategory::DecimalNumber
}

/// Planning never writes. Source identity takes precedence, then email, phone,
/// then SQLite NOCASE name matching. Only the first reason per person survives.
pub fn plan(
    db: &Connection,
    source: &str,
    rows: Option<Vec<ImportRow>>,
    issues: Option<Vec<ValidationIssue>>,
) -> rusqlite::Result<ImportPlan> {
    let mut duplicates = Vec::new();
    for row in rows.as_deref().unwrap_or_default() {
        let mut candidates = Vec::new();
        let mut add = |id: String, name: String, matched_on: String, exact_source_rematch: bool| {
            if !candidates
                .iter()
                .any(|c: &DuplicateCandidate| c.existing_person_id == id)
            {
                candidates.push(DuplicateCandidate {
                    row_number: row.row_number,
                    existing_person_id: id,
                    existing_name: name,
                    matched_on,
                    exact_source_rematch,
                });
            }
        };
        if !row.source_ref.is_empty() {
            let m:Option<(String,String)>=db.query_row("SELECT p.id,p.display_name FROM import_sources src JOIN persons p ON p.id=src.person_id WHERE src.source_kind=? AND src.source_ref=?",(source,&row.source_ref),|r|Ok((r.get(0)?,r.get(1)?))).optional()?;
            if let Some((id, name)) = m {
                add(
                    id,
                    name,
                    format!("source:{source}:{}", row.source_ref),
                    true,
                );
            }
        }
        for (kind, values) in [("email", &row.emails), ("phone", &row.phones)] {
            for raw in values.as_deref().unwrap_or_default() {
                let n = normalize(kind, raw);
                if n.is_empty() {
                    continue;
                }
                let mut stmt=db.prepare("SELECT DISTINCT cp.normalized_value,p.id,p.display_name FROM contact_points cp JOIN persons p ON p.id=cp.person_id WHERE cp.kind=? AND cp.normalized_value IN (?)")?;
                for m in stmt.query_map((kind, n), |r| {
                    Ok((r.get::<_, String>(1)?, r.get::<_, String>(2)?))
                })? {
                    let (id, name) = m?;
                    add(id, name, format!("{kind}:{raw}"), false);
                }
            }
        }
        if !row.display_name.is_empty() {
            let mut stmt = db.prepare(
                "SELECT id,display_name FROM persons WHERE display_name COLLATE NOCASE IN (?)",
            )?;
            for m in stmt.query_map([&row.display_name], |r| {
                Ok((r.get::<_, String>(0)?, r.get::<_, String>(1)?))
            })? {
                let (id, name) = m?;
                add(id, name, format!("name:{}", row.display_name), false);
            }
        }
        duplicates.extend(candidates);
    }
    Ok(ImportPlan {
        source: source.into(),
        rows,
        issues,
        duplicates: (!duplicates.is_empty()).then_some(duplicates),
    })
}

/// Apply with explicit UUID and RFC3339Nano UTC clock providers. These seams
/// keep deterministic fixtures independent of wall time or random identifiers.
/// No per-row savepoint: like Go, a failed row can leave earlier statements in
/// the outer transaction intact. Unexpected transaction errors roll back it all.
pub fn apply(
    db: &mut Connection,
    plan: &ImportPlan,
    resolutions: &[RowResolution],
    mut new_id: impl FnMut() -> String,
    mut now: impl FnMut() -> String,
) -> Result<RunResult, String> {
    let tx = db
        .transaction()
        .map_err(|e| format!("importer.Apply: import apply failed: {e}"))?;
    let mut result = RunResult {
        source: plan.source.clone(),
        ..RunResult::default()
    };
    for row in plan.rows.as_deref().unwrap_or_default() {
        let res = resolutions
            .iter()
            .rev()
            .find(|r| r.row_number == row.row_number);
        let dup = plan
            .duplicates
            .as_deref()
            .unwrap_or_default()
            .iter()
            .any(|d| d.row_number == row.row_number);
        if res.is_some_and(|r| r.resolution == "skip") || dup && res.is_none() {
            result.skipped += 1;
            continue;
        }
        let action = if dup {
            res.map(|r| r.resolution.as_str()).unwrap_or_default()
        } else {
            "create"
        };
        let mut issue_field = "";
        let outcome: Result<(), String> = match action {
            "create" => create_row(&tx, row, &mut new_id, &mut now).and_then(|id| {
                link_source(
                    &tx,
                    &plan.source,
                    &row.source_ref,
                    &id,
                    &mut new_id,
                    &mut now,
                )
                .map_err(|e| e.to_string())
            }),
            "merge" => {
                let target = &res.unwrap().merge_person_id;
                if target.is_empty() {
                    issue_field = "merge_person_id";
                    Err("merge resolution requires a target person id".into())
                } else {
                    merge_row(&tx, row, target, &mut new_id, &mut now).and_then(|()| {
                        link_source(
                            &tx,
                            &plan.source,
                            &row.source_ref,
                            target,
                            &mut new_id,
                            &mut now,
                        )
                        .map_err(|e| e.to_string())
                    })
                }
            }
            other => {
                issue_field = "resolution";
                Err(format!("unknown resolution: {other}"))
            }
        };
        match outcome {
            Ok(()) => {
                if action == "create" {
                    result.created += 1
                } else {
                    result.merged += 1
                }
            }
            Err(message) => {
                result.failed += 1;
                result
                    .errors
                    .get_or_insert_with(Vec::new)
                    .push(ValidationIssue {
                        row_number: row.row_number,
                        field: issue_field.into(),
                        message,
                    });
            }
        }
    }
    tx.execute("INSERT INTO import_runs (id,source_kind,created,merged,skipped,failed) VALUES (?,?,?,?,?,?)",(&new_id(),&plan.source,result.created,result.merged,result.skipped,result.failed)).map_err(|e|format!("importer.Apply: import apply failed: {e}"))?;
    tx.commit()
        .map_err(|e| format!("importer.Apply: import apply failed: {e}"))?;
    Ok(result)
}
fn insert_points(
    tx: &Transaction<'_>,
    id: &str,
    row: &ImportRow,
    new_id: &mut impl FnMut() -> String,
    now: &mut impl FnMut() -> String,
) -> rusqlite::Result<()> {
    for (kind, values) in [
        ("email", &row.emails),
        ("phone", &row.phones),
        ("url", &row.urls),
    ] {
        for raw in values.as_deref().unwrap_or_default() {
            let ts = now();
            tx.execute("INSERT OR IGNORE INTO contact_points (id,person_id,kind,raw_value,normalized_value,label,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?)",(&new_id(),id,kind,raw,normalize(kind,raw),"",&ts,&ts))?;
        }
    }
    Ok(())
}
fn create_row(
    tx: &Transaction<'_>,
    row: &ImportRow,
    new_id: &mut impl FnMut() -> String,
    now: &mut impl FnMut() -> String,
) -> Result<String, String> {
    let id = new_id();
    let ts = now();
    let run = || -> rusqlite::Result<()> {
        tx.execute("INSERT INTO persons (id,display_name,given_name,family_name,notes,source,created_at,updated_at) VALUES (?,?,?,?,?,?,?,?)",(&id,&row.display_name,&row.given_name,&row.family_name,"","import",&ts,&ts))?;
        Ok(())
    };
    run().map_err(|e| e.to_string())?;
    insert_points(tx, &id, row, new_id, now).map_err(|e| e.to_string())?;
    if !row.organization_name.is_empty() {
        let org: Option<String> = tx
            .query_row(
                "SELECT id FROM organizations WHERE name=? COLLATE NOCASE",
                [&row.organization_name],
                |r| r.get(0),
            )
            .optional()
            .map_err(|e| e.to_string())?;
        let org = match org {
            Some(id) => id,
            None => {
                let id = new_id();
                let ts = now();
                tx.execute("INSERT INTO organizations (id,name,notes,source,created_at,updated_at) VALUES (?,?,?,?,?,?)",(&id,&row.organization_name,"","import",&ts,&ts)).map_err(|e|e.to_string())?;
                id
            }
        };
        tx.execute("INSERT INTO organization_memberships (id,person_id,organization_id,role,title,created_at,updated_at) VALUES (?,?,?,?,?,?,?)",(&new_id(),&id,&org,"",&row.title,&ts,&ts)).map_err(|e|e.to_string())?;
    }
    Ok(id)
}
fn merge_row(
    tx: &Transaction<'_>,
    row: &ImportRow,
    target: &str,
    new_id: &mut impl FnMut() -> String,
    now: &mut impl FnMut() -> String,
) -> Result<(), String> {
    let count: i64 = tx
        .query_row("SELECT COUNT(*) FROM persons WHERE id=?", [target], |r| {
            r.get(0)
        })
        .map_err(|e| e.to_string())?;
    if count == 0 {
        return Err("importer.mergeIntoRow: merge target person does not exist".into());
    }
    insert_points(tx, target, row, new_id, now).map_err(|e| e.to_string())
}
fn link_source(
    tx: &Transaction<'_>,
    source: &str,
    source_ref: &str,
    person: &str,
    new_id: &mut impl FnMut() -> String,
    now: &mut impl FnMut() -> String,
) -> rusqlite::Result<()> {
    if !source_ref.is_empty() {
        tx.execute("INSERT INTO import_sources (id,source_kind,source_ref,person_id,imported_at) VALUES (?,?,?,?,?) ON CONFLICT (source_kind,source_ref) DO UPDATE SET person_id=excluded.person_id,imported_at=excluded.imported_at",(&new_id(),source,source_ref,person,&now()))?;
    }
    Ok(())
}
