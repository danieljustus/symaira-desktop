use std::path::Path;

use rusqlite::{Connection, params_from_iter, types::Value};
use symdesk_core::query::{self, Field, Plan};
use time::{OffsetDateTime, macros::format_description};

use crate::{FTS_MATCH_JOIN, SearchHit, SidecarError};

pub(crate) const INVALID_SYNTAX_HINT: &str =
    "Search syntax was invalid, so this was searched as plain full text.";

#[derive(Debug)]
pub struct SearchPlanResponse {
    pub results: Vec<SearchHit>,
    pub hint: Option<&'static str>,
}

pub(crate) fn search_plan(
    connection: &Connection,
    roots: &[String],
    plan: &Plan,
) -> Result<SearchPlanResponse, SidecarError> {
    search_parsed(connection, roots, plan)
}

fn search_parsed(
    connection: &Connection,
    roots: &[String],
    plan: &Plan,
) -> Result<SearchPlanResponse, SidecarError> {
    let positives = plan
        .terms
        .iter()
        .filter(|term| !term.negated)
        .collect::<Vec<_>>();
    let negatives = plan
        .terms
        .iter()
        .filter(|term| term.negated)
        .collect::<Vec<_>>();
    let fts_query = positives
        .iter()
        .map(|term| symdesk_core::german::fts_term(&term.value, term.phrase))
        .filter(|term| !term.is_empty())
        .collect::<Vec<_>>()
        .join(" AND ");
    let has_full_text = !fts_query.is_empty();
    let trigram_query = positives
        .iter()
        .map(|term| symdesk_core::german::trigram_term(&term.value, term.phrase))
        .collect::<Vec<_>>();
    let trigram_query = if trigram_query.iter().any(|part| part == "\"\"") {
        "\"\"".to_owned()
    } else {
        trigram_query.join(" AND ")
    };

    let mut sql = String::from("SELECT f.path, f.title, ");
    if has_full_text {
        sql.push_str("COALESCE(sm.snip, ''), COALESCE(sm.body, ''), ");
    } else {
        sql.push_str(
            "substr(COALESCE(fts_search.body, ''), 1, 256), COALESCE(fts_search.body, ''), ",
        );
    }
    sql.push_str(
        "COALESCE((SELECT value FROM file_properties WHERE file_id=f.id AND key='tags'), ''), \
      COALESCE(f.status, ''), COALESCE(f.\"type\", ''), \
      COALESCE(f.created_at, (SELECT value FROM file_properties WHERE file_id=f.id AND key='created'), ''), \
      COALESCE(f.modified_at, ''), COALESCE(lifecycle.state, 'indexed') \
      FROM files f LEFT JOIN index_lifecycle lifecycle ON lifecycle.path=f.path ",
    );
    if has_full_text {
        sql.push_str(FTS_MATCH_JOIN);
        sql.push_str(" WHERE ");
    } else {
        sql.push_str("LEFT JOIN fts_search ON fts_search.rowid=f.id WHERE ");
    }
    sql.push_str(&root_predicate(roots.len()));

    let mut args = Vec::<Value>::new();
    if has_full_text {
        args.extend([
            Value::Text(fts_query.clone()),
            Value::Text(fts_query),
            Value::Text(trigram_query),
        ]);
    }
    for root in roots {
        let prefix = root_prefix(root);
        args.extend([
            Value::Text(root.clone()),
            Value::Text(prefix.clone()),
            Value::Text(prefix),
        ]);
    }
    for filter in &plan.filters {
        match filter.field {
            Field::Path if !filter.value.contains(',') => {
                sql.push_str(if filter.negated {
                    " AND LOWER(f.path) NOT LIKE LOWER(?) ESCAPE '\\'"
                } else {
                    " AND LOWER(f.path) LIKE LOWER(?) ESCAPE '\\'"
                });
                args.push(Value::Text(format!("%{}%", escape_like(&filter.value))));
            }
            Field::Status if !filter.value.contains(',') => {
                sql.push_str(if filter.negated {
                    " AND COALESCE(f.status, '') != ? COLLATE NOCASE"
                } else {
                    " AND COALESCE(f.status, '') = ? COLLATE NOCASE"
                });
                args.push(Value::Text(filter.value.clone()));
            }
            Field::Index => {
                sql.push_str(if filter.negated {
                    " AND COALESCE(lifecycle.state, 'indexed') != ? COLLATE NOCASE"
                } else {
                    " AND COALESCE(lifecycle.state, 'indexed') = ? COLLATE NOCASE"
                });
                args.push(Value::Text(filter.value.clone()));
            }
            Field::Type if !filter.value.contains(',') => {
                sql.push_str(if filter.negated { " AND NOT EXISTS (SELECT 1 FROM file_properties p WHERE p.file_id=f.id AND p.key='document_type' AND p.value=? COLLATE NOCASE)" } else { " AND EXISTS (SELECT 1 FROM file_properties p WHERE p.file_id=f.id AND p.key='document_type' AND p.value=? COLLATE NOCASE)" });
                args.push(Value::Text(filter.value.clone()));
            }
            _ => {}
        }
    }
    for term in negatives {
        let expression = symdesk_core::german::fts_term(&term.value, term.phrase);
        if expression.is_empty() {
            continue;
        }
        sql.push_str(" AND NOT EXISTS (SELECT 1 FROM fts_search WHERE rowid=f.id AND fts_search MATCH ?) AND NOT EXISTS (SELECT 1 FROM fts_norm WHERE rowid=f.id AND fts_norm MATCH ?)");
        args.extend([Value::Text(expression.clone()), Value::Text(expression)]);
    }
    sql.push_str(if has_full_text {
        " ORDER BY sm.rank IS NULL, sm.rank"
    } else {
        " ORDER BY f.path COLLATE NOCASE"
    });

    let mut statement = connection.prepare(&sql)?;
    let rows = statement.query_map(params_from_iter(args), |row| {
        Ok(PlanRow {
            hit: SearchHit {
                path: row.get(0)?,
                title: row.get(1)?,
                snippet: row.get(2)?,
            },
            body: row.get(3)?,
            tags: row.get(4)?,
            status: row.get(5)?,
            document_type: row.get(6)?,
            created_at: row.get(7)?,
            modified_at: row.get(8)?,
            index_state: row.get(9)?,
        })
    })?;
    let reference = OffsetDateTime::now_utc();
    let mut results = Vec::new();
    for row in rows {
        let row = row?;
        if !post_filters_match(&row, plan, reference) {
            continue;
        }
        results.push(row.hit);
        if results.len() == 20 {
            break;
        }
    }
    Ok(SearchPlanResponse {
        results,
        hint: None,
    })
}

