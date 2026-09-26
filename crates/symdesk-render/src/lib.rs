use serde::{Deserialize, Serialize};
use std::collections::{BTreeMap, HashSet};

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, Default)]
#[serde(default, deny_unknown_fields)]
pub struct Diagram {
    pub version: String,
    pub kind: String,
    pub title: String,
    pub direction: String,
    pub theme: String,
    pub width: f64,
    pub height: f64,
    pub nodes: Vec<Node>,
    pub edges: Vec<Edge>,
    pub groups: Vec<Group>,
    pub chart: Option<ChartSpec>,
    pub custom: Option<BTreeMap<String, serde_json::Value>>,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, Default)]
#[serde(default, deny_unknown_fields)]
pub struct Node {
    pub id: String,
    pub label: String,
    pub shape: String,
    pub note: String,
    pub icon: String,
    pub style: NodeStyle,
    pub width: f64,
    pub height: f64,
    pub x: f64,
    pub y: f64,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, Default)]
#[serde(default, deny_unknown_fields)]
pub struct NodeStyle {
    pub fill: String,
    pub stroke: String,
    pub stroke_width: Option<f64>,
    pub text_color: String,
    pub opacity: Option<f64>,
    pub dash_array: String,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, Default)]
#[serde(default, deny_unknown_fields)]
pub struct Edge {
    pub from: String,
    pub to: String,
    pub label: String,
    pub style: String,
    pub arrow: String,
    pub color: String,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, Default)]
#[serde(default, deny_unknown_fields)]
pub struct Group {
    pub id: String,
    pub label: String,
    pub members: Vec<String>,
    pub style: GroupStyle,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, Default)]
#[serde(default, deny_unknown_fields)]
pub struct GroupStyle {
    pub fill: String,
    pub stroke: String,
    pub stroke_width: Option<f64>,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, Default)]
#[serde(default, deny_unknown_fields)]
pub struct ChartSpec {
    pub r#type: String,
    pub title: String,
    pub series: Vec<Series>,
    pub x_axis: AxisSpec,
    pub y_axis: AxisSpec,
    pub legend: bool,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, Default)]
#[serde(default, deny_unknown_fields)]
pub struct Series {
    pub name: String,
    pub data: Vec<DataPoint>,
    pub color: String,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, Default)]
#[serde(default, deny_unknown_fields)]
pub struct DataPoint {
    pub x: f64,
    pub y: f64,
    pub label: String,
    pub color: String,
}

#[derive(Debug, Clone, PartialEq, Serialize, Deserialize, Default)]
#[serde(default, deny_unknown_fields)]
pub struct AxisSpec {
    pub title: String,
    pub labels: Vec<String>,
    pub min: Option<f64>,
    pub max: Option<f64>,
}

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct ParseError {
    pub stage: &'static str,
    pub field: String,
    pub message: String,
}

impl std::fmt::Display for ParseError {
    fn fmt(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
        if self.field.is_empty() {
            write!(f, "{}: {}", self.stage, self.message)
        } else {
            write!(f, "{} {}: {}", self.stage, self.field, self.message)
        }
    }
}

impl std::error::Error for ParseError {}

pub fn parse_json(input: &[u8]) -> Result<Diagram, ParseError> {
    if input.iter().all(u8::is_ascii_whitespace) {
        return Err(error("parse", "", "empty JSON diagram input"));
    }
    let diagram: Diagram = serde_json::from_slice(input).map_err(|err| {
        let detail = err.to_string();
        if detail.starts_with("unknown field ") {
            error(
                "schema",
                quoted_field(&detail),
                "unknown field in diagram JSON",
            )
        } else {
            error("parse", "", "malformed JSON diagram input")
        }
    })?;
    validate(&diagram)?;
    Ok(diagram)
}

fn quoted_field(message: &str) -> String {
    let Some((start, quote)) = message
        .find('`')
        .map(|i| (i + 1, '`'))
        .or_else(|| message.find('\'').map(|i| (i + 1, '\'')))
    else {
        return String::new();
    };
    message[start..]
        .find(quote)
        .map(|end| message[start..start + end].to_owned())
        .unwrap_or_default()
}

fn error(stage: &'static str, field: impl Into<String>, message: impl Into<String>) -> ParseError {
    ParseError {
        stage,
        field: field.into(),
        message: message.into(),
    }
}

fn contract(field: impl Into<String>, message: impl Into<String>) -> ParseError {
    error("contract", field, message)
}

