//! Structured dataset queries over the rebuildable sidecar rows.
//!
//! This is a statement-for-statement port of Go's `sidecar.QueryDataset`
//! (filter, set-membership, ordering and aggregate SQL assembly), the thin
//! `service.DatasetQuery` projection on top of it and the dataset-sourced view
//! execution (`executeDatasetViewWithTotal`). No caller-supplied text reaches
//! SQL: columns become bound JSON paths and every value is a bound argument.

use std::collections::BTreeMap;

use rusqlite::{params_from_iter, types::Value as Sql};
use serde::{Deserialize, Serialize};
use serde_json::{Number, Value};
use symdesk_vault::{go_lowercase, go_quote};

use crate::{DatasetQueryFilter, DatasetQueryFilterGroup, Sidecar, SidecarError};

/// Largest page a sidecar dataset query returns (Go `DatasetQueryMaxRows`).
pub const DATASET_QUERY_MAX_ROWS: i64 = 1000;

#[derive(Clone, Debug, Default, Eq, PartialEq)]
pub struct DatasetSort {
    pub key: String,
    pub ascending: bool,
}

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq)]
pub struct DatasetAggregate {
    #[serde(default)]
    pub column: String,
    #[serde(default)]
    pub function: String,
    #[serde(default, rename = "as")]
    pub alias: String,
}

#[derive(Clone, Debug, Default)]
pub struct DatasetQueryOptions {
    pub schema: BTreeMap<String, String>,
    pub columns: Vec<String>,
    pub filters: Vec<DatasetQueryFilter>,
    pub filter_group: Option<DatasetQueryFilterGroup>,
    pub sorts: Vec<DatasetSort>,
    pub group_by: String,
    pub aggregates: Vec<DatasetAggregate>,
    pub limit: i64,
    pub offset: i64,
}

#[derive(Clone, Debug, PartialEq)]
pub struct DatasetQueryRow {
    pub row_key: String,
    pub identity: String,
    pub values: BTreeMap<String, Value>,
    pub source_path: String,
    pub row_number: i64,
}

#[derive(Clone, Debug, PartialEq)]
pub struct DatasetQueryOutcome {
    pub rows: Vec<DatasetQueryRow>,
    pub total_rows: i64,
    pub limit: i64,
    pub offset: i64,
}

/// Options of the service-level projection (Go `service.DatasetQueryOptions`).
#[derive(Clone, Debug, Default)]
pub struct DatasetServiceOptions {
    pub columns: Vec<String>,
    pub filters: Vec<DatasetQueryFilter>,
    pub filter_group: Option<DatasetQueryFilterGroup>,
    pub sorts: Vec<DatasetSort>,
    pub group_by: String,
    pub aggregates: Vec<DatasetAggregate>,
    pub limit: i64,
}

/// Go `service.DatasetQueryResult`, field order and names included.
#[derive(Clone, Debug, PartialEq, Serialize)]
pub struct DatasetServiceResult {
    pub dataset: String,
    pub columns: Vec<String>,
    pub rows: Vec<BTreeMap<String, Value>>,
    pub total_rows: i64,
    pub returned_rows: i64,
    pub limit: i64,
    pub capped: bool,
}

/// The saved-view fields that drive a dataset-sourced view projection.
#[derive(Clone, Debug, Default)]
pub struct DatasetViewSpec {
    pub source: String,
    pub filters: Vec<DatasetQueryFilter>,
    pub filter_group: Option<DatasetQueryFilterGroup>,
    pub sorts: Vec<DatasetSort>,
    pub group_by: String,
    /// Computed columns: `(name, formula, rollup)` in Go map iteration order
    /// is unspecified, so callers pass them sorted by name.
    pub computed: Vec<(String, String, String)>,
}

fn contract(message: String) -> SidecarError {
    SidecarError::Contract(message)
}

/// Go's `float64` JSON rendering keeps integral values without a fraction.
#[must_use]
pub fn go_number(value: f64) -> Value {
    if value.is_finite() && value.fract() == 0.0 && value.abs() < 9.0e18 {
        #[allow(clippy::cast_possible_truncation)]
        return Value::Number(Number::from(value as i64));
    }
    Number::from_f64(value).map_or(Value::Null, Value::Number)
}

fn sql_to_json(value: Sql) -> Value {
    match value {
        Sql::Null => Value::Null,
        #[allow(clippy::cast_precision_loss)]
        Sql::Integer(n) => go_number(n as f64),
        Sql::Real(f) => go_number(f),
        Sql::Text(s) => Value::String(s),
        Sql::Blob(b) => Value::String(String::from_utf8_lossy(&b).into_owned()),
    }
}

