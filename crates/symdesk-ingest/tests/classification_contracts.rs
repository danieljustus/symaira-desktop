//! Replays `classification.json` (real Go facade: rules, TestRules, DryRunRule
//! over real note files) and `classify-pipeline.json` (real Go Pipeline.Ingest
//! classification and persisted columns).
use serde_json::Value;
use sha2::{Digest, Sha256};
use std::path::{Path, PathBuf};
use symdesk_ingest::classification::{Presets, classify, dry_run_rule, test_rules};
use symdesk_ingest::{DocumentPaths, Store, extract};

struct Temp(PathBuf);
impl Drop for Temp {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

fn fixture(name: &str) -> Value {
    let path = Path::new(env!("CARGO_MANIFEST_DIR"))
        .join("../../testdata/port/ingest")
        .join(name);
    serde_json::from_slice(&std::fs::read(path).unwrap()).unwrap()
}

fn go_json<T: serde::Serialize>(v: &T) -> String {
    serde_json::to_string(v)
        .unwrap()
        .replace('&', "\\u0026")
        .replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029")
}

fn text(v: &Value) -> &str {
    v.as_str().unwrap()
}

#[test]
fn facade_rules_and_dry_run() {
    let fx = fixture("classification.json");
    let temp = Temp(std::env::temp_dir().join(format!("ingest-class-{}", std::process::id())));
    let root = temp.0.to_string_lossy().into_owned();
    let vault = temp.0.join("vault");
    std::fs::create_dir_all(&vault).unwrap();
    let store = Store::open(&temp.0.join("db/ingest.sqlite")).unwrap();
    let norm = |s: &str| s.replace(&root, "<ROOT>");
    let mut set_up = false;
    let steps = fx["steps"].as_array().unwrap();
    assert_eq!(steps.len(), 48);
    let (mut tests, mut dry) = (0, 0);
    for (i, step) in steps.iter().enumerate() {
        let input = &step["input"];
        let (pattern, kind, value) = (
            text(&input["pattern"]),
            text(&input["kind"]),
            text(&input["value"]),
        );
        let (raw, error) = match text(&input["op"]) {
            "rules" | "add" => {
                let r: Result<String, String> = if text(&input["op"]) == "rules" {
                    store.list_rules().map(|r| {
                        let mut r = r.unwrap_or_default();
                        for x in &mut r {
                            x.created_at = "<CREATED_AT>".into();
                        }
                        go_json(&r)
                    })
                } else {
                    store.add_rule(pattern, kind, value).map(|mut r| {
                        r.created_at = "<CREATED_AT>".into();
                        go_json(&r)
                    })
                };
                match r {
                    Ok(raw) => (raw, String::new()),
                    Err(e) => ("null".into(), e),
                }
            }
            "test" => {
                tests += 1;
                let rules = store.list_rules().unwrap().unwrap_or_default();
                (
                    go_json(&test_rules(&rules, text(&input["text"]))),
                    String::new(),
                )
            }
            "dryrun" => {
                dry += 1;
                if !set_up {
                    set_up = true;
                    for (n, note) in fx["notes"].as_array().unwrap().iter().enumerate() {
                        let path = vault.join(text(&note["name"]));
                        match text(&note["mode"]) {
                            "file" => std::fs::write(&path, text(&note["content"])).unwrap(),
                            "dir" => std::fs::create_dir_all(&path).unwrap(),
                            _ => {}
                        }
                        let (doc, _) = store
                            .create_or_get(
                                &format!("src{}", text(&note["name"])),
                                &format!("hash-{n}"),
                                "text/markdown",
                                &vault.to_string_lossy(),
                            )
                            .unwrap();
                        store
                            .set_vault_and_archive_path(
                                doc.id,
                                &DocumentPaths {
                                    vault_path: &path.to_string_lossy(),
                                    ..DocumentPaths::default()
                                },
                            )
                            .unwrap();
                    }
                    store
                        .create_or_get(
                            "pending.txt",
                            "hash-pending",
                            "text/plain",
                            &vault.to_string_lossy(),
                        )
                        .unwrap();
                }
                match dry_run_rule(&store, &vault.to_string_lossy(), pattern, kind, value) {
                    Ok(mut r) => {
                        r.vault_path = norm(&r.vault_path);
                        for m in &mut r.matches {
                            m.note_path = norm(&m.note_path);
                        }
                        for s in &mut r.skipped {
                            s.note_path = norm(&s.note_path);
                            s.reason = norm(&s.reason);
                        }
                        (go_json(&r), String::new())
                    }
                    Err(e) => ("null".into(), norm(&e)),
                }
            }
            other => panic!("unknown op {other}"),
        };
        assert_eq!(error, text(&step["error"]), "error step {i} {input}");
        assert_eq!(raw, text(&step["result_json"]), "result step {i} {input}");
    }
    assert!(tests >= 15 && dry >= 12, "tests={tests} dry={dry}");
}

#[test]
fn pipeline_classification_matches_go() {
    let fx = fixture("classify-pipeline.json");
    let temp = Temp(std::env::temp_dir().join(format!("ingest-classify-{}", std::process::id())));
    std::fs::create_dir_all(&temp.0).unwrap();
    let store = Store::open(&temp.0.join("docs.db")).unwrap();
    for r in fx["rules"].as_array().unwrap() {
        store
            .add_rule(text(&r[0]), text(&r[1]), text(&r[2]))
            .unwrap();
    }
    let rules = store.list_rules().unwrap().unwrap();
    let cases = fx["cases"].as_array().unwrap();
    let outcomes = fx["outcomes"].as_array().unwrap();
    assert_eq!(cases.len(), outcomes.len());
    assert!(cases.len() >= 15);
    for (case, out) in cases.iter().zip(outcomes) {
        let name = text(&case["name"]);
        assert_eq!(text(&out["error"]), "", "{name}: Go ingest failed");
        let body = text(&case["body"]).as_bytes();
        let file_name = text(&case["file_name"]);
        let mime = extract::detect_bytes(file_name, body).unwrap();
        let extracted = extract::extract_bytes(body, &mime, usize::MAX).unwrap();
        assert_eq!(extracted.text, text(&out["text"]), "{name}: extracted text");
        let preset_tags: Vec<String> = case["preset_tags"]
            .as_array()
            .map(|a| a.iter().map(|v| text(v).to_owned()).collect())
            .unwrap_or_default();
        let presets = Presets {
            category: case["preset_category"].as_str().unwrap_or(""),
            tags: &preset_tags,
            correspondent: case["preset_correspondent"].as_str().unwrap_or(""),
            document_type: case["preset_document_type"].as_str().unwrap_or(""),
        };
        let c = classify(&rules, &extracted.text, &presets);
        assert_eq!(c.category, text(&out["category"]), "{name}: category");
        assert_eq!(
            serde_json::to_value(&c.tags).unwrap(),
            out["tags"],
            "{name}: tags"
        );
        assert_eq!(c.correspondent, text(&out["correspondent"]), "{name}");
        assert_eq!(c.document_type, text(&out["document_type"]), "{name}");

        // Persisted columns: the same write the pipeline performs after the job.
        let hash = format!("{:x}", Sha256::digest(body));
        let (doc, created) = store
            .create_or_get(file_name, &hash, &mime, &format!("/v/{name}"))
            .unwrap();
        assert!(created, "{name}");
        let tags = c.tags.clone().unwrap_or_default();
        store
            .set_vault_and_archive_path(
                doc.id,
                &DocumentPaths {
                    vault_path: "/note.md",
                    archive_path: "",
                    category: &c.category,
                    tags: &tags,
                    correspondent: &c.correspondent,
                    document_type: &c.document_type,
                },
            )
            .unwrap();
        let stored = store
            .by_hash_in_vault(&hash, &format!("/v/{name}"))
            .unwrap();
        assert_eq!(stored.category, text(&out["doc_category"]), "{name}");
        assert_eq!(
            serde_json::to_value(&stored.tags).unwrap(),
            out["doc_tags"],
            "{name}: stored tags"
        );
        assert_eq!(stored.correspondent, text(&out["doc_correspondent"]));
        assert_eq!(stored.document_type, text(&out["doc_document_type"]));
    }
}

#[test]
fn classification_negative_control() {
    let rule = |id, pattern: &str, kind: &str, value: &str| symdesk_ingest::ClassificationRule {
        id,
        pattern: pattern.into(),
        kind: kind.into(),
        value: value.into(),
        created_at: String::new(),
    };
    let rules = [rule(1, "invoice", "tag", "billing")];
    let c = classify(&rules, "INVOICE", &Presets::default());
    assert_eq!(c.tags, Some(vec!["billing".into()]));
    assert_ne!(c.tags, Some(vec!["Billing".into()]));
    // Go lowercases İ to plain i; Unicode full lowercase would not match.
    let rules = [rule(1, "istanbul", "tag", "x")];
    assert!(
        classify(&rules, "İSTANBUL", &Presets::default())
            .tags
            .is_some()
    );
}
