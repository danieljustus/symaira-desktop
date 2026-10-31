//! Markdown note writer (`internal/ingest/internal/writer`): flat sidecar or
//! layout placement, yaml.v3-compatible frontmatter, atomic writes and the
//! in-place update used by reprocessing.

use crate::goos;
use std::collections::BTreeMap;
use std::io::Write;
use std::sync::Mutex;
use symdesk_vault::{render_go_yaml_map, render_go_yaml_string};
use time::OffsetDateTime;

/// Go `time.Time.Format(time.RFC3339Nano)`.
#[must_use]
pub fn rfc3339_nano(t: OffsetDateTime) -> String {
    let mut out = format!(
        "{:04}-{:02}-{:02}T{:02}:{:02}:{:02}",
        t.year(),
        u8::from(t.month()),
        t.day(),
        t.hour(),
        t.minute(),
        t.second()
    );
    let nanos = t.nanosecond();
    if nanos != 0 {
        let frac = format!("{nanos:09}");
        out.push('.');
        out.push_str(frac.trim_end_matches('0'));
    }
    let offset = t.offset().whole_seconds();
    if offset == 0 {
        out.push('Z');
    } else {
        let a = offset.abs();
        out.push_str(&format!(
            "{}{:02}:{:02}",
            if offset < 0 { '-' } else { '+' },
            a / 3600,
            a % 3600 / 60
        ));
    }
    out
}

/// Traceability metadata from a Paperless-ngx document.
#[derive(Clone, Debug, Default, PartialEq)]
pub struct PaperlessMeta {
    pub document_id: i64,
    pub title: String,
    pub created: Option<OffsetDateTime>,
    pub added: Option<OffsetDateTime>,
    pub modified: Option<OffsetDateTime>,
    pub storage_path: String,
    pub original_file_name: String,
    pub archived_file_name: String,
    pub page_count: i64,
    pub url: String,
}

/// Machine-owned frontmatter (Go `writer.Note`).
#[derive(Clone, Debug, Default)]
pub struct Note {
    pub source_path: String,
    pub imported_from: String,
    pub import_run_id: String,
    pub source_uri: String,
    pub download_uri: String,
    pub ingested_at: Option<OffsetDateTime>,
    pub sha256: String,
    pub mime: String,
    pub tags: Vec<String>,
    pub category: String,
    pub correspondent: String,
    pub document_type: String,
    pub ocr_engine: String,
    pub ocr_language: String,
    pub archive_path: String,
    pub paperless: Option<PaperlessMeta>,
}

/// Placement override within the vault (Go `NoteLayout`).
#[derive(Clone, Debug, Default)]
pub struct NoteLayout {
    pub subdir: String,
    pub base_name: String,
}

/// Everything `WriteNote` takes besides the writer and text.
#[derive(Clone, Debug, Default)]
pub struct NoteInput<'a> {
    pub source_path: &'a str,
    pub sha256: &'a str,
    pub mime: &'a str,
    pub ocr_engine: &'a str,
    pub ocr_language: &'a str,
    pub archive_path: &'a str,
    pub ingested_at: Option<OffsetDateTime>,
    pub category: &'a str,
    pub tags: &'a [String],
    pub correspondent: &'a str,
    pub document_type: &'a str,
    pub imported_from: &'a str,
    pub import_run_id: &'a str,
    pub source_uri: &'a str,
    pub download_uri: &'a str,
    pub paperless: Option<&'a PaperlessMeta>,
    pub layout: Option<&'a NoteLayout>,
}

/// Deduplicated Markdown sidecars written into a vault.
pub struct NoteWriter {
    pub vault: String,
    lock: Mutex<()>,
}

/// `<vault>/<base of source>.md`.
#[must_use]
pub fn sidecar_path(vault: &str, source: &str) -> String {
    goos::join(&[vault, &format!("{}.md", goos::base(source))])
}

/// Go `SanitizeStoragePath`.
#[must_use]
pub fn sanitize_storage_path(storage_path: &str) -> String {
    let clean: Vec<String> = storage_path
        .split(['/', '\\'])
        .filter(|s| !s.is_empty())
        .map(sanitize_segment)
        .filter(|s| !s.is_empty() && s != "." && s != "..")
        .collect();
    if clean.is_empty() {
        return String::new();
    }
    goos::join(&clean.iter().map(String::as_str).collect::<Vec<_>>())
}

