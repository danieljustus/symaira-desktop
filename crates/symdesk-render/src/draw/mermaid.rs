//! Port of `internal/draw/parse/mermaid.go` (flowchart/graph subset) and
//! `sequence.go` (sequenceDiagram subset).

use std::collections::HashMap;

use super::ParseError;
use super::gotext::{
    equal_fold_ascii, fields, find_ci_alternation, quote, to_lower, trim_quotes, trim_space,
};
use super::ir::{Diagram, Edge, Group, Node, validate};

pub const DIALECT_VERSION: &str = "1.0";

const EMPTY_SOURCE_HINT: &str = "Provide a valid Mermaid diagram starting with 'graph <direction>' or 'flowchart <direction>' (e.g. 'graph TD').";

const UNSUPPORTED_DIAGRAMS: &[(&str, Option<&str>)] = &[
    ("pie", None),
    ("classdiagram", Some("-v2")),
    ("statediagram", Some("-v2")),
    ("erdiagram", None),
    ("gantt", None),
    ("gitgraph", None),
    ("journey", None),
    ("mindmap", None),
    ("quadrantchart", None),
    ("xychart", Some("-beta")),
    ("requirementdiagram", None),
    ("c4context", None),
    ("c4container", None),
    ("c4component", None),
    ("c4dynamic", None),
    ("c4deployment", None),
    ("architecture", Some("-beta")),
    ("packet", Some("-beta")),
    ("kanban", None),
    ("block", Some("-beta")),
    ("sankey", Some("-beta")),
];

const UNSUPPORTED_DIRECTIVES: &[(&str, Option<&str>)] = &[
    ("click", None),
    ("style", None),
    ("classdef", None),
    ("class", None),
    ("linkstyle", None),
    ("acctitle", None),
    ("accdescr", None),
    ("direction", None),
    ("interpolate", None),
];

const NODE_SHAPES: &[(&str, &str, &str)] = &[
    ("((", "))", "circle"),
    ("([", "])", "stadium"),
    ("[[", "]]", "subroutine"),
    ("[(", ")]", "cylinder"),
    ("{{", "}}", "hexagon"),
    (">", "]", "asymmetric"),
    ("[", "]", "rect"),
    ("(", ")", "round"),
    ("{", "}", "diamond"),
];

// --- error constructors (parse/errors.go) ---

fn detail_for(line: usize, raw: &str) -> String {
    if line > 0 && !raw.is_empty() {
        format!("line {line}: {}", trim_space(raw))
    } else if line > 0 {
        format!("line {line}")
    } else {
        String::new()
    }
}

fn parse_error(message: String, detail: String, hint: &str, line: usize) -> ParseError {
    ParseError {
        stage: "parse",
        message,
        detail,
        hint: hint.to_owned(),
        line,
    }
}

fn unsupported_construct(construct: &str, hint: &str, line: usize, raw: &str) -> ParseError {
    parse_error(
        format!("unsupported construct {}", quote(construct)),
        detail_for(line, raw),
        hint,
        line,
    )
}

fn unsupported_diagram(kind: &str, hint: &str, line: usize, raw: &str) -> ParseError {
    parse_error(
        format!("unsupported diagram kind {}", quote(kind)),
        detail_for(line, raw),
        hint,
        line,
    )
}

fn syntax_error(message: impl Into<String>, line: usize, raw: &str, hint: &str) -> ParseError {
    parse_error(message.into(), detail_for(line, raw), hint, line)
}

fn empty_source() -> ParseError {
    ParseError {
        stage: "parse",
        message: "empty diagram source".to_owned(),
        detail: String::new(),
        hint: EMPTY_SOURCE_HINT.to_owned(),
        line: 0,
    }
}

