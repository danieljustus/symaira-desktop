use serde_json::{Value, json};
use sha2::{Digest, Sha256};
use std::{
    collections::BTreeMap,
    fs,
    path::{Path, PathBuf},
};
use symdesk_ingest::paperless_export::{ImportOptions, import_at};
struct Root(PathBuf);
impl Root {
    fn new() -> Self {
        let p = std::env::temp_dir().join(format!(
            "paperless-export-{}-{}",
            std::process::id(),
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
            Path::new(env!("CARGO_MANIFEST_DIR"))
                .join("../../testdata/port/ingest/paperless-export.json"),
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
        for e in fs::read_dir(p).unwrap() {
            let p = e.unwrap().path();
            if p.is_dir() {
                walk(root, &p, out)
            } else {
                let body = root.norm(&String::from_utf8(fs::read(&p).unwrap()).unwrap());
                out.insert(
                    p.strip_prefix(root.path("vault"))
                        .unwrap()
                        .to_string_lossy()
                        .replace('\\', "/"),
                    json!({"body":body,"sha256":format!("{:x}",Sha256::digest(body.as_bytes()))}),
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
    fs::create_dir(root.path("vault")).unwrap();
    for (p, b) in f["input_files"].as_object().unwrap() {
        let p = root.path(p);
        fs::create_dir_all(p.parent().unwrap()).unwrap();
        fs::write(p, b.as_str().unwrap()).unwrap();
    }
    for (i, step) in f["steps"].as_array().unwrap().iter().enumerate() {
        let op = step["op"].as_str().unwrap();
        match op {
            "move" => fs::rename(
                root.path("vault/paperless/Paperless_1_Invoice _ α.md"),
                root.path("vault/Moved.md"),
            )
            .unwrap(),
            "collision" => fs::write(
                root.path("vault/collision.md"),
                "---\nasn: 77\n---\n\nUser document\n",
            )
            .unwrap(),
            _ => {
                let opts = ImportOptions {
                    vault_root: root.path("vault"),
                    export_dir: root.path("export"),
                    dry_run: op == "dry-run",
                };
                let result = import_at(&opts, "2000-01-01T00:00:00Z");
                let (actual, error) = match result {
                    Ok(s) => (normalize(&root, json!(s)), String::new()),
                    Err(e) => (Value::Null, root.norm(&e)),
                };
                if actual != step["summary"] {
                    return Err(format!(
                        "step {i} summary\nactual {actual:#}\nexpected {:#}",
                        step["summary"]
                    ));
                }
                if json!(error) != step["error"] {
                    return Err(format!("step {i} error {error}"));
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
    Ok(())
}
#[test]
fn paperless_export_go_contract() {
    let f = fixture();
    assert_eq!(f["steps"].as_array().unwrap().len(), 7);
    replay(&f).unwrap();
}
#[test]
fn paperless_export_negative_control() {
    let mut f = fixture();
    replay(&f).unwrap();
    f["steps"][1]["summary"]["created"] = json!(99);
    assert!(replay(&f).unwrap_err().contains("step 1 summary"));
}