fn sanitize_segment(seg: &str) -> String {
    let replaced: String = seg
        .trim()
        .chars()
        .map(|c| {
            if (c as u32) < 0x20 || "<>:\"|?*/\\".contains(c) {
                '_'
            } else {
                c
            }
        })
        .collect();
    replaced.trim().trim_matches('.').to_owned()
}

fn file_exists(path: &str) -> bool {
    std::fs::metadata(path).is_ok()
}

/// Go `fsutil.AtomicWriteFile`: temporary file next to the target (mode
/// `perm`), fsync, rename.
pub fn atomic_write_file(path: &str, data: &[u8], perm: u32) -> Result<(), String> {
    use std::os::unix::fs::{OpenOptionsExt, PermissionsExt};
    let dir = goos::dir(path);
    let base = goos::base(path);
    let pid = u128::from(std::process::id());
    let mut seed = std::time::SystemTime::now()
        .duration_since(std::time::UNIX_EPOCH)
        .map_or(0, |d| d.as_nanos())
        ^ (pid << 20);
    let mut attempt = 0;
    let (mut file, tmp) = loop {
        seed = seed
            .wrapping_mul(6_364_136_223_846_793_005)
            .wrapping_add(1_442_695_040_888_963_407);
        let name = goos::join(&[&dir, &format!("{base}.tmp-{}", (seed >> 33) as u32)]);
        match std::fs::OpenOptions::new()
            .write(true)
            .create_new(true)
            .mode(0o600)
            .open(&name)
        {
            Ok(f) => break (f, name),
            Err(e) if e.kind() == std::io::ErrorKind::AlreadyExists && attempt < 10_000 => {
                attempt += 1;
            }
            Err(e) => return Err(goos::path_error("open", &name, &e)),
        }
    };
    let cleanup = |tmp: &str| {
        let _ = std::fs::remove_file(tmp);
    };
    if let Err(e) = file.set_permissions(std::fs::Permissions::from_mode(perm)) {
        cleanup(&tmp);
        return Err(goos::path_error("chmod", &tmp, &e));
    }
    if let Err(e) = file.write_all(data) {
        cleanup(&tmp);
        return Err(goos::path_error("write", &tmp, &e));
    }
    if let Err(e) = file.sync_all() {
        cleanup(&tmp);
        return Err(goos::path_error("sync", &tmp, &e));
    }
    drop(file);
    if let Err(e) = std::fs::rename(&tmp, path) {
        cleanup(&tmp);
        return Err(format!("rename {tmp} {path}: {}", crate::goos::io_text(&e)));
    }
    Ok(())
}

fn scalar(value: &str, indent: usize) -> String {
    render_go_yaml_string(value, indent, false)
        .unwrap_or_else(|_| symdesk_vault::render_go_yaml_double_quoted(value))
}

fn push_str_field(out: &mut String, indent: usize, key: &str, value: &str, omit_empty: bool) {
    if omit_empty && value.is_empty() {
        return;
    }
    out.push_str(&" ".repeat(indent));
    out.push_str(key);
    out.push_str(": ");
    out.push_str(&scalar(value, indent + 4));
    out.push('\n');
}

fn push_time(out: &mut String, indent: usize, key: &str, t: Option<OffsetDateTime>) {
    if let Some(t) = t {
        out.push_str(&format!(
            "{}{key}: {}\n",
            " ".repeat(indent),
            rfc3339_nano(t)
        ));
    }
}