/// Go `ParseMermaid`.
pub fn parse_mermaid(source: &str) -> Result<Diagram, ParseError> {
    let lines: Vec<&str> = source.split('\n').collect();
    let mut diag = Diagram {
        kind: "graph".to_owned(),
        version: DIALECT_VERSION.to_owned(),
        ..Diagram::default()
    };

    let mut line_idx = 0;
    if trim_space(lines[0]) == "---" {
        line_idx = 1;
        while line_idx < lines.len() {
            let trimmed = trim_space(lines[line_idx]);
            if trimmed == "---" {
                line_idx += 1;
                break;
            }
            if let Some(rest) = trimmed.strip_prefix("title:") {
                diag.title = trim_quotes(trim_space(rest)).to_owned();
            } else if let Some(rest) = trimmed.strip_prefix("theme:") {
                diag.theme = trim_quotes(trim_space(rest)).to_owned();
            }
            line_idx += 1;
        }
    }

    let mut header_line = String::new();
    let mut header_line_num = 0;
    while line_idx < lines.len() {
        let raw = lines[line_idx];
        let line_num = line_idx + 1;
        line_idx += 1;
        let cleaned = strip_comment(raw);
        let trimmed = trim_space(&cleaned);
        if trimmed.is_empty() {
            continue;
        }
        header_line = trimmed.to_owned();
        header_line_num = line_num;
        break;
    }
    if header_line.is_empty() {
        return Err(empty_source());
    }
    let header_owned = header_line;

    if let Some(matched) = find_ci_alternation(&header_owned, UNSUPPORTED_DIAGRAMS) {
        let hint = unsupported_diagram_hint(matched);
        return Err(unsupported_diagram(
            matched,
            &hint,
            header_line_num,
            lines[header_line_num - 1],
        ));
    }

    let header_parts = fields(&header_owned);
    let first_word = to_lower(header_parts[0]);
    if first_word == "sequencediagram" || first_word == "sequence" {
        return super::sequence::parse_sequence_mermaid(header_line_num, &lines);
    }
    if first_word != "graph" && first_word != "flowchart" {
        return Err(syntax_error(
            format!(
                "unknown or unsupported diagram header {}",
                quote(header_parts[0])
            ),
            header_line_num,
            lines[header_line_num - 1],
            "Diagram source must start with 'graph <direction>' or 'flowchart <direction>' (e.g. 'graph TD' or 'flowchart LR').",
        ));
    }

    let mut direction = "TD";
    if header_parts.len() > 1 {
        direction = match header_parts[1].to_ascii_uppercase().as_str() {
            "TD" | "TB" => "TD",
            "BT" => "BT",
            "LR" => "LR",
            "RL" => "RL",
            _ => {
                return Err(unsupported_construct(
                    &format!("direction {}", quote(header_parts[1])),
                    "Supported diagram flow directions are TD, TB, BT, LR, RL.",
                    header_line_num,
                    lines[header_line_num - 1],
                ));
            }
        };
    }
    diag.direction = direction.to_owned();

    let mut builder = Builder::default();
    while line_idx < lines.len() {
        let raw = lines[line_idx];
        let line_num = line_idx + 1;
        line_idx += 1;
        let cleaned = strip_comment(raw);
        let trimmed = trim_space(&cleaned);
        if trimmed.is_empty() {
            continue;
        }
        let stmts = split_statements(trimmed, line_num, raw)?;
        for stmt in stmts {
            let stmt = trim_space(&stmt);
            if stmt.is_empty() {
                continue;
            }
            let lower = to_lower(stmt);
            if lower.starts_with("title ") || lower.starts_with("title:") {
                if diag.title.is_empty() {
                    diag.title = trim_quotes(trim_space(byte_tail(stmt, 6))).to_owned();
                }
                continue;
            }
            if is_subgraph_start(&lower) {
                let group = parse_subgraph_header(stmt, builder.groups.len() + 1);
                let idx = builder.groups.len();
                builder.groups.push(group);
                builder.subgraph_stack.push(idx);
                builder.subgraph_line_nums.insert(idx, line_num);
                continue;
            }
            if equal_fold_ascii(stmt, "end") {
                if builder.subgraph_stack.is_empty() {
                    return Err(syntax_error(
                        "unexpected 'end' outside subgraph",
                        line_num,
                        raw,
                        "Remove the extraneous 'end' directive.",
                    ));
                }
                builder.subgraph_stack.pop();
                continue;
            }
            if let Some(matched) = find_ci_alternation(stmt, UNSUPPORTED_DIRECTIVES) {
                let hint = unsupported_directive_hint(matched);
                return Err(unsupported_construct(matched, &hint, line_num, raw));
            }
            if lower.contains("%%{init:") {
                return Err(unsupported_construct(
                    "directive %%{init:...}%%",
                    "Mermaid init directives are not supported; configure themes via note frontmatter ('theme: ...').",
                    line_num,
                    raw,
                ));
            }
            if stmt.contains("~~~") {
                return Err(unsupported_construct(
                    "invisible link '~~~'",
                    "Invisible links ('~~~') are not supported in SymDraw layout engine.",
                    line_num,
                    raw,
                ));
            }
            let parsed = parse_statement(stmt, line_num, raw, &mut builder)?;
            builder.edges.extend(parsed);
        }
    }

    if let Some(&idx) = builder.subgraph_stack.last() {
        let start_line = builder.subgraph_line_nums[&idx];
        return Err(ParseError {
            stage: "parse",
            message: format!("unclosed subgraph {}", quote(&builder.groups[idx].id)),
            detail: format!("subgraph started at line {start_line} is missing 'end'"),
            hint: "Close the subgraph with 'end'.".to_owned(),
            line: start_line,
        });
    }

    diag.nodes = builder.nodes;
    diag.edges = builder.edges;
    diag.groups = builder
        .groups
        .into_iter()
        .filter(|group| {
            group
                .members
                .as_ref()
                .is_some_and(|members| !members.is_empty())
        })
        .collect();

    if let Err(err) = validate(&diag) {
        return Err(ParseError {
            stage: "contract",
            message: err.message,
            detail: err.field,
            hint: "Check diagram structure against SymDraw IR rules.".to_owned(),
            line: 0,
        });
    }
    Ok(diag)
}

