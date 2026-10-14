//! Test-only JSON driver. Production code lives in symdesk_render::pdfops.
use serde::Deserialize;
use serde_json::{Value, json};
use std::{io, path::Path, thread, time::Duration};
use symdesk_render::pdfops::{self, Context, Tools};

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Request {
    operation: String,
    #[serde(default)]
    layer: String,
    #[serde(default)]
    input: String,
    #[serde(default)]
    inputs: Vec<String>,
    #[serde(default)]
    output: String,
    #[serde(default)]
    pages: String,
    #[serde(default)]
    degrees: i64,
    tools: ToolPaths,
    #[serde(default)]
    cancel: bool,
    #[serde(default)]
    cancel_marker: String,
}
#[derive(Deserialize)]
#[serde(rename_all = "PascalCase", deny_unknown_fields)]
struct ToolPaths {
    #[serde(rename = "PDFInfo")]
    pdfinfo: String,
    #[serde(rename = "PDFSeparate")]
    pdfseparate: String,
    #[serde(rename = "PDFUnite")]
    pdfunite: String,
    #[serde(rename = "QPDF")]
    qpdf: String,
}
fn main() {
    let r: Request = serde_json::from_reader(io::stdin()).expect("request");
    let tools = Tools {
        pdfinfo: r.tools.pdfinfo,
        pdfseparate: r.tools.pdfseparate,
        pdfunite: r.tools.pdfunite,
        qpdf: r.tools.qpdf,
    };
    let ctx = Context::default();
    if r.cancel {
        ctx.cancel();
    }
    let monitor = if r.cancel_marker.is_empty() {
        None
    } else {
        let ctx = ctx.clone();
        Some(thread::spawn(move || {
            for _ in 0..10_000 {
                if Path::new(&r.cancel_marker).exists() {
                    ctx.cancel();
                    return;
                }
                thread::sleep(Duration::from_millis(1));
            }
            panic!("tool did not signal readiness");
        }))
    };
    let api = r.layer == "api";
    let result: Result<Value, String> = match r.operation.as_str() {
        "parse" => pdfops::parse_pages(&r.pages).map(|n| json!(n)),
        "split" => if api {
            pdfops::split_pdf_at_spec(&ctx, &tools, &r.input, &r.pages, &r.output)
        } else {
            tools.split(&ctx, &r.input, &r.pages, &r.output)
        }
        .map(|p| json!(p)),
        "merge" => if api {
            pdfops::merge_pdfs(&ctx, &tools, &r.inputs, &r.output)
        } else {
            tools.merge(&ctx, &r.inputs, &r.output)
        }
        .map(|()| Value::Null),
        "rotate" => if api {
            pdfops::rotate_pdf(&ctx, &tools, &r.input, &r.output, r.degrees, &r.pages)
        } else {
            tools.rotate(&ctx, &r.input, &r.output, r.degrees, &r.pages)
        }
        .map(|()| Value::Null),
        _ => panic!("unknown operation"),
    };
    if let Some(monitor) = monitor {
        monitor.join().expect("cancel monitor");
    }
    let response = match result {
        Ok(value) => json!({"result":value,"error":""}),
        Err(error) => json!({"result":null,"error":error}),
    };
    println!("{response}");
}
