use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
};
use symdesk_ingest::notion::{self, Options};
struct Root(PathBuf);
impl Root {
    fn new() -> Self {
        let p = std::env::temp_dir().join(format!(
            "notion-parity-{}-{}-{}",
            std::process::id(),
            {
                // Parallel tests can read the same clock tick.
                static NEXT: std::sync::atomic::AtomicU64 = std::sync::atomic::AtomicU64::new(0);
                NEXT.fetch_add(1, std::sync::atomic::Ordering::Relaxed)
            },
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        fs::create_dir(&p).unwrap();
        Self(p)
    }
    fn path(&self, s: &str) -> PathBuf {
        self.0.join(s)
    }
    fn norm(&self, s: &str) -> String {
        s.replace('\\', "/")
            .replace(&self.0.to_string_lossy().replace('\\', "/"), "$ROOT")
    }
}
impl Drop for Root {
    fn drop(&mut self) {
        fs::remove_dir_all(&self.0).unwrap();
    }
}
fn fixture() -> Value {
    serde_json::from_slice(
        &fs::read(
            Path::new(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/ingest/notion.json"),
        )
        .unwrap(),
    )
    .unwrap()
}
fn normalize(root: &Root, v: Value) -> Value {
    match v {
        Value::String(s) => json!(root.norm(&s)),
        Value::Array(a) => Value::Array(a.into_iter().map(|v| normalize(root, v)).collect()),
        Value::Object(m) => Value::Object(
            m.into_iter()
                .map(|(k, v)| (k, normalize(root, v)))
                .collect(),
        ),
        other => other,
    }
}
fn snapshot(root: &Root) -> Value {
    fn walk(root: &Root, p: &Path, out: &mut BTreeMap<String, Value>) {
        if !p.exists() {
            return;
        }
        for e in fs::read_dir(p).unwrap() {
            let e = e.unwrap();
            let p = e.path();
            if p.is_dir() {
                walk(root, &p, out)
            } else {
                let body = root.norm(&String::from_utf8(fs::read(&p).unwrap()).unwrap());
                out.insert(
                    p.strip_prefix(root.path("vault"))
                        .unwrap()
                        .to_string_lossy()
                        .replace('\\', "/"),
                    json!({"sha256":format!("{:x}",Sha256::digest(body.as_bytes())),"body":body}),
                );
            }
        }
    }
    let mut out = BTreeMap::new();
    walk(root, &root.path("vault"), &mut out);
    json!(out)
}
fn replay(f: &Value) -> Result<(), String> {
    let root = Root::new();
    for (p, body) in f["input_files"].as_object().unwrap() {
        let p = root.path(p);
        fs::create_dir_all(p.parent().unwrap()).unwrap();
        fs::write(p, body.as_str().unwrap()).unwrap();
    }
    for (i, step) in f["steps"].as_array().unwrap().iter().enumerate() {
        let op = step["op"].as_str().unwrap();
        if op == "move" {
            fs::rename(root.path("vault/Alpha.md"), root.path("vault/Moved.md")).unwrap();
        } else {
            let opts = Options {
                source_dir: root.path("export").to_string_lossy().into_owned(),
                vault: root.path("vault").to_string_lossy().into_owned(),
                dry_run: op == "dry-run",
                import_run_id: if op == "new-run" {
                    "next-run"
                } else {
                    "fixed-run"
                }
                .into(),
            };
            let s = notion::run_at(&opts, "2000-01-01T00:00:00Z", "unused")
                .map_err(|e| format!("step {i}: {e}"))?;
            let actual = normalize(&root, json!(s.results));
            if actual != step["results"] {
                return Err(format!(
                    "step {i} results\nactual {actual:#}\nexpected {:#}",
                    step["results"]
                ));
            }
            for (k, v) in [
                ("imported", s.imported),
                ("skipped", s.skipped),
                ("failed", s.failed),
                ("total", s.total),
            ] {
                if json!(v) != step[k] {
                    return Err(format!("step {i} {k}"));
                }
            }
        }
        let actual = snapshot(&root);
        if actual != step["files"] {
            return Err(format!(
                "step {i} files\nactual {actual:#}\nexpected {:#}",
                step["files"]
            ));
        }
    }
    for c in f["names"].as_array().unwrap() {
        let name = c["input"].as_str().unwrap();
        let e = notion::parse_entry(
            &root.path(&format!("{name}.md")),
            &format!("Folder/{name}.md"),
            &root.0,
            false,
        );
        let path = root.norm(&notion::compute_vault_path(
            &root.path("vault").to_string_lossy(),
            &e,
        ));
        if json!(e.display_name) != c["display"]
            || json!(e.notion_id) != c["notion_id"]
            || json!(path) != c["vault_path"]
            || json!(notion::sanitize_base_name(name)) != c["sanitized"]
        {
            return Err(format!("name {name:?}: {e:?} path {path}"));
        }
    }
    let names = BTreeMap::from([("aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa".into(), "Alpha".into())]);
    for c in f["links"].as_array().unwrap() {
        let actual = notion::rewrite_links(c["input"].as_str().unwrap(), &names);
        if json!(actual) != c["output"] {
            return Err(format!("link {c}"));
        }
    }
    for c in f["csv"].as_array().unwrap() {
        let path = root.path("case.csv");
        fs::write(&path, c["input"].as_str().unwrap()).unwrap();
        let e = notion::parse_entry(&path, "case.csv", &root.0, true);
        let actual = normalize(
            &root,
            json!(notion::convert_csv(
                &e,
                &root.path("unused").to_string_lossy(),
                "fixed",
                true,
                "2000-01-01T00:00:00Z"
            )),
        );
        if actual != c["results"] {
            return Err(format!(
                "csv {}\nactual {actual:#}\nexpected {:#}",
                c["input"], c["results"]
            ));
        }
    }
    Ok(())
}
#[test]
fn notion_go_contract() {
    let f = fixture();
    assert_eq!(f["schema_version"], 1);
    assert_eq!(f["steps"].as_array().unwrap().len(), 6);
    replay(&f).unwrap();
}
#[test]
fn notion_negative_control() {
    let mut f = fixture();
    replay(&f).unwrap();
    f["steps"][0]["results"][0]["status"] = json!("corrupted");
    assert!(replay(&f).unwrap_err().contains("step 0 results"));
}
