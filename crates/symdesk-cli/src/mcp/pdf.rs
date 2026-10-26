//! Canonical Go PDF tool handlers; filesystem/process policy stays in pdfops.
use std::path::Path;

use serde_json::{Value, json, value::RawValue};
use symdesk_render::pdfops::{self, Context, Tools};

use super::{go_json_type as value_type, go_raw_fields};

pub(super) fn canonical_name(name: &str) -> Option<&'static str> {
    match name {
        "desk_split_pdf" | "split_pdf" => Some("desk_split_pdf"),
        "desk_merge_pdf" | "merge_pdf" => Some("desk_merge_pdf"),
        "desk_rotate_pdf" | "rotate_pdf" => Some("desk_rotate_pdf"),
        _ => None,
    }
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
            "desk_split_pdf" => {
                r#"struct { Input string "json:\"input\""; At string "json:\"at\""; OutputDir string "json:\"output_dir\"" }"#
            }
            "desk_merge_pdf" => {
                r#"struct { Inputs []string "json:\"inputs\""; Output string "json:\"output\"" }"#
            }
            "desk_rotate_pdf" => {
                r#"struct { Input string "json:\"input\""; Output string "json:\"output\""; Degrees int "json:\"degrees\""; Pages string "json:\"pages\"" }"#
            }
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
                    args.degrees = raw
                        .parse::<i64>()
                        .map_err(|_| field_error(&format!("number {raw}"), field, "int"))?;
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
            let parts =
                pdfops::split_pdf_at_spec(ctx, &tools, &args.input, &args.at, &args.output)?;
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
                ctx,
                &tools,
                &args.input,
                &args.output,
                args.degrees,
                &args.pages,
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
            assert_eq!(
                arguments("desk_rotate_pdf", Some(&raw)).unwrap_err(),
                field_error(&format!("number {token}"), "degrees", "int")
            );
        }
        assert_eq!(
            arguments("desk_rotate_pdf", Some(r#"{"pages":false,"input":1}"#)).unwrap_err(),
            field_error("bool", "pages", "string")
        );
        assert_eq!(
            arguments("desk_rotate_pdf", Some(r#"{"degrees":false,"degrees":90}"#)).unwrap_err(),
            field_error("bool", "degrees", "int")
        );
        let args = arguments(
            "desk_merge_pdf",
            Some(r#"{"inputs":["one","two"],"inputs":[null,"three"]}"#),
        )
        .unwrap();
        assert_eq!(args.inputs, ["one", "three"]);
        let args = arguments(
            "desk_merge_pdf",
            Some(r#"{"inputs":["one","two"],"inputs":["three"],"inputs":[null,null]}"#),
        )
        .unwrap();
        assert_eq!(args.inputs, ["three", "two"]);
        let args = arguments(
            "desk_merge_pdf",
            Some(r#"{"inputs":["one","two"],"inputs":[],"inputs":[null,null]}"#),
        )
        .unwrap();
        assert_eq!(args.inputs, ["", ""]);
        let args = arguments(
            "desk_merge_pdf",
            Some(r#"{"inputs":["one","two"],"inputs":null}"#),
        )
        .unwrap();
        assert!(args.inputs.is_empty());
        assert_eq!(
            arguments("desk_merge_pdf", Some(r#"{"inputs":[false]}"#)).unwrap_err(),
            field_error("bool", "inputs", "string")
        );
    }
}
