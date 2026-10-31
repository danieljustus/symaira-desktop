//! DATA-002: replays the Go-owned full dataset-query fixture
//! (`testdata/port/dataset/query-full.json`) against the Rust engine.

#[path = "../../../scripts/rust-port/rust/oracle_identity.rs"]
mod oracle_identity;

use std::{
    collections::BTreeMap,
    fs,
    sync::atomic::{AtomicU64, Ordering},
};

use serde_json::{Value, json};
use symdesk_index::{
    DatasetAggregate, DatasetQueryFilter, DatasetQueryFilterGroup, DatasetQueryOptions, DatasetRow,
    DatasetServiceOptions, DatasetSort, DatasetViewSpec, Sidecar,
};

const FIXTURE: &str = include_str!("../../../testdata/port/dataset/query-full.json");
static COUNTER: AtomicU64 = AtomicU64::new(0);

struct Fixture {
    value: Value,
    schema: BTreeMap<String, String>,
}

fn fixture() -> Fixture {
    let value: Value = serde_json::from_str(FIXTURE).expect("parse fixture");
    assert_eq!(value["schema_version"], 1);
    assert_eq!(value["oracle"]["commit"], oracle_identity::commit());
    assert_eq!(value["oracle"]["release"], oracle_identity::release());
    let schema = value["schema"]
        .as_object()
        .expect("schema")
        .iter()
        .map(|(k, v)| (k.clone(), v.as_str().expect("type").to_owned()))
        .collect();
    Fixture { value, schema }
}

fn seeded() -> (Sidecar, std::path::PathBuf, Fixture) {
    let fx = fixture();
    let root = std::env::temp_dir().join(format!(
        "symdesk-dataset-query-full-{}-{}",
        std::process::id(),
        COUNTER.fetch_add(1, Ordering::Relaxed)
    ));
    fs::create_dir_all(&root).expect("sandbox");
    let mut sidecar = Sidecar::open(&root.join("sidecar.db")).expect("open sidecar");
    let rows: Vec<DatasetRow> = fx.value["rows"]
        .as_array()
        .expect("rows")
        .iter()
        .map(|row| DatasetRow {
            dataset_slug: "orders".to_owned(),
            row_key: row["row_key"].as_str().unwrap().to_owned(),
            identity: row["identity"].as_str().unwrap().to_owned(),
            values_json: row["values_json"].as_str().unwrap().to_owned(),
            source_path: row["source_path"].as_str().unwrap().to_owned(),
            row_number: usize::try_from(row["row_number"].as_u64().unwrap()).unwrap(),
        })
        .collect();
    assert_eq!(rows.len(), 8);
    sidecar.replace_dataset_rows("orders", &rows).expect("seed rows");
    (sidecar, root, fx)
}

fn filters(value: &Value) -> Vec<DatasetQueryFilter> {
    value.as_array().map_or_else(Vec::new, |list| {
        list.iter().map(|f| serde_json::from_value(f.clone()).unwrap()).collect()
    })
}

fn group(value: &Value) -> Option<DatasetQueryFilterGroup> {
    (!value.is_null()).then(|| serde_json::from_value(value.clone()).unwrap())
}

fn sorts(value: &Value) -> Vec<DatasetSort> {
    value.as_array().map_or_else(Vec::new, |list| {
        list.iter()
            .map(|s| DatasetSort {
                key: s["key"].as_str().unwrap().to_owned(),
                ascending: s["ascending"].as_bool().unwrap_or(false),
            })
            .collect()
    })
}

fn aggregates(value: &Value) -> Vec<DatasetAggregate> {
    value.as_array().map_or_else(Vec::new, |list| {
        list.iter().map(|a| serde_json::from_value(a.clone()).unwrap()).collect()
    })
}

fn strings(value: &Value) -> Vec<String> {
    value.as_array().map_or_else(Vec::new, |list| {
        list.iter().map(|s| s.as_str().unwrap().to_owned()).collect()
    })
}

fn service_options(options: &Value) -> DatasetServiceOptions {
    DatasetServiceOptions {
        columns: strings(&options["columns"]),
        filters: filters(&options["filters"]),
        filter_group: group(&options["filter_group"]),
        sorts: sorts(&options["sorts"]),
        group_by: options["group_by"].as_str().unwrap_or("").to_owned(),
        aggregates: aggregates(&options["aggregates"]),
        limit: options["limit"].as_i64().unwrap_or(0),
    }
}

fn sidecar_options(fx: &Fixture, options: &Value, offset: i64) -> DatasetQueryOptions {
    let service = service_options(options);
    let columns = if service.columns.is_empty() {
        fx.schema.keys().cloned().collect()
    } else {
        let mut seen = std::collections::BTreeSet::new();
        service
            .columns
            .iter()
            .map(|c| c.trim().to_owned())
            .filter(|c| !c.is_empty() && seen.insert(c.clone()))
            .collect()
    };
    DatasetQueryOptions {
        schema: fx.schema.clone(),
        columns,
        filters: service.filters,
        filter_group: service.filter_group,
        sorts: service.sorts,
        group_by: service.group_by,
        aggregates: service.aggregates,
        limit: service.limit,
        offset,
    }
}

