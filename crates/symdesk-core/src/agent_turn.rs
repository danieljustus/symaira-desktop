//! One provider round trip of the agentic loop (Go `internal/ai/agent_turn.go`):
//! the message history plus the tool list go to Ollama `/api/chat` or the
//! Anthropic Messages API, answer text streams to the caller, and the turn
//! returns the tool calls the model requested. The HTTP call is cancellable
//! through a [`CancelToken`].

use std::{
    collections::BTreeMap,
    io::{BufRead, BufReader, Read},
    time::Duration,
};

use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;

use crate::{
    cancel::CancelToken,
    gofmt::{compact_json, error_body_v, json_syntax_error, lossy},
};

pub const DEFAULT_ANTHROPIC_URL: &str = "https://api.anthropic.com/v1/messages";
/// Go's `bufio.Scanner` limit for one stream line.
const MAX_LINE: usize = 1 << 20;
const REQUEST_TIMEOUT: Duration = Duration::from_secs(5 * 60);

#[derive(Clone, Copy, Debug, Eq, PartialEq)]
pub enum Kind {
    Ollama,
    Anthropic,
}

#[derive(Clone, Debug)]
pub struct ToolDef {
    pub name: String,
    pub description: String,
    /// Raw JSON Schema text; empty means `{"type":"object"}`.
    pub input_schema: String,
}

#[derive(Clone, Debug, Default, Eq, PartialEq)]
pub struct ToolCall {
    pub id: String,
    pub name: String,
    /// Raw JSON arguments exactly as the provider sent them.
    pub input: String,
}

#[derive(Clone, Debug)]
pub enum Message {
    User(String),
    Assistant {
        text: String,
        tool_calls: Vec<ToolCall>,
    },
    Tool {
        call_id: String,
        output: Vec<u8>,
    },
}

#[derive(Clone, Debug, Default)]
pub struct TurnResult {
    pub text: String,
    pub tool_calls: Vec<ToolCall>,
    pub stop_reason: String,
    pub token_usage: i64,
    pub context_window: i64,
}

/// Everything one turn needs; `url` is the complete endpoint.
pub struct TurnRequest<'a> {
    pub kind: Kind,
    pub url: &'a str,
    pub model: &'a str,
    pub max_tokens: i64,
    pub api_key: &'a str,
    pub messages: &'a [Message],
    pub tools: &'a [ToolDef],
}

fn schema_or_default(schema: &str) -> Box<RawValue> {
    let text = if schema.trim().is_empty() {
        r#"{"type":"object"}"#.to_owned()
    } else {
        compact_json(schema)
    };
    RawValue::from_string(text.clone())
        .or_else(|_| RawValue::from_string(r#"{"type":"object"}"#.to_owned()))
        .expect("static schema is valid JSON")
}

/// Go marshals a `json.RawMessage` after validating it; an invalid one fails the
/// whole request body.
fn raw_message(raw: &str) -> Result<Box<RawValue>, String> {
    RawValue::from_string(compact_json(raw)).map_err(|_| {
        format!(
            "json: error calling MarshalJSON for type json.RawMessage: {}",
            json_syntax_error(raw)
        )
    })
}

#[derive(Serialize)]
struct TextBlock<'a> {
    text: &'a str,
    #[serde(rename = "type")]
    kind: &'static str,
}

#[derive(Serialize)]
struct ToolUseBlock {
    id: String,
    input: Box<RawValue>,
    name: String,
    #[serde(rename = "type")]
    kind: &'static str,
}

#[derive(Serialize)]
struct ToolResultBlock {
    content: String,
    tool_use_id: String,
    #[serde(rename = "type")]
    kind: &'static str,
}

#[derive(Serialize)]
#[serde(untagged)]
enum AnthropicBlock<'a> {
    Text(TextBlock<'a>),
    ToolUse(ToolUseBlock),
    ToolResult(ToolResultBlock),
}

#[derive(Serialize)]
struct AnthropicMessage<'a> {
    content: Vec<AnthropicBlock<'a>>,
    role: &'static str,
}

#[derive(Serialize)]
struct AnthropicTool<'a> {
    description: &'a str,
    input_schema: Box<RawValue>,
    name: &'a str,
}

#[derive(Serialize)]
struct AnthropicPayload<'a> {
    max_tokens: i64,
    messages: Vec<AnthropicMessage<'a>>,
    model: &'a str,
    stream: bool,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    tools: Vec<AnthropicTool<'a>>,
}

