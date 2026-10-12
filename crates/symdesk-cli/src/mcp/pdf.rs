//! Canonical Go PDF tool handlers; filesystem/process policy stays in pdfops.
use std::path::Path;

use serde_json::{Value, json, value::RawValue};
use symdesk_render::pdfops::{self, Context, Tools};

use super::{go_raw_fields, go_json_type as value_type};

pub(super) fn canonical_name(name: &str) -> Option<&'static str> {
    match name {
        "desk_split_pdf" | "split_pdf" => Some("desk_split_pdf"),
        "desk_merge_pdf" | "merge_pdf" => Some("desk_merge_pdf"),
        "desk_rotate_pdf" | "rotate_pdf" => Some("desk_rotate_pdf"),
        _ => None,
    }
}

pub(super) fn tool_definitions() -> Vec<Value> {
    let mut tools = vec![
        json!({
            "name": "desk_split_pdf",
            "description": "Splits a PDF into new part files after the given pages. at is a comma-separated page spec such as \"2,4\" or \"2-3,6\". Requires pdfinfo and qpdf on PATH. Existing outputs and input aliases are refused. Legacy alias: split_pdf.",
            "inputSchema": {"type":"object","properties":{"input":{"type":"string","description":"input PDF path"},"at":{"type":"string","description":"split after these pages, e.g. 2,4 or 2-3,6"},"output_dir":{"type":"string","description":"directory for the generated part PDFs"}},"required":["input","at","output_dir"]},
            // CoreKit's non-nil annotations omit all false hints, including
            // readOnlyHint and destructiveHint. These writers create new files.
            "annotations": {},
        }),
        json!({
            "name": "desk_merge_pdf",
            "description": "Merges two or more PDFs into a new output file without modifying the inputs. inputs is an array of PDF paths; output must not exist or alias an input. Requires qpdf on PATH. Legacy alias: merge_pdf.",
            "inputSchema": {"type":"object","properties":{"inputs":{"type":"array","items":{"type":"string"},"description":"input PDF paths, at least two"},"output":{"type":"string","description":"destination PDF path"}},"required":["inputs","output"]},
            "annotations": {},
        }),
        json!({
            "name": "desk_rotate_pdf",
            "description": "Rotates pages of a PDF and writes the result to an output path without modifying the input. degrees must be one of -270, -180, -90, 90, 180, 270; pages is an optional comma-separated 1-based page selector (e.g. 1-3,5), empty rotates all pages. Requires qpdf on PATH. Legacy alias: rotate_pdf.",
            "inputSchema": {"type":"object","properties":{"input":{"type":"string","description":"input PDF path"},"output":{"type":"string","description":"destination PDF path"},"degrees":{"type":"integer","description":"rotation in degrees: -270, -180, -90, 90, 180 or 270"},"pages":{"type":"string","description":"optional page selector, e.g. 1-3,5; empty rotates all pages"}},"required":["input","output","degrees"]},
            "annotations": {},
        }),
    ];
    for (index, name, description) in [
        (0, "split_pdf", "Splits a PDF into parts after the given pages. Legacy alias for desk_split_pdf."),
        (1, "merge_pdf", "Merges two or more PDFs into one output file. Legacy alias for desk_merge_pdf."),
        (2, "rotate_pdf", "Rotates pages of a PDF and writes the result to an output path. Legacy alias for desk_rotate_pdf."),
    ] {
        let mut alias = tools[index].clone();
        alias["name"] = name.into();
        alias["description"] = description.into();
        tools.push(alias);
    }
    tools
}

#[derive(Debug, Default, PartialEq)]
struct Arguments {
    input: String,
    at: String,
    output: String,
    inputs: Vec<String>,
    degrees: i64,
    pages: String,
}