fn view_spec(view: &Value) -> DatasetViewSpec {
    let mut computed: Vec<(String, String, String)> = view["computed"]
        .as_object()
        .map_or_else(Vec::new, |map| {
            map.iter()
                .map(|(name, c)| {
                    (
                        name.clone(),
                        c["formula"].as_str().unwrap_or("").to_owned(),
                        c["rollup"].as_str().unwrap_or("").to_owned(),
                    )
                })
                .collect()
        });
    computed.sort();
    DatasetViewSpec {
        source: view["source"].as_str().unwrap_or("").to_owned(),
        filters: filters(&view["filters"]),
        filter_group: group(&view["filter_group"]),
        sorts: sorts(&view["sorts"]),
        group_by: view["group_by"].as_str().unwrap_or("").to_owned(),
        computed,
    }
}

fn run_case(sidecar: &Sidecar, fx: &Fixture, case: &Value) -> Result<Value, String> {
    let slug = case["slug"].as_str().unwrap();
    match case["kind"].as_str().unwrap() {
        "service" => sidecar
            .dataset_service_query(slug, &fx.schema, &service_options(&case["options"]))
            .map(|result| serde_json::to_value(result).unwrap())
            .map_err(|e| e.to_string()),
        "sidecar" => {
            let offset = case["offset"].as_i64().unwrap_or(0);
            sidecar
                .query_dataset(slug, &sidecar_options(fx, &case["options"], offset))
                .map(|out| {
                    json!({
                        "rows": out.rows.iter().map(|r| json!({
                            "row_key": r.row_key, "identity": r.identity, "values": r.values,
                            "source_path": r.source_path, "row_number": r.row_number,
                        })).collect::<Vec<_>>(),
                        "total_rows": out.total_rows, "limit": out.limit, "offset": out.offset,
                    })
                })
                .map_err(|e| e.to_string())
        }
        "view" => sidecar
            .dataset_view_rows(&fx.schema, &view_spec(&case["view"]), case["limit"].as_i64().unwrap_or(0))
            .map(|(rows, total)| json!({"rows": rows, "total_rows": total}))
            .map_err(|e| e.to_string()),
        other => panic!("unknown kind {other}"),
    }
}

#[test]
fn go_dataset_query_contract_replays_every_case() {
    let (sidecar, root, fx) = seeded();
    let cases = fx.value["cases"].as_array().expect("cases");
    assert!(cases.len() >= 200, "corpus shrank to {} cases", cases.len());
    let (mut ok, mut failed) = (0usize, 0usize);
    for case in cases {
        let name = case["name"].as_str().unwrap();
        match run_case(&sidecar, &fx, case) {
            Ok(got) => {
                assert_eq!(case["error"], "", "{name}: Go failed but Rust succeeded");
                if got != case["result"] {
                    panic!("{name}: first difference at {}", first_difference("", &got, &case["result"]));
                }
                ok += 1;
            }
            Err(error) => {
                assert_eq!(case["error"], Value::String(error.clone()), "{name}: error text");
                assert!(case["result"].is_null(), "{name}: Go succeeded but Rust failed: {error}");
                failed += 1;
            }
        }
    }
    println!("dataset query full: {ok} ok, {failed} error cases of {}", cases.len());
    assert!(ok >= 190 && failed >= 8);
    drop(sidecar);
    fs::remove_dir_all(root).expect("remove sandbox");
}

#[test]
fn negative_control_a_mutated_expectation_is_detected() {
    let (sidecar, root, fx) = seeded();
    let case = fx.value["cases"]
        .as_array()
        .unwrap()
        .iter()
        .find(|c| c["name"] == "text-equals-non-ascii-fold")
        .expect("case");
    let got = run_case(&sidecar, &fx, case).expect("query");
    let mut mutated = case["result"].clone();
    mutated["rows"][0]["id"] = json!("r1");
    assert_ne!(got, mutated, "a wrong expectation must not compare equal");
    // The ASCII-only SQL LOWER must not fold 'Ä': Go lowers the operand to 'äpfel'.
    assert_eq!(got["rows"], json!([{"id": "r2"}]));
    drop(sidecar);
    fs::remove_dir_all(root).expect("remove sandbox");
}

fn first_difference(path: &str, left: &Value, right: &Value) -> String {
    match (left, right) {
        (Value::Object(a), Value::Object(b)) => {
            for key in a.keys().chain(b.keys()) {
                let (l, r) = (a.get(key).unwrap_or(&Value::Null), b.get(key).unwrap_or(&Value::Null));
                if l != r || a.contains_key(key) != b.contains_key(key) {
                    return first_difference(&format!("{path}/{key}"), l, r);
                }
            }
            format!("{path}: objects equal?")
        }
        (Value::Array(a), Value::Array(b)) if a.len() == b.len() => {
            for (i, (l, r)) in a.iter().zip(b).enumerate() {
                if l != r {
                    return first_difference(&format!("{path}[{i}]"), l, r);
                }
            }
            format!("{path}: arrays equal?")
        }
        _ => format!("{path}: rust={left} go={right}"),
    }
}