/// yaml.v3 `Marshal(Note)`.
#[must_use]
pub fn marshal_note(n: &Note) -> String {
    let mut out = String::new();
    push_str_field(&mut out, 0, "source_path", &n.source_path, false);
    push_str_field(&mut out, 0, "imported_from", &n.imported_from, true);
    push_str_field(&mut out, 0, "import_run_id", &n.import_run_id, true);
    push_str_field(&mut out, 0, "source_uri", &n.source_uri, true);
    push_str_field(&mut out, 0, "download_uri", &n.download_uri, true);
    push_time(
        &mut out,
        0,
        "ingested_at",
        n.ingested_at.or(Some(OffsetDateTime::UNIX_EPOCH)),
    );
    push_str_field(&mut out, 0, "sha256", &n.sha256, false);
    push_str_field(&mut out, 0, "mime", &n.mime, false);
    if n.tags.is_empty() {
        out.push_str("tags: []\n");
    } else {
        out.push_str("tags:\n");
        for tag in &n.tags {
            out.push_str("    - ");
            out.push_str(&scalar(tag, 8));
            out.push('\n');
        }
    }
    push_str_field(&mut out, 0, "category", &n.category, false);
    push_str_field(&mut out, 0, "correspondent", &n.correspondent, true);
    push_str_field(&mut out, 0, "document_type", &n.document_type, true);
    push_str_field(&mut out, 0, "ocr_engine", &n.ocr_engine, true);
    push_str_field(&mut out, 0, "ocr_language", &n.ocr_language, true);
    push_str_field(&mut out, 0, "archive_path", &n.archive_path, false);
    if let Some(p) = &n.paperless {
        out.push_str("paperless:\n");
        out.push_str(&format!("    document_id: {}\n", p.document_id));
        push_str_field(&mut out, 4, "title", &p.title, true);
        push_time(&mut out, 4, "created", p.created);
        push_time(&mut out, 4, "added", p.added);
        push_time(&mut out, 4, "modified", p.modified);
        push_str_field(&mut out, 4, "storage_path", &p.storage_path, true);
        push_str_field(
            &mut out,
            4,
            "original_file_name",
            &p.original_file_name,
            true,
        );
        push_str_field(
            &mut out,
            4,
            "archived_file_name",
            &p.archived_file_name,
            true,
        );
        if p.page_count != 0 {
            out.push_str(&format!("    page_count: {}\n", p.page_count));
        }
        push_str_field(&mut out, 4, "url", &p.url, true);
    }
    out
}

fn render_body(text: &[u8], archive_path: &str) -> Vec<u8> {
    let mut body = text.to_vec();
    if !body.ends_with(b"\n") {
        body.push(b'\n');
    }
    if !archive_path.is_empty() {
        body.extend_from_slice(b"\n---\n");
        body.extend_from_slice(format!("[Archived Original](file://{archive_path})\n").as_bytes());
    }
    body
}

type Fields = BTreeMap<String, noyalib::Value>;

/// Splits `---\n<yaml>\n---\n<body>` and decodes the mapping; the body keeps
/// its raw bytes.
fn parse_frontmatter(content: &[u8]) -> Result<(Fields, Vec<u8>, Vec<String>), String> {
    if !content.starts_with(b"---\n") {
        return Err("note missing YAML frontmatter".into());
    }
    let end = content[4..]
        .windows(5)
        .position(|w| w == b"\n---\n")
        .ok_or_else(|| "note missing closing frontmatter delimiter".to_owned())?
        + 4;
    let (fields, stamps) = decode_fields(&String::from_utf8_lossy(&content[4..end]))?;
    Ok((fields, content[end + 5..].to_vec(), stamps))
}

fn decode_fields(yaml: &str) -> Result<(Fields, Vec<String>), String> {
    if yaml.trim().is_empty() {
        return Ok((Fields::new(), Vec::new()));
    }
    // yaml.v3 rejects a repeated mapping key; noyalib keeps the last one.
    let mut seen = std::collections::HashSet::new();
    for line in yaml.lines() {
        if line.starts_with([' ', '\t', '-', '#']) {
            continue;
        }
        if let Some((key, _)) = line.split_once(':') {
            let key = key.trim().trim_matches(['"', '\'']);
            if !seen.insert(key.to_owned()) {
                return Err(format!(
                    "parse frontmatter: yaml: unmarshal errors:\n  mapping key {} already defined",
                    symdesk_vault::go_quote(key)
                ));
            }
        }
    }
    let mut fields =
        noyalib::from_str::<Fields>(yaml).map_err(|e| format!("parse frontmatter: yaml: {e}"))?;
    for value in fields.values_mut() {
        untag(value);
    }
    strip_final_block_newline(yaml, &mut fields);
    Ok((fields, unquoted_timestamps(yaml)))
}

/// The frontmatter text handed to the parser ends without a newline; yaml.v3
/// then clips a final block scalar without adding one, noyalib adds it.
fn strip_final_block_newline(yaml: &str, fields: &mut Fields) {
    let lines: Vec<&str> = yaml.lines().collect();
    let Some(head) = lines
        .iter()
        .rposition(|l| !l.starts_with([' ', '\t']) && !l.is_empty())
    else {
        return;
    };
    let Some((key, rest)) = lines[head].split_once(':') else {
        return;
    };
    let indicator = rest.trim();
    let block = indicator.starts_with(['|', '>']) && !indicator.contains(['+', '-']);
    if !block || yaml.ends_with('\n') {
        return;
    }
    let key = key.trim().trim_matches(['"', '\'']);
    if let Some(noyalib::Value::String(text)) = fields.get_mut(key)
        && text.ends_with('\n')
    {
        text.pop();
    }
}