fn anthropic_placeholder<'a>() -> AnthropicBlock<'a> {
    AnthropicBlock::Text(TextBlock {
        text: "…",
        kind: "text",
    })
}

fn anthropic_body(request: &TurnRequest<'_>) -> Result<String, String> {
    let mut messages: Vec<AnthropicMessage<'_>> = Vec::new();
    let mut index = 0;
    while index < request.messages.len() {
        match &request.messages[index] {
            Message::User(text) => {
                let content = if text.is_empty() {
                    vec![anthropic_placeholder()]
                } else {
                    vec![AnthropicBlock::Text(TextBlock { text, kind: "text" })]
                };
                messages.push(AnthropicMessage {
                    content,
                    role: "user",
                });
                index += 1;
            }
            Message::Assistant { text, tool_calls } => {
                let mut content = Vec::new();
                if !text.is_empty() {
                    content.push(AnthropicBlock::Text(TextBlock { text, kind: "text" }));
                }
                for call in tool_calls {
                    content.push(AnthropicBlock::ToolUse(ToolUseBlock {
                        id: call.id.clone(),
                        input: raw_message(&call.input)?,
                        name: call.name.clone(),
                        kind: "tool_use",
                    }));
                }
                if content.is_empty() {
                    content.push(anthropic_placeholder());
                }
                messages.push(AnthropicMessage {
                    content,
                    role: "assistant",
                });
                index += 1;
            }
            Message::Tool { .. } => {
                let mut content = Vec::new();
                while let Some(Message::Tool { call_id, output }) = request.messages.get(index) {
                    content.push(AnthropicBlock::ToolResult(ToolResultBlock {
                        content: lossy(output),
                        tool_use_id: call_id.clone(),
                        kind: "tool_result",
                    }));
                    index += 1;
                }
                messages.push(AnthropicMessage {
                    content,
                    role: "user",
                });
            }
        }
    }
    let tools = request
        .tools
        .iter()
        .map(|tool| AnthropicTool {
            description: &tool.description,
            input_schema: schema_or_default(&tool.input_schema),
            name: &tool.name,
        })
        .collect();
    serde_json::to_string(&AnthropicPayload {
        max_tokens: request.max_tokens,
        messages,
        model: request.model,
        stream: true,
        tools,
    })
    .map_err(|error| error.to_string())
}

#[derive(Serialize)]
struct OllamaFunctionCall {
    arguments: Box<RawValue>,
    name: String,
}

#[derive(Serialize)]
struct OllamaToolCall {
    function: OllamaFunctionCall,
    id: String,
    #[serde(rename = "type")]
    kind: &'static str,
}

#[derive(Serialize)]
struct OllamaMessage {
    content: String,
    role: &'static str,
    #[serde(skip_serializing_if = "Option::is_none")]
    tool_call_id: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    tool_calls: Option<Vec<OllamaToolCall>>,
}

#[derive(Serialize)]
struct OllamaFunctionDef<'a> {
    description: &'a str,
    name: &'a str,
    parameters: Box<RawValue>,
}

#[derive(Serialize)]
struct OllamaToolDef<'a> {
    function: OllamaFunctionDef<'a>,
    #[serde(rename = "type")]
    kind: &'static str,
}

#[derive(Serialize)]
struct OllamaPayload<'a> {
    messages: Vec<OllamaMessage>,
    model: &'a str,
    stream: bool,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    tools: Vec<OllamaToolDef<'a>>,
}

