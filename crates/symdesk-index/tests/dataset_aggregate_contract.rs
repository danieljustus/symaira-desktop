use std::{
    fs,
    path::PathBuf,
    sync::atomic::{AtomicU64, Ordering},
};

use serde_json::{Value, json};
use symdesk_index::{DatasetRow, Sidecar};

static COUNTER: AtomicU64 = AtomicU64::new(0);
const FIXTURE: &str = include_str!("../../../testdata/port/dataset/query-aggregate.json");

#[test]
fn grouped_count_projection_matches_go_service_fixture() {
    let fixture: Value = serde_json::from_str(FIXTURE).expect("parse Go aggregate fixture");
    assert_eq!(fixture["schema_version"], 1);
    assert_eq!(
        fixture["oracle"]["commit"],
        "38891d35eb8ceb6c348eca9a78b3fb2873677e3d"
    );
    assert_eq!(fixture["oracle"]["release"], "post-v0.12.2-security-880");
    let dataset = fixture["dataset"].as_str().expect("dataset");
    let query = &fixture["query"];
    let group_by = query["group_by"].as_str().expect("group_by");
    assert_eq!(group_by, "status");
    assert_eq!(query["aggregates"][0]["function"], "count");
    assert_eq!(query["aggregates"][0]["column"], "");
    let limit = usize::try_from(query["limit"].as_u64().expect("limit")).expect("limit range");

    let root = sandbox_root();
    let mut sidecar = Sidecar::open(&root.join("sidecar.db")).expect("open sidecar");
    let source_rows = fixture["rows"].as_array().expect("rows");
    assert_eq!(source_rows.len(), 4);
    let rows = source_rows
        .iter()
        .enumerate()
        .map(|(index, row)| {
            let identity = row["identity"].as_str().expect("identity");
            DatasetRow {
                dataset_slug: dataset.to_owned(),
                row_key: format!("identity:{identity}"),
                identity: identity.to_owned(),
                values_json: row["values"].to_string(),
                source_path: "datasets/orders.csv".to_owned(),
                row_number: index + 2,
            }
        })
        .collect::<Vec<_>>();
    sidecar
        .replace_dataset_rows(dataset, &rows)
        .expect("seed dataset rows");

    let result = sidecar
        .dataset_group_count(dataset, group_by, limit)
        .expect("query group counts");
    let rows = result
        .rows
        .into_iter()
        .map(|row| {
            json!({
                "status": row.group_value,
                "count": row.count,
                "identity": "",
                "_identity": "",
                "_key": "",
            })
        })
        .collect::<Vec<_>>();
    let returned_rows = rows.len();
    let got = json!({
        "dataset": dataset,
        "columns": ["status", "count"],
        "rows": rows,
        "total_rows": result.total_groups,
        "returned_rows": returned_rows,
        "limit": result.limit,
        "capped": result.capped,
    });
    assert_eq!(got, fixture["result"]);

    sidecar.close().expect("close sidecar");
    fs::remove_dir_all(&root).expect("remove sandbox");
}

fn sandbox_root() -> PathBuf {
    let counter = COUNTER.fetch_add(1, Ordering::Relaxed);
    let root = std::env::temp_dir().join(format!(
        "symdesk-dataset-aggregate-rust-{}-{counter}",
        std::process::id()
    ));
    fs::create_dir(&root).expect("create unique sandbox");
    root
}
