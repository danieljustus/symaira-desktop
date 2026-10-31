//! Barcode/QR separator splitting (`internal/ingest/barcode.go`, `split.go`):
//! `DetectBarcode` over a decoded image, `ScanPDFForBarcodes` (pdftoppm at
//! 300 dpi, pages in directory order) and `SplitPDFByBarcode` (qpdf legacy
//! split). The decoder is `rxing`, a Rust port of the same ZXing readers that
//! gozxing ports, tried in gozxing's order with default hints.

use image::DynamicImage;
use rxing::{
    BarcodeFormat, BinaryBitmap, Reader,
    common::HybridBinarizer,
    datamatrix::DataMatrixReader,
    oned::{
        Code39Reader, Code128Reader, EAN8Reader, EAN13Reader, ITFReader, UPCAReader, UPCEReader,
    },
    qrcode::QRCodeReader,
};
use std::collections::BTreeMap;
use std::fs;
use std::path::{Path, PathBuf};
use symdesk_render::pdfops::{Context, Tools, copy};

/// `BarcodeConfig` (defaults: `PATCH-T`, discard separators, `ASN:`).
#[derive(Clone, Debug)]
pub struct BarcodeConfig {
    pub separator_pattern: String,
    pub discard_separators: bool,
    pub parse_asn: bool,
    pub asn_prefix: String,
}

impl Default for BarcodeConfig {
    fn default() -> Self {
        Self {
            separator_pattern: "PATCH-T".into(),
            discard_separators: true,
            parse_asn: false,
            asn_prefix: "ASN:".into(),
        }
    }
}

/// `BarcodeSplitResult`.
#[derive(Clone, Debug, Default)]
pub struct BarcodeSplitResult {
    pub parts: Vec<String>,
    pub asns: BTreeMap<usize, i64>,
    pub separator_pages: Vec<i64>,
    pub errors: BTreeMap<i64, String>,
}

fn format_name(format: &BarcodeFormat) -> &'static str {
    match format {
        BarcodeFormat::CODE_39 => "CODE_39",
        BarcodeFormat::CODE_128 => "CODE_128",
        BarcodeFormat::EAN_13 => "EAN_13",
        BarcodeFormat::EAN_8 => "EAN_8",
        BarcodeFormat::UPC_A => "UPC_A",
        BarcodeFormat::UPC_E => "UPC_E",
        BarcodeFormat::ITF => "ITF",
        BarcodeFormat::QR_CODE => "QR_CODE",
        BarcodeFormat::DATA_MATRIX => "DATA_MATRIX",
        _ => "UNKNOWN",
    }
}

/// `DetectBarcode`: first reader (Code39, Code128, EAN13, EAN8, UPCA, UPCE,
/// ITF, QR, DataMatrix) that decodes wins; `("", "")` when none does.
pub fn detect_barcode(img: &DynamicImage) -> (String, String) {
    macro_rules! attempt {
        ($reader:expr) => {{
            let mut bitmap = BinaryBitmap::new(HybridBinarizer::new(
                rxing::BufferedImageLuminanceSource::new(img.clone()),
            ));
            if let Ok(result) = $reader.decode(&mut bitmap) {
                return (
                    result.getText().to_string(),
                    format_name(result.getBarcodeFormat()).to_string(),
                );
            }
        }};
    }
    attempt!(Code39Reader::default());
    attempt!(Code128Reader::default());
    attempt!(EAN13Reader::default());
    attempt!(EAN8Reader::default());
    attempt!(UPCAReader::default());
    attempt!(UPCEReader::default());
    attempt!(ITFReader::default());
    attempt!(QRCodeReader::default());
    attempt!(DataMatrixReader::default());
    (String::new(), String::new())
}

/// Decode an encoded PNG/JPEG like Go's `image.Decode` with those two
/// decoders registered, then `DetectBarcode`.
pub fn detect_barcode_bytes(data: &[u8]) -> Result<(String, String), String> {
    let img = decode_image(data)?;
    Ok(detect_barcode(&img))
}

fn decode_image(data: &[u8]) -> Result<DynamicImage, String> {
    let format = match image::guess_format(data) {
        Ok(f @ (image::ImageFormat::Png | image::ImageFormat::Jpeg)) => f,
        _ => return Err("image: unknown format".into()),
    };
    image::load_from_memory_with_format(data, format).map_err(|e| e.to_string())
}

fn go_upper(s: &str) -> String {
    s.chars()
        .map(|c| {
            let mut up = c.to_uppercase();
            match (up.next(), up.next()) {
                (Some(u), None) => u,
                _ => c,
            }
        })
        .collect()
}

/// `exec.LookPath` for a bare command name on `PATH`.
fn look_path(name: &str) -> Option<PathBuf> {
    use std::os::unix::fs::PermissionsExt;
    let paths = std::env::var_os("PATH")?;
    for dir in std::env::split_paths(&paths) {
        if !dir.is_absolute() {
            continue;
        }
        let candidate = dir.join(name);
        if let Ok(meta) = fs::metadata(&candidate)
            && meta.is_file()
            && meta.permissions().mode() & 0o111 != 0
        {
            return Some(candidate);
        }
    }
    None
}

