//! Ingest detection and local extraction. No OCR, barcode, subprocess or network.
//! Ingest and retrieval have different whitespace/XML/table/limit contracts;
//! retrieval's parsers therefore cannot be substituted for these readers.
use crate::structured;
use symdesk_core::{document_format as formats, textnorm};

pub const MAX_ZIP_ENTRY_BYTES: usize = 32 << 20;
pub const MAX_ZIP_TOTAL_BYTES: usize = 128 << 20;
pub const MAX_ZIP_ENTRIES: usize = 4096;
/// Explicit opt-in caller bound for non-archive input. Go ReadText is unbounded.
pub const DEFAULT_SOURCE_LIMIT: usize = 128 << 20;
#[derive(Clone, Debug, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
pub struct Extraction {
    #[serde(rename = "Text")]
    pub text: String,
    #[serde(rename = "MIME")]
    pub mime: String,
    #[serde(rename = "Engine")]
    pub engine: String,
    #[serde(rename = "Language")]
    pub language: String,
}
fn kind(ext: &str) -> &'static str {
    formats::kind_for_extension(ext).unwrap_or("")
}
fn extension(path: &str) -> String {
    let base = path
        .rsplit(std::path::MAIN_SEPARATOR)
        .next()
        .unwrap_or(path);
    base.rfind('.')
        .map(|i| base[i..].to_lowercase())
        .unwrap_or_default()
}
fn unsupported(k: &str) -> bool {
    formats::UNSUPPORTED_FORMATS
        .iter()
        .any(|f| f.format.kind == k)
}

