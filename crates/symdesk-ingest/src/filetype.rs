//! Optional content-type verification through the `magika` CLI
//! (`internal/ingest/internal/filetype/magika.go`). The detection is advisory:
//! it is logged and never overrides the detected kind. A missing or failing
//! tool degrades silently to "no prediction".

use crate::gojson::{self, Val};
use crate::ocr::{Context, look_path, run_process};
use symdesk_vault::go_quote;

/// Detected content type reported by magika.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct MagikaPrediction {
    pub ct_label: String,
    pub mime_type: String,
    pub score: f64,
}

/// True when the `magika` binary is found on `PATH` (Go `Available`).
#[must_use]
pub fn available() -> bool {
    look_path("magika").is_ok()
}

/// `magika --json <path>`; every failure is logged through `logf` and yields
/// `None` (Go `Detect`).
pub fn detect(path: &str, logf: &mut dyn FnMut(String)) -> Option<MagikaPrediction> {
    let tool = look_path("magika").ok()?;
    let out = match run_process(
        &Context::background(),
        &tool,
        &["--json", path],
        None,
        &tool,
        &[],
    ) {
        Ok(out) => out,
        Err(e) => {
            logf(format!("magika: {e}"));
            return None;
        }
    };
    if !out.success {
        let msg = String::from_utf8_lossy(&out.stderr).trim().to_owned();
        let msg = if msg.is_empty() { out.status_text } else { msg };
        logf(format!("magika: {msg}"));
        return None;
    }
    match decode_result(&out.stdout) {
        Ok(Some(p)) => Some(p),
        Ok(None) => {
            logf(format!("magika: unexpected empty result for {path}"));
            None
        }
        Err(e) => {
            logf(format!("magika: failed to parse JSON output: {e}"));
            None
        }
    }
}

#[derive(Default)]
struct Prediction {
    ct_label: String,
    mime_type: String,
    score: f64,
}
#[derive(Default)]
struct Output {
    dl: Option<Prediction>,
    output: Option<Prediction>,
}

fn type_error(word: &str, strukt: &str, field: &str, ty: &str) -> String {
    format!("json: cannot unmarshal {word} into Go struct field {strukt}.{field} of type {ty}")
}

/// Decodes `magikaResult` like `json.Unmarshal`: the first type error wins,
/// later duplicates overwrite or merge. `Ok(None)` means no `result.output`.
fn decode_result(data: &[u8]) -> Result<Option<MagikaPrediction>, String> {
    let members = match gojson::parse(data)? {
        Val::Null => return Ok(None),
        Val::Obj(m) => m,
        other => {
            return Err(format!(
                "json: cannot unmarshal {} into Go value of type filetype.magikaResult",
                other.word()
            ));
        }
    };
    let mut first: Option<String> = None;
    let mut keep = |e: String| {
        if first.is_none() {
            first = Some(e);
        }
    };
    let mut result: Option<Output> = None;
    for (key, value) in &members {
        if gojson::key_matches(key, "path") {
            match value {
                Val::Null | Val::Str(_) => {}
                other => keep(type_error(other.word(), "magikaResult", "path", "string")),
            }
        } else if gojson::key_matches(key, "result") {
            match value {
                Val::Null => result = None,
                Val::Obj(fields) => {
                    let out = result.get_or_insert_with(Output::default);
                    for (k, v) in fields {
                        let (slot, name) = if gojson::key_matches(k, "dl") {
                            (&mut out.dl, "dl")
                        } else if gojson::key_matches(k, "output") {
                            (&mut out.output, "output")
                        } else {
                            continue;
                        };
                        match v {
                            Val::Null => *slot = None,
                            Val::Obj(preds) => {
                                let p = slot.get_or_insert_with(Prediction::default);
                                decode_prediction(p, preds, name, &mut keep);
                            }
                            other => keep(type_error(
                                other.word(),
                                "magikaOutput",
                                &format!("result.{name}"),
                                "filetype.magikaPrediction",
                            )),
                        }
                    }
                }
                other => keep(type_error(
                    other.word(),
                    "magikaResult",
                    "result",
                    "filetype.magikaOutput",
                )),
            }
        }
    }
    if let Some(e) = first {
        return Err(e);
    }
    Ok(result.and_then(|r| r.output).map(|p| MagikaPrediction {
        ct_label: p.ct_label,
        mime_type: p.mime_type,
        score: p.score,
    }))
}

fn decode_prediction(
    p: &mut Prediction,
    fields: &[(String, Val)],
    name: &str,
    keep: &mut dyn FnMut(String),
) {
    for (k, v) in fields {
        let path = |f: &str| format!("result.{name}.{f}");
        if gojson::key_matches(k, "ct_label") || gojson::key_matches(k, "mime_type") {
            let (target, fname) = if gojson::key_matches(k, "ct_label") {
                (&mut p.ct_label, "ct_label")
            } else {
                (&mut p.mime_type, "mime_type")
            };
            match v {
                Val::Null => {}
                Val::Str(s) => target.clone_from(s),
                other => keep(type_error(
                    other.word(),
                    "magikaPrediction",
                    &path(fname),
                    "string",
                )),
            }
        } else if gojson::key_matches(k, "score") {
            match v {
                Val::Null => {}
                Val::Num(n) => match gojson::float64(n) {
                    Ok(f) => p.score = f,
                    Err(()) => keep(type_error(
                        &format!("number {n}"),
                        "magikaPrediction",
                        &path("score"),
                        "float64",
                    )),
                },
                other => keep(type_error(
                    other.word(),
                    "magikaPrediction",
                    &path("score"),
                    "float64",
                )),
            }
        } else if gojson::key_matches(k, "group") {
            // Declared in Go (`group,omitempty`) and ignored, but a wrongly
            // typed value still fails the decode.
            match v {
                Val::Null | Val::Str(_) => {}
                other => keep(type_error(
                    other.word(),
                    "magikaPrediction",
                    &path("group"),
                    "string",
                )),
            }
        }
    }
}

/// Maps a magika `ct_label` to a MIME type; empty for unmapped labels.
#[must_use]
pub fn label_to_mime(label: &str) -> &'static str {
    match symdesk_vault::go_lowercase(label).as_str() {
        "pdf" => "application/pdf",
        "png" => "image/png",
        "jpeg" => "image/jpeg",
        "tiff" => "image/tiff",
        "webp" => "image/webp",
        "txt" | "text" => "text/plain",
        "csv" => "text/csv",
        "markdown" => "text/markdown",
        "html" => "text/html",
        "rtf" => "application/rtf",
        "docx" => "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
        "xlsx" => "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
        "odt" => "application/vnd.oasis.opendocument.text",
        "eml" => "message/rfc822",
        _ => "",
    }
}

/// Compares magika's verdict with the extension-derived `ext_mime` and warns on
/// a mismatch. Purely advisory (Go `VerifyAgainstGuess`).
pub fn verify_against_guess(path: &str, ext_mime: &str, logf: &mut dyn FnMut(String)) {
    let Some(pred) = detect(path, logf) else {
        return;
    };
    let magika_mime = label_to_mime(&pred.ct_label);
    if magika_mime.is_empty() || magika_mime == ext_mime {
        return;
    }
    let base = path.rsplit('/').next().unwrap_or(path);
    logf(format!(
        "Warning: {base} extension suggests {} but magika detected {} (label={}, score={:.3}); proceeding with extension-based guess",
        go_quote(ext_mime),
        go_quote(magika_mime),
        go_quote(&pred.ct_label),
        pred.score
    ));
}
