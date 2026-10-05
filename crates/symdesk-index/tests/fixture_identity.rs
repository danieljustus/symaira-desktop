#![deny(unsafe_code)]

#[path = "../../../scripts/rust-port/rust/oracle_identity.rs"]
mod oracle_identity;

use std::{fs, path::Path};

use serde_json::{Value, json};
use sha2::{Digest, Sha256};

// Payloads include deliberately deep parser inputs. Identity validation only
// needs top-level provenance fields; skip payloads without recursively building
// a serde_json::Value for those independent contract cases.
struct IdentityHeader(Value);

impl<'de> serde::Deserialize<'de> for IdentityHeader {
    fn deserialize<D: serde::Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        struct Visitor;
        impl<'de> serde::de::Visitor<'de> for Visitor {
            type Value = IdentityHeader;

            fn expecting(&self, formatter: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
                formatter.write_str("a fixture object with optional source headers")
            }

            fn visit_map<A: serde::de::MapAccess<'de>>(
                self,
                mut map: A,
            ) -> Result<Self::Value, A::Error> {
                let mut header = serde_json::Map::new();
                while let Some(key) = map.next_key::<String>()? {
                    match key.as_str() {
                        "oracle" | "oracle_commit" | "oracle_revision" | "oracle_release" => {
                            if header.contains_key(&key) {
                                return Err(serde::de::Error::custom(
                                    "duplicate fixture identity field",
                                ));
                            }
                            header.insert(key, map.next_value::<Value>()?);
                        }
                        _ => {
                            map.next_value::<serde::de::IgnoredAny>()?;
                        }
                    }
                }
                Ok(IdentityHeader(Value::Object(header)))
            }
        }
        deserializer.deserialize_map(Visitor)
    }
}

#[test]
fn frozen_live_fixtures_have_one_source_identity_and_exact_checksums() {
    let root = Path::new(env!("CARGO_MANIFEST_DIR")).join("../..");
    let provenance: Value =
        serde_json::from_str(include_str!("../../../testdata/port/provenance.json"))
            .expect("frozen provenance");
    let checksums = provenance["fixture_checksums"]
        .as_object()
        .expect("fixture manifest");
    assert!(checksums.len() >= 90, "complete port fixture inventory");
    for (rel, expected) in checksums {
        assert!(rel.starts_with("testdata/port/") && !rel.split('/').any(|part| part == ".."));
        let bytes = fs::read(root.join(rel)).expect("committed fixture bytes");
        assert_eq!(
            format!("{:x}", Sha256::digest(&bytes)),
            expected.as_str().expect("SHA-256"),
            "{rel}"
        );
        if rel.ends_with(".json") {
            let IdentityHeader(document) =
                serde_json::from_slice(&bytes).expect("fixture header JSON");
            assert_eq!(
                oracle_identity::validate_live_document(rel, &document),
                Ok(()),
                "{rel}"
            );
        }
    }
}

#[test]
fn changing_only_one_embedded_identity_cannot_relabel_a_frozen_fixture() {
    let rel = "testdata/port/core/config.json";
    let live = json!({"oracle": {"commit": oracle_identity::commit(), "release": oracle_identity::release()}});
    assert_eq!(oracle_identity::validate_live_document(rel, &live), Ok(()));
    for mutated in [
        json!({"oracle": {"commit": "ffffffffffffffffffffffffffffffffffffffff", "release": oracle_identity::release()}}),
        json!({"oracle": {"commit": oracle_identity::commit(), "release": "invented-release"}}),
        json!({"oracle_commit": oracle_identity::commit(), "oracle_revision": "HEAD"}),
        json!({"oracle": "undeclared API description"}),
    ] {
        assert!(oracle_identity::validate_live_document(rel, &mutated).is_err());
    }
}