/// `s[offset:]` with Go byte-slice semantics at a rune boundary.
pub(super) fn byte_tail(s: &str, offset: usize) -> &str {
    s.get(offset..).unwrap_or("")
}

#[derive(Default)]
struct Builder {
    nodes: Vec<Node>,
    node_map: HashMap<String, usize>,
    edges: Vec<Edge>,
    groups: Vec<Group>,
    subgraph_stack: Vec<usize>,
    subgraph_line_nums: HashMap<usize, usize>,
}

impl Builder {
    fn register_node(&mut self, n: &Node) {
        match self.node_map.get(&n.id) {
            None => {
                self.node_map.insert(n.id.clone(), self.nodes.len());
                self.nodes.push(n.clone());
            }
            Some(&idx) => {
                let existing = &mut self.nodes[idx];
                if !n.label.is_empty() && (existing.label.is_empty() || existing.label == n.id) {
                    existing.label.clone_from(&n.label);
                }
                if !n.shape.is_empty() && existing.shape == "rect" {
                    existing.shape.clone_from(&n.shape);
                }
                if !n.note.is_empty() && existing.note.is_empty() {
                    existing.note.clone_from(&n.note);
                }
            }
        }
        for &g in &self.subgraph_stack {
            let members = self.groups[g].members.get_or_insert_with(Vec::new);
            if !members.contains(&n.id) {
                members.push(n.id.clone());
            }
        }
    }
}

/// Go `stripComment`: a `%%` outside quotes starts a comment unless followed
/// by `{`.
pub(super) fn strip_comment(line: &str) -> String {
    let runes: Vec<char> = line.chars().collect();
    let mut out = String::new();
    let mut in_quote = false;
    let mut quote_char = '\0';
    let mut i = 0;
    while i < runes.len() {
        let r = runes[i];
        if in_quote {
            out.push(r);
            if r == '\\' && i + 1 < runes.len() {
                i += 1;
                out.push(runes[i]);
            } else if r == quote_char {
                in_quote = false;
            }
            i += 1;
            continue;
        }
        if r == '"' || r == '\'' {
            in_quote = true;
            quote_char = r;
            out.push(r);
            i += 1;
            continue;
        }
        if r == '%'
            && i + 1 < runes.len()
            && runes[i + 1] == '%'
            && (i + 2 >= runes.len() || runes[i + 2] != '{')
        {
            break;
        }
        out.push(r);
        i += 1;
    }
    out
}

fn split_statements(line: &str, line_num: usize, raw: &str) -> Result<Vec<String>, ParseError> {
    let runes: Vec<char> = line.chars().collect();
    let mut stmts = Vec::new();
    let mut cur = String::new();
    let mut in_quote = false;
    let mut quote_char = '\0';
    let mut i = 0;
    while i < runes.len() {
        let r = runes[i];
        if in_quote {
            cur.push(r);
            if r == '\\' && i + 1 < runes.len() {
                i += 1;
                cur.push(runes[i]);
            } else if r == quote_char {
                in_quote = false;
            }
            i += 1;
            continue;
        }
        if r == '"' || r == '\'' {
            in_quote = true;
            quote_char = r;
            cur.push(r);
            i += 1;
            continue;
        }
        if r == ';' {
            stmts.push(std::mem::take(&mut cur));
            i += 1;
            continue;
        }
        cur.push(r);
        i += 1;
    }
    if in_quote {
        return Err(syntax_error(
            "unclosed quote string literal",
            line_num,
            raw,
            &format!("Close string literal with matching quote ({quote_char})."),
        ));
    }
    stmts.push(cur);
    Ok(stmts)
}