pub fn validate(d: &Diagram) -> Result<(), ParseError> {
    if !matches!(
        d.kind.as_str(),
        "graph" | "sequence" | "timeline" | "tree" | "chart" | "custom"
    ) {
        return Err(contract(
            "kind",
            if d.kind.is_empty() {
                "diagram kind is required".into()
            } else {
                format!("unsupported kind {:?}", d.kind)
            },
        ));
    }
    if !d.direction.is_empty() && !matches!(d.direction.as_str(), "TD" | "TB" | "BT" | "LR" | "RL")
    {
        return Err(contract(
            "direction",
            format!("unsupported direction {:?}", d.direction),
        ));
    }

    let mut ids = HashSet::with_capacity(d.nodes.len());
    for (i, n) in d.nodes.iter().enumerate() {
        let id = n.id.trim();
        if id.is_empty() {
            return Err(contract(
                format!("nodes[{i}].id"),
                "node id cannot be empty",
            ));
        }
        if !ids.insert(id) {
            return Err(contract(
                format!("nodes[{i}].id"),
                format!("duplicate node id {id:?}"),
            ));
        }
        if !n.shape.is_empty()
            && !matches!(
                n.shape.as_str(),
                "rect"
                    | "round"
                    | "circle"
                    | "cylinder"
                    | "diamond"
                    | "pill"
                    | "stadium"
                    | "subroutine"
                    | "hexagon"
                    | "asymmetric"
            )
        {
            return Err(contract(
                format!("nodes[{i}].shape"),
                format!("unsupported shape {:?}", n.shape),
            ));
        }
        if n.width < 0.0 || n.height < 0.0 {
            return Err(contract(
                format!("nodes[{i}]"),
                "width and height cannot be negative",
            ));
        }
    }

    for (i, e) in d.edges.iter().enumerate() {
        let from = e.from.trim();
        let to = e.to.trim();
        if from.is_empty() {
            return Err(contract(
                format!("edges[{i}].from"),
                "edge from cannot be empty",
            ));
        }
        if to.is_empty() {
            return Err(contract(
                format!("edges[{i}].to"),
                "edge to cannot be empty",
            ));
        }
        if !ids.is_empty() && !ids.contains(from) {
            return Err(contract(
                format!("edges[{i}].from"),
                format!("referenced node {from:?} does not exist"),
            ));
        }
        if !ids.is_empty() && !ids.contains(to) {
            return Err(contract(
                format!("edges[{i}].to"),
                format!("referenced node {to:?} does not exist"),
            ));
        }
        if !e.style.is_empty()
            && !matches!(e.style.as_str(), "solid" | "dashed" | "dotted" | "thick")
        {
            return Err(contract(
                format!("edges[{i}].style"),
                format!("unsupported edge style {:?}", e.style),
            ));
        }
        if !e.arrow.is_empty()
            && !matches!(
                e.arrow.as_str(),
                "none" | "single" | "double" | "cross" | "circle"
            )
        {
            return Err(contract(
                format!("edges[{i}].arrow"),
                format!("unsupported arrow type {:?}", e.arrow),
            ));
        }
    }

    for (i, group) in d.groups.iter().enumerate() {
        if group.members.is_empty() {
            return Err(contract(
                format!("groups[{i}].members"),
                "group must have at least one member",
            ));
        }
        if !ids.is_empty() {
            for member in &group.members {
                if !ids.contains(member.as_str()) {
                    return Err(contract(
                        format!("groups[{i}].members"),
                        format!("grouped node {member:?} does not exist"),
                    ));
                }
            }
        }
    }

    if d.kind == "chart" || d.chart.is_some() {
        let Some(chart) = &d.chart else {
            return Err(contract(
                "chart",
                "chart specification is required for chart diagram kind",
            ));
        };
        if !matches!(
            chart.r#type.as_str(),
            "bar" | "line" | "pie" | "scatter" | "area" | "donut"
        ) {
            return Err(contract(
                "chart.type",
                format!("unsupported chart type {:?}", chart.r#type),
            ));
        }
        if chart.series.is_empty() {
            return Err(contract(
                "chart.series",
                "chart must have at least one series",
            ));
        }
    }
    Ok(())
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn go_oracle_fixture() {
        let fixture: serde_json::Value =
            serde_json::from_str(include_str!("../../../testdata/port/render/json-ir.json"))
                .unwrap();
        for case in fixture["cases"].as_array().unwrap() {
            let input = case["input"].as_str().unwrap();
            let result = parse_json(input.as_bytes());
            if case["accepted"] == true {
                let parsed = result.unwrap_or_else(|err| panic!("{} rejected: {err}", case["id"]));
                let expected: Diagram = serde_json::from_value(case["diagram"].clone()).unwrap();
                assert_eq!(parsed, expected, "{}", case["id"]);
            } else {
                let err = result.unwrap_err();
                assert_eq!(err.stage, case["stage"].as_str().unwrap(), "{}", case["id"]);
                assert_eq!(err.field, case["field"].as_str().unwrap(), "{}", case["id"]);
            }
        }
    }
}
