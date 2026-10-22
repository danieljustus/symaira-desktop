//! Contract test for the archive text stamp against the Go oracle
//! (`testdata/port/render/archive-stamp.json`, issue #1142).
//!
//! The Rust side measures the output the way the Go oracle does: Poppler's
//! `pdftotext` runs on every output page and the sorted letters and digits it
//! recovers are compared with what Go recorded. Validity comes from a real
//! parse of the output plus a `pdftoppm` render of every page that must not
//! print a syntax error. Structure is asserted on top: the stamp font, the
//! original `/Contents` streams and resources, and untouched pages.
//!
//! Poppler is required. Where it is missing (only Windows CI has none) the
//! `pdftotext`/`pdftoppm` measurements are skipped with a note and the
//! structural checks still run; any other platform fails with a clear
//! message instead of passing without measuring.

use std::path::{Path, PathBuf};
use std::process::Command;
use std::sync::atomic::{AtomicUsize, Ordering};

use base64::Engine as _;
use lopdf::{Dictionary, Document, Object, ObjectId, Stream, dictionary};
use symdesk_render::archive::split_pages;
use symdesk_render::stamp::stamp_pdf;

#[derive(serde::Deserialize)]
#[serde(deny_unknown_fields)]
struct Fixture {
    schema_version: u32,
    oracle: Oracle,
    cases: Vec<Case>,
}

#[derive(serde::Deserialize)]
#[serde(deny_unknown_fields)]
struct Oracle {
    commit: String,
    go_version: String,
    source: String,
    source_sha256: String,
}

#[derive(serde::Deserialize)]
#[serde(deny_unknown_fields)]
struct Case {
    id: String,
    input_pdf: String,
    ocr_text: String,
    want: Want,
}

#[derive(serde::Deserialize)]
#[serde(deny_unknown_fields)]
struct Want {
    ok: bool,
    error_kind: String,
    page_count: usize,
    valid: bool,
    pages: Option<Vec<WantPage>>,
}

#[derive(serde::Deserialize)]
#[serde(deny_unknown_fields)]
struct WantPage {
    width: f64,
    height: f64,
    stamped: bool,
    text: String,
}

// ---- Poppler ---------------------------------------------------------------

/// True when Poppler is on PATH. Missing Poppler is a failure everywhere but
/// Windows, where CI installs none and the measurement is skipped.
fn poppler() -> bool {
    let present = ["pdftotext", "pdftoppm"]
        .iter()
        .all(|tool| Command::new(tool).arg("-v").output().is_ok());
    if present {
        return true;
    }
    if !cfg!(windows) {
        panic!(
            "pdftotext/pdftoppm (Poppler) are required on PATH for the archive stamp \
             contract; install poppler-utils (Linux) or poppler (Homebrew)"
        );
    }
    eprintln!("note: Poppler missing on Windows, skipping pdftotext/pdftoppm measurements");
    false
}

struct TempDir(PathBuf);

impl TempDir {
    fn new() -> Self {
        static NEXT: AtomicUsize = AtomicUsize::new(0);
        let path = std::env::temp_dir().join(format!(
            "symdesk-stamp-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        std::fs::create_dir_all(&path).expect("create temp dir");
        Self(path)
    }

    fn write(&self, name: &str, bytes: &[u8]) -> PathBuf {
        let path = self.0.join(name);
        std::fs::write(&path, bytes).expect("write temp file");
        path
    }
}

impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = std::fs::remove_dir_all(&self.0);
    }
}

fn pdftotext_page(path: &Path, page: usize) -> String {
    let output = Command::new("pdftotext")
        .args(["-f", &page.to_string(), "-l", &page.to_string()])
        .arg(path)
        .arg("-")
        .output()
        .expect("run pdftotext");
    assert!(
        output.status.success(),
        "pdftotext page {page}: {}",
        String::from_utf8_lossy(&output.stderr)
    );
    String::from_utf8_lossy(&output.stdout).into_owned()
}

/// The page's letters and digits in byte order, the Go oracle's `sortedAlnum`.
fn sorted_alnum(text: &str) -> String {
    let mut chars: Vec<char> = text.chars().filter(char::is_ascii_alphanumeric).collect();
    chars.sort_unstable();
    chars.into_iter().collect()
}

