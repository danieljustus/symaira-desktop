//! Frozen identity shared by Rust fixture consumers. This reads embedded JSON
//! only; Git and Go verification belongs to the immutable provenance gate.

use std::sync::OnceLock;

fn identity() -> &'static serde_json::Value {
    static IDENTITY: OnceLock<serde_json::Value> = OnceLock::new();
    IDENTITY.get_or_init(|| {
        let value: serde_json::Value =
            serde_json::from_str(include_str!("../../../testdata/port/provenance.json"))
                .expect("canonical provenance JSON");
        let commit = value["oracle"]["commit"].as_str().expect("source P");
        assert!(
            commit.len() == 40
                && commit
                    .bytes()
                    .all(|byte| byte.is_ascii_digit() || (b'a'..=b'f').contains(&byte)),
            "source P must be a full lowercase commit"
        );
        assert!(
            !value["oracle"]["release"]
                .as_str()
                .expect("source release")
                .trim()
                .is_empty(),
            "source release must not be blank"
        );
        value
    })
}

pub fn commit() -> &'static str {
    identity()["oracle"]["commit"].as_str().expect("source P")
}

#[allow(dead_code)] // Some fixture schemas record a commit without a release.
pub fn release() -> &'static str {
    identity()["oracle"]["release"]
        .as_str()
        .expect("source release")
}

#[allow(dead_code)] // Typed consumers validate their fields directly.
pub fn validate_live_document(rel: &str, doc: &serde_json::Value) -> Result<(), &'static str> {
    let mut commits = Vec::new();
    let mut releases = Vec::new();
    for field in ["oracle_commit", "oracle_revision"] {
        if let Some(value) = doc.get(field) {
            commits.push(value.as_str().ok_or("source commit must be a string")?);
        }
    }
    if let Some(value) = doc.get("oracle_release") {
        releases.push(value.as_str().ok_or("source release must be a string")?);
    }
    if let Some(value) = doc.get("oracle") {
        if let Some(scalar) = value.as_str() {
            let description = match rel {
                "testdata/port/vault/base-view-write.json" => "internal/dbviews.Manager file APIs",
                "testdata/port/vault/notebook-write.json" => {
                    "internal/notebook (Go production API)"
                }
                _ => "",
            };
            if scalar.is_empty() || scalar != description {
                commits.push(scalar);
            }
        } else {
            commits.push(value["commit"].as_str().ok_or("source commit is missing")?);
            if let Some(release) = value.get("release") {
                releases.push(release.as_str().ok_or("source release must be a string")?);
            }
        }
    }
    if commits.iter().any(|value| *value != commit()) {
        return Err("live fixture differs from canonical source P");
    }
    if releases.iter().any(|value| *value != release()) {
        return Err("live fixture differs from canonical source release");
    }
    Ok(())
}