// Decode the anonymous Go structs in ingest_tools.go, preserving source order,
// duplicate fields, case folding, null semantics and the numeric token itself.
// In particular, 90.0 and 9e1 are NOT Go int values even though both equal 90.
fn arguments(name: &str, raw: Option<&str>) -> Result<Arguments, String> {
    let raw = raw.ok_or_else(|| "unexpected end of JSON input".to_owned())?;
    let mut args = Arguments::default();
    let mut inputs_len = 0;
    if raw == "null" {
        return Ok(args);
    }
    if !raw.starts_with('{') {
        let target = match name {
            "desk_split_pdf" => r#"struct { Input string "json:\"input\""; At string "json:\"at\""; OutputDir string "json:\"output_dir\"" }"#,
            "desk_merge_pdf" => r#"struct { Inputs []string "json:\"inputs\""; Output string "json:\"output\"" }"#,
            "desk_rotate_pdf" => r#"struct { Input string "json:\"input\""; Output string "json:\"output\""; Degrees int "json:\"degrees\""; Pages string "json:\"pages\"" }"#,
            _ => return Err(format!("Unknown tool: {name}")),
        };
        return Err(format!(
            "json: cannot unmarshal {} into Go value of type {target}",
            value_type(raw)
        ));
    }
    let fields = go_raw_fields(raw)?;
    let permitted: &[&str] = match name {
        "desk_split_pdf" => &["input", "at", "output_dir"],
        "desk_merge_pdf" => &["inputs", "output"],
        "desk_rotate_pdf" => &["input", "output", "degrees", "pages"],
        _ => return Err(format!("Unknown tool: {name}")),
    };
    for (key, value) in fields.0 {
        let Some(&field) = permitted
            .iter()
            .find(|field| symdesk_vault::dataset::go_equal_fold(&key, field))
        else {
            continue;
        };
        let raw = value.get();
        match field {
            "inputs" => {
                if raw == "null" {
                    args.inputs.clear();
                    inputs_len = 0;
                } else if raw.starts_with('[') {
                    let values: Vec<Box<RawValue>> =
                        serde_json::from_str(raw).map_err(|error| error.to_string())?;
                    // Go reuses a slice's backing array across duplicate keys.
                    // Null string elements leave the previous element unchanged.
                    inputs_len = values.len();
                    if inputs_len == 0 {
                        args.inputs.clear();
                    } else if inputs_len > args.inputs.len() {
                        args.inputs.resize(inputs_len, String::new());
                    }
                    for (target, value) in args.inputs.iter_mut().zip(values) {
                        decode_string(target, value.get(), field)?;
                    }
                } else {
                    return Err(field_error(value_type(raw), field, "[]string"));
                }
            }
            "degrees" => {
                if raw != "null" {
                    if value_type(raw) != "number" {
                        return Err(field_error(value_type(raw), field, "int"));
                    }
                    args.degrees = raw.parse::<i64>().map_err(|_| {
                        field_error(&format!("number {raw}"), field, "int")
                    })?;
                }
            }
            _ => {
                let target = match field {
                    "input" => &mut args.input,
                    "at" => &mut args.at,
                    "output" | "output_dir" => &mut args.output,
                    "pages" => &mut args.pages,
                    _ => unreachable!("permitted fields above"),
                };
                decode_string(target, raw, field)?;
            }
        }
    }
    args.inputs.truncate(inputs_len);
    Ok(args)
}

fn field_error(value_type: &str, field: &str, target: &str) -> String {
    format!("json: cannot unmarshal {value_type} into Go struct field .{field} of type {target}")
}

fn decode_string(target: &mut String, raw: &str, field: &str) -> Result<(), String> {
    if raw == "null" {
        return Ok(());
    }
    if !raw.starts_with('"') {
        return Err(field_error(value_type(raw), field, "string"));
    }
    *target = serde_json::from_str(raw).map_err(|error| error.to_string())?;
    Ok(())
}

pub(super) fn call(name: &str, raw: Option<&str>, ctx: &Context) -> Result<Value, String> {
    let name = canonical_name(name).ok_or_else(|| format!("Unknown tool: {name}"))?;
    let args = arguments(name, raw)?;
    let tools = Tools::default();
    match name {
        "desk_split_pdf" => {
            if args.input.is_empty() || args.at.is_empty() || args.output.is_empty() {
                return Err("input, at and output_dir are required".into());
            }
            // Go checks availability BEFORE touching/validating the input.
            if !on_path("pdfinfo") || !on_path("qpdf") {
                return Err("PDF splitting requires pdfinfo and qpdf on PATH".into());
            }
            let parts = pdfops::split_pdf_at_spec(
                ctx, &tools, &args.input, &args.at, &args.output,
            )?;
            Ok(json!({"parts": parts}))
        }
        "desk_merge_pdf" => {
            if args.inputs.len() < 2 {
                return Err("at least two input PDFs are required".into());
            }
            if args.output.is_empty() {
                return Err("output is required".into());
            }
            pdfops::merge_pdfs(ctx, &tools, &args.inputs, &args.output)?;
            Ok(json!({"output": args.output}))
        }
        "desk_rotate_pdf" => {
            if args.input.is_empty() || args.output.is_empty() {
                return Err("input and output are required".into());
            }
            pdfops::rotate_pdf(
                ctx, &tools, &args.input, &args.output, args.degrees, &args.pages,
            )?;
            Ok(json!({"output": args.output}))
        }
        _ => unreachable!("canonical PDF names above"),
    }
}