/// Decodes a stored row like Go's `json.Unmarshal` into `map[string]any`:
/// every number becomes a float64.
fn decode_values(text: &str) -> Result<BTreeMap<String, Value>, String> {
    fn lower(value: Value) -> Value {
        match value {
            Value::Number(n) => go_number(n.as_f64().unwrap_or(0.0)),
            Value::Array(items) => Value::Array(items.into_iter().map(lower).collect()),
            Value::Object(map) => {
                Value::Object(map.into_iter().map(|(k, v)| (k, lower(v))).collect())
            }
            other => other,
        }
    }
    match serde_json::from_str::<Value>(text).map_err(|e| e.to_string())? {
        Value::Object(map) => Ok(map.into_iter().map(|(k, v)| (k, lower(v))).collect()),
        Value::Null => Ok(BTreeMap::new()),
        other => Err(format!(
            "json: cannot unmarshal {} into Go value of type map[string]interface {{}}",
            match other {
                Value::Array(_) => "array",
                Value::String(_) => "string",
                Value::Number(_) => "number",
                _ => "bool",
            }
        )),
    }
}

fn is_pseudo(column: &str) -> bool {
    matches!(column, "identity" | "_identity" | "_key")
}

fn validate_column(column: &str, schema: &BTreeMap<String, String>) -> Result<(), SidecarError> {
    if is_pseudo(column) {
        return Ok(());
    }
    if (column.trim().is_empty() || schema.get(column).is_none_or(String::is_empty))
        && !schema.contains_key(column)
    {
        return Err(contract(format!(
            "dataset column {} not found",
            go_quote(column)
        )));
    }
    Ok(())
}

fn validate_group(
    group: &DatasetQueryFilterGroup,
    schema: &BTreeMap<String, String>,
) -> Result<(), SidecarError> {
    for filter in &group.filters {
        validate_column(&filter.key, schema)?;
    }
    for child in &group.groups {
        validate_group(child, schema)?;
    }
    Ok(())
}

fn path(column: &str) -> String {
    format!(r#"$."{}""#, column.replace('"', r#"\""#))
}

fn t(value: &str) -> Sql {
    Sql::Text(value.to_owned())
}

type Frag = (String, Vec<Sql>);

fn raw(column: &str) -> Frag {
    match column {
        "identity" | "_identity" => ("identity".to_owned(), Vec::new()),
        "_key" => ("row_key".to_owned(), Vec::new()),
        _ => (
            "json_extract(values_json, ?)".to_owned(),
            vec![t(&path(column))],
        ),
    }
}

fn present(column: &str) -> Frag {
    match column {
        "identity" | "_identity" => ("identity IS NOT NULL".to_owned(), Vec::new()),
        "_key" => ("row_key IS NOT NULL".to_owned(), Vec::new()),
        _ => (
            "json_type(values_json, ?) IS NOT NULL".to_owned(),
            vec![t(&path(column))],
        ),
    }
}

fn is_number(typ: &str) -> bool {
    ["number", "integer", "float"]
        .iter()
        .any(|n| typ.eq_ignore_ascii_case(n))
}

fn is_date(typ: &str) -> bool {
    ["date", "datetime"]
        .iter()
        .any(|n| typ.eq_ignore_ascii_case(n))
}

fn typed(column: &str, typ: &str) -> Frag {
    let (raw, args) = raw(column);
    let lowered = typ.to_ascii_lowercase();
    match lowered.as_str() {
        "number" | "integer" | "float" => (format!("CAST({raw} AS REAL)"), args),
        "date" | "datetime" => (format!("julianday({raw})"), args),
        _ => (format!("LOWER(CAST({raw} AS TEXT))"), args),
    }
}

fn cat(mut a: Vec<Sql>, b: &[Sql]) -> Vec<Sql> {
    a.extend(b.iter().cloned());
    a
}

fn cast_type(typ: &str) -> &'static str {
    if is_number(typ) { "REAL" } else { "TEXT" }
}