struct PlanRow {
    hit: SearchHit,
    body: String,
    tags: String,
    status: String,
    document_type: String,
    created_at: String,
    modified_at: String,
    index_state: String,
}

fn post_filters_match(row: &PlanRow, plan: &Plan, reference: OffsetDateTime) -> bool {
    for filter in &plan.filters {
        // These singleton filters are applied in SQL, so don't evaluate a
        // placeholder match here (in particular for negated filters).
        if !filter.value.contains(',')
            && matches!(filter.field, Field::Path | Field::Status | Field::Type)
        {
            continue;
        }
        let matched = match filter.field {
            Field::Path if filter.value.contains(',') => {
                any_value(&row.hit.path, &filter.value, |raw, wanted| {
                    crate::go_simple_lowercase(raw).contains(&crate::go_simple_lowercase(wanted))
                })
            }
            Field::Path => true,
            Field::Tag => any_value(&row.tags, &filter.value, has_tag),
            Field::Type if filter.value.contains(',') => {
                any_value(&row.document_type, &filter.value, |raw, wanted| {
                    symdesk_vault::dataset::go_equal_fold(raw, wanted)
                })
            }
            Field::Type => true,
            Field::Status if filter.value.contains(',') => {
                any_value(&row.status, &filter.value, |raw, wanted| {
                    symdesk_vault::dataset::go_equal_fold(raw, wanted)
                })
            }
            Field::Status => true,
            Field::Index => row.index_state.eq_ignore_ascii_case(&filter.value),
            Field::Filename => any_value(file_name(&row.hit.path), &filter.value, |raw, wanted| {
                crate::go_simple_lowercase(raw).contains(&crate::go_simple_lowercase(wanted))
            }),
            Field::FileType => any_value(
                file_extension(&row.hit.path),
                &filter.value,
                |raw, wanted| {
                    symdesk_vault::dataset::go_equal_fold(
                        raw,
                        wanted.trim().trim_start_matches('.'),
                    )
                },
            ),
            Field::Created => any_date(&row.created_at, &filter.value, reference),
            Field::Modified => any_date(&row.modified_at, &filter.value, reference),
        };
        if filter.negated == matched {
            return false;
        }
    }
    let content = format!("{}\n{}", row.hit.title, row.body);
    plan.regexes
        .iter()
        .all(|regex| regex.negated != regex.matches(&content))
}