// Availability only, without executing a tool or opening a vault. The renderer
// deliberately keeps its resolver private; actual execution uses its facade.
fn on_path(name: &str) -> bool {
    #[cfg(windows)]
    let names: Vec<String> = std::env::var("PATHEXT")
        .ok()
        .filter(|value| !value.is_empty())
        .unwrap_or_else(|| ".COM;.EXE;.BAT;.CMD".into())
        .split(';')
        .filter(|extension| !extension.is_empty())
        .map(|extension| format!("{name}{extension}"))
        .collect();
    #[cfg(not(windows))]
    let names = [name.to_owned()];
    #[cfg(windows)]
    if std::env::var_os("NoDefaultCurrentDirectoryInExePath").is_none()
        && names.iter().any(|name| executable(Path::new(name)))
    {
        return false; // Go exec.ErrDot: do not admit an implicit CWD tool.
    }
    for dir in std::env::split_paths(&std::env::var_os("PATH").unwrap_or_default()) {
        #[cfg(windows)]
        if dir.as_os_str().is_empty() {
            continue;
        }
        for name in &names {
            let path = dir.join(name);
            if executable(&path) {
                return path.is_absolute(); // exec.ErrDot for a relative PATH hit.
            }
        }
    }
    false
}

fn executable(path: &Path) -> bool {
    std::fs::metadata(path).is_ok_and(|metadata| {
        if !metadata.is_file() {
            return false;
        }
        #[cfg(unix)]
        {
            use std::os::unix::fs::PermissionsExt;
            metadata.permissions().mode() & 0o111 != 0
        }
        #[cfg(not(unix))]
        {
            true
        }
    })
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn pdf_raw_arguments_keep_go_order_numbers_and_nulls() {
        let args = arguments("desk_rotate_pdf", Some(
            r#"{"Input":"keep","input":null,"DEGREES":90,"degrees":null,"pages":"","unknown":false}"#,
        )).unwrap();
        assert_eq!(args.input, "keep");
        assert_eq!(args.degrees, 90);
        for token in ["90.0", "9e1", "9223372036854775808", "-9223372036854775809"] {
            let raw = format!("{{\"degrees\":{token}}}");
            assert_eq!(arguments("desk_rotate_pdf", Some(&raw)).unwrap_err(),
                field_error(&format!("number {token}"), "degrees", "int"));
        }
        assert_eq!(arguments("desk_rotate_pdf", Some(r#"{"pages":false,"input":1}"#)).unwrap_err(),
            field_error("bool", "pages", "string"));
        assert_eq!(arguments("desk_rotate_pdf", Some(r#"{"degrees":false,"degrees":90}"#)).unwrap_err(),
            field_error("bool", "degrees", "int"));
        let args = arguments("desk_merge_pdf", Some(r#"{"inputs":["one","two"],"inputs":[null,"three"]}"#)).unwrap();
        assert_eq!(args.inputs, ["one", "three"]);
        let args = arguments("desk_merge_pdf", Some(r#"{"inputs":["one","two"],"inputs":["three"],"inputs":[null,null]}"#)).unwrap();
        assert_eq!(args.inputs, ["three", "two"]);
        let args = arguments("desk_merge_pdf", Some(r#"{"inputs":["one","two"],"inputs":[],"inputs":[null,null]}"#)).unwrap();
        assert_eq!(args.inputs, ["", ""]);
        let args = arguments("desk_merge_pdf", Some(r#"{"inputs":["one","two"],"inputs":null}"#)).unwrap();
        assert!(args.inputs.is_empty());
        assert_eq!(arguments("desk_merge_pdf", Some(r#"{"inputs":[false]}"#)).unwrap_err(),
            field_error("bool", "inputs", "string"));
    }
}