/// The Go oracle's `archiveStampPresent`: every letter and digit of the
/// stamp text occurs at least as often in the recovered page text.
fn stamp_present(page_text: &str, stamp_text: &str) -> bool {
    let stamp = sorted_alnum(stamp_text);
    if stamp.is_empty() {
        return false;
    }
    let mut found = sorted_alnum(page_text).into_bytes();
    for byte in stamp.bytes() {
        match found.iter().position(|candidate| *candidate == byte) {
            Some(index) => {
                found.swap_remove(index);
            }
            None => return false,
        }
    }
    true
}

/// Render every page. Poppler prints `Syntax Error` or `Error` to stderr for
/// a broken page tree, an unknown resource or a bad content stream.
fn render_clean(path: &Path, pages: usize) -> Result<(), String> {
    let dir = TempDir::new();
    for page in 1..=pages {
        let output = Command::new("pdftoppm")
            .args(["-r", "10", "-f", &page.to_string(), "-l", &page.to_string()])
            .arg("-png")
            .arg(path)
            .arg(dir.0.join("page"))
            .output()
            .map_err(|error| format!("run pdftoppm: {error}"))?;
        let stderr = String::from_utf8_lossy(&output.stderr);
        if !output.status.success() || !stderr.trim().is_empty() {
            return Err(format!("pdftoppm page {page}: {}", stderr.trim()));
        }
    }
    Ok(())
}

// ---- PDF structure, measured independently of the writer -------------------

/// An inheritable page attribute by walking `/Parent` with a visited set.
fn inherited(doc: &Document, page: ObjectId, key: &[u8]) -> Option<Object> {
    let mut seen = Vec::new();
    let mut node = page;
    while !seen.contains(&node) && seen.len() < 64 {
        seen.push(node);
        let dictionary = doc.get_dictionary(node).ok()?;
        if let Ok(value) = dictionary.get(key) {
            return doc.dereference(value).ok().map(|(_, value)| value.clone());
        }
        node = dictionary.get(b"Parent").ok()?.as_reference().ok()?;
    }
    None
}

fn effective_resources(doc: &Document, page: ObjectId) -> Dictionary {
    match inherited(doc, page, b"Resources") {
        Some(Object::Dictionary(dictionary)) => dictionary,
        _ => Dictionary::new(),
    }
}

/// Semantic object equality. lopdf rewrites an integral real such as `8.0` as
/// `8`, so numbers compare by value; everything else compares structurally,
/// stream bytes included.
fn same(left: &Object, right: &Object) -> bool {
    let number = |object: &Object| match object {
        Object::Integer(value) => Some(*value as f64),
        Object::Real(value) => Some(f64::from(*value)),
        _ => None,
    };
    if let (Some(left), Some(right)) = (number(left), number(right)) {
        return left == right;
    }
    match (left, right) {
        (Object::Array(left), Object::Array(right)) => {
            left.len() == right.len() && left.iter().zip(right).all(|(l, r)| same(l, r))
        }
        (Object::Dictionary(left), Object::Dictionary(right)) => same_dict(left, right),
        (Object::Stream(left), Object::Stream(right)) => {
            left.content == right.content && same_dict(&left.dict, &right.dict)
        }
        _ => left == right,
    }
}

fn same_dict(left: &Dictionary, right: &Dictionary) -> bool {
    left.len() == right.len()
        && left
            .iter()
            .all(|(key, value)| right.get(key).is_ok_and(|other| same(value, other)))
}

fn dict_of(doc: &Document, object: &Object) -> Option<Dictionary> {
    doc.dereference(object).ok()?.1.as_dict().ok().cloned()
}

fn media_size(doc: &Document, page: ObjectId) -> (f64, f64) {
    let Some(Object::Array(values)) = inherited(doc, page, b"MediaBox") else {
        panic!("page has no MediaBox");
    };
    let numbers: Vec<f64> = values
        .iter()
        .map(|value| {
            value
                .as_float()
                .map(f64::from)
                .or_else(|_| value.as_i64().map(|v| v as f64))
                .expect("numeric MediaBox entry")
        })
        .collect();
    (numbers[2] - numbers[0], numbers[3] - numbers[1])
}