fn any_value(raw: &str, wanted: &str, matches: impl Fn(&str, &str) -> bool) -> bool {
    wanted.split(',').any(|value| {
        let value = value.trim();
        !value.is_empty() && matches(raw, value)
    })
}

fn has_tag(raw: &str, wanted: &str) -> bool {
    if symdesk_vault::dataset::go_equal_fold(raw.trim(), wanted) {
        return true;
    }
    let value = raw.trim().trim_start_matches('[').trim_end_matches(']');
    value
        .split(|character: char| character == ',' || character.is_whitespace())
        .map(|tag| tag.trim_matches(['"', '\'']))
        .any(|tag| symdesk_vault::dataset::go_equal_fold(tag, wanted))
}

fn file_name(path: &str) -> &str {
    Path::new(path)
        .file_name()
        .and_then(|name| name.to_str())
        .unwrap_or(path)
}
fn file_extension(path: &str) -> &str {
    Path::new(path)
        .extension()
        .and_then(|extension| extension.to_str())
        .unwrap_or("")
}

fn any_date(raw: &str, wanted: &str, reference: OffsetDateTime) -> bool {
    let Some(timestamp) = parse_timestamp(raw) else {
        return false;
    };
    wanted.split(',').any(|value| {
        query::parse_date_value(value.trim(), reference)
            .is_ok_and(|range| timestamp >= range.from && timestamp <= range.to)
    })
}

fn parse_timestamp(raw: &str) -> Option<OffsetDateTime> {
    const SQL_OFFSET: &[time::format_description::FormatItem<'static>] = format_description!(
        "[year]-[month]-[day] [hour]:[minute]:[second][offset_hour sign:mandatory]:[offset_minute]"
    );
    const SQL_UTC: &[time::format_description::FormatItem<'static>] =
        format_description!("[year]-[month]-[day] [hour]:[minute]:[second]");
    OffsetDateTime::parse(raw, &time::format_description::well_known::Rfc3339)
        .ok()
        .or_else(|| OffsetDateTime::parse(raw, SQL_OFFSET).ok())
        .or_else(|| {
            time::PrimitiveDateTime::parse(raw, SQL_UTC)
                .ok()
                .map(|value| value.assume_utc())
        })
}

fn root_predicate(root_count: usize) -> String {
    let mut predicate = String::from("(");
    for index in 0..root_count {
        if index > 0 {
            predicate.push_str(" OR ");
        }
        predicate.push_str("(f.path = ? OR substr(f.path, 1, length(?)) = ?)");
    }
    predicate.push(')');
    predicate
}
fn root_prefix(root: &str) -> String {
    let mut value = root.trim_end_matches(std::path::MAIN_SEPARATOR).to_owned();
    value.push(std::path::MAIN_SEPARATOR);
    value
}
fn escape_like(value: &str) -> String {
    value
        .replace('\\', "\\\\")
        .replace('%', "\\%")
        .replace('_', "\\_")
}

#[cfg(test)]
mod tests {
    #[cfg(unix)]
    #[test]
    fn root_prefix_preserves_legal_trailing_backslash() {
        let root = "/tmp/vault\\";
        assert_eq!(super::root_prefix(root), "/tmp/vault\\/");
    }
}