/// Detect already-read source bytes; content-bearing containers override suffixes.
/// Optional Magika advisory is deliberately not run (it never changes the kind).
pub fn detect_bytes(path: &str, data: &[u8]) -> Result<String, String> {
    let head = &data[..data.len().min(512)];
    let image = if head.starts_with(b"%PDF") {
        "application/pdf"
    } else if head.starts_with(b"\x89PNG\r\n\x1a\n") {
        "image/png"
    } else if head.starts_with(b"\xff\xd8\xff") {
        "image/jpeg"
    } else if head.starts_with(b"II*\0") || head.starts_with(b"MM\0*") {
        "image/tiff"
    } else if head.len() >= 12 && &head[..4] == b"RIFF" && &head[8..12] == b"WEBP" {
        "image/webp"
    } else if head.len() >= 12
        && &head[4..8] == b"ftyp"
        && [
            b"heic", b"heix", b"hevc", b"hevx", b"heim", b"heis", b"mif1", b"msf1",
        ]
        .contains(&head[8..12].try_into().unwrap())
    {
        "image/heic"
    } else {
        ""
    };
    if !image.is_empty() {
        return Ok(image.into());
    }
    let ext = extension(path);
    let zip = head.starts_with(b"PK\x03\x04");
    let ole = head.starts_with(b"\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1");
    let container = zip || ole || head.starts_with(b"PK\x05\x06");
    if container {
        let found = if zip {
            detect_zip(data)
        } else if ole {
            detect_ole(data)
        } else {
            ""
        };
        if !found.is_empty() {
            return Ok(found.into());
        }
        let fallback = kind(&ext);
        return Ok(if unsupported(fallback) { fallback } else { "" }.into());
    }
    let found = formats::kind_for_extension(&ext).unwrap_or(match ext.as_str() {
        ".eml" => "message/rfc822",
        ".png" => "image/png",
        ".jpg" | ".jpeg" => "image/jpeg",
        ".tif" | ".tiff" => "image/tiff",
        ".webp" => "image/webp",
        ".heic" | ".heif" => "image/heic",
        _ => "",
    });
    if found.is_empty() {
        Err(format!("unsupported file type: {path}"))
    } else {
        Ok(found.into())
    }
}
fn detect_ole(data: &[u8]) -> &'static str {
    if data.len() < 52 {
        return "";
    }
    // Go trusts the exponent; restrict allocation/offset arithmetic on malformed input.
    let Some(size) = 1usize
        .checked_shl(u32::from(data[30]))
        .filter(|n| *n <= MAX_ZIP_ENTRY_BYTES)
    else {
        return "";
    };
    let sector = u32::from_le_bytes(data[48..52].try_into().unwrap());
    let Some(start) = (sector as usize)
        .checked_add(1)
        .and_then(|n| n.checked_mul(size))
    else {
        return "";
    };
    let Some(dir) = start.checked_add(size).and_then(|end| data.get(start..end)) else {
        return "";
    };
    for entry in dir.as_chunks::<128>().0 {
        let len = u16::from_le_bytes([entry[64], entry[65]]) as usize;
        if !(2..=64).contains(&len) {
            continue;
        }
        let name: String = entry[..len - 2]
            .as_chunks::<2>()
            .0
            .iter()
            .map(|c| char::from_u32(u16::from_le_bytes([c[0], c[1]]) as u32).unwrap_or('\u{fffd}'))
            .collect();
        match name.as_str() {
            "WordDocument" => return kind("doc"),
            "Workbook" | "Book" => return kind("xls"),
            "PowerPoint Document" => return kind("ppt"),
            _ => {}
        }
    }
    ""
}
struct Archive<'a> {
    zip: crate::gzip::Reader<'a>,
    pub(crate) names: Vec<String>,
    total: usize,
}
impl<'a> Archive<'a> {
    fn open(data: &'a [u8]) -> Result<Self, String> {
        let zip = crate::gzip::Reader::new(data, MAX_ZIP_ENTRIES + 1)
            .map_err(|e| format!("open archive: {e}"))?;
        if zip.count > MAX_ZIP_ENTRIES {
            return Err(format!(
                "extract: archive limit exceeded: {} entries exceed the cap of {MAX_ZIP_ENTRIES}",
                zip.count
            ));
        }
        let names = zip.files.iter().map(|f| f.name.clone()).collect();
        Ok(Self {
            zip,
            names,
            total: 0,
        })
    }
    fn part(&mut self, name: &str) -> Result<Option<Vec<u8>>, String> {
        let Some(i) = self.names.iter().position(|n| n == name) else {
            return Ok(None);
        };
        let quoted = crate::gmime::go_quote(name.as_bytes());
        let data = self
            .zip
            .read(&self.zip.files[i], MAX_ZIP_ENTRY_BYTES)
            .map_err(|e| match e {
                crate::gzip::ReadError::Open(m) => format!("open part {quoted}: {m}"),
                crate::gzip::ReadError::Read(m) => format!("read part {quoted}: {m}"),
            })?;
        if data.len() > MAX_ZIP_ENTRY_BYTES {
            return Err(format!(
                "extract: archive limit exceeded: part {quoted} exceeds the per-entry cap of {MAX_ZIP_ENTRY_BYTES} bytes"
            ));
        }
        self.total += data.len();
        if self.total > MAX_ZIP_TOTAL_BYTES {
            return Err(format!(
                "extract: archive limit exceeded: archive total exceeds the cap of {MAX_ZIP_TOTAL_BYTES} bytes"
            ));
        }
        Ok(Some(data))
    }
}
fn detect_zip(data: &[u8]) -> &'static str {
    let Ok(mut a) = Archive::open(data) else {
        return "";
    };
    if let Ok(Some(m)) = a.part("mimetype") {
        let m = String::from_utf8_lossy(&m);
        let m = m.trim();
        for ext in ["odt", "ods", "odp", "epub"] {
            if m == kind(ext) {
                return kind(ext);
            }
        }
    }
    if let Ok(Some(rels)) = a.part("_rels/.rels") {
        let main = opc_office_document_part(&rels);
        if !main.is_empty() {
            for (p, ext) in [
                ("word/document.xml", "docx"),
                ("xl/workbook.xml", "xlsx"),
                ("ppt/presentation.xml", "pptx"),
            ] {
                if main == p {
                    return kind(ext);
                }
            }
        }
    }
    if let Ok(Some(ct)) = a.part("[Content_Types].xml") {
        let k = opc_content_type_kind(&ct);
        if !k.is_empty() {
            return k;
        }
    }
    for (p, ext) in [
        ("word/document.xml", "docx"),
        ("xl/workbook.xml", "xlsx"),
        ("ppt/presentation.xml", "pptx"),
    ] {
        if a.names.iter().any(|n| n == p) {
            return kind(ext);
        }
    }
    ""
}
/// `opcOfficeDocumentPart`.
fn opc_office_document_part(data: &[u8]) -> String {
    let mut dec = crate::gxml::Decoder::new(data);
    loop {
        let Ok(tok) = dec.token() else {
            return String::new();
        };
        let crate::gxml::Token::Start { name, attr } = tok else {
            continue;
        };
        if name.local != "Relationship" {
            continue;
        }
        let (mut rel_type, mut target) = (String::new(), String::new());
        for a in attr {
            match a.name.local.as_str() {
                "Type" => rel_type = a.value,
                "Target" => target = a.value,
                _ => {}
            }
        }
        if rel_type.ends_with("/officeDocument") && !target.is_empty() {
            return target.strip_prefix('/').unwrap_or(&target).to_string();
        }
    }
}
/// `opcContentTypeKind`.
fn opc_content_type_kind(data: &[u8]) -> &'static str {
    let mut dec = crate::gxml::Decoder::new(data);
    loop {
        let Ok(tok) = dec.token() else {
            return "";
        };
        let crate::gxml::Token::Start { name, attr } = tok else {
            continue;
        };
        if name.local != "Override" {
            continue;
        }
        let ct = attr
            .iter()
            .find(|a| a.name.local == "ContentType")
            .map(|a| a.value.clone())
            .unwrap_or_default();
        for (needle, ext) in [
            ("wordprocessingml.document.main+xml", "docx"),
            ("spreadsheetml.sheet.main+xml", "xlsx"),
            ("presentationml.presentation.main+xml", "pptx"),
        ] {
            if ct.contains(needle) {
                return kind(ext);
            }
        }
    }
}