fn is_subgraph_start(lower: &str) -> bool {
    lower.starts_with("subgraph ")
        || lower.starts_with("subgraph[")
        || lower.starts_with("subgraph(")
        || lower.starts_with("subgraph\"")
}

fn parse_subgraph_header(stmt: &str, auto_idx: usize) -> Group {
    let trimmed = trim_space(stmt);
    let rest = trim_space(byte_tail(trimmed, "subgraph".len()));
    let new_group = |id: String, label: String| Group {
        id,
        label,
        members: Some(Vec::new()),
        ..Group::default()
    };
    if rest.is_empty() {
        let id = format!("subgraph_{auto_idx}");
        return new_group(id.clone(), id);
    }
    for (open, close) in [('[', ']'), ('(', ')')] {
        if let Some(open_idx) = rest.find(open)
            && rest.ends_with(close)
        {
            let mut id = trim_space(&rest[..open_idx]).to_owned();
            let inner = rest.get(open_idx + 1..rest.len() - 1).unwrap_or("");
            let label = trim_quotes(trim_space(inner)).to_owned();
            if id.is_empty() {
                id = slugify(&label);
                if id.is_empty() {
                    id = format!("subgraph_{auto_idx}");
                }
            }
            return new_group(id, label);
        }
    }
    let title = trim_quotes(rest).to_owned();
    let mut id = slugify(&title);
    if id.is_empty() {
        id = format!("subgraph_{auto_idx}");
    }
    new_group(id, title)
}

fn slugify(s: &str) -> String {
    let mut out = String::new();
    for r in s.chars() {
        if r.is_ascii_alphanumeric() || r == '_' || r == '-' {
            out.push(r);
        } else if r == ' ' || r == '\t' {
            out.push('_');
        }
    }
    out
}

struct Connector {
    style: &'static str,
    arrow: &'static str,
    label: String,
}

fn parse_statement(
    stmt: &str,
    line_num: usize,
    raw: &str,
    builder: &mut Builder,
) -> Result<Vec<Edge>, ParseError> {
    let (nodes, rest) = scan_node_group(stmt, line_num, raw)?;
    for n in &nodes {
        builder.register_node(n);
    }
    let mut from_nodes = nodes;
    let mut curr = trim_space(&rest).to_owned();
    let mut edges = Vec::new();
    if curr.is_empty() {
        return Ok(edges);
    }
    while !curr.is_empty() {
        let Some((conn, rest_conn)) = scan_connector(&curr, line_num, raw)? else {
            return Err(syntax_error(
                format!("unexpected token {}", quote(&curr)),
                line_num,
                raw,
                "Expected an arrow connector (e.g. '-->', '-.->', '==>') or statement delimiter.",
            ));
        };
        curr = trim_space(&rest_conn).to_owned();
        let missing = |_: ()| {
            syntax_error(
                "missing target node in edge definition",
                line_num,
                raw,
                "Specify a destination node after the arrow (e.g. 'A --> B').",
            )
        };
        if curr.is_empty() {
            return Err(missing(()));
        }
        let (to_nodes, rest_to) = scan_node_group(&curr, line_num, raw)?;
        if to_nodes.is_empty() {
            return Err(missing(()));
        }
        for n in &to_nodes {
            builder.register_node(n);
        }
        for from in &from_nodes {
            for to in &to_nodes {
                edges.push(Edge {
                    from: from.id.clone(),
                    to: to.id.clone(),
                    label: conn.label.clone(),
                    style: conn.style.to_owned(),
                    arrow: conn.arrow.to_owned(),
                    ..Edge::default()
                });
            }
        }
        from_nodes = to_nodes;
        curr = trim_space(&rest_to).to_owned();
    }
    Ok(edges)
}