fn ollama_body(request: &TurnRequest<'_>) -> Result<String, String> {
    let mut messages = Vec::new();
    for message in request.messages {
        match message {
            Message::User(text) => messages.push(OllamaMessage {
                content: text.clone(),
                role: "user",
                tool_call_id: None,
                tool_calls: None,
            }),
            Message::Assistant { text, tool_calls } => {
                let calls = (!tool_calls.is_empty()).then(|| {
                    tool_calls
                        .iter()
                        .map(|call| {
                            // Ollama requires the arguments as an object; an
                            // empty or invalid input is replaced like Go does.
                            let arguments = if call.input.is_empty()
                                || serde_json::from_str::<&RawValue>(&call.input).is_err()
                            {
                                "{}".to_owned()
                            } else {
                                compact_json(&call.input)
                            };
                            OllamaToolCall {
                                function: OllamaFunctionCall {
                                    arguments: RawValue::from_string(arguments)
                                        .expect("validated or static JSON"),
                                    name: call.name.clone(),
                                },
                                id: call.id.clone(),
                                kind: "function",
                            }
                        })
                        .collect()
                });
                messages.push(OllamaMessage {
                    content: text.clone(),
                    role: "assistant",
                    tool_call_id: None,
                    tool_calls: calls,
                });
            }
            Message::Tool { call_id, output } => messages.push(OllamaMessage {
                content: lossy(output),
                role: "tool",
                tool_call_id: Some(call_id.clone()),
                tool_calls: None,
            }),
        }
    }
    let tools = request
        .tools
        .iter()
        .map(|tool| OllamaToolDef {
            function: OllamaFunctionDef {
                description: &tool.description,
                name: &tool.name,
                parameters: schema_or_default(&tool.input_schema),
            },
            kind: "function",
        })
        .collect();
    serde_json::to_string(&OllamaPayload {
        messages,
        model: request.model,
        stream: true,
        tools,
    })
    .map_err(|error| error.to_string())
}

/// Go `encoding/json` escapes HTML and the Unicode line separators.
fn go_escape(rendered: String) -> String {
    rendered
        .replace('&', "\\u0026")
        .replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029")
}

/// Go `anthropicContextWindow`: conservative, known models only.
#[must_use]
pub fn anthropic_context_window(model: &str) -> i64 {
    if model.contains("sonnet") || model.contains("opus") || model.contains("haiku") {
        200_000
    } else {
        0
    }
}

/// Run one turn. `on_text` receives answer text as it streams. Errors carry the
/// text Go's `%v` of the same failure prints after "Agent request failed: ".
///
/// # Errors
///
/// A request build failure, a transport failure, a non-200 status (with the
/// decoded error body) or an unreadable stream.
pub fn run_turn(
    request: &TurnRequest<'_>,
    cancel: &CancelToken,
    on_text: &mut dyn FnMut(&str),
) -> Result<TurnResult, String> {
    let body = go_escape(match request.kind {
        Kind::Anthropic => anthropic_body(request)?,
        Kind::Ollama => ollama_body(request)?,
    });
    if cancel.is_cancelled() {
        return Err(canceled(request.url));
    }
    let config = ureq::Agent::config_builder()
        .http_status_as_error(false)
        .max_redirects(0)
        .proxy(None)
        .timeout_global(Some(REQUEST_TIMEOUT))
        .build();
    let agent = cancel.agent(config);
    let mut post = agent
        .post(request.url)
        .header("content-type", "application/json");
    if request.kind == Kind::Anthropic {
        post = post
            .header("x-api-key", request.api_key)
            .header("anthropic-version", "2023-06-01");
    }
    let mut response = match post.send(body.as_bytes()) {
        Ok(response) => response,
        Err(error) => {
            return Err(if cancel.is_cancelled() {
                canceled(request.url)
            } else {
                transport_error(request.url, &error.to_string())
            });
        }
    };
    let status = response.status().as_u16();
    if status != 200 {
        let mut raw = Vec::new();
        let _ = response
            .body_mut()
            .as_reader()
            .take(4 * 1024 * 1024)
            .read_to_end(&mut raw);
        let provider = match request.kind {
            Kind::Anthropic => "anthropic",
            Kind::Ollama => "ollama",
        };
        return Err(format!(
            "{provider} api error (HTTP {status}): {}",
            error_body_v(&raw)
        ));
    }
    let reader = BufReader::new(response.body_mut().as_reader());
    let result = match request.kind {
        Kind::Anthropic => read_anthropic(reader, request.model, on_text),
        Kind::Ollama => read_ollama(reader, on_text),
    };
    if cancel.is_cancelled() {
        // A shut-down socket reads as end of stream; that is a cancellation,
        // never a finished turn.
        return Err(canceled(request.url));
    }
    result
}

fn canceled(url: &str) -> String {
    format!("Post {url:?}: context canceled")
}

fn transport_error(url: &str, error: &str) -> String {
    format!("Post {url:?}: {error}")
}