/// A real structural parse: every page has a MediaBox, resolvable and
/// decodable content streams, a dictionary `/Resources` and fonts that
/// resolve to dictionaries.
fn structurally_valid(doc: &Document) -> Result<(), String> {
    let pages = doc.get_pages();
    if pages.is_empty() {
        return Err("no pages".to_owned());
    }
    for (number, page) in pages {
        if !matches!(inherited(doc, page, b"MediaBox"), Some(Object::Array(_))) {
            return Err(format!("page {number}: no MediaBox"));
        }
        for stream_id in doc.get_page_contents(page) {
            let stream = doc
                .get_object(stream_id)
                .and_then(Object::as_stream)
                .map_err(|error| format!("page {number}: content {stream_id:?}: {error}"))?;
            stream
                .decompressed_content()
                .map_err(|error| format!("page {number}: content {stream_id:?}: {error}"))?;
        }
        let resources = effective_resources(doc, page);
        if let Ok(fonts) = resources.get(b"Font") {
            let fonts =
                dict_of(doc, fonts).ok_or_else(|| format!("page {number}: /Font not a dict"))?;
            for (name, font) in fonts.iter() {
                dict_of(doc, font).ok_or_else(|| {
                    format!(
                        "page {number}: font {} unresolved",
                        String::from_utf8_lossy(name)
                    )
                })?;
            }
        }
    }
    Ok(())
}

/// Which pages of the output must carry a stamp: a page with its own OCR
/// text, or every page when OCR text exists for pages the PDF lacks.
fn expected_stamped(ocr_text: &str, page_count: usize) -> Vec<bool> {
    let texts = split_pages(ocr_text);
    let overflow = texts
        .iter()
        .skip(page_count)
        .any(|text| !text.trim().is_empty());
    (0..page_count)
        .map(|index| overflow || texts.get(index).is_some_and(|text| !text.trim().is_empty()))
        .collect()
}

/// Structural contract of a stamped output against its input.
fn assert_structure(id: &str, input: &Document, output: &Document, stamped: &[bool]) {
    let in_pages: Vec<ObjectId> = input.get_pages().into_values().collect();
    let out_pages: Vec<ObjectId> = output.get_pages().into_values().collect();
    assert_eq!(in_pages.len(), out_pages.len(), "case {id}: page count");
    for (index, (&in_page, &out_page)) in in_pages.iter().zip(&out_pages).enumerate() {
        let label = format!("case {id} page {}", index + 1);
        let in_contents = input.get_page_contents(in_page);
        let out_contents = output.get_page_contents(out_page);
        // Every original content stream survives byte for byte.
        for stream_id in &in_contents {
            assert!(
                same(
                    input.get_object(*stream_id).unwrap(),
                    output.get_object(*stream_id).unwrap()
                ),
                "{label}: original content stream {stream_id:?} changed"
            );
        }
        if !stamped[index] {
            assert!(
                same_dict(
                    input.get_dictionary(in_page).unwrap(),
                    output.get_dictionary(out_page).unwrap()
                ),
                "{label}: unstamped page dictionary changed"
            );
            continue;
        }
        // Stamp streams first, original streams after them in order (z-order).
        assert!(
            out_contents.len() > in_contents.len(),
            "{label}: no stamp stream added"
        );
        let split = out_contents.len() - in_contents.len();
        assert_eq!(
            out_contents[split..],
            in_contents[..],
            "{label}: original /Contents not preserved after the stamp"
        );
        let in_resources = effective_resources(input, in_page);
        let out_resources = effective_resources(output, out_page);
        for (key, value) in in_resources.iter() {
            if key == b"Font" {
                continue;
            }
            assert!(
                out_resources.get(key).is_ok_and(|out| same(out, value)),
                "{label}: resource /{} lost",
                String::from_utf8_lossy(key)
            );
        }
        let in_fonts = in_resources
            .get(b"Font")
            .ok()
            .and_then(|fonts| dict_of(input, fonts))
            .unwrap_or_default();
        let out_fonts = out_resources
            .get(b"Font")
            .ok()
            .and_then(|fonts| dict_of(output, fonts))
            .unwrap_or_else(|| panic!("{label}: /Font resources missing"));
        for (name, font) in in_fonts.iter() {
            assert!(
                out_fonts.get(name).is_ok_and(|out| same(out, font)),
                "{label}: font {} lost",
                String::from_utf8_lossy(name)
            );
        }
        let stamp_fonts: Vec<(&Vec<u8>, &Object)> = out_fonts
            .iter()
            .filter(|(name, _)| name.starts_with(b"ArchiveStampFont"))
            .collect();
        assert!(!stamp_fonts.is_empty(), "{label}: ArchiveStampFont missing");
        for (name, font) in stamp_fonts {
            let font = dict_of(output, font).expect("stamp font resolves");
            assert_eq!(
                font.get(b"BaseFont").and_then(Object::as_name).ok(),
                Some(&b"Helvetica"[..]),
                "{label}: stamp font is not Helvetica"
            );
            let used = out_contents[..split].iter().any(|stream_id| {
                let stream = output.get_object(*stream_id).unwrap().as_stream().unwrap();
                let content = stream.decompressed_content().unwrap();
                let tf = format!("/{} ", String::from_utf8_lossy(name));
                content
                    .windows(tf.len())
                    .any(|window| window == tf.as_bytes())
            });
            assert!(used, "{label}: stamp content does not select its font");
        }
    }
}