fn filter_expression(
    filter: &DatasetQueryFilter,
    schema: &BTreeMap<String, String>,
) -> Result<Frag, SidecarError> {
    validate_column(&filter.key, schema)?;
    let mut op = filter.operator.trim().to_ascii_lowercase();
    if op.is_empty() || matches!(op.as_str(), "is" | "=" | "==") {
        op = "equals".to_owned();
    }
    let typ = schema.get(&filter.key).map_or("", String::as_str);
    let key = filter.key.as_str();
    let (present_sql, present_args) = present(key);
    let (typed_sql, typed_args) = typed(key, typ);
    let value = filter.value.trim();
    let date = is_date(typ);
    let number = is_number(typ);
    let compare = |operator: &str| -> Frag {
        if date {
            return (
                format!(
                    "{present_sql} AND julianday(json_extract(values_json, ?)) {operator} julianday(?)"
                ),
                cat(cat(present_args.clone(), &[t(&path(key))]), &[t(value)]),
            );
        }
        (
            format!(
                "{present_sql} AND {typed_sql} {operator} CAST(? AS {})",
                cast_type(typ)
            ),
            cat(cat(present_args.clone(), &typed_args), &[t(value)]),
        )
    };
    let result: Frag = match op.as_str() {
        "equals" => {
            if value.is_empty() {
                let (r, ra) = raw(key);
                (
                    format!("NOT ({present_sql}) OR {r} IS NULL OR CAST({r} AS TEXT) = ''"),
                    cat(cat(present_args, &ra), &ra),
                )
            } else if date {
                (
                    format!(
                        "{present_sql} AND julianday(json_extract(values_json, ?)) = julianday(?)"
                    ),
                    cat(cat(present_args, &[t(&path(key))]), &[t(value)]),
                )
            } else if number {
                (
                    format!("{present_sql} AND {typed_sql} = CAST(? AS REAL)"),
                    cat(cat(present_args, &typed_args), &[t(value)]),
                )
            } else {
                (
                    format!("{present_sql} AND {typed_sql} = LOWER(?)"),
                    cat(cat(present_args, &typed_args), &[t(&go_lowercase(value))]),
                )
            }
        }
        "not_equals" | "is_not" | "!=" => {
            if date {
                (
                    format!(
                        "(NOT ({present_sql}) OR NOT (julianday(json_extract(values_json, ?)) = julianday(?)))"
                    ),
                    cat(cat(present_args, &[t(&path(key))]), &[t(value)]),
                )
            } else if number {
                (
                    format!("(NOT ({present_sql}) OR NOT ({typed_sql} = CAST(? AS REAL)))"),
                    cat(cat(present_args, &typed_args), &[t(value)]),
                )
            } else {
                (
                    format!("(NOT ({present_sql}) OR NOT ({typed_sql} = LOWER(?)))"),
                    cat(cat(present_args, &typed_args), &[t(&go_lowercase(value))]),
                )
            }
        }
        "is_empty" | "empty" => {
            let (r, ra) = raw(key);
            (
                format!("NOT ({present_sql}) OR {r} IS NULL OR CAST({r} AS TEXT) = ''"),
                cat(cat(present_args, &ra), &ra),
            )
        }
        "is_not_empty" | "not_empty" => {
            let (r, ra) = raw(key);
            (
                format!("{present_sql} AND {r} IS NOT NULL AND CAST({r} AS TEXT) <> ''"),
                cat(cat(present_args, &ra), &ra),
            )
        }
        "contains" | "not_contains" | "starts_with" | "prefix" | "ends_with" | "suffix" => {
            let (r, ra) = raw(key);
            let pattern = match op.as_str() {
                "starts_with" | "prefix" => format!("{value}%"),
                "ends_with" | "suffix" => format!("%{value}"),
                _ => format!("%{value}%"),
            };
            let expr = format!("LOWER(CAST({r} AS TEXT)) LIKE LOWER(?)");
            let args = cat(cat(present_args, &ra), &[Sql::Text(pattern)]);
            if op == "not_contains" {
                (format!("NOT ({present_sql} AND {expr})"), args)
            } else {
                (format!("{present_sql} AND {expr}"), args)
            }
        }
        "greater_than" | "gt" | ">" => compare(">"),
        "greater_than_or_equal" | "gte" | ">=" => compare(">="),
        "less_than" | "lt" | "<" => compare("<"),
        "less_than_or_equal" | "lte" | "<=" => compare("<="),
        "after" | "before" | "on_or_after" | "on_or_before" => {
            let operator = match op.as_str() {
                "before" => "<",
                "on_or_after" => ">=",
                "on_or_before" => "<=",
                _ => ">",
            };
            (
                format!(
                    "{present_sql} AND julianday(json_extract(values_json, ?)) {operator} julianday(?)"
                ),
                cat(cat(present_args, &[t(&path(key))]), &[t(value)]),
            )
        }
        "in" | "not_in" | "contains_all" | "contains_any" | "contains_none" => {
            set_filter(key, typ, &op, value)
        }
        _ => {
            return filter_expression(
                &DatasetQueryFilter {
                    key: filter.key.clone(),
                    operator: "equals".to_owned(),
                    value: filter.value.clone(),
                },
                schema,
            );
        }
    };
    Ok(result)
}

fn go_fields(value: &str) -> Vec<String> {
    value.split_whitespace().map(str::to_owned).collect()
}

fn parse_set(value: &str) -> Vec<String> {
    let value = value.trim_matches(['[', ']']).trim();
    if value.is_empty() {
        return Vec::new();
    }
    if value.contains(',') {
        return value
            .split(',')
            .map(str::trim)
            .filter(|p| !p.is_empty())
            .map(str::to_owned)
            .collect();
    }
    go_fields(value)
}