/// yaml.v3 decodes `!!str`/`!!binary` and unknown tags into plain strings.
fn untag(value: &mut noyalib::Value) {
    use noyalib::Value;
    match value {
        Value::Tagged(tagged) => {
            let tag = tagged.tag().to_string();
            let mut inner = tagged.value().clone();
            untag(&mut inner);
            if (tag.ends_with("binary") || tag.ends_with("!binary"))
                && let Value::String(text) = &inner
            {
                use base64::{Engine, engine::general_purpose::STANDARD};
                let compact: String = text.split_whitespace().collect();
                if let Ok(bytes) = STANDARD.decode(compact) {
                    inner = Value::String(symdesk_core::gofmt::lossy(&bytes));
                }
            }
            *value = inner;
        }
        Value::Sequence(items) => items.iter_mut().for_each(untag),
        Value::Mapping(map) => map.iter_mut().for_each(|(_, v)| untag(v)),
        _ => {}
    }
}

/// Plain (unquoted) scalars yaml.v3 resolves to `time.Time`, keyed by their
/// source text, with the RFC 3339 text Go marshals them back to.
fn unquoted_timestamps(yaml: &str) -> Vec<String> {
    let mut out = Vec::new();
    for line in yaml.lines() {
        let body = line.trim_start().trim_start_matches("- ");
        let value = body.split_once(": ").map_or(body, |(_, v)| v);
        let value = value.split(" #").next().unwrap_or(value).trim();
        if let Some(go) = go_timestamp(value) {
            out.push(format!("{value}\u{0}{go}"));
        }
    }
    out
}

/// yaml.v3 timestamp resolution (`2006-1-2[T| ]15:4:5[.f][ ]Z07:00`) and
/// `time.Time` marshalling.
fn go_timestamp(v: &str) -> Option<String> {
    let b = v.as_bytes();
    if b.len() < 8 || !b[0].is_ascii_digit() {
        return None;
    }
    let (date, rest) = match v.find(['T', 't', ' ']) {
        Some(i) => (&v[..i], Some(&v[i + 1..])),
        None => (v, None),
    };
    let mut d = date.split('-');
    let (y, m, day) = (d.next()?, d.next()?, d.next()?);
    if d.next().is_some()
        || y.len() != 4
        || !(1..=2).contains(&m.len())
        || !(1..=2).contains(&day.len())
    {
        return None;
    }
    let (y, m, day): (i32, u8, u8) = (y.parse().ok()?, m.parse().ok()?, day.parse().ok()?);
    let (mut h, mut mi, mut sec, mut nanos, mut off) = (0_u8, 0_u8, 0_u8, 0_u32, 0_i32);
    if let Some(rest) = rest {
        let rest = rest.trim_start();
        let split = rest.find(['Z', 'z', '+', '-', ' ']).unwrap_or(rest.len());
        let (clock, zone) = rest.split_at(split);
        let (clock, frac) = clock.split_once('.').unwrap_or((clock, ""));
        let mut c = clock.split(':');
        h = c.next()?.parse().ok()?;
        mi = c.next()?.parse().ok()?;
        sec = c.next()?.parse().ok()?;
        if c.next().is_some() {
            return None;
        }
        if !frac.is_empty() {
            if !frac.bytes().all(|x| x.is_ascii_digit()) || frac.len() > 9 {
                return None;
            }
            nanos = format!("{frac:0<9}").parse().ok()?;
        }
        let zone = zone.trim();
        if !zone.is_empty() && !zone.eq_ignore_ascii_case("z") {
            let sign = if zone.starts_with('-') { -1 } else { 1 };
            let z = zone.trim_start_matches(['+', '-']);
            let (zh, zm) = z.split_once(':').unwrap_or((z, "0"));
            off = sign * (zh.parse::<i32>().ok()? * 3600 + zm.parse::<i32>().ok()? * 60);
        }
    }
    let date = time::Date::from_calendar_date(y, time::Month::try_from(m).ok()?, day).ok()?;
    let t = time::Time::from_hms_nano(h, mi, sec, nanos).ok()?;
    let at = time::PrimitiveDateTime::new(date, t)
        .assume_offset(time::UtcOffset::from_whole_seconds(off).ok()?);
    Some(rfc3339_nano(at))
}