/// Go `bufio.Scanner` lines: split on `\n`, drop a trailing `\r`, fail on a
/// token of 1 MiB or more.
fn next_line<R: BufRead>(reader: &mut R) -> Result<Option<String>, String> {
    let mut line = Vec::new();
    let read = reader
        .by_ref()
        .take(MAX_LINE as u64 + 1)
        .read_until(b'\n', &mut line)
        .map_err(|error| error.to_string())?;
    if read == 0 {
        return Ok(None);
    }
    if line.last() == Some(&b'\n') {
        line.pop();
    } else if line.len() > MAX_LINE {
        return Err("bufio.Scanner: token too long".to_owned());
    }
    if line.len() >= MAX_LINE {
        return Err("bufio.Scanner: token too long".to_owned());
    }
    if line.last() == Some(&b'\r') {
        line.pop();
    }
    Ok(Some(lossy(&line)))
}

#[derive(Deserialize, Default)]
struct OllamaChunk {
    #[serde(default)]
    message: OllamaChunkMessage,
    #[serde(default)]
    done: bool,
    #[serde(default)]
    prompt_eval_count: i64,
    #[serde(default)]
    eval_count: i64,
}

#[derive(Deserialize, Default)]
struct OllamaChunkMessage {
    #[serde(default)]
    content: String,
    #[serde(default)]
    tool_calls: Vec<OllamaChunkCall>,
}

#[derive(Deserialize)]
struct OllamaChunkCall {
    #[serde(default)]
    function: OllamaChunkFunction,
}

#[derive(Deserialize, Default)]
struct OllamaChunkFunction {
    #[serde(default)]
    name: String,
    #[serde(default, deserialize_with = "raw_argument")]
    arguments: Option<Box<RawValue>>,
}

/// A present `null` is the raw value `null` (Go keeps it), not "absent".
fn raw_argument<'de, D>(deserializer: D) -> Result<Option<Box<RawValue>>, D::Error>
where
    D: serde::Deserializer<'de>,
{
    Box::<RawValue>::deserialize(deserializer).map(Some)
}

fn read_ollama<R: BufRead>(
    mut reader: R,
    on_text: &mut dyn FnMut(&str),
) -> Result<TurnResult, String> {
    let mut result = TurnResult::default();
    let (mut prompt_eval, mut eval_count) = (0_i64, 0_i64);
    while let Some(line) = next_line(&mut reader)? {
        if line.trim().is_empty() {
            continue;
        }
        let Ok(chunk) = serde_json::from_str::<OllamaChunk>(&line) else {
            continue;
        };
        if !chunk.message.content.is_empty() {
            result.text.push_str(&chunk.message.content);
            on_text(&chunk.message.content);
        }
        for call in chunk.message.tool_calls {
            let arguments = call
                .function
                .arguments
                .map(|raw| raw.get().to_owned())
                .filter(|raw| !raw.is_empty())
                .unwrap_or_else(|| "{}".to_owned());
            // Ollama does not issue tool_call ids; synthesize stable ones.
            let id = format!("call_{}", result.tool_calls.len());
            result.tool_calls.push(ToolCall {
                id,
                name: call.function.name,
                input: arguments,
            });
        }
        if chunk.prompt_eval_count > 0 {
            prompt_eval = chunk.prompt_eval_count;
        }
        if chunk.eval_count > 0 {
            eval_count = chunk.eval_count;
        }
        if chunk.done {
            break;
        }
    }
    if prompt_eval + eval_count > 0 {
        result.token_usage = prompt_eval + eval_count;
    }
    Ok(result)
}

#[derive(Default)]
struct PartialTool {
    id: String,
    name: String,
    input: String,
}

