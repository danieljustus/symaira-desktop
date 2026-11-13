//! Ollama vision-language OCR engine (`internal/ingest/internal/ocr/vlm.go`).
//! Images go to a local model through the CoreKit Ollama client; any Ollama
//! failure falls back to the Tesseract [`Runner`].

use crate::goos;
use crate::ocr::{
    Context, KIND_HEIC, KIND_JPEG, KIND_PDF, KIND_PNG, KIND_TIFF, KIND_WEBP, OcrResult, Runner,
};
use base64::Engine as _;
use std::time::Duration;
use symaira_core_llm::{ClientBuilder, GenerateOption, lookup};
use symdesk_vault::go_quote;

/// Default Ollama endpoint when none is configured.
pub const DEFAULT_BASE_URL: &str = "http://localhost:11434";

/// Default OCR instruction (`defaultVlmPrompt`).
pub const DEFAULT_PROMPT: &str = "Transcribe all text from this document image exactly as it appears.\nPreserve line breaks. Do not translate, summarize, or add commentary.\nOutput only the transcribed text \u{2014} no preamble, no markdown formatting.";

/// `VLMRunner`.
pub struct VlmRunner {
    pub base_url: String,
    pub model: String,
    pub prompt: String,
    pub fallback: Runner,
}

impl VlmRunner {
    /// `NewVLMRunner`: empty `base_url` selects the default endpoint; a
    /// missing fallback is the standard runner for `ocr_lang`.
    #[must_use]
    pub fn new(base_url: &str, model: &str, ocr_lang: &str, fallback: Option<Runner>) -> Self {
        Self {
            base_url: if base_url.is_empty() {
                DEFAULT_BASE_URL.to_owned()
            } else {
                base_url.to_owned()
            },
            model: model.to_owned(),
            prompt: String::new(),
            fallback: fallback.unwrap_or_else(|| Runner::default_runner(ocr_lang)),
        }
    }

    fn prompt(&self) -> &str {
        if self.prompt.is_empty() {
            DEFAULT_PROMPT
        } else {
            &self.prompt
        }
    }

    /// `Extract`: images through the model, PDF/HEIC through the fallback.
    pub fn extract(&self, ctx: &Context, path: &str, kind: &str) -> Result<OcrResult, String> {
        match kind {
            KIND_PNG | KIND_JPEG | KIND_TIFF | KIND_WEBP => self.extract_with_vlm(ctx, path, kind),
            KIND_PDF | KIND_HEIC => self.fallback.extract(ctx, path, kind),
            other => Err(format!("ocr: unsupported source kind {}", go_quote(other))),
        }
    }

    fn generate(&self, b64: String) -> Result<String, String> {
        let descriptor = lookup("ollama").ok_or_else(|| {
            "ollama provider descriptor missing from embedded registry".to_owned()
        })?;
        let agent = symdesk_core::ai::go_agent_config(0).proxy(None).build();
        let client = ClientBuilder::new(descriptor.clone(), "")
            .agent(ureq::Agent::new_with_config(agent))
            .timeout(Duration::from_secs(5 * 60))
            .base_url(&self.base_url)
            .build()
            .map_err(|e| e.to_string())?;
        let mut text = String::new();
        client
            .generate(
                &self.model,
                self.prompt(),
                &GenerateOption {
                    images: vec![b64],
                    ..GenerateOption::default()
                },
                |chunk| {
                    text.push_str(&chunk.response);
                    Ok(())
                },
            )
            .map_err(|e| e.to_string())?;
        Ok(text)
    }

    fn extract_with_vlm(&self, ctx: &Context, path: &str, kind: &str) -> Result<OcrResult, String> {
        let raw = std::fs::read(path).map_err(|e| {
            format!(
                "vlm ocr: read image: {}",
                goos::path_error("open", path, &e)
            )
        })?;
        let b64 = base64::engine::general_purpose::STANDARD.encode(raw);
        match self.generate(b64) {
            Err(error) => {
                let fallback = self.fallback.extract(ctx, path, kind).map_err(|fb| {
                    format!(
                        "vlm ocr failed (ollama: {error}), and tesseract fallback also failed: {fb}"
                    )
                })?;
                Ok(OcrResult {
                    text: fallback.text,
                    mime: fallback.mime,
                    engine: "tesseract(vlm-fallback)".to_owned(),
                    language: fallback.language,
                })
            }
            Ok(text) => Ok(OcrResult {
                text: text.trim().to_owned(),
                mime: "image/ocr".to_owned(),
                engine: if self.model.is_empty() {
                    "vlm".to_owned()
                } else {
                    self.model.clone()
                },
                language: String::new(),
            }),
        }
    }
}