fn string_value(s: &str) -> noyalib::Value {
    noyalib::Value::String(s.to_owned())
}

fn write_frontmatter_and_body(
    path: &str,
    fields: &Fields,
    body: &[u8],
    stamps: &[String],
) -> Result<(), String> {
    let mut yaml = render_go_yaml_map(fields).map_err(|e| format!("marshal frontmatter: {e}"))?;
    // noyalib decodes timestamps as strings; Go re-emits them as time.Time.
    yaml = yaml
        .lines()
        .map(|line| restore_timestamp(line, stamps) + "\n")
        .collect();
    let mut out = Vec::new();
    out.extend_from_slice(b"---\n");
    out.extend_from_slice(yaml.as_bytes());
    out.extend_from_slice(b"---\n\n");
    out.extend_from_slice(body);
    atomic_write_file(path, &out, 0o600)
}

/// Re-emits a quoted timestamp lookalike as the plain `time.Time` text when
/// the source held it as an unquoted timestamp (or it is a machine key).
fn restore_timestamp(line: &str, stamps: &[String]) -> String {
    let Some((head, value)) = line.split_once(": ") else {
        let item = line.trim_start();
        if let Some(rest) = item.strip_prefix("- ") {
            return restore_scalar(line, &line[..line.len() - item.len()], "- ", rest, stamps);
        }
        return line.to_owned();
    };
    for key in ["ingested_at", "    created", "    added", "    modified"] {
        if head == key {
            let unquoted = value
                .strip_prefix('\'')
                .and_then(|r| r.strip_suffix('\''))
                .or_else(|| value.strip_prefix('"').and_then(|r| r.strip_suffix('"')));
            if let Some(v) = unquoted
                && looks_like_timestamp(v)
            {
                return format!("{head}: {v}");
            }
        }
    }
    restore_scalar(line, &format!("{head}: "), "", value, stamps)
}

fn restore_scalar(
    line: &str,
    prefix: &str,
    marker: &str,
    value: &str,
    stamps: &[String],
) -> String {
    let unquoted = value
        .strip_prefix('\'')
        .and_then(|r| r.strip_suffix('\''))
        .or_else(|| value.strip_prefix('"').and_then(|r| r.strip_suffix('"')));
    if let Some(v) = unquoted {
        for entry in stamps {
            if let Some((raw, go)) = entry.split_once('\u{0}')
                && raw == v
            {
                return format!("{prefix}{marker}{go}");
            }
        }
    }
    line.to_owned()
}

fn looks_like_timestamp(v: &str) -> bool {
    v.len() >= 20
        && v.as_bytes()[4] == b'-'
        && v.as_bytes()[10] == b'T'
        && time::OffsetDateTime::parse(v, &time::format_description::well_known::Rfc3339).is_ok()
}

impl NoteWriter {
    #[must_use]
    pub fn new(vault: &str) -> Self {
        Self {
            vault: vault.to_owned(),
            lock: Mutex::new(()),
        }
    }

    fn resolve_note_path(&self, source: &str, layout: Option<&NoteLayout>) -> String {
        let Some(layout) = layout else {
            return sidecar_path(&self.vault, source);
        };
        let dir = goos::join(&[&self.vault, &layout.subdir]);
        let mut base = sanitize_segment(&layout.base_name);
        if base.is_empty() {
            let b = goos::base(source);
            let ext = goos::ext(&b);
            base = b[..b.len() - ext.len()].to_owned();
        }
        let mut candidate = goos::join(&[&dir, &format!("{base}.md")]);
        let mut i = 2;
        while file_exists(&candidate) {
            candidate = goos::join(&[&dir, &format!("{base}-{i}.md")]);
            i += 1;
        }
        candidate
    }

