//! Port of `internal/draw/parse/sequence.go` (sequenceDiagram subset).

use std::collections::HashMap;

use super::ParseError;
use super::gotext::{fields, quote, to_lower, trim_quotes, trim_space};
use super::ir::{Diagram, Value};
use super::mermaid::{DIALECT_VERSION, byte_tail, strip_comment};

struct Actor {
    id: String,
    label: String,
}

struct Message {
    from: String,
    to: String,
    text: String,
    order: i64,
}

pub(super) fn parse_sequence_mermaid(
    header_line_num: usize,
    raw_lines: &[&str],
) -> Result<Diagram, ParseError> {
    let mut diag = Diagram {
        kind: "sequence".to_owned(),
        version: DIALECT_VERSION.to_owned(),
        ..Diagram::default()
    };
    let mut actors: Vec<Actor> = Vec::new();
    let mut actor_index: HashMap<String, usize> = HashMap::new();
    let mut messages: Vec<Message> = Vec::new();
    let mut order = 0i64;

    let declare = |actors: &mut Vec<Actor>, index: &mut HashMap<String, usize>, rest: &str| {
        let mut id = rest.to_owned();
        let mut label = rest.to_owned();
        let parts = fields(rest);
        if parts.len() > 1 {
            id = parts[0].to_owned();
            label = trim_quotes(parts[1]).to_owned();
        }
        if !index.contains_key(&id) {
            index.insert(id.clone(), actors.len());
            actors.push(Actor { id, label });
        }
    };

    for (i, raw) in raw_lines.iter().enumerate().skip(header_line_num) {
        let cleaned = strip_comment(raw);
        let trimmed = trim_space(&cleaned);
        if trimmed.is_empty() {
            continue;
        }
        let lower = to_lower(trimmed);
        if lower.starts_with("participant ") {
            let rest = trim_space(byte_tail(trimmed, "participant ".len()));
            declare(&mut actors, &mut actor_index, rest);
        } else if lower.starts_with("actor ") {
            let rest = trim_space(byte_tail(trimmed, "actor ".len()));
            declare(&mut actors, &mut actor_index, rest);
        } else {
            let Some((from, to, text)) = parse_sequence_message_line(trimmed) else {
                return Err(ParseError {
                    stage: "parse",
                    message: format!("unsupported construct {}", quote(&format!("sequence line {}", quote(trimmed)))),
                    detail: format!("line {}: {}", i + 1, trim_space(raw)),
                    hint: "Supported sequence syntax: participant <id>, actor <id>, and message lines '<from>->><to>: <text>'.".to_owned(),
                    line: i + 1,
                });
            };
            for id in [&from, &to] {
                if !actor_index.contains_key(id) {
                    actor_index.insert(id.clone(), actors.len());
                    actors.push(Actor {
                        id: id.clone(),
                        label: id.clone(),
                    });
                }
            }
            messages.push(Message {
                from,
                to,
                text,
                order,
            });
            order += 1;
        }
    }

    if actors.is_empty() {
        return Err(ParseError {
            stage: "parse",
            message: "sequence diagram has no participants".to_owned(),
            detail: String::new(),
            hint: "Declare at least one participant with 'participant <id>'.".to_owned(),
            line: 0,
        });
    }

    let string = |s: &str| Value::Str(s.to_owned());
    diag.custom.insert(
        "actors".to_owned(),
        Value::Arr(
            actors
                .iter()
                .map(|a| Value::Struct(vec![("ID", string(&a.id)), ("Label", string(&a.label))]))
                .collect(),
        ),
    );
    diag.custom.insert(
        "messages".to_owned(),
        Value::Arr(
            messages
                .iter()
                .map(|m| {
                    Value::Struct(vec![
                        ("From", string(&m.from)),
                        ("To", string(&m.to)),
                        ("Text", string(&m.text)),
                        ("Order", Value::Int(m.order)),
                    ])
                })
                .collect(),
        ),
    );
    Ok(diag)
}

fn parse_sequence_message_line(line: &str) -> Option<(String, String, String)> {
    let (arrow_idx, arrow_len) = match line.find("-->>") {
        Some(idx) => (idx, 4),
        None => (line.find("->>")?, 3),
    };
    let from = trim_space(&line[..arrow_idx]);
    let mut to = trim_space(&line[arrow_idx + arrow_len..]);
    let mut text = "";
    if let Some(colon) = to.find(':') {
        text = trim_space(&to[colon + 1..]);
        to = trim_space(&to[..colon]);
    }
    if from.is_empty() || to.is_empty() {
        return None;
    }
    Some((from.to_owned(), to.to_owned(), text.to_owned()))
}