fn set_raw(column: &str) -> Frag {
    match column {
        "identity" | "_identity" => ("identity".to_owned(), Vec::new()),
        "_key" => ("row_key".to_owned(), Vec::new()),
        _ => (
            "CASE WHEN json_valid(values_json) = 1 THEN json_extract(values_json, ?) ELSE NULL END"
                .to_owned(),
            vec![t(&path(column))],
        ),
    }
}

fn set_present(column: &str) -> Frag {
    match column {
        "identity" | "_identity" => ("identity IS NOT NULL".to_owned(), Vec::new()),
        "_key" => ("row_key IS NOT NULL".to_owned(), Vec::new()),
        _ => (
            "CASE WHEN json_valid(values_json) = 1 THEN json_type(values_json, ?) ELSE NULL END IS NOT NULL".to_owned(),
            vec![t(&path(column))],
        ),
    }
}

fn set_valid(column: &str) -> &'static str {
    if is_pseudo(column) {
        "1"
    } else {
        "json_valid(values_json) = 1"
    }
}

fn set_container(column: &str) -> Frag {
    if is_pseudo(column) {
        return ("'[]'".to_owned(), Vec::new());
    }
    let p = path(column);
    (
        "CASE WHEN json_valid(values_json) = 1 THEN CASE WHEN json_type(values_json, ?) IN ('array', 'object') THEN json_extract(values_json, ?) ELSE '[]' END ELSE '[]' END".to_owned(),
        vec![t(&p), t(&p)],
    )
}

fn set_items(typ: &str, items: &[String]) -> Vec<Sql> {
    items
        .iter()
        .map(|item| {
            let mut item = item.clone();
            if typ.eq_ignore_ascii_case("bool") || typ.eq_ignore_ascii_case("boolean") {
                match go_lowercase(item.trim()).as_str() {
                    "true" | "1" => item = "1".to_owned(),
                    "false" | "0" => item = "0".to_owned(),
                    _ => {}
                }
            } else if !is_number(typ) {
                item = go_lowercase(&item);
            }
            Sql::Text(item)
        })
        .collect()
}

fn set_in_expression(raw: &str, typ: &str, count: usize) -> String {
    let number = is_number(typ);
    let placeholder = if number { "CAST(? AS REAL)" } else { "?" };
    let list = vec![placeholder; count].join(",");
    if number {
        format!("CAST({raw} AS REAL) IN ({list})")
    } else {
        format!("LOWER(CAST({raw} AS TEXT)) IN ({list})")
    }
}

fn set_equals_expression(value_expr: &str, typ: &str) -> String {
    if is_number(typ) {
        format!("CAST({value_expr} AS REAL) = CAST(? AS REAL)")
    } else {
        format!("LOWER(CAST({value_expr} AS TEXT)) = LOWER(?)")
    }
}

fn set_filter(column: &str, typ: &str, op: &str, value: &str) -> Frag {
    let items = parse_set(value);
    if items.is_empty() {
        return if matches!(op, "not_in" | "contains_none") {
            ("1".to_owned(), Vec::new())
        } else {
            ("0".to_owned(), Vec::new())
        };
    }
    let (present_sql, present_args) = set_present(column);
    let (raw_sql, raw_args) = set_raw(column);
    let (container, container_args) = set_container(column);
    let values = set_items(typ, &items);
    let scalar = set_in_expression(&raw_sql, typ, items.len());
    let array = format!(
        "EXISTS (SELECT 1 FROM json_each({container}) WHERE {})",
        set_in_expression("value", typ, items.len())
    );
    let one = format!("{present_sql} AND ({scalar} OR {array})");
    let mut one_args = present_args.clone();
    one_args.extend(raw_args.iter().cloned());
    one_args.extend(values.iter().cloned());
    one_args.extend(container_args.iter().cloned());
    one_args.extend(values.iter().cloned());
    match op {
        "in" | "contains_any" => (one, one_args),
        "not_in" => (format!("{} AND NOT ({one})", set_valid(column)), one_args),
        "contains_none" => {
            let array_one = format!("{present_sql} AND {array}");
            let mut array_args = present_args.clone();
            array_args.extend(container_args.iter().cloned());
            array_args.extend(values.iter().cloned());
            (
                format!("{} AND NOT ({array_one})", set_valid(column)),
                array_args,
            )
        }
        _ => {
            let mut parts = Vec::with_capacity(items.len());
            let mut args = Vec::new();
            for item in values.iter().take(items.len()) {
                parts.push(format!(
                    "{present_sql} AND ({} OR EXISTS (SELECT 1 FROM json_each({container}) WHERE {}))",
                    set_equals_expression(&raw_sql, typ),
                    set_equals_expression("value", typ)
                ));
                args.extend(present_args.iter().cloned());
                args.extend(raw_args.iter().cloned());
                args.push(item.clone());
                args.extend(container_args.iter().cloned());
                args.push(item.clone());
            }
            (format!("({})", parts.join(" AND ")), args)
        }
    }
}