    /// Writes a new note; returns its path.
    pub fn write_note(&self, input: &NoteInput<'_>, text: &[u8]) -> Result<String, String> {
        let _guard = self.lock.lock().unwrap_or_else(|e| e.into_inner());
        let vault_path = self.resolve_note_path(input.source_path, input.layout);
        goos::mkdir_all(&goos::dir(&vault_path), 0o700)
            .map_err(|e| format!("create vault directory: {e}"))?;
        let note = Note {
            source_path: input.source_path.into(),
            imported_from: input.imported_from.into(),
            import_run_id: input.import_run_id.into(),
            source_uri: input.source_uri.into(),
            download_uri: input.download_uri.into(),
            ingested_at: input.ingested_at,
            sha256: input.sha256.into(),
            mime: input.mime.into(),
            tags: input.tags.to_vec(),
            category: input.category.into(),
            correspondent: input.correspondent.into(),
            document_type: input.document_type.into(),
            ocr_engine: input.ocr_engine.into(),
            ocr_language: input.ocr_language.into(),
            archive_path: input.archive_path.into(),
            paperless: input.paperless.cloned(),
        };
        let mut data = b"---\n".to_vec();
        data.extend_from_slice(marshal_note(&note).as_bytes());
        data.extend_from_slice(b"---\n\n");
        data.extend_from_slice(text);
        if !text.ends_with(b"\n") {
            data.push(b'\n');
        }
        if !input.archive_path.is_empty() {
            data.extend_from_slice(b"\n---\n");
            data.extend_from_slice(
                format!("[Archived Original](file://{})\n", input.archive_path).as_bytes(),
            );
        }
        atomic_write_file(&vault_path, &data, 0o600).map_err(|e| format!("write note: {e}"))?;
        Ok(vault_path)
    }

    /// Replaces the machine-owned fields and body of an existing note.
    pub fn update_note(&self, vault_path: &str, meta: &Note, text: &[u8]) -> Result<(), String> {
        let _guard = self.lock.lock().unwrap_or_else(|e| e.into_inner());
        let data = std::fs::read(vault_path)
            .map_err(|e| format!("read note: {}", goos::path_error("open", vault_path, &e)))?;
        let (mut fields, _, stamps) = parse_frontmatter(&data)?;
        fields.insert("source_path".into(), string_value(&meta.source_path));
        fields.insert(
            "ingested_at".into(),
            string_value(&rfc3339_nano(
                meta.ingested_at.unwrap_or(OffsetDateTime::UNIX_EPOCH),
            )),
        );
        fields.insert("sha256".into(), string_value(&meta.sha256));
        fields.insert("mime".into(), string_value(&meta.mime));
        fields.insert(
            "tags".into(),
            noyalib::Value::Sequence(meta.tags.iter().map(|t| string_value(t)).collect()),
        );
        fields.insert("category".into(), string_value(&meta.category));
        for (key, value) in [
            ("correspondent", &meta.correspondent),
            ("document_type", &meta.document_type),
        ] {
            if value.is_empty() {
                fields.remove(key);
            } else {
                fields.insert(key.into(), string_value(value));
            }
        }
        fields.insert("ocr_engine".into(), string_value(&meta.ocr_engine));
        if meta.ocr_language.is_empty() {
            fields.remove("ocr_language");
        } else {
            fields.insert("ocr_language".into(), string_value(&meta.ocr_language));
        }
        fields.insert("archive_path".into(), string_value(&meta.archive_path));
        let body = render_body(text, &meta.archive_path);
        write_frontmatter_and_body(vault_path, &fields, &body, &stamps)
            .map_err(|e| format!("rewrite note: {e}"))
    }

    /// Adds the sidecar path and extraction count without touching the body.
    pub fn update_note_sidecar(
        &self,
        vault_path: &str,
        sidecar_path: &str,
        extraction_count: usize,
    ) -> Result<(), String> {
        let _guard = self.lock.lock().unwrap_or_else(|e| e.into_inner());
        let data = std::fs::read(vault_path)
            .map_err(|e| format!("read note: {}", goos::path_error("open", vault_path, &e)))?;
        let (mut fields, body, stamps) = parse_frontmatter(&data)?;
        fields.insert("sidecar_path".into(), string_value(sidecar_path));
        fields.insert(
            "extraction_count".into(),
            noyalib::Value::Number(noyalib::Number::from(extraction_count as i64)),
        );
        write_frontmatter_and_body(vault_path, &fields, &body, &stamps)
            .map_err(|e| format!("rewrite note: {e}"))
    }
}

/// The `ocr_language` frontmatter field, trimmed; empty when absent, not a
/// string, or the note cannot be parsed.
#[must_use]
pub fn read_ocr_language(vault_path: &str) -> String {
    let Ok(data) = std::fs::read(vault_path) else {
        return String::new();
    };
    match parse_frontmatter(&data) {
        Ok((fields, _, _)) => match fields.get("ocr_language") {
            Some(noyalib::Value::String(s)) => s.trim().to_owned(),
            _ => String::new(),
        },
        Err(_) => String::new(),
    }
}