struct TempDir(PathBuf);
impl TempDir {
    fn new(prefix: &str) -> Result<Self, String> {
        use std::sync::atomic::{AtomicU64, Ordering};
        static NONCE: AtomicU64 = AtomicU64::new(0);
        for _ in 0..64 {
            let stamp = std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap_or_default()
                .as_nanos();
            let path = std::env::temp_dir().join(format!(
                "{prefix}{}{}",
                stamp % 1_000_000_000,
                NONCE.fetch_add(1, Ordering::Relaxed)
            ));
            match fs::create_dir(&path) {
                Ok(()) => {
                    #[cfg(unix)]
                    {
                        use std::os::unix::fs::PermissionsExt;
                        let _ = fs::set_permissions(&path, fs::Permissions::from_mode(0o700));
                    }
                    return Ok(Self(path));
                }
                Err(e) if e.kind() == std::io::ErrorKind::AlreadyExists => continue,
                Err(e) => return Err(format!("temp dir: {e}")),
            }
        }
        Err("temp dir: too many collisions".into())
    }
}
impl Drop for TempDir {
    fn drop(&mut self) {
        if let Err(error) = fs::remove_dir_all(&self.0) {
            eprintln!("barcode workspace cleanup failed: {error}");
        }
    }
}

/// Go's `exec.ExitError` text.
fn exit_text(status: &std::process::ExitStatus) -> String {
    use std::os::unix::process::ExitStatusExt;
    if let Some(code) = status.code() {
        format!("exit status {code}")
    } else if let Some(signal) = status.signal() {
        format!(
            "signal: {}",
            match signal {
                9 => "killed".to_string(),
                15 => "terminated".to_string(),
                11 => "segmentation fault".to_string(),
                6 => "aborted".to_string(),
                other => other.to_string(),
            }
        )
    } else {
        status.to_string()
    }
}

/// `parsePPMFilePage`: the integer after the last `-` of `page-NN.png`.
fn parse_ppm_file_page(name: &str) -> Option<i64> {
    let base = name.strip_suffix(".png").unwrap_or(name);
    let parts: Vec<&str> = base.split('-').collect();
    if parts.len() < 2 {
        return None;
    }
    parts[parts.len() - 1].parse::<i64>().ok()
}

/// `ScanPDFForBarcodes`: 1-based pages (in directory-name order, so
/// unpadded names sort lexically) whose barcode starts with the pattern.
pub fn scan_pdf_for_barcodes(pdf_path: &str, cfg: &BarcodeConfig) -> Result<Vec<i64>, String> {
    let ppm = look_path("pdftoppm")
        .ok_or("pdftoppm not found: barcode scanning requires poppler-utils".to_string())?;
    let tmp = TempDir::new("symdesk-barcode-")?;
    let output = std::process::Command::new(&ppm)
        .args(["-png", "-r", "300", pdf_path])
        .arg(tmp.0.join("page"))
        .stdin(std::process::Stdio::null())
        .stdout(std::process::Stdio::null())
        .stderr(std::process::Stdio::piped())
        .output()
        .map_err(|e| format!("pdftoppm failed: {e}: "))?;
    if !output.status.success() {
        return Err(format!(
            "pdftoppm failed: {}: {}",
            exit_text(&output.status),
            String::from_utf8_lossy(&output.stderr)
        ));
    }
    let mut names = Vec::new();
    for entry in fs::read_dir(&tmp.0).map_err(|e| format!("read temp dir: {e}"))? {
        let entry = entry.map_err(|e| format!("read temp dir: {e}"))?;
        let is_dir = entry.file_type().map(|t| t.is_dir()).unwrap_or(false);
        let name = entry.file_name().to_string_lossy().into_owned();
        if is_dir || !name.ends_with(".png") {
            continue;
        }
        names.push(name);
    }
    names.sort();
    let pattern = go_upper(&cfg.separator_pattern);
    let mut pages = Vec::new();
    for name in names {
        let Some(page) = parse_ppm_file_page(&name) else {
            continue;
        };
        let Ok(data) = fs::read(tmp.0.join(&name)) else {
            continue;
        };
        let Ok(img) = decode_image(&data) else {
            continue;
        };
        let (text, _) = detect_barcode(&img);
        if !text.is_empty() && go_upper(text.trim()).starts_with(&pattern) {
            pages.push(page);
        }
    }
    Ok(pages)
}

/// `SplitPDF`: copy when there are no split points, otherwise the
/// qpdf-only legacy split into `part-N.pdf`.
pub fn split_pdf(
    pdf_path: &str,
    split_points: &[i64],
    output_dir: &str,
) -> Result<Vec<String>, String> {
    if split_points.is_empty() {
        let dst = Path::new(output_dir).join("part-1.pdf");
        let dst = dst.to_string_lossy().into_owned();
        copy(pdf_path, &dst).map_err(|e| format!("write part: {e}"))?;
        return Ok(vec![dst]);
    }
    Tools::default().split_legacy(&Context::default(), pdf_path, split_points, output_dir)
}

/// `SplitPDFByBarcode`. ASNs are never populated (Go parses none).
pub fn split_pdf_by_barcode(
    pdf_path: &str,
    cfg: &BarcodeConfig,
    output_dir: &str,
) -> Result<BarcodeSplitResult, String> {
    let sep_pages =
        scan_pdf_for_barcodes(pdf_path, cfg).map_err(|e| format!("scan barcodes: {e}"))?;
    let mut result = BarcodeSplitResult {
        separator_pages: sep_pages.clone(),
        ..Default::default()
    };
    let split_points: Vec<i64> = if cfg.discard_separators {
        sep_pages.iter().filter(|p| **p > 1).map(|p| p - 1).collect()
    } else {
        sep_pages
    };
    result.parts =
        split_pdf(pdf_path, &split_points, output_dir).map_err(|e| format!("split pdf: {e}"))?;
    Ok(result)
}
