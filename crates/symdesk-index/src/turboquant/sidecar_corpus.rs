//! Renders Rust-written TurboQuant sidecars for the Go read-back gate (#1137).
//!
//! `turboquant-sidecar-writer` reads the Go property corpus and writes one
//! `encode_sidecar` blob and metadata JSON per case. The Go test
//! `TestGoReadsRustSidecars` then decodes those bytes with CoreKit.

use serde::Deserialize;

use super::Codec;

#[derive(Deserialize)]
struct PropertyFile {
    cases: Vec<PropertyCase>,
}

#[derive(Deserialize)]
struct PropertyCase {
    id: String,
    dim: i64,
    bit_width: i64,
    seed: i64,
    vector_f32_hex: String,
}

/// Renders the sidecar corpus for a property corpus JSON document.
///
/// Text is built by hand, not through `serde_json::Map`, so key order does not
/// depend on serde_json feature unification.
pub fn render_sidecar_corpus(property_json: &str) -> Result<String, String> {
    let file: PropertyFile = serde_json::from_str(property_json).map_err(|e| e.to_string())?;
    let mut entries = Vec::with_capacity(file.cases.len());
    for case in &file.cases {
        let codec =
            Codec::new(case.dim, case.bit_width, case.seed, 0).map_err(|e| e.to_string())?;
        let vector = f32s(&case.vector_f32_hex)?;
        let (blob, meta) = codec
            .encode_sidecar(&vector, 0.0)
            .map_err(|e| e.to_string())?;
        let meta_json = meta.to_json().map_err(|e| e.to_string())?;
        entries.push(format!(
            "    {{\n      \"id\": {},\n      \"blob_hex\": {},\n      \"meta_json\": {}\n    }}",
            quote(&case.id)?,
            quote(&hex(&blob))?,
            quote(&meta_json)?,
        ));
    }
    Ok(format!(
        "{{\n  \"schema_version\": 1,\n  \"writer\": {},\n  \"cases\": [\n{}\n  ]\n}}\n",
        quote("symdesk-index turboquant-sidecar-writer")?,
        entries.join(",\n"),
    ))
}

fn quote(text: &str) -> Result<String, String> {
    serde_json::to_string(text).map_err(|e| e.to_string())
}

fn f32s(hex_text: &str) -> Result<Vec<f32>, String> {
    if !hex_text.len().is_multiple_of(8) {
        return Err(format!(
            "f32 hex length {} is not a multiple of 8",
            hex_text.len()
        ));
    }
    (0..hex_text.len())
        .step_by(8)
        .map(|at| {
            let word = u32::from_str_radix(&hex_text[at..at + 8], 16).map_err(|e| e.to_string())?;
            // Hex is little-endian bytes, so swap the byte pairs back.
            Ok(f32::from_bits(word.swap_bytes()))
        })
        .collect()
}

fn hex(bytes: &[u8]) -> String {
    bytes.iter().map(|b| format!("{b:02x}")).collect()
}