// ---- Go oracle fixture ------------------------------------------------------

#[test]
fn go_archive_stamp_fixture_matches_semantics() {
    let fixture: Fixture = serde_json::from_str(include_str!(
        "../../../testdata/port/render/archive-stamp.json"
    ))
    .expect("decode Go-generated archive stamp fixture");
    let provenance: serde_json::Value =
        serde_json::from_str(include_str!("../../../testdata/port/provenance.json"))
            .expect("decode central port provenance");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(
        Some(fixture.oracle.commit.as_str()),
        provenance["oracle"]["commit"].as_str()
    );
    assert_eq!(fixture.oracle.go_version, "go1.26.10");
    assert_eq!(fixture.oracle.source, "internal/archive/archive.go");
    assert_eq!(fixture.oracle.source_sha256.len(), 64);
    let measure = poppler();
    let mut compared = 0;
    for case in &fixture.cases {
        let input = base64::engine::general_purpose::STANDARD
            .decode(&case.input_pdf)
            .expect("decode fixture input");
        let output = match stamp_pdf(&input, &case.ocr_text) {
            Err(error) => {
                assert!(!case.want.ok, "case {} failed: {error}", case.id);
                let kind = error.split(':').next().unwrap_or_default();
                assert_eq!(kind, case.want.error_kind, "case {}", case.id);
                continue;
            }
            Ok(output) => output,
        };
        assert!(case.want.ok, "case {} unexpectedly stamped", case.id);
        let input_doc = Document::load_mem(&input).expect("fixture input parses");
        let output_doc = Document::load_mem(&output)
            .unwrap_or_else(|error| panic!("case {} output does not parse: {error}", case.id));
        let pages: Vec<ObjectId> = output_doc.get_pages().into_values().collect();
        assert_eq!(pages.len(), case.want.page_count, "case {}", case.id);
        let want_pages = case.want.pages.as_deref().unwrap_or_default();
        assert_eq!(pages.len(), want_pages.len(), "case {}", case.id);

        let stamped = expected_stamped(&case.ocr_text, pages.len());
        assert_structure(&case.id, &input_doc, &output_doc, &stamped);

        let path = TempDir::new();
        let file = path.write("output.pdf", &output);
        let valid = structurally_valid(&output_doc).is_ok()
            && (!measure || render_clean(&file, pages.len()).is_ok());
        assert_eq!(valid, case.want.valid, "case {} validity", case.id);

        let texts = split_pages(&case.ocr_text);
        for (index, (&page, want)) in pages.iter().zip(want_pages).enumerate() {
            let number = index + 1;
            let (width, height) = media_size(&output_doc, page);
            assert_eq!(
                (width, height),
                (want.width, want.height),
                "case {} page {number} MediaBox",
                case.id
            );
            if !measure {
                continue;
            }
            let recovered = pdftotext_page(&file, number);
            let own_text = texts.get(index).map_or("", |text| text.trim());
            assert_eq!(
                stamp_present(&recovered, own_text),
                want.stamped,
                "case {} page {number} stamped",
                case.id
            );
            assert_eq!(
                sorted_alnum(&recovered),
                want.text,
                "case {} page {number} recovered text",
                case.id
            );
        }
        compared += 1;
    }
    assert!(compared >= 10, "only {compared} stamped cases compared");
}

