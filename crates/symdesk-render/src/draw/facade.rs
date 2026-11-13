//! Public facade: port of `internal/draw/draw.go`, the single entry point of
//! the SymDraw pipeline (parse -> IR -> scene -> SVG / PNG / Typst).
//!
//! The Go facade takes the request by value but shares `Scene` through a
//! pointer, so `Render` mutates a caller-provided scene (theme, background,
//! padding). The Rust request owns its scene; the mutated scene is dropped
//! with the request. GIF output is, as in Go, not a `Render` format: animated
//! GIFs go through [`super::animate`] and [`super::gif`].

use super::emit::{SvgOptions, emit_svg};
use super::gotext::quote;
use super::ir::{Diagram, validate as ir_validate};
use super::measure::{Measurer, fonts_version_key};
use super::png::{RasterOptions, emit_png};
use super::scene::{Scene, default_measurer as scene_measurer};
use super::typst::emit_typst;
use super::{ParseError, parse_json, parse_mermaid, parse_source, theme};

/// Go `draw.FormatSVG`.
pub const FORMAT_SVG: &str = "svg";
/// Go `draw.FormatPNG`.
pub const FORMAT_PNG: &str = "png";
/// Go `draw.FormatGIF` (declared by Go but rejected by `Render`).
pub const FORMAT_GIF: &str = "gif";
/// Go `draw.FormatTypst`.
pub const FORMAT_TYPST: &str = "typst";

/// Go `draw.RenderRequest`. An empty `format` selects SVG; `scene` wins over
/// `ir`, which wins over `source`.
#[derive(Debug, Clone, Default)]
pub struct RenderRequest {
    pub source: String,
    pub ir: Option<Diagram>,
    pub scene: Option<Scene>,
    pub format: String,
    pub theme: String,
    pub scale: f64,
    pub text_as_paths: bool,
    pub padding: f64,
}

/// Go `draw.RenderResult`.
#[derive(Debug, Clone, PartialEq)]
pub struct RenderResult {
    pub data: Vec<u8>,
    pub format: String,
    pub width: f64,
    pub height: f64,
    pub font_hash: String,
    pub duration_ms: i64,
}

/// Go `draw.Render` with a live context.
pub fn render(req: RenderRequest) -> Result<RenderResult, String> {
    render_with_context(req, false)
}

/// Go `draw.Render`; `canceled` models an already-done `context.Context`.
pub fn render_with_context(req: RenderRequest, canceled: bool) -> Result<RenderResult, String> {
    let start = std::time::Instant::now();
    if canceled {
        return Err("context canceled".to_owned());
    }

    let mut sc = match req.scene {
        Some(sc) => sc,
        None => {
            let mut diag = req.ir;
            if diag.is_none() && !req.source.is_empty() {
                diag = Some(parse(&req.source).map_err(|e| format!("parse diagram source: {e}"))?);
            }
            let diag = diag.ok_or_else(|| {
                "either Source, IR or Scene must be provided in RenderRequest".to_owned()
            })?;
            super::builder::build_scene(&diag).map_err(|e| format!("build scene from ir: {e}"))?
        }
    };

    if !req.theme.is_empty() {
        let th = theme::resolve(&req.theme);
        if sc.background.is_empty() || sc.background == "none" {
            sc.background = th.background.clone();
        }
        sc.theme = th;
    }

    if req.padding > 0.0 {
        sc.fit_to_bounds(req.padding);
    }

    let format = if req.format.is_empty() {
        FORMAT_SVG.to_owned()
    } else {
        req.format
    };

    let emitted: Result<Vec<u8>, String> = match format.as_str() {
        FORMAT_SVG => emit_svg(
            Some(&sc),
            &SvgOptions {
                text_as_paths: req.text_as_paths,
                font_hash: font_hash(),
                generator: "symdraw".to_owned(),
            },
        )
        .map(String::into_bytes),
        FORMAT_PNG => {
            let scale = if req.scale <= 0.0 { 1.0 } else { req.scale };
            emit_png(Some(&sc), &RasterOptions { scale })
        }
        FORMAT_TYPST => emit_typst(Some(&sc)).map(String::into_bytes),
        _ => {
            return Err(format!("unsupported render format {}", quote(&format)));
        }
    };
    let data = emitted.map_err(|e| format!("emit {format}: {e}"))?;

    Ok(RenderResult {
        data,
        format,
        width: sc.width,
        height: sc.height,
        font_hash: font_hash(),
        duration_ms: i64::try_from(start.elapsed().as_millis()).unwrap_or(i64::MAX),
    })
}

/// Go `draw.RenderScene`: render a scene directly to `format`.
pub fn render_scene(sc: Scene, format: &str) -> Result<Vec<u8>, String> {
    render(RenderRequest {
        scene: Some(sc),
        format: format.to_owned(),
        ..Default::default()
    })
    .map(|r| r.data)
}

/// Go `draw.Parse`.
pub fn parse(source: &str) -> Result<Diagram, ParseError> {
    parse_source(source)
}

/// Go `draw.ParseMermaid`.
pub fn parse_mermaid_source(source: &str) -> Result<Diagram, ParseError> {
    parse_mermaid(source)
}

/// Go `draw.ParseJSON`.
pub fn parse_json_diagram(data: &[u8]) -> Result<Diagram, ParseError> {
    parse_json(data)
}

/// Go `draw.DialectVersion`.
pub fn dialect_version() -> &'static str {
    super::DIALECT_VERSION
}

/// Go `draw.Validate`; `None` is Go's nil diagram.
pub fn validate(d: Option<&Diagram>) -> Result<(), String> {
    match d {
        None => Err("diagram is nil".to_owned()),
        Some(d) => ir_validate(d).map_err(|e| e.to_string()),
    }
}

/// Go `draw.ValidateJSON`.
pub fn validate_json(data: &[u8]) -> Result<(), ParseError> {
    parse_json(data).map(|_| ())
}

/// Go `draw.Kinds`.
pub fn kinds() -> Vec<&'static str> {
    vec!["graph", "sequence", "timeline", "tree", "chart", "custom"]
}

/// Go `draw.Themes`.
pub fn themes() -> Vec<String> {
    theme::names()
}

/// Go `draw.FontHash`.
pub fn font_hash() -> String {
    fonts_version_key()
}

/// Go `draw.NewScene`.
pub fn new_scene(width: f64, height: f64, theme_name: &str) -> Scene {
    Scene::new(width, height, &theme::resolve(theme_name))
}

/// Go `draw.DefaultMeasurer`.
pub fn default_measurer() -> &'static Measurer {
    scene_measurer()
}
