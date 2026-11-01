//! `symdesk related <file>` and `symdesk relations related <file>` (Go
//! `cmd/symdesk/related.go`, `Service.Related`): the Memory entities that a
//! note mentions, their one-hop neighbours and the other notes that mention
//! any of them. Without the sibling `symmemory` tool the answer is empty.
use std::{path::Path, process::ExitCode};

use serde::Serialize;
use symdesk_core::{compose::Resolver, compose_probe::ToolCache};
use symdesk_service::memory::{self, MemoryEntity};
use symdesk_vault::Document;

use crate::{
    mutate_cli::parse,
    service_cli::with_deps,
    write_go_json, write_stdout,
};

#[derive(Serialize)]
struct RelatedEntity {
    name: String,
    #[serde(rename = "type")]
    kind: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    relation: String,
}

#[derive(Serialize)]
struct RelatedData {
    entities: Vec<RelatedEntity>,
    notes: Vec<String>,
}

fn matches_other(document: &Document, entity: &MemoryEntity) -> bool {
    let aliases = entity.aliases.as_deref().unwrap_or_default();
    let title = document.title.to_lowercase();
    if title == entity.name.to_lowercase() {
        return true;
    }
    let name = Path::new(&document.path)
        .file_stem()
        .map(|stem| stem.to_string_lossy().to_lowercase())
        .unwrap_or_default();
    if name == entity.name.to_lowercase() {
        return true;
    }
    if aliases.iter().any(|alias| title == alias.to_lowercase()) {
        return true;
    }
    if !document.body.is_empty() {
        let body = document.body.to_lowercase();
        if body.contains(&entity.name.to_lowercase())
            || aliases
                .iter()
                .any(|alias| body.contains(&alias.to_lowercase()))
        {
            return true;
        }
    }
    false
}

/// Go `vault.ParseFileInRoot`.
fn parse_in_root(root: &Path, requested: &str) -> Result<Document, String> {
    let (absolute, bytes) = crate::duplicates_cli::read_vault_file(root, requested)?;
    symdesk_vault::parse_bytes(&absolute.to_string_lossy(), &bytes).map_err(|e| e.to_string())
}

pub fn run(file: &str, vault: Option<&str>, json: bool) -> ExitCode {
    with_deps(vault, json, |root, db| {
        let document = parse_in_root(root, file)?;
        let mut result = RelatedData {
            entities: Vec::new(),
            notes: Vec::new(),
        };
        let render = |result: &RelatedData| {
            if json {
                return write_go_json(result);
            }
            let entities = result
                .entities
                .iter()
                .map(|entity| {
                    format!(
                        "{{Name:{} Type:{} Relation:{}}}",
                        entity.name, entity.kind, entity.relation
                    )
                })
                .collect::<Vec<_>>()
                .join(" ");
            write_stdout(format!(
                "&{{Entities:[{entities}] Notes:[{}]}}\n",
                result.notes.join(" ")
            ))
        };
        let resolver = Resolver::from_environment().map_err(|error| error.to_string())?;
        if !ToolCache::default().has_tool(&resolver, "symmemory").available {
            return Ok(render(&result));
        }
        let resolve = |name: &str| resolver.resolve(name);
        let entities = memory::list_entities(&resolve)?.unwrap_or_default();

        let lower_title = document.title.to_lowercase();
        let lower_name = Path::new(&document.path)
            .file_stem()
            .map(|stem| stem.to_string_lossy().to_lowercase())
            .unwrap_or_default();
        let lower_body = document.body.to_lowercase();
        let mut matched: Vec<MemoryEntity> = Vec::new();
        for entity in entities {
            let aliases = entity.aliases.clone().unwrap_or_default();
            let lower_entity = entity.name.to_lowercase();
            let mut matches = lower_title == lower_entity || lower_name == lower_entity;
            if !matches {
                matches = aliases.iter().any(|alias| lower_title == alias.to_lowercase());
            }
            if !matches && !document.body.is_empty() {
                matches = lower_body.contains(&lower_entity)
                    || aliases
                        .iter()
                        .any(|alias| lower_body.contains(&alias.to_lowercase()));
            }
            if matches {
                result.entities.push(RelatedEntity {
                    name: entity.name.clone(),
                    kind: entity.kind.clone(),
                    relation: String::new(),
                });
                // Go keys the matched set by entity id.
                if let Some(slot) = matched.iter_mut().find(|known| known.id == entity.id) {
                    *slot = entity;
                } else {
                    matched.push(entity);
                }
            }
        }

        let mut neighbors: Vec<MemoryEntity> = Vec::new();
        for entity in &matched {
            let Ok(found) = memory::get_neighbors(&resolve, &entity.name) else {
                continue;
            };
            let edges = found.edges.unwrap_or_default();
            for node in found.nodes.unwrap_or_default() {
                if matched.iter().any(|known| known.id == node.id) {
                    continue;
                }
                let relation = edges
                    .iter()
                    .find(|edge| {
                        (edge.from_entity_id == entity.id && edge.to_entity_id == node.id)
                            || (edge.from_entity_id == node.id && edge.to_entity_id == entity.id)
                    })
                    .map(|edge| edge.relation_type.clone())
                    .unwrap_or_default();
                if neighbors.iter().all(|known| known.id != node.id) {
                    result.entities.push(RelatedEntity {
                        name: node.name.clone(),
                        kind: node.kind.clone(),
                        relation,
                    });
                    neighbors.push(node);
                }
            }
        }

        let Ok(listed) = db.list_files(root, "") else {
            return Ok(render(&result));
        };
        for listed in listed {
            let relative = crate::relative_path(root, &listed.path);
            if relative == file {
                continue;
            }
            let Ok(other) = parse(Path::new(&listed.path)) else {
                continue;
            };
            let hit = matched
                .iter()
                .chain(neighbors.iter())
                .any(|entity| matches_other(&other, entity));
            if hit && !result.notes.contains(&relative) {
                result.notes.push(relative);
            }
        }
        Ok(render(&result))
    })
}