fn scan_node_group(s: &str, line_num: usize, raw: &str) -> Result<(Vec<Node>, String), ParseError> {
    let mut curr = trim_space(s).to_owned();
    if curr.is_empty() {
        return Ok((Vec::new(), String::new()));
    }
    let mut nodes = Vec::new();
    loop {
        let (node, rest) = scan_node_spec(&curr, line_num, raw)?;
        nodes.push(node);
        curr = trim_space(&rest).to_owned();
        if let Some(after) = curr.strip_prefix('&') {
            curr = trim_space(after).to_owned();
            if curr.is_empty() {
                return Err(syntax_error(
                    "missing node following '&' operator",
                    line_num,
                    raw,
                    "Specify a valid node identifier following '&' (e.g. 'A & B --> C').",
                ));
            }
            continue;
        }
        break;
    }
    Ok((nodes, curr))
}

fn scan_node_spec(s: &str, line_num: usize, raw: &str) -> Result<(Node, String), ParseError> {
    let curr = trim_space(s);
    if curr.is_empty() {
        return Err(syntax_error(
            "expected node identifier",
            line_num,
            raw,
            "Specify a node identifier (e.g. 'A').",
        ));
    }
    let runes: Vec<char> = curr.chars().collect();
    let mut end_id = 0;
    while end_id < runes.len() {
        let r = runes[end_id];
        if matches!(r, '[' | '(' | '{' | '>' | '&' | ';' | ' ' | '\t') {
            break;
        }
        let tail: String = runes[end_id..].iter().take(5).collect();
        if is_arrow_connector_start(&tail) {
            break;
        }
        end_id += 1;
    }
    let id: String = runes[..end_id].iter().collect();
    if id.is_empty() {
        return Err(syntax_error(
            format!("invalid node identifier near {}", quote(curr)),
            line_num,
            raw,
            "Node identifier must be an alphanumeric name (e.g. 'A', 'node_1').",
        ));
    }
    let rest_owned: String = runes[end_id..].iter().collect();
    let rest = trim_space(&rest_owned);
    if rest.is_empty() {
        return Ok((
            Node {
                id: id.clone(),
                label: id,
                shape: "rect".to_owned(),
                ..Node::default()
            },
            String::new(),
        ));
    }

    if rest.starts_with("(((") {
        return Err(unsupported_construct(
            "shape '(((...)))' (double circle)",
            "Double circle shape is not supported. Use circle ((...)) or stadium ([...]).",
            line_num,
            raw,
        ));
    }
    if rest.starts_with("[/") {
        return Err(unsupported_construct(
            "shape '[/.../]' or '[/...\\\\]'",
            "Parallelogram and trapezoid shapes are not supported. Use standard shapes like [rect], (round), {diamond}, or {{hexagon}}.",
            line_num,
            raw,
        ));
    }
    if rest.starts_with("[\\") {
        return Err(unsupported_construct(
            "shape '[\\...\\\\]' or '[\\.../]'",
            "Parallelogram and trapezoid shapes are not supported. Use standard shapes like [rect], (round), {diamond}, or {{hexagon}}.",
            line_num,
            raw,
        ));
    }

    for (open, close, shape) in NODE_SHAPES {
        if !rest.starts_with(open) {
            continue;
        }
        let (inner, after) =
            extract_delimited_content(&rest[open.len()..], close, line_num, raw, open)?;
        let mut label = trim_space(&inner).to_owned();
        let mut note = String::new();

        let quoted = (label.starts_with('"') && label.ends_with('"'))
            || (label.starts_with('\'') && label.ends_with('\''));
        if quoted && label.len() >= 2 {
            label = unescape_string(&label[1..label.len() - 1]);
        }

        if label.starts_with("[[") && label.ends_with("]]") && label.len() >= 4 {
            let inner_link = &label[2..label.len() - 2];
            let (target, display) = match inner_link.split_once('|') {
                Some((target, display)) => (target, Some(display)),
                None => (inner_link, None),
            };
            note = trim_space(target).to_owned();
            label = match display {
                Some(display) => trim_space(display).to_owned(),
                None => note.clone(),
            };
        } else if *shape == "subroutine" {
            note.clone_from(&label);
        }
        if label.is_empty() {
            label.clone_from(&id);
        }
        return Ok((
            Node {
                id,
                label,
                shape: (*shape).to_owned(),
                note,
                ..Node::default()
            },
            after,
        ));
    }

    Ok((
        Node {
            id: id.clone(),
            label: id,
            shape: "rect".to_owned(),
            ..Node::default()
        },
        rest.to_owned(),
    ))
}