fn group_expression(
    group: &DatasetQueryFilterGroup,
    schema: &BTreeMap<String, String>,
) -> Result<Frag, SidecarError> {
    let mut parts = Vec::new();
    let mut args = Vec::new();
    for filter in &group.filters {
        let (expr, a) = filter_expression(&filter_of(filter), schema)?;
        parts.push(expr);
        args.extend(a);
    }
    for child in &group.groups {
        let (expr, a) = group_expression(child, schema)?;
        parts.push(expr);
        args.extend(a);
    }
    if parts.is_empty() {
        return Ok(("1".to_owned(), Vec::new()));
    }
    let joiner = if group.operator.trim().eq_ignore_ascii_case("any") {
        " OR "
    } else {
        " AND "
    };
    Ok((format!("({})", parts.join(joiner)), args))
}

fn filter_of(filter: &symdesk_vault::Filter) -> DatasetQueryFilter {
    DatasetQueryFilter {
        key: filter.key.clone(),
        operator: filter.operator.clone(),
        value: filter.value.clone(),
    }
}

fn where_clause(opts: &DatasetQueryOptions) -> Result<Frag, SidecarError> {
    let mut parts = Vec::new();
    let mut args = Vec::new();
    for filter in &opts.filters {
        let (expr, a) = filter_expression(filter, &opts.schema)?;
        parts.push(expr);
        args.extend(a);
    }
    if let Some(group) = &opts.filter_group {
        let (expr, a) = group_expression(group, &opts.schema)?;
        parts.push(expr);
        args.extend(a);
    }
    if parts.is_empty() {
        return Ok((String::new(), args));
    }
    Ok((format!(" AND ({})", parts.join(") AND (")), args))
}

fn order_clause(
    sorts: &[DatasetSort],
    schema: &BTreeMap<String, String>,
) -> Result<Frag, SidecarError> {
    if sorts.is_empty() {
        return Ok((" ORDER BY row_key ASC".to_owned(), Vec::new()));
    }
    let mut parts = Vec::new();
    let mut args = Vec::new();
    for sort in sorts {
        validate_column(&sort.key, schema)?;
        let (r, ra) = raw(&sort.key);
        let (ty, ta) = typed(&sort.key, schema.get(&sort.key).map_or("", String::as_str));
        let order = if sort.ascending { "ASC" } else { "DESC" };
        parts.push(format!("({r} IS NULL) ASC, {ty} {order}"));
        args.extend(ra);
        args.extend(ta);
    }
    parts.push("row_key ASC".to_owned());
    Ok((format!(" ORDER BY {}", parts.join(", ")), args))
}

impl Sidecar {
    /// Evaluates only structured, validated query fields (Go
    /// `sidecar.QueryDataset`); `LIMIT`/`OFFSET` are applied by `SQLite`.
    ///
    /// # Errors
    /// Returns Go's validation errors, the closed-database diagnostic or
    /// `SQLite` errors.
    pub fn query_dataset(
        &self,
        slug: &str,
        opts: &DatasetQueryOptions,
    ) -> Result<DatasetQueryOutcome, SidecarError> {
        if self.closed {
            return Err(SidecarError::Closed);
        }
        if slug.trim().is_empty() {
            return Err(contract("dataset slug is required".to_owned()));
        }
        if opts.schema.is_empty() {
            return Err(contract("dataset schema is required".to_owned()));
        }
        let columns = if opts.columns.is_empty() {
            opts.schema.keys().cloned().collect()
        } else {
            opts.columns.clone()
        };
        for column in &columns {
            validate_column(column, &opts.schema)?;
        }
        if !opts.group_by.is_empty() && validate_column(&opts.group_by, &opts.schema).is_err() {
            return Err(contract(format!(
                "dataset group column {} not found",
                go_quote(&opts.group_by)
            )));
        }
        for filter in &opts.filters {
            validate_column(&filter.key, &opts.schema)?;
        }
        if let Some(group) = &opts.filter_group {
            validate_group(group, &opts.schema)?;
        }
        for sort in &opts.sorts {
            if validate_column(&sort.key, &opts.schema).is_err() {
                return Err(contract(format!(
                    "dataset sort column {} not found",
                    go_quote(&sort.key)
                )));
            }
        }
        for aggregate in &opts.aggregates {
            let function = aggregate.function.trim().to_ascii_lowercase();
            if !matches!(
                function.as_str(),
                "count" | "sum" | "min" | "max" | "average"
            ) {
                return Err(contract(format!(
                    "unsupported dataset aggregate {}",
                    go_quote(&aggregate.function)
                )));
            }
            if function != "count" && validate_column(&aggregate.column, &opts.schema).is_err() {
                return Err(contract(format!(
                    "dataset aggregate column {} not found",
                    go_quote(&aggregate.column)
                )));
            }
        }
        let (where_sql, where_args) = where_clause(opts)?;
        let mut limit = if opts.limit <= 0 { 10 } else { opts.limit };
        limit = limit.min(DATASET_QUERY_MAX_ROWS);
        let offset = opts.offset.max(0);
        if !opts.group_by.is_empty() || !opts.aggregates.is_empty() {
            return self.query_aggregate(slug, opts, &where_sql, &where_args, limit, offset);
        }
        let total: i64 = self.connection.query_row(
            &format!("SELECT COUNT(*) FROM dataset_rows WHERE dataset_slug = ?{where_sql}"),
            params_from_iter(std::iter::once(t(slug)).chain(where_args.iter().cloned())),
            |row| row.get(0),
        )?;
        let (order_sql, order_args) = order_clause(&opts.sorts, &opts.schema)?;
        let select = format!(
            "SELECT row_key, COALESCE(identity, ''), values_json, source_path, row_number FROM dataset_rows WHERE dataset_slug = ?{where_sql}{order_sql} LIMIT ? OFFSET ?"
        );
        let mut args = vec![t(slug)];
        args.extend(where_args);
        args.extend(order_args);
        args.push(Sql::Integer(limit));
        args.push(Sql::Integer(offset));
        let mut statement = self.connection.prepare(&select)?;
        let mapped = statement.query_map(params_from_iter(args), |row| {
            Ok((
                row.get::<_, String>(0)?,
                row.get::<_, String>(1)?,
                row.get::<_, String>(2)?,
                row.get::<_, String>(3)?,
                row.get::<_, i64>(4)?,
            ))
        })?;
        let mut rows = Vec::new();
        for item in mapped {
            let (row_key, identity, values_json, source_path, row_number) = item?;
            let values = decode_values(&values_json)
                .map_err(|e| contract(format!("decode dataset row {}: {e}", go_quote(&row_key))))?;
            rows.push(DatasetQueryRow {
                row_key,
                identity,
                values,
                source_path,
                row_number,
            });
        }
        Ok(DatasetQueryOutcome {
            rows,
            total_rows: total,
            limit,
            offset,
        })
    }

