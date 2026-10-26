//! Deterministic offline vault graph rendering (no AI or provider calls).
use std::fmt::Write;

use serde_json::{Value, json};

use super::ServerConfig;

pub(super) fn call(config: &ServerConfig) -> Result<Value, String> {
    let (root, sidecar) = super::super::open_sidecar(config)?;
    let (nodes, edges) = sidecar.vault_link_graph(&root).map_err(|e| e.to_string())?;
    let mut svg = String::from(
        "<svg xmlns=\"http://www.w3.org/2000/svg\" viewBox=\"0 0 1200 800\" font-family=\"system-ui, sans-serif\">\n",
    );
    for (i, node) in nodes.iter().enumerate() {
        let label = node
            .label
            .replace('&', "&amp;")
            .replace('<', "&lt;")
            .replace('>', "&gt;")
            .replace('"', "&quot;");
        let _ = writeln!(
            svg,
            "<g transform=\"translate({},{})\"><rect width=\"240\" height=\"60\" rx=\"8\" fill=\"#f3f4f6\" stroke=\"#9ca3af\"/><text x=\"120\" y=\"36\" text-anchor=\"middle\" font-size=\"13\">{label}</text></g>",
            80 + (i % 4) * 280,
            60 + (i / 4) * 90
        );
    }
    for edge in edges {
        let fx = nodes
            .iter()
            .position(|node| node.id == edge.source)
            .map_or(0, |i| (i % 4) * 280 + 200);
        let tx = nodes
            .iter()
            .position(|node| node.id == edge.target)
            .map_or(0, |i| (i % 4) * 280 + 80);
        let _ = writeln!(
            svg,
            "<line x1=\"{}\" y1=\"120\" x2=\"{}\" y2=\"120\" stroke=\"#c4b5fd\" stroke-width=\"1.5\"/>",
            fx + 80,
            tx + 80
        );
    }
    svg.push_str("</svg>\n");
    Ok(json!({"svg": svg}))
}