fn read_anthropic<R: BufRead>(
    mut reader: R,
    model: &str,
    on_text: &mut dyn FnMut(&str),
) -> Result<TurnResult, String> {
    let mut result = TurnResult::default();
    let (mut input_tokens, mut output_tokens) = (0_i64, 0_i64);
    let mut partials: BTreeMap<i64, PartialTool> = BTreeMap::new();
    while let Some(line) = next_line(&mut reader)? {
        let Some(data) = line.strip_prefix("data: ") else {
            continue;
        };
        if data == "[DONE]" {
            break;
        }
        let Ok(event) = serde_json::from_str::<serde_json::Value>(data) else {
            continue;
        };
        let index = event.get("index").and_then(serde_json::Value::as_f64);
        let usage_of = |key: &str| {
            event
                .get("usage")
                .and_then(|usage| usage.get(key))
                .and_then(serde_json::Value::as_f64)
        };
        match event.get("type").and_then(serde_json::Value::as_str) {
            Some("message_start") => {
                if let Some(tokens) = usage_of("input_tokens") {
                    input_tokens = tokens as i64;
                }
            }
            Some("content_block_start") => {
                let Some(index) = index else { continue };
                let block = event.get("content_block");
                if block.and_then(|b| b.get("type")).and_then(|t| t.as_str()) == Some("tool_use") {
                    let field = |key: &str| {
                        block
                            .and_then(|b| b.get(key))
                            .and_then(|v| v.as_str())
                            .unwrap_or_default()
                            .to_owned()
                    };
                    partials.insert(
                        index as i64,
                        PartialTool {
                            id: field("id"),
                            name: field("name"),
                            input: String::new(),
                        },
                    );
                }
            }
            Some("content_block_delta") => {
                let delta = event.get("delta");
                let kind = delta.and_then(|d| d.get("type")).and_then(|t| t.as_str());
                match kind {
                    Some("text_delta") => {
                        if let Some(text) = delta
                            .and_then(|d| d.get("text"))
                            .and_then(|t| t.as_str())
                            .filter(|text| !text.is_empty())
                        {
                            result.text.push_str(text);
                            on_text(text);
                        }
                    }
                    Some("input_json_delta") => {
                        let Some(index) = index else { continue };
                        if let (Some(partial), Some(fragment)) = (
                            partials.get_mut(&(index as i64)),
                            delta
                                .and_then(|d| d.get("partial_json"))
                                .and_then(|f| f.as_str()),
                        ) {
                            partial.input.push_str(fragment);
                        }
                    }
                    _ => {}
                }
            }
            Some("content_block_stop") => {
                let Some(index) = index else { continue };
                let key = index as i64;
                if partials.get(&key).is_some_and(|p| !p.input.is_empty())
                    && let Some(partial) = partials.remove(&key)
                {
                    result.tool_calls.push(ToolCall {
                        id: partial.id,
                        name: partial.name,
                        input: partial.input,
                    });
                }
            }
            Some("message_delta") => {
                if let Some(stop) = event
                    .get("delta")
                    .and_then(|d| d.get("stop_reason"))
                    .and_then(|s| s.as_str())
                {
                    stop.clone_into(&mut result.stop_reason);
                }
                if let Some(tokens) = usage_of("output_tokens") {
                    output_tokens = tokens as i64;
                }
            }
            _ => {}
        }
    }
    if input_tokens + output_tokens > 0 {
        result.token_usage = input_tokens + output_tokens;
    }
    result.context_window = anthropic_context_window(model);
    Ok(result)
}

#[cfg(test)]
mod tests {
    use super::*;

    fn request<'a>(kind: Kind, messages: &'a [Message], tools: &'a [ToolDef]) -> TurnRequest<'a> {
        TurnRequest {
            kind,
            url: "http://127.0.0.1:1/x",
            model: "m",
            max_tokens: 8,
            api_key: "k",
            messages,
            tools,
        }
    }

    #[test]
    fn anthropic_groups_tool_results_and_sorts_keys_like_go() {
        let messages = vec![
            Message::User("Q".into()),
            Message::Assistant {
                text: String::new(),
                tool_calls: vec![ToolCall {
                    id: "t1".into(),
                    name: "desk_ls".into(),
                    input: "{ \"dir\": \"n\" }".into(),
                }],
            },
            Message::Tool {
                call_id: "t1".into(),
                output: b"a<b".to_vec(),
            },
        ];
        let tools = vec![ToolDef {
            name: "desk_ls".into(),
            description: "Lists".into(),
            input_schema: String::new(),
        }];
        let body = go_escape(anthropic_body(&request(Kind::Anthropic, &messages, &tools)).unwrap());
        assert_eq!(
            body,
            r#"{"max_tokens":8,"messages":[{"content":[{"text":"Q","type":"text"}],"role":"user"},{"content":[{"id":"t1","input":{"dir":"n"},"name":"desk_ls","type":"tool_use"}],"role":"assistant"},{"content":[{"content":"a\u003cb","tool_use_id":"t1","type":"tool_result"}],"role":"user"}],"model":"m","stream":true,"tools":[{"description":"Lists","input_schema":{"type":"object"},"name":"desk_ls"}]}"#
        );
    }

