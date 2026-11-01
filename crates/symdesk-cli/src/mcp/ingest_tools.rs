//! Ingest-store tools: classification rules (`desk_rules_*` and the legacy
//! `list_rules`/`add_rule`/`delete_rule` aliases). Go registers each alias as
//! the same handler as its canonical tool.
use serde_json::{Value, json};

use super::{
    ServerConfig,
    args::{Kind, decode, field},
    read::encoded,
};
use crate::ingest_common::{SCHEMA_VERSION, configured_vault, open_store, resolve};

pub(super) fn canonical(name: &str) -> Option<&'static str> {
    Some(match name {
        "desk_rules_list" | "list_rules" => "desk_rules_list",
        "desk_rules_add" | "add_rule" => "desk_rules_add",
        "desk_rules_update" => "desk_rules_update",
        "desk_rules_delete" | "delete_rule" => "desk_rules_delete",
        _ => return None,
    })
}

/// Tools that mutate the store: only registered with `--allow-write`.
pub(super) fn is_write(canonical: &str) -> bool {
    canonical != "desk_rules_list"
}

fn store(config: &ServerConfig) -> Result<symdesk_ingest::Store, String> {
    // Go `ingestOptions(cfg)`: the root configuration with the vault flag applied.
    let vault = configured_vault(config.vault.as_deref())?;
    let resolved = resolve(&vault)?;
    open_store(&resolved)
}

pub(super) fn call(name: &str, raw: Option<&str>, config: &ServerConfig) -> Result<Value, String> {
    match canonical(name).unwrap_or_default() {
        "desk_rules_list" => {
            let store = store(config)?;
            let rules = store.list_rules().map_err(|error| error.to_string())?;
            let rules: Vec<Value> = rules
                .unwrap_or_default()
                .iter()
                .map(crate::rules_cli::rule_value)
                .collect();
            encoded(&json!({"rules": rules, "schema_version": SCHEMA_VERSION}))
        }
        "desk_rules_add" => {
            let args = decode(
                raw,
                &[
                    field("pattern", "Pattern", Kind::Str),
                    field("kind", "Kind", Kind::Str),
                    field("value", "Value", Kind::Str),
                ],
            )?;
            let (pattern, kind, value) = (args.str("pattern"), args.str("kind"), args.str("value"));
            if pattern.is_empty() || kind.is_empty() || value.is_empty() {
                return Err("pattern, kind and value are required".into());
            }
            let store = store(config)?;
            let rule = store.add_rule(&pattern, &kind, &value)?;
            encoded(
                &json!({"rule": crate::rules_cli::rule_value(&rule), "schema_version": SCHEMA_VERSION}),
            )
        }
        "desk_rules_update" => {
            let args = decode(
                raw,
                &[
                    field("id", "ID", Kind::I64),
                    field("pattern", "Pattern", Kind::Str),
                    field("kind", "Kind", Kind::Str),
                    field("value", "Value", Kind::Str),
                ],
            )?;
            let (id, pattern, kind, value) = (
                args.i64("id"),
                args.str("pattern"),
                args.str("kind"),
                args.str("value"),
            );
            if id == 0 || pattern.is_empty() || kind.is_empty() || value.is_empty() {
                return Err("id, pattern, kind and value are required".into());
            }
            let store = store(config)?;
            let rule = store.update_rule(id, &pattern, &kind, &value)?;
            encoded(
                &json!({"rule": crate::rules_cli::rule_value(&rule), "schema_version": SCHEMA_VERSION}),
            )
        }
        "desk_rules_delete" => {
            let args = decode(raw, &[field("id", "ID", Kind::I64)])?;
            let id = args.i64("id");
            if id == 0 {
                return Err("id is required".into());
            }
            let store = store(config)?;
            store.delete_rule(id)?;
            encoded(&json!({"deleted": true, "id": id, "schema_version": SCHEMA_VERSION}))
        }
        other => Err(format!("Unknown tool: {other}")),
    }
}