fn extract_delimited_content(
    s: &str,
    close: &str,
    line_num: usize,
    raw: &str,
    open: &str,
) -> Result<(String, String), ParseError> {
    let runes: Vec<char> = s.chars().collect();
    let close_runes: Vec<char> = close.chars().collect();
    let mut in_quote = false;
    let mut quote_char = '\0';
    let mut i = 0usize;
    while i + close_runes.len() <= runes.len() {
        let r = runes[i];
        if in_quote {
            if r == '\\' && i + 1 < runes.len() {
                i += 1;
            } else if r == quote_char {
                in_quote = false;
            }
            i += 1;
            continue;
        }
        if r == '"' || r == '\'' {
            in_quote = true;
            quote_char = r;
            i += 1;
            continue;
        }
        if runes[i..i + close_runes.len()] == close_runes[..] {
            let inner: String = runes[..i].iter().collect();
            let rest: String = runes[i + close_runes.len()..].iter().collect();
            return Ok((inner, rest));
        }
        i += 1;
    }
    Err(syntax_error(
        format!("unclosed shape delimiter {}", quote(open)),
        line_num,
        raw,
        &format!("Close delimiter with matching {}.", quote(close)),
    ))
}

fn unescape_string(s: &str) -> String {
    let runes: Vec<char> = s.chars().collect();
    let mut out = String::new();
    let mut i = 0;
    while i < runes.len() {
        if runes[i] == '\\' && i + 1 < runes.len() {
            i += 1;
            match runes[i] {
                'n' => out.push('\n'),
                't' => out.push('\t'),
                other => out.push(other),
            }
            i += 1;
            continue;
        }
        out.push(runes[i]);
        i += 1;
    }
    out
}

const ARROW_STARTS: &[&str] = &[
    "<-->", "-->", "---", "--o", "--x", "o--o", "x--x", "o--", "x--", "-.->", "-..->", "-.-",
    "-..-", "==>", "===", "..->", "..>", "...", "..", "--", "-.", "==",
];

fn is_arrow_connector_start(s: &str) -> bool {
    ARROW_STARTS.iter().any(|p| s.starts_with(p))
}

const LABEL_END_DEFS: &[(&str, &str, &str)] = &[
    ("-->", "solid", "single"),
    ("---", "solid", "none"),
    ("<-->", "solid", "double"),
    ("--o", "solid", "circle"),
    ("--x", "solid", "cross"),
    (".->", "dashed", "single"),
    ("-.-", "dashed", "none"),
    ("==>", "thick", "single"),
    ("===", "thick", "none"),
];

const ARROW_DEFS: &[(&str, &str, &str)] = &[
    ("<-->", "solid", "double"),
    ("o--o", "solid", "circle"),
    ("x--x", "solid", "cross"),
    ("-..->", "dashed", "single"),
    ("-.->", "dashed", "single"),
    ("-..-", "dashed", "none"),
    ("-.-", "dashed", "none"),
    ("-->", "solid", "single"),
    ("---", "solid", "none"),
    ("--o", "solid", "circle"),
    ("--x", "solid", "cross"),
    ("o--", "solid", "none"),
    ("x--", "solid", "none"),
    ("==>", "thick", "single"),
    ("===", "thick", "none"),
    ("..->", "dotted", "single"),
    ("..>", "dotted", "single"),
    ("...", "dotted", "none"),
    ("..", "dotted", "none"),
];

