//! Local indexed vault graph. Optional external entity enrichment is not yet ported.
use std::{collections::BTreeSet, process::ExitCode};

use clap::{Arg, ArgMatches, Command};
use serde::Serialize;
use symdesk_index::open_for_vault;
use symdesk_vault::{ResolveDocument, Resolver};

use crate::{emit_error, relative_path, resolve_vault, write_go_json, write_stdout};

#[derive(Serialize)]
struct Node {
    id: String,
    label: String,
}
#[derive(Serialize)]
struct Edge {
    source: String,
    target: String,
}
#[derive(Serialize)]
struct Graph {
    nodes: Vec<Node>,
    edges: Vec<Edge>,
}

pub fn cli() -> Command {
    // Go deliberately accepts and ignores positional arguments.
    Command::new("graph")
        .about("Get graph data")
        .arg(Arg::new("ignored").num_args(0..))
}

pub fn run(_command: &ArgMatches, vault: Option<&str>, output_json: bool) -> ExitCode {
    let result = (|| -> Result<Graph, String> {
        let root = resolve_vault(vault).map_err(|e| {
            if e.starts_with("vault path not configured") {
                "vault path not configured (use flag or SYMDESK_VAULT env)".to_owned()
            } else {
                crate::index_cli::index_vault_error(vault, &e)
            }
        })?;
        let mut sidecar = open_for_vault(&root).map_err(|e| e.to_string())?;
        let result = sidecar
            .graph_inputs()
            .map_err(|e| e.to_string())
            .map(|(documents, links)| {
                let documents: Vec<_> = documents
                    .into_iter()
                    .map(|mut d| {
                        d.path =
                            relative_path(&root, &d.path).replace(std::path::MAIN_SEPARATOR, "/");
                        d
                    })
                    .collect();
                let resolver = Resolver::new(&documents);
                let nodes = documents
                    .iter()
                    .map(|d: &ResolveDocument| Node {
                        id: d.path.clone(),
                        label: d.title.clone(),
                    })
                    .collect();
                let mut seen = BTreeSet::new();
                let edges = links
                    .into_iter()
                    .filter_map(|(source, target)| {
                        let source =
                            relative_path(&root, &source).replace(std::path::MAIN_SEPARATOR, "/");
                        let target = resolver.resolve(&target);
                        seen.insert((source.clone(), target.clone()))
                            .then_some(Edge { source, target })
                    })
                    .collect();
                Graph { nodes, edges }
            });
        if let Err(e) = sidecar.close() {
            eprintln!("warning: failed to close sidecar database: {e}");
        }
        result
    })();
    match result {
        Ok(graph) if output_json => write_go_json(&graph),
        Ok(graph) => write_stdout(format!(
            "&{{Nodes:[{}] Edges:[{}]}}\n",
            graph
                .nodes
                .iter()
                .map(|n| format!("{{ID:{} Label:{}}}", n.id, n.label))
                .collect::<Vec<_>>()
                .join(" "),
            graph
                .edges
                .iter()
                .map(|e| format!("{{Source:{} Target:{}}}", e.source, e.target))
                .collect::<Vec<_>>()
                .join(" ")
        )),
        Err(e) => emit_error(e, output_json),
    }
}