    fn query_aggregate(
        &self,
        slug: &str,
        opts: &DatasetQueryOptions,
        where_sql: &str,
        where_args: &[Sql],
        limit: i64,
        offset: i64,
    ) -> Result<DatasetQueryOutcome, SidecarError> {
        let (group_expr, group_args) = if opts.group_by.is_empty() {
            (String::new(), Vec::new())
        } else {
            raw(&opts.group_by)
        };
        let mut agg_exprs = Vec::new();
        let mut agg_args = Vec::new();
        for aggregate in &opts.aggregates {
            let function = aggregate.function.trim().to_ascii_lowercase();
            if function == "count" && aggregate.column.is_empty() {
                agg_exprs.push("COUNT(*)".to_owned());
                continue;
            }
            let (r, ra) = raw(&aggregate.column);
            agg_exprs.push(match function.as_str() {
                "count" => format!("COUNT({r})"),
                "sum" => format!("SUM(CAST({r} AS REAL))"),
                "min" => format!("MIN({r})"),
                "max" => format!("MAX({r})"),
                _ => format!("AVG(CAST({r} AS REAL))"),
            });
            agg_args.extend(ra);
        }
        if agg_exprs.is_empty() && !opts.group_by.is_empty() {
            agg_exprs.push("COUNT(*)".to_owned());
        }
        let mut select_parts = Vec::new();
        if !opts.group_by.is_empty() {
            select_parts.push(group_expr.clone());
        }
        select_parts.extend(agg_exprs);
        let base = format!(" FROM dataset_rows WHERE dataset_slug = ?{where_sql}");
        let total: i64 = if opts.group_by.is_empty() {
            1
        } else {
            let mut args = group_args.clone();
            args.push(t(slug));
            args.extend(where_args.iter().cloned());
            args.extend(group_args.iter().cloned());
            self.connection.query_row(
                &format!("SELECT COUNT(*) FROM (SELECT {group_expr}{base} GROUP BY {group_expr})"),
                params_from_iter(args),
                |row| row.get(0),
            )?
        };
        let mut query = format!("SELECT {}{base}", select_parts.join(","));
        let mut args = group_args.clone();
        args.extend(agg_args);
        args.push(t(slug));
        args.extend(where_args.iter().cloned());
        if !opts.group_by.is_empty() {
            query.push_str(&format!(" GROUP BY {group_expr} ORDER BY {group_expr} ASC"));
            args.extend(group_args.iter().cloned());
            args.extend(group_args.iter().cloned());
        }
        query.push_str(" LIMIT ? OFFSET ?");
        args.push(Sql::Integer(limit));
        args.push(Sql::Integer(offset));
        let mut statement = self.connection.prepare(&query)?;
        let width = select_parts.len();
        let mapped = statement.query_map(params_from_iter(args), |row| {
            (0..width)
                .map(|i| row.get::<_, Sql>(i))
                .collect::<Result<Vec<_>, _>>()
        })?;
        let mut rows = Vec::new();
        for item in mapped {
            let cells = item?;
            let mut values = BTreeMap::new();
            let mut index = 0;
            if !opts.group_by.is_empty() {
                values.insert(opts.group_by.clone(), sql_to_json(cells[0].clone()));
                index = 1;
            }
            for (i, aggregate) in opts.aggregates.iter().enumerate() {
                values.insert(
                    aggregate_name(aggregate),
                    sql_to_json(cells[index + i].clone()),
                );
            }
            if opts.aggregates.is_empty() {
                values.insert("count".to_owned(), sql_to_json(cells[index].clone()));
            }
            rows.push(DatasetQueryRow {
                row_key: String::new(),
                identity: String::new(),
                values,
                source_path: String::new(),
                row_number: 0,
            });
        }
        Ok(DatasetQueryOutcome {
            rows,
            total_rows: total,
            limit,
            offset,
        })
    }

