//! Ingest detection and local extraction. No OCR, barcode, subprocess or network.
//! Ingest and retrieval have different whitespace/XML/table/limit contracts;
//! retrieval's parsers therefore cannot be substituted for these readers.
use quick_xml::{Reader, events::Event};
use std::io::{Cursor, Read};
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
    zip: zip::ZipArchive<Cursor<&'a [u8]>>,
    names: Vec<String>,
    total: usize,
}
impl<'a> Archive<'a> {
    fn open(data: &'a [u8]) -> Result<Self, String> {
        let mut zip = zip::ZipArchive::new(Cursor::new(data))
            .map_err(|_| "open archive: zip: not a valid zip file".to_string())?;
        if zip.len() > MAX_ZIP_ENTRIES {
            return Err(format!(
                "extract: archive limit exceeded: {} entries exceed the cap of {MAX_ZIP_ENTRIES}",
                zip.len()
            ));
        }
        let names = (0..zip.len())
            .map(|i| {
                zip.by_index(i)
                    .and_then(|f| f.name().map(|name| name.into_owned()))
                    .map_err(|e| e.to_string())
            })
            .collect::<Result<_, _>>()?;
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
        let mut file = self
            .zip
            .by_index(i)
            .map_err(|e| format!("open part {name:?}: {e}"))?;
        let mut data = Vec::new();
        file.by_ref()
            .take((MAX_ZIP_ENTRY_BYTES + 1) as u64)
            .read_to_end(&mut data)
            .map_err(|e| format!("read part {name:?}: {e}"))?;
        if data.len() > MAX_ZIP_ENTRY_BYTES {
            return Err(format!(
                "extract: archive limit exceeded: part {name:?} exceeds the per-entry cap of {MAX_ZIP_ENTRY_BYTES} bytes"
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
        for ext in ["odt", "ods", "odp", "epub"] {
            if m.trim() == kind(ext) {
                return kind(ext);
            }
        }
    }
    for (part, elem) in [
        ("_rels/.rels", b"Relationship".as_slice()),
        ("[Content_Types].xml", b"Override".as_slice()),
    ] {
        if let Ok(Some(data)) = a.part(part) {
            let mut reader = Reader::from_reader(data.as_slice());
            loop {
                match reader.read_event() {
                    Ok(Event::Start(e) | Event::Empty(e)) if e.local_name().as_ref() == elem => {
                        let attrs = e
                            .attributes()
                            .flatten()
                            .filter_map(|a| {
                                Some((
                                    String::from_utf8_lossy(a.key.local_name().as_ref())
                                        .into_owned(),
                                    quick_xml::escape::unescape(std::str::from_utf8(&a.value).ok()?)
                                        .ok()?
                                        .into_owned(),
                                ))
                            })
                            .collect::<std::collections::HashMap<_, _>>();
                        if elem == b"Relationship" {
                            if attrs
                                .get("Type")
                                .is_some_and(|t| t.ends_with("/officeDocument"))
                                && let Some(target) = attrs.get("Target")
                            {
                                let target = target.strip_prefix('/').unwrap_or(target);
                                for (p, ext) in [
                                    ("word/document.xml", "docx"),
                                    ("xl/workbook.xml", "xlsx"),
                                    ("ppt/presentation.xml", "pptx"),
                                ] {
                                    if p == target {
                                        return kind(ext);
                                    }
                                }
                                break;
                            }
                        } else if let Some(ct) = attrs.get("ContentType") {
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
                    Ok(Event::Eof) | Err(_) => break,
                    _ => {}
                }
            }
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
fn html_text(text: &str) -> String {
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
    // Decode HTML entities without interpreting remaining literal '<' as markup.
    let fragment = scraper::Html::parse_fragment(&s.replace('<', "&lt;"));
    fragment.root_element().text().collect()
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
/// DOCX/XLSX/EML readers are not yet ported and return an explicit residual error.
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
        "text/html" => (normalize_text(&html_text(&text)), "html"),
        "application/rtf" => (
            normalize_text(&String::from_utf8_lossy(&rtf_text(data))),
            "rtf",
        ),
        k if [
            kind("odt"),
            kind("ods"),
            kind("odp"),
            kind("pptx"),
            kind("epub"),
        ]
        .contains(&k) =>
        {
            let ext = ["odt", "ods", "odp", "pptx", "epub"]
                .into_iter()
                .find(|e| kind(e) == k)
                .unwrap();
            (
                normalize_text(&archive_text(data, ext)?),
                match ext {
                    "odt" => "odt-native",
                    "ods" => "ods-native",
                    "odp" => "odp-native",
                    "pptx" => "pptx-native",
                    _ => "epub-native",
                },
            )
        }
        _ => {
            return Err(format!(
                "ingest Rust extraction not yet ported for kind {mime:?}"
            ));
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
fn archive_text(data: &[u8], ext: &str) -> Result<String, String> {
    let mut a = Archive::open(data).map_err(|e| format!("open {ext}: {e}"))?;
    if ext == "epub"
        && a.names
            .iter()
            .any(|n| n == "META-INF/rights.xml" || n == "META-INF/encryption.xml")
    {
        return Err(
            "document is DRM-protected: EPUB rights or encryption metadata is present".into(),
        );
    }
    if matches!(ext, "odt" | "ods" | "odp") {
        let data = a
            .part("content.xml")?
            .ok_or_else(|| format!("{ext} missing content.xml"))?;
        return xml_text(&data, false);
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
    let mut texts = Vec::new();
    for name in names {
        let data = a.part(&name)?.unwrap_or_default();
        let text = if ext == "epub" {
            html_text(&String::from_utf8_lossy(&data))
        } else {
            xml_text(&data, true)?
        };
        if !text.trim().is_empty() {
            texts.push(text);
        }
    }
    Ok(texts.join("\n\n"))
}
fn xml_text(data: &[u8], pptx: bool) -> Result<String, String> {
    let mut reader = Reader::from_reader(data);
    reader.config_mut().expand_empty_elements = true;
    let (mut out, mut active, mut depth) = (String::new(), false, 0usize);
    let eof_error = |position: usize| {
        let family = if pptx { "pptx" } else { "odf" };
        format!(
            "parse {family} xml: XML syntax error on line {}: unexpected EOF",
            data[..position.min(data.len())]
                .iter()
                .filter(|b| **b == b'\n')
                .count()
                + 1
        )
    };
    loop {
        match reader.read_event() {
            Ok(Event::Start(e)) => {
                depth += 1;
                match e.local_name().as_ref() {
                    b"p" | b"h" => {
                        if !out.is_empty() {
                            out.push('\n');
                        }
                    }
                    b"t" if pptx => active = true,
                    b"tab" if !pptx => out.push('\t'),
                    b"line-break" if !pptx => out.push('\n'),
                    _ => {}
                }
            }
            Ok(Event::End(e)) => {
                depth = depth.saturating_sub(1);
                if pptx && e.local_name().as_ref() == b"t" {
                    active = false;
                }
            }
            Ok(Event::Text(e)) if !pptx || active => {
                out.push_str(&e.xml11_content().map_err(|e| e.to_string())?)
            }
            Ok(Event::CData(e)) if !pptx || active => {
                out.push_str(&e.decode().map_err(|e| e.to_string())?)
            }
            Ok(Event::GeneralRef(e)) if !pptx || active => {
                let entity = format!("&{};", e.decode().map_err(|e| e.to_string())?);
                out.push_str(&quick_xml::escape::unescape(&entity).map_err(|e| e.to_string())?);
            }
            Ok(Event::Eof) => {
                if depth > 0 {
                    return Err(eof_error(data.len()));
                }
                break;
            }
            Err(e) => {
                let family = if pptx { "pptx" } else { "odf" };
                return Err(format!("parse {family} xml: {e}"));
            }
            _ => {}
        }
    }
    Ok(out)
}