    #[test]
    fn anthropic_rejects_an_invalid_recorded_tool_input_like_go() {
        let messages = vec![Message::Assistant {
            text: String::new(),
            tool_calls: vec![ToolCall {
                id: "t".into(),
                name: "n".into(),
                input: "{\"dir\":".into(),
            }],
        }];
        let error = anthropic_body(&request(Kind::Anthropic, &messages, &[])).unwrap_err();
        assert_eq!(
            error,
            "json: error calling MarshalJSON for type json.RawMessage: unexpected end of JSON input"
        );
    }

    #[test]
    fn ollama_replaces_invalid_arguments_and_keeps_tool_ids() {
        let messages = vec![
            Message::Assistant {
                text: "x".into(),
                tool_calls: vec![ToolCall {
                    id: "call_0".into(),
                    name: "n".into(),
                    input: "{\"a\":".into(),
                }],
            },
            Message::Tool {
                call_id: "call_0".into(),
                output: b"ok".to_vec(),
            },
        ];
        let body = ollama_body(&request(Kind::Ollama, &messages, &[])).unwrap();
        assert_eq!(
            body,
            r#"{"messages":[{"content":"x","role":"assistant","tool_calls":[{"function":{"arguments":{},"name":"n"},"id":"call_0","type":"function"}]},{"content":"ok","role":"tool","tool_call_id":"call_0"}],"model":"m","stream":true}"#
        );
    }

    #[test]
    fn ollama_stream_collects_text_calls_and_usage() {
        let stream = concat!(
            "{\"message\":{\"role\":\"assistant\",\"content\":\"Hi \"},\"done\":false}\n",
            "\n",
            "not json\n",
            "{\"message\":{\"tool_calls\":[{\"function\":{\"name\":\"a\",\"arguments\": {\"x\": 1}}},{\"function\":{\"name\":\"b\",\"arguments\":null}},{\"function\":{\"name\":\"c\"}}]},\"done\":false}\n",
            "{\"done\":true,\"prompt_eval_count\":3,\"eval_count\":4}\n",
            "{\"message\":{\"content\":\"ignored\"},\"done\":false}\n"
        );
        let mut seen = String::new();
        let result = read_ollama(stream.as_bytes(), &mut |text| seen.push_str(text)).unwrap();
        assert_eq!(seen, "Hi ");
        assert_eq!(result.token_usage, 7);
        let calls: Vec<(&str, &str, &str)> = result
            .tool_calls
            .iter()
            .map(|c| (c.id.as_str(), c.name.as_str(), c.input.as_str()))
            .collect();
        assert_eq!(
            calls,
            [
                ("call_0", "a", "{\"x\": 1}"),
                ("call_1", "b", "null"),
                ("call_2", "c", "{}")
            ]
        );
    }

    #[test]
    fn anthropic_stream_assembles_fragments_and_drops_empty_tool_inputs() {
        let stream = concat!(
            "data: {\"type\":\"message_start\",\"usage\":{\"input_tokens\":5}}\n",
            "data: {\"type\":\"content_block_start\",\"index\":0,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t1\",\"name\":\"desk_ls\"}}\n",
            "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"{\\\"a\\\":\"}}\n",
            "data: {\"type\":\"content_block_delta\",\"index\":0,\"delta\":{\"type\":\"input_json_delta\",\"partial_json\":\"1}\"}}\n",
            "data: {\"type\":\"content_block_stop\",\"index\":0}\n",
            "data: {\"type\":\"content_block_start\",\"index\":1,\"content_block\":{\"type\":\"tool_use\",\"id\":\"t2\",\"name\":\"x\"}}\n",
            "data: {\"type\":\"content_block_stop\",\"index\":1}\n",
            "data:{\"type\":\"content_block_delta\"}\n",
            "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"tool_use\"},\"usage\":{\"output_tokens\":7}}\n",
            "data: [DONE]\n",
            "data: {\"type\":\"message_delta\",\"delta\":{\"stop_reason\":\"late\"}}\n"
        );
        let result = read_anthropic(stream.as_bytes(), "claude-sonnet-5", &mut |_| {}).unwrap();
        assert_eq!(result.tool_calls.len(), 1);
        assert_eq!(result.tool_calls[0].input, "{\"a\":1}");
        assert_eq!(result.stop_reason, "tool_use");
        assert_eq!((result.token_usage, result.context_window), (12, 200_000));
    }
}
