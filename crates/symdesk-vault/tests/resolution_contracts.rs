#![deny(unsafe_code)]

use noyalib as _;
use serde::Deserialize;
use symdesk_vault::{ResolveDocument, ResolvedEdge, ResolvedNode, resolve_graph};
use thiserror as _;
use unicode_general_category as _;

#[derive(Deserialize)]
struct Fixture {
    schema_version: u8,
    documents: Vec<Document>,
    nodes: Vec<Node>,
    edges: Vec<Edge>,
}

#[derive(Deserialize)]
struct Document {
    path: String,
    title: String,
    #[serde(default)]
    aliases: Vec<String>,
    #[serde(default)]
    links: Vec<String>,
}

#[derive(Deserialize)]
struct Node {
    id: String,
    label: String,
}

#[derive(Deserialize)]
struct Edge {
    source: String,
    target: String,
}

#[test]
fn graph_target_resolution_matches_go_service() {
    let fixture: Fixture =
        serde_json::from_str(include_str!("../../../testdata/port/vault/resolution.json"))
            .expect("decode resolution fixture");
    assert_eq!(fixture.schema_version, 1);
    let documents: Vec<_> = fixture
        .documents
        .into_iter()
        .map(|document| ResolveDocument {
            path: document.path,
            title: document.title,
            aliases: document.aliases,
            links: document.links,
        })
        .collect();
    let (nodes, edges) = resolve_graph(&documents);
    let expected_nodes: Vec<_> = fixture
        .nodes
        .into_iter()
        .map(|node| ResolvedNode {
            id: node.id,
            label: node.label,
        })
        .collect();
    let expected_edges: Vec<_> = fixture
        .edges
        .into_iter()
        .map(|edge| ResolvedEdge {
            source: edge.source,
            target: edge.target,
        })
        .collect();
    assert_eq!(nodes, expected_nodes);
    assert_eq!(edges, expected_edges);
}