// ---- Resource preservation regressions (#1310 F1) --------------------------

#[derive(Clone, Copy, Debug)]
enum ResourcesAt {
    /// `/Resources` is a direct dictionary on the page.
    PageDirect,
    /// `/Resources` is an indirect object referenced from the page.
    PageIndirect,
    /// No page `/Resources`; the `/Pages` node holds an indirect one.
    InheritedIndirect,
    /// No page `/Resources`; the `/Pages` node holds a direct one.
    InheritedDirect,
}

const ORIGINAL_TEXT: &str = "orig";

/// A one-page PDF drawing image `Im0` and the text `orig` in font `F1`.
fn build_pdf(at: ResourcesAt, contents_as_array: bool) -> Vec<u8> {
    let mut doc = Document::with_version("1.5");
    let pages_id = doc.new_object_id();
    let image = doc.add_object(Stream::new(
        dictionary! {
            "Type" => "XObject",
            "Subtype" => "Image",
            "Width" => 1,
            "Height" => 1,
            "ColorSpace" => "DeviceGray",
            "BitsPerComponent" => 8,
        },
        vec![0x40],
    ));
    let font = doc.add_object(dictionary! {
        "Type" => "Font",
        "Subtype" => "Type1",
        "BaseFont" => "Helvetica",
    });
    let resources = dictionary! {
        "Font" => dictionary! { "F1" => Object::Reference(font) },
        "XObject" => dictionary! { "Im0" => Object::Reference(image) },
    };
    let content =
        format!("q 100 0 0 100 10 10 cm /Im0 Do Q BT /F1 20 Tf 20 120 Td ({ORIGINAL_TEXT}) Tj ET");
    let content_id = doc.add_object(Stream::new(Dictionary::new(), content.into_bytes()));
    let mut page = dictionary! {
        "Type" => "Page",
        "Parent" => Object::Reference(pages_id),
        "Contents" => if contents_as_array {
            Object::Array(vec![Object::Reference(content_id)])
        } else {
            Object::Reference(content_id)
        },
    };
    let mut pages = dictionary! {
        "Type" => "Pages",
        "Kids" => Object::Array(vec![]),
        "Count" => 1,
        "MediaBox" => Object::Array(vec![0.into(), 0.into(), 200.into(), 200.into()]),
    };
    match at {
        ResourcesAt::PageDirect => page.set("Resources", Object::Dictionary(resources)),
        ResourcesAt::PageIndirect => {
            let id = doc.add_object(resources);
            page.set("Resources", Object::Reference(id));
        }
        ResourcesAt::InheritedIndirect => {
            let id = doc.add_object(resources);
            pages.set("Resources", Object::Reference(id));
        }
        ResourcesAt::InheritedDirect => pages.set("Resources", Object::Dictionary(resources)),
    }
    let page_id = doc.add_object(page);
    pages.set("Kids", Object::Array(vec![Object::Reference(page_id)]));
    doc.objects.insert(pages_id, Object::Dictionary(pages));
    let catalog = doc.add_object(dictionary! {
        "Type" => "Catalog",
        "Pages" => Object::Reference(pages_id),
    });
    doc.trailer.set("Root", Object::Reference(catalog));
    let mut out = Vec::new();
    doc.save_to(&mut out).expect("serialize test PDF");
    out
}