fn scan_connector(
    s: &str,
    line_num: usize,
    raw: &str,
) -> Result<Option<(Connector, String)>, ParseError> {
    let curr = trim_space(s);
    if curr.is_empty() {
        return Ok(None);
    }

    if curr.starts_with("-- ") || curr.starts_with("-. ") || curr.starts_with("== ") {
        let prefix = &curr[..2];
        let rest = &curr[3..];
        for &(arrow_str, style, arrow) in LABEL_END_DEFS {
            if let Some(idx) = rest.find(&format!(" {arrow_str}")) {
                let label = trim_quotes(trim_space(&rest[..idx])).to_owned();
                let rest_after = rest[idx + 1 + arrow_str.len()..].to_owned();
                let style = match prefix {
                    "-." => "dashed",
                    "==" => "thick",
                    _ => style,
                };
                return Ok(Some((
                    Connector {
                        style,
                        arrow,
                        label,
                    },
                    rest_after,
                )));
            }
        }
    }

    for &(token, style, arrow) in ARROW_DEFS {
        if !curr.starts_with(token) {
            continue;
        }
        let mut rest = curr[token.len()..].to_owned();
        let mut label = String::new();
        let trimmed_rest = trim_space(&rest).to_owned();
        if let Some(after_pipe) = trimmed_rest.strip_prefix('|') {
            let Some(close_idx) = after_pipe.find('|') else {
                return Err(syntax_error(
                    "unclosed edge pipe label '|...|'",
                    line_num,
                    raw,
                    "Close the edge label with a matching '|' (e.g. '-->|label|').",
                ));
            };
            label = trim_quotes(trim_space(&after_pipe[..close_idx])).to_owned();
            rest = after_pipe[close_idx + 1..].to_owned();
        }
        return Ok(Some((
            Connector {
                style,
                arrow,
                label,
            },
            rest,
        )));
    }
    Ok(None)
}

fn unsupported_diagram_hint(kind: &str) -> String {
    let lower = to_lower(kind);
    if lower.contains("sequence") {
        "SymDraw Mermaid dialect supports 'graph' and 'flowchart'. For sequence diagrams, use SymDraw IR JSON with kind 'sequence'.".to_owned()
    } else if lower.contains("pie") {
        "SymDraw Mermaid dialect supports 'graph' and 'flowchart'. For pie charts, use SymDraw IR JSON with kind 'chart' (type 'pie').".to_owned()
    } else if lower.contains("class") {
        "Class diagrams are not supported in Mermaid dialect. Use flowchart/graph diagrams or SymDraw IR.".to_owned()
    } else if lower.contains("state") {
        "State diagrams are not supported in Mermaid dialect. Use flowchart/graph diagrams or SymDraw IR.".to_owned()
    } else if lower.contains("er") {
        "Entity-Relationship diagrams are not supported in Mermaid dialect. Use flowchart/graph diagrams or SymDraw IR.".to_owned()
    } else if lower.contains("gantt") {
        "Gantt diagrams are not supported in Mermaid dialect. Use flowchart/graph diagrams or SymDraw IR with kind 'timeline'.".to_owned()
    } else if lower.contains("mindmap") {
        "Mindmap diagrams are not supported in Mermaid dialect. Use SymDraw IR with kind 'tree'."
            .to_owned()
    } else if lower.contains("chart") {
        "Charts are not supported in Mermaid dialect. Use SymDraw IR with kind 'chart'.".to_owned()
    } else {
        format!(
            "Diagram kind {} is not supported in the SymDraw Mermaid subset. Use flowchart/graph diagrams or SymDraw IR JSON.",
            quote(kind)
        )
    }
}

fn unsupported_directive_hint(directive: &str) -> String {
    let lower = to_lower(directive);
    if lower.starts_with("click") {
        "Interactive click bindings ('click') are not supported in static vector export; use vault note wikilinks or frontmatter links instead.".to_owned()
    } else if lower.starts_with("style") {
        "Inline 'style' directives are not supported. Use note frontmatter themes or SymDraw IR node styles.".to_owned()
    } else if lower.starts_with("classdef") {
        "CSS class definitions ('classDef') are not supported. Use note frontmatter themes or SymDraw IR.".to_owned()
    } else if lower.starts_with("class") {
        "CSS class assignments ('class') are not supported. Use note frontmatter themes or SymDraw IR.".to_owned()
    } else if lower.starts_with("linkstyle") {
        "Link styling directives ('linkStyle') are not supported. Use edge arrow syntax (--> , -.-> , ==>) to control stroke styles.".to_owned()
    } else if lower.starts_with("acctitle") {
        "Accessibility title directives ('accTitle:') are not supported in diagram source. Use note frontmatter.".to_owned()
    } else if lower.starts_with("accdescr") {
        "Accessibility description directives ('accDescr:') are not supported in diagram source. Use note frontmatter.".to_owned()
    } else if lower.starts_with("direction") {
        "Per-subgraph direction directives are not supported in SymDraw layered layout; diagram direction applies globally.".to_owned()
    } else if lower.starts_with("interpolate") {
        "Interpolation directives ('interpolate') are not supported.".to_owned()
    } else {
        format!(
            "Directive {} is not supported in the SymDraw Mermaid dialect.",
            quote(directive)
        )
    }
}