pub fn normalize_text(text: &str) -> String {
    let text = text
        .replace('\u{a0}', " ")
        .replace("\r\n", "\n")
        .replace('\r', "\n");
    let text = textnorm::dehyphenate(&text);
    let space = regex::Regex::new(r"[ \t\r\x0c\x0b]+").unwrap();
    let lines = text
        .split('\n')
        .map(|l| space.replace_all(l, " ").trim().to_owned())
        .collect::<Vec<_>>()
        .join("\n");
    regex::Regex::new(r"\n{3,}")
        .unwrap()
        .replace_all(&lines, "\n\n")
        .trim()
        .to_owned()
}
pub fn html_to_text(text: &str) -> String {
    let mut s = text.to_owned();
    for (pattern, replacement) in [
        (r"(?is)<script[^>]*>.*?</script>", " "),
        (r"(?is)<style[^>]*>.*?</style>", " "),
        (r"(?i)<br[\t\n\x0c\r ]*/?>", "\n"),
        (r"(?i)</(p|div|li|h[1-6]|tr)>", "\n"),
        (r"(?i)</t[dh]>", "\t"),
        (r"(?s)<[^>]+>", " "),
    ] {
        s = regex::Regex::new(pattern)
            .unwrap()
            .replace_all(&s, replacement)
            .into_owned();
    }
    crate::ghtml::unescape(&s)
}
fn rtf_text(data: &[u8]) -> Vec<u8> {
    let (mut ignored, mut depth, mut i) = (0, 0, 0);
    let mut out = Vec::new();
    while i < data.len() {
        match data[i] {
            b'{' => {
                depth += 1;
                if [
                    b"\\fonttbl".as_slice(),
                    b"\\colortbl",
                    b"\\stylesheet",
                    b"\\*",
                ]
                .iter()
                .any(|p| data[i + 1..].starts_with(p))
                {
                    ignored = depth;
                }
            }
            b'}' => {
                if ignored == depth {
                    ignored = 0;
                }
                if depth > 0 {
                    depth -= 1;
                }
            }
            b'\\' => {
                let start = i + 1;
                if start == data.len() {
                    i += 1;
                    continue;
                }
                if matches!(data[start], b'\\' | b'{' | b'}') {
                    if ignored == 0 {
                        out.push(data[start]);
                    }
                    i += 2;
                    continue;
                }
                if data[start] == b'\'' && start + 2 < data.len() {
                    if ignored == 0 {
                        let s = String::from_utf8_lossy(&data[start + 1..start + 3]);
                        out.push(u8::from_str_radix(&s, 16).unwrap_or(0));
                    }
                    i += 4;
                    continue;
                }
                let mut j = start;
                while j < data.len() && data[j].is_ascii_alphabetic() {
                    j += 1;
                }
                if ignored == 0 {
                    match &data[start..j] {
                        b"par" | b"line" | b"page" => out.push(b'\n'),
                        b"tab" => out.push(b' '),
                        _ => {}
                    }
                }
                while j < data.len() && (data[j] == b'-' || data[j].is_ascii_digit()) {
                    j += 1;
                }
                if data.get(j) == Some(&b' ') {
                    j += 1;
                }
                i = j;
                continue;
            }
            c => {
                if ignored == 0 && !matches!(c, b'\r' | b'\n') {
                    out.push(c);
                }
            }
        }
        i += 1;
    }
    out
}
/// Byte input is bounded by the caller, and archive decompression has fixed caps.
/// PDF/images are intentionally left to INGEST-003's engine; this API never OCRs.
pub fn extract_bytes(data: &[u8], mime: &str, limit: usize) -> Result<Extraction, String> {
    if unsupported(mime) {
        return Err(formats::unsupported_format_error(mime));
    }
    if data.len() > limit {
        return Err(format!(
            "extract: source exceeds the caller byte limit of {limit} bytes"
        ));
    }
    let text = String::from_utf8_lossy(data);
    let (text, engine) = match mime {
        "text/plain" | "text/csv" | "text/markdown" | "" => (text.into_owned(), "text"),
        "text/html" => (normalize_text(&html_to_text(&text)), "html"),
        "application/rtf" => (
            normalize_text(&String::from_utf8_lossy(&rtf_text(data))),
            "rtf",
        ),
        "message/rfc822" => (normalize_text(&structured::read_eml(data)?), "eml-native"),
        k => {
            let Some(ext) = ["docx", "xlsx", "pptx", "odt", "ods", "odp", "epub"]
                .into_iter()
                .find(|e| kind(e) == k)
            else {
                return Err(format!(
                    "ingest Rust extraction not yet ported for kind {mime:?}"
                ));
            };
            (
                normalize_text(&archive_text(data, ext)?),
                match ext {
                    "docx" => "docx-native",
                    "xlsx" => "xlsx-native",
                    "odt" => "odt-native",
                    "ods" => "ods-native",
                    "odp" => "odp-native",
                    "pptx" => "pptx-native",
                    _ => "epub-native",
                },
            )
        }
    };
    Ok(Extraction {
        text,
        mime: if mime.is_empty() {
            "text/plain".into()
        } else {
            mime.into()
        },
        engine: engine.into(),
        language: String::new(),
    })
}
fn append_unit(out: &mut String, text: String) {
    if !text.trim().is_empty() {
        if !out.is_empty() {
            out.push_str("\n\n");
        }
        out.push_str(&text);
    }
}
fn archive_text(data: &[u8], ext: &str) -> Result<String, String> {
    let mut a = Archive::open(data).map_err(|e| format!("open {ext}: {e}"))?;
    match ext {
        "docx" => {
            let d = a
                .part("word/document.xml")?
                .ok_or_else(|| "docx missing word/document.xml".to_string())?;
            structured::word_xml_to_text(&d)
        }
        "odt" | "ods" | "odp" => {
            let d = a
                .part("content.xml")?
                .ok_or_else(|| format!("{ext} missing content.xml"))?;
            structured::odf_xml_to_text(&d)
        }
        "xlsx" => {
            let shared = match a.part("xl/sharedStrings.xml")? {
                Some(d) => structured::read_shared_strings(&d)?,
                None => Vec::new(),
            };
            let workbook = a.part("xl/workbook.xml")?;
            let rels = if workbook.is_some() {
                a.part("xl/_rels/workbook.xml.rels")?
            } else {
                None
            };
            let sheet_names = structured::workbook_sheet_names(workbook.as_deref(), rels.as_deref())?;
            let mut names = a
                .names
                .iter()
                .filter(|n| n.starts_with("xl/worksheets/sheet") && n.ends_with(".xml"))
                .cloned()
                .collect::<Vec<_>>();
            names.sort();
            let mut out = String::new();
            for name in names {
                let d = a.part(&name)?.unwrap_or_default();
                let text = structured::sheet_xml_to_text(&d, &shared)?;
                if !text.trim().is_empty() {
                    if !out.is_empty() {
                        out.push_str("\n\n");
                    }
                    let base = structured::go_base(&name);
                    out.push_str(sheet_names.get(&base).map_or(base.as_str(), String::as_str));
                    out.push('\n');
                    out.push_str(&text);
                }
            }
            Ok(out)
        }
        _ => {
            if ext == "epub"
                && a.names
                    .iter()
                    .any(|n| n == "META-INF/rights.xml" || n == "META-INF/encryption.xml")
            {
                return Err(
                    "document is DRM-protected: EPUB rights or encryption metadata is present"
                        .into(),
                );
            }
            let mut names = a
                .names
                .iter()
                .filter(|n| {
                    if ext == "pptx" {
                        n.starts_with("ppt/slides/slide") && n.ends_with(".xml")
                    } else {
                        !n.starts_with("META-INF/")
                            && !n.ends_with("toc.ncx")
                            && [".html", ".htm", ".xhtml"].contains(&extension(n).as_str())
                    }
                })
                .cloned()
                .collect::<Vec<_>>();
            names.sort();
            let mut out = String::new();
            for name in names {
                let d = a.part(&name)?.unwrap_or_default();
                let text = if ext == "epub" {
                    html_to_text(&String::from_utf8_lossy(&d))
                } else {
                    structured::pptx_xml_to_text(&d)?
                };
                append_unit(&mut out, text);
            }
            Ok(out)
        }
    }
}