    /// Go `service.DatasetQuery` over an already described dataset: `schema`
    /// maps each declared column to its type (blank types are `text`).
    ///
    /// # Errors
    /// Returns the sidecar query errors.
    pub fn dataset_service_query(
        &self,
        slug: &str,
        schema: &BTreeMap<String, String>,
        options: &DatasetServiceOptions,
    ) -> Result<DatasetServiceResult, SidecarError> {
        let query = sidecar_options(schema, options);
        let queried = self.query_dataset(slug, &query)?;
        let grouped = !options.group_by.is_empty() || !options.aggregates.is_empty();
        let mut result = DatasetServiceResult {
            dataset: slug.to_owned(),
            columns: if grouped {
                aggregate_columns(&options.group_by, &options.aggregates)
            } else {
                query.columns.clone()
            },
            rows: Vec::with_capacity(queried.rows.len()),
            total_rows: queried.total_rows,
            returned_rows: 0,
            limit: queried.limit,
            capped: false,
        };
        for row in &queried.rows {
            let mut values = row.values.clone();
            values.insert("identity".to_owned(), Value::String(row.identity.clone()));
            values.insert("_identity".to_owned(), Value::String(row.identity.clone()));
            values.insert("_key".to_owned(), Value::String(row.row_key.clone()));
            if !grouped {
                values = query
                    .columns
                    .iter()
                    .map(|c| (c.clone(), values.get(c).cloned().unwrap_or(Value::Null)))
                    .collect();
            }
            for aggregate in &options.aggregates {
                if aggregate.function.eq_ignore_ascii_case("count") {
                    let name = if aggregate.alias.is_empty() {
                        if aggregate.column.is_empty() {
                            "count".to_owned()
                        } else {
                            format!("count_{}", aggregate.column)
                        }
                    } else {
                        aggregate.alias.clone()
                    };
                    if let Some(Value::Number(count)) = values.get(&name) {
                        #[allow(clippy::cast_possible_truncation)]
                        let truncated = count.as_f64().unwrap_or(0.0) as i64;
                        values.insert(name, Value::Number(Number::from(truncated)));
                    }
                }
            }
            result.rows.push(values);
        }
        #[allow(clippy::cast_possible_wrap)]
        {
            result.returned_rows = result.rows.len() as i64;
        }
        result.capped = result.returned_rows < result.total_rows - queried.offset.max(0);
        Ok(result)
    }

    /// Go `executeDatasetViewWithTotal`: projects a dataset-sourced saved view
    /// into rows plus the uncapped total.
    ///
    /// # Errors
    /// Returns Go's empty-source error or the sidecar query errors.
    pub fn dataset_view_rows(
        &self,
        schema: &BTreeMap<String, String>,
        view: &DatasetViewSpec,
        limit: i64,
    ) -> Result<(Vec<BTreeMap<String, Value>>, i64), SidecarError> {
        let source = view.source.trim();
        let slug = source.strip_prefix("dataset:").unwrap_or(source).trim();
        if slug.is_empty() {
            return Err(contract(
                "view source dataset slug cannot be empty".to_owned(),
            ));
        }
        let mut options = DatasetServiceOptions {
            filters: view.filters.clone(),
            filter_group: view.filter_group.clone(),
            sorts: view.sorts.clone(),
            limit,
            ..DatasetServiceOptions::default()
        };
        if !view.group_by.is_empty() {
            options.group_by.clone_from(&view.group_by);
            options.aggregates = vec![DatasetAggregate {
                column: String::new(),
                function: "count".to_owned(),
                alias: "count".to_owned(),
            }];
        }
        let query = sidecar_options(schema, &options);
        let queried = self.query_dataset(slug, &query)?;
        let mut results = Vec::with_capacity(queried.rows.len());
        for row in &queried.rows {
            let mut values = row.values.clone();
            if !row.identity.is_empty() {
                values.insert("identity".to_owned(), Value::String(row.identity.clone()));
                values.insert("_identity".to_owned(), Value::String(row.identity.clone()));
                values
                    .entry("_title".to_owned())
                    .or_insert_with(|| Value::String(row.identity.clone()));
            }
            if !row.row_key.is_empty() {
                values.insert("_key".to_owned(), Value::String(row.row_key.clone()));
            }
            if !row.source_path.is_empty() {
                values.insert("_path".to_owned(), Value::String(row.source_path.clone()));
            }
            for (name, formula, rollup) in &view.computed {
                if !formula.is_empty() {
                    let evaluated = evaluate_formula(formula, &values);
                    values.insert(name.clone(), Value::String(evaluated));
                } else if !rollup.is_empty() {
                    values.insert(name.clone(), Value::String(String::new()));
                }
            }
            results.push(values);
        }
        Ok((results, queried.total_rows))
    }
}

