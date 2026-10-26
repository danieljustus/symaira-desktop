//! SymDraw DSL parser: byte-exact port of Go `internal/draw/parse`
//! (`Source`, `ParseMermaid`, `ParseJSON`) and the `internal/draw/ir`
//! model/validation it feeds, plus the `internal/draw/measure` text
//! measurement layer (`measure`, backed by the minimal `sfnt` reader) that
//! layout and emit depend on, the `internal/draw/theme` registry and the
//! `internal/draw/scene` graph (primitives, bounds, JSON) and the
//! `internal/draw/layout` engines (layered graph layout, closed-form sequence
//! layout). Builder lowering and SVG/PDF output are not part of this module.

mod gotext;
#[rustfmt::skip]
mod go_unicode;
pub mod ir;
mod json;
pub mod layout;
pub mod measure;
mod mermaid;
pub mod scene;
mod sequence;
pub mod sfnt;
pub mod theme;

pub use ir::{Diagram, ValidationError, Value, validate};
pub use mermaid::DIALECT_VERSION;

use gotext::{quote, trim_space};

/// The `parse.ParseJSON` decoder (strict, unknown fields rejected) without
/// `ir.Validate`: the input of the layout engines' own rejections, which the
/// validator makes unreachable through `parse_source`.
pub fn decode_unvalidated(data: &[u8]) -> Result<Diagram, String> {
    json::decode_diagram(data)
}

/// Go `parse.ParseError`: stage is `parse`, `schema` or `contract`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ParseError {
    pub stage: &'static str,
    pub message: String,
    pub detail: String,
    pub hint: String,
    pub line: usize,
}

impl std::fmt::Display for ParseError {
    /// Go `(*ParseError).Error`.
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        f.write_str(&self.message)?;
        if !self.detail.is_empty() {
            write!(f, ": {}", self.detail)?;
        }
        if !self.hint.is_empty() {
            write!(f, " (hint: {})", self.hint)?;
        }
        Ok(())
    }
}

impl std::error::Error for ParseError {}

/// Go `parse.Source`: JSON IR when the trimmed source starts with `{`,
/// otherwise the Mermaid subset.
pub fn parse_source(source: &str) -> Result<Diagram, ParseError> {
    if trim_space(source).starts_with('{') {
        parse_json(source.as_bytes())
    } else {
        parse_mermaid(source)
    }
}

/// Go `parse.ParseMermaid`.
pub fn parse_mermaid(source: &str) -> Result<Diagram, ParseError> {
    mermaid::parse_mermaid(source)
}

/// Go `parse.ParseJSON`.
pub fn parse_json(data: &[u8]) -> Result<Diagram, ParseError> {
    if trim_space(&String::from_utf8_lossy(data)).is_empty() {
        return Err(ParseError {
            stage: "parse",
            message: "empty JSON diagram input".to_owned(),
            detail: String::new(),
            hint: "Provide a valid JSON payload adhering to the SymDraw IR schema.".to_owned(),
            line: 0,
        });
    }
    let diagram = json::decode_diagram(data).map_err(|detail| {
        if detail.contains("unknown field") {
            ParseError {
                stage: "schema",
                message: "unknown field in diagram JSON".to_owned(),
                detail,
                hint: "Check property names against the SymDraw IR JSON schema (internal/draw/ir/schema.go).".to_owned(),
                line: 0,
            }
        } else {
            ParseError {
                stage: "parse",
                message: "malformed JSON diagram input".to_owned(),
                detail,
                hint: "Verify JSON syntax, commas, and string quotes.".to_owned(),
                line: 0,
            }
        }
    })?;
    validate(&diagram).map_err(|err| ParseError {
        stage: "contract",
        hint: validation_hint(&err.field),
        message: err.message,
        detail: err.field,
        line: 0,
    })?;
    Ok(diagram)
}

fn validation_hint(field: &str) -> String {
    if field == "kind" {
        "Diagram kind must be one of: 'graph', 'sequence', 'timeline', 'tree', 'chart', 'custom'."
            .to_owned()
    } else if field == "direction" {
        "Diagram flow direction must be one of: 'TD', 'TB', 'BT', 'LR', 'RL'.".to_owned()
    } else if field.contains("shape") {
        "Supported shapes are: 'rect', 'round', 'circle', 'cylinder', 'diamond', 'pill', 'stadium', 'subroutine', 'hexagon', 'asymmetric'.".to_owned()
    } else if field.contains("style") {
        "Supported edge styles are: 'solid', 'dashed', 'dotted', 'thick'.".to_owned()
    } else if field.contains("arrow") {
        "Supported arrow types are: 'none', 'single', 'double', 'cross', 'circle'.".to_owned()
    } else if field.starts_with("edges") {
        "Ensure edge 'from' and 'to' fields reference valid node IDs defined in 'nodes'.".to_owned()
    } else if field.starts_with("groups") {
        "Ensure group 'members' list contains valid node IDs defined in 'nodes'.".to_owned()
    } else if field.starts_with("chart") {
        "Chart requires a valid 'type' ('bar', 'line', 'pie', 'scatter', 'area', 'donut') and at least one series.".to_owned()
    } else {
        format!(
            "Refer to SymDraw IR schema documentation for valid {} values.",
            quote(field)
        )
    }
}