#[test]
fn stamp_keeps_original_resources_for_every_resources_layout() {
    let measure = poppler();
    for at in [
        ResourcesAt::PageDirect,
        ResourcesAt::PageIndirect,
        ResourcesAt::InheritedIndirect,
        ResourcesAt::InheritedDirect,
    ] {
        for contents_as_array in [false, true] {
            let id = format!("{at:?}/array={contents_as_array}");
            let input = build_pdf(at, contents_as_array);
            let output = stamp_pdf(&input, "stamp text").unwrap_or_else(|e| panic!("{id}: {e}"));
            let input_doc = Document::load_mem(&input).unwrap();
            let output_doc = Document::load_mem(&output).unwrap();
            assert_structure(&id, &input_doc, &output_doc, &[true]);
            let page = *output_doc.get_pages().values().next().unwrap();
            let resources = effective_resources(&output_doc, page);
            assert!(
                dict_of(&output_doc, resources.get(b"XObject").unwrap())
                    .unwrap()
                    .has(b"Im0"),
                "{id}: XObject Im0 lost"
            );
            assert!(
                structurally_valid(&output_doc).is_ok(),
                "{id}: structure invalid"
            );
            if !measure {
                continue;
            }
            let dir = TempDir::new();
            let file = dir.write("out.pdf", &output);
            render_clean(&file, 1).unwrap_or_else(|e| panic!("{id}: {e}"));
            let text = pdftotext_page(&file, 1);
            // The original text and the stamp text are both recovered.
            assert_eq!(
                sorted_alnum(&text),
                sorted_alnum(&format!("{ORIGINAL_TEXT}stamptext")),
                "{id}: recovered text {text:?}"
            );
        }
    }
}

#[test]
fn stamp_without_new_text_leaves_the_pdf_bytes_alone() {
    let input = build_pdf(ResourcesAt::PageIndirect, false);
    assert_eq!(stamp_pdf(&input, "  \n ").unwrap(), input);
}

#[test]
fn stamp_stays_under_the_page_content() {
    let input = build_pdf(ResourcesAt::PageDirect, false);
    let output = stamp_pdf(&input, "under").unwrap();
    let doc = Document::load_mem(&output).unwrap();
    let page = *doc.get_pages().values().next().unwrap();
    let contents = doc.get_page_contents(page);
    assert_eq!(contents.len(), 2);
    let first = doc
        .get_object(contents[0])
        .unwrap()
        .as_stream()
        .unwrap()
        .decompressed_content()
        .unwrap();
    assert!(String::from_utf8_lossy(&first).contains("ArchiveStampFont"));
}

#[test]
fn stamp_uses_a_numbered_font_name_when_the_page_owns_the_stamp_name() {
    let mut doc = Document::load_mem(&build_pdf(ResourcesAt::PageDirect, false)).unwrap();
    let page = *doc.get_pages().values().next().unwrap();
    let foreign = doc.add_object(dictionary! {
        "Type" => "Font", "Subtype" => "Type1", "BaseFont" => "Courier",
    });
    let mut resources = effective_resources(&doc, page);
    let mut fonts = dict_of(&doc, resources.get(b"Font").unwrap()).unwrap();
    fonts.set("ArchiveStampFont", Object::Reference(foreign));
    resources.set("Font", Object::Dictionary(fonts));
    doc.get_dictionary_mut(page)
        .unwrap()
        .set("Resources", Object::Dictionary(resources));
    let mut input = Vec::new();
    doc.save_to(&mut input).unwrap();

    let output = stamp_pdf(&input, "name").unwrap();
    let out = Document::load_mem(&output).unwrap();
    let page = *out.get_pages().values().next().unwrap();
    let fonts = dict_of(&out, effective_resources(&out, page).get(b"Font").unwrap()).unwrap();
    assert_eq!(
        fonts.get(b"ArchiveStampFont").ok(),
        Some(&Object::Reference(foreign)),
        "the page's own font keeps its name"
    );
    assert!(fonts.has(b"ArchiveStampFont1"), "stamp font renamed");
}

#[test]
fn stamp_ends_on_a_looping_parent_chain_instead_of_hanging() {
    let mut doc = Document::load_mem(&build_pdf(ResourcesAt::InheritedDirect, false)).unwrap();
    let page = *doc.get_pages().values().next().unwrap();
    doc.get_dictionary_mut(page)
        .unwrap()
        .set("Parent", Object::Reference(page));
    let mut input = Vec::new();
    doc.save_to(&mut input).unwrap();
    let error = stamp_pdf(&input, "loop").unwrap_err();
    assert!(
        error.starts_with("add watermark to page 1:") && error.contains("loops"),
        "unexpected error: {error}"
    );
}