fn aggregate_name(aggregate: &DatasetAggregate) -> String {
    if !aggregate.alias.is_empty() {
        return aggregate.alias.clone();
    }
    if aggregate.function.eq_ignore_ascii_case("count") && aggregate.column.is_empty() {
        return "count".to_owned();
    }
    format!("{}_{}", go_lowercase(&aggregate.function), aggregate.column)
}

fn aggregate_columns(group_by: &str, aggregates: &[DatasetAggregate]) -> Vec<String> {
    let mut columns = Vec::new();
    if !group_by.is_empty() {
        columns.push(group_by.to_owned());
    }
    columns.extend(aggregates.iter().map(aggregate_name));
    if aggregates.is_empty() && !group_by.is_empty() {
        columns.push("count".to_owned());
    }
    columns
}

fn sidecar_options(
    schema: &BTreeMap<String, String>,
    options: &DatasetServiceOptions,
) -> DatasetQueryOptions {
    let schema: BTreeMap<String, String> = schema
        .iter()
        .map(|(k, v)| {
            (
                k.clone(),
                if v.is_empty() {
                    "text".to_owned()
                } else {
                    v.clone()
                },
            )
        })
        .collect();
    let columns = if options.columns.is_empty() {
        schema.keys().cloned().collect()
    } else {
        let mut seen = std::collections::BTreeSet::new();
        options
            .columns
            .iter()
            .map(|c| c.trim().to_owned())
            .filter(|c| !c.is_empty() && seen.insert(c.clone()))
            .collect()
    };
    DatasetQueryOptions {
        schema,
        columns,
        filters: options.filters.clone(),
        filter_group: options.filter_group.clone(),
        sorts: options.sorts.clone(),
        group_by: options.group_by.clone(),
        aggregates: options.aggregates.clone(),
        limit: options.limit,
        offset: 0,
    }
}

/// Go `fmt.Sprintf("%v", value)` for a JSON-decoded value.
#[must_use]
pub fn go_percent_v(value: &Value) -> String {
    match value {
        Value::Null => "<nil>".to_owned(),
        Value::Bool(b) => b.to_string(),
        Value::String(s) => s.clone(),
        Value::Number(n) => go_float_v(n.as_f64().unwrap_or(0.0)),
        Value::Array(items) => format!(
            "[{}]",
            items.iter().map(go_percent_v).collect::<Vec<_>>().join(" ")
        ),
        Value::Object(map) => {
            let sorted: BTreeMap<&String, &Value> = map.iter().collect();
            format!(
                "map[{}]",
                sorted
                    .iter()
                    .map(|(k, v)| format!("{k}:{}", go_percent_v(v)))
                    .collect::<Vec<_>>()
                    .join(" ")
            )
        }
    }
}

fn go_float_v(f: f64) -> String {
    if f == 0.0 {
        return if f.is_sign_negative() {
            "-0".to_owned()
        } else {
            "0".to_owned()
        };
    }
    let sci = format!("{f:e}");
    let (mantissa, exp) = sci.split_once('e').unwrap_or((&sci, "0"));
    let exp: i32 = exp.parse().unwrap_or(0);
    // fmt's %v is strconv's shortest %g: exponent form below 1e-4 and from 1e6.
    if (-4..6).contains(&exp) {
        return format!("{f}");
    }
    format!(
        "{mantissa}e{}{:02}",
        if exp < 0 { '-' } else { '+' },
        exp.abs()
    )
}

fn evaluate_formula(formula: &str, props: &BTreeMap<String, Value>) -> String {
    // Go ranges over a map in unspecified order; sorted order is one valid
    // order and agrees whenever no substituted value contains a placeholder.
    let mut result = formula.to_owned();
    for (key, value) in props {
        result = result.replace(&format!("{{{key}}}"), &go_percent_v(value));
    }
    result
}
