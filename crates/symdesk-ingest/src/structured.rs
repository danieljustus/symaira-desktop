//! Byte-for-byte ports of `internal/ingest/internal/extract/structured.go`
//! readers that parse XML or mail with Go's own decoders (see `gxml`, `gmime`).
//! Error strings reproduce Go's wrapping exactly, including the readers that
//! return a decoder error unwrapped.
use crate::gmime::{
    self, End, Header, Multipart, Stream, base64_stream, parse_media_type, quoted_printable,
};
use crate::gxml::{Decoder, Token, XmlError};
use std::borrow::Cow;
use std::collections::HashMap;

type Res = Result<String, String>;

fn is_eof(e: &XmlError) -> bool {
    matches!(e, XmlError::Eof)
}

/// Go `filepath.Base` for slash-separated names.
pub fn go_base(p: &str) -> String {
    if p.is_empty() {
        return ".".into();
    }
    let t = p.trim_end_matches('/');
    if t.is_empty() {
        return "/".into();
    }
    t.rsplit('/').next().unwrap().to_string()
}

fn escape_cell(s: &str) -> String {
    s.replace('|', "\\|").replace('\n', " ")
}

/// GFM table: header row, alignment row, remaining rows. Go panics
/// ("strings: negative Repeat count") when every row is empty; that is
/// returned as an error instead of crashing the process.
pub fn gfm_table(rows: &[Vec<String>]) -> Res {
    if rows.is_empty() {
        return Ok(String::new());
    }
    let width = rows.iter().map(Vec::len).max().unwrap_or(0);
    if width == 0 {
        return Err("extract: panic in Go reader: strings: negative Repeat count".into());
    }
    let row_text = |cells: &[String]| {
        let esc = (0..width)
            .map(|i| cells.get(i).map(|c| escape_cell(c)).unwrap_or_default())
            .collect::<Vec<_>>();
        format!("| {} |\n", esc.join(" | "))
    };
    let mut out = row_text(&rows[0]);
    out.push_str("| ");
    out.push_str(&"--- | ".repeat(width - 1));
    out.push_str("--- |\n");
    for row in &rows[1..] {
        out.push_str(&row_text(row));
    }
    Ok(out)
}

/// `wordXMLToText`.
pub fn word_xml_to_text(data: &[u8]) -> Res {
    let mut dec = Decoder::new(data);
    let mut out = String::new();
    let mut table: Vec<Vec<String>> = Vec::new();
    let mut row: Vec<String> = Vec::new();
    let mut cell = String::new();
    let (mut in_table, mut in_cell) = (false, false);
    loop {
        let tok = match dec.token() {
            Ok(t) => t,
            Err(e) if is_eof(&e) => break,
            Err(e) => return Err(format!("parse word xml: {e}")),
        };
        match tok {
            Token::Start { name, .. } => match name.local.as_str() {
                "tbl" => in_table = true,
                "tr" => {
                    if in_table {
                        row = Vec::new();
                    }
                }
                "tc" => {
                    if in_table {
                        in_cell = true;
                        cell.clear();
                    }
                }
                "t" => {
                    let text = dec.decode_element_string().map_err(|e| e.to_string())?;
                    if in_cell {
                        cell.push_str(&text);
                    } else if !in_table {
                        out.push_str(&text);
                    }
                }
                "tab" => {
                    if in_cell {
                        cell.push(' ');
                    } else if !in_table {
                        out.push('\t');
                    }
                }
                "br" | "cr" | "p" => {
                    if in_cell {
                        cell.push('\n');
                    } else if !in_table {
                        out.push('\n');
                    }
                }
                _ => {}
            },
            Token::End(name) => match name.local.as_str() {
                "tc" => {
                    if in_table && in_cell {
                        row.push(std::mem::take(&mut cell));
                        in_cell = false;
                    }
                }
                "tr" => {
                    if in_table {
                        table.push(std::mem::take(&mut row));
                    }
                }
                "tbl" if in_table => {
                    if !table.is_empty() {
                        out.push_str(&gfm_table(&table)?);
                        table = Vec::new();
                    }
                    in_table = false;
                }
                _ => {}
            },
            _ => {}
        }
    }
    Ok(out)
}

/// `odfXMLToText`.
pub fn odf_xml_to_text(data: &[u8]) -> Res {
    let mut dec = Decoder::new(data);
    let mut out: Vec<u8> = Vec::new();
    loop {
        let tok = match dec.token() {
            Ok(t) => t,
            Err(e) if is_eof(&e) => break,
            Err(e) => return Err(format!("parse odf xml: {e}")),
        };
        match tok {
            Token::Start { name, .. } => match name.local.as_str() {
                "p" | "h" => {
                    if !out.is_empty() {
                        out.push(b'\n');
                    }
                }
                "tab" => out.push(b'\t'),
                "line-break" => out.push(b'\n'),
                _ => {}
            },
            Token::CharData(d) => out.extend_from_slice(&d),
            _ => {}
        }
    }
    Ok(String::from_utf8_lossy(&out).into_owned())
}

/// `pptxXMLToText`.
pub fn pptx_xml_to_text(data: &[u8]) -> Res {
    let mut dec = Decoder::new(data);
    let mut out = String::new();
    loop {
        let tok = match dec.token() {
            Ok(t) => t,
            Err(e) if is_eof(&e) => break,
            Err(e) => return Err(format!("parse pptx xml: {e}")),
        };
        if let Token::Start { name, .. } = tok {
            match name.local.as_str() {
                "p" => {
                    if !out.is_empty() {
                        out.push('\n');
                    }
                }
                "t" => {
                    let text = dec.decode_element_string().map_err(|e| e.to_string())?;
                    out.push_str(&text);
                }
                _ => {}
            }
        }
    }
    Ok(out)
}

/// `collectXMLText`: character data of the current element and descendants.
fn collect_xml_text(dec: &mut Decoder<'_>) -> Result<String, String> {
    let mut out: Vec<u8> = Vec::new();
    let mut depth = 1usize;
    while depth > 0 {
        match dec.token().map_err(|e| e.to_string())? {
            Token::Start { .. } => depth += 1,
            Token::End(_) => depth -= 1,
            Token::CharData(d) => out.extend_from_slice(&d),
            _ => {}
        }
    }
    Ok(String::from_utf8_lossy(&out).into_owned())
}

pub fn read_shared_strings(data: &[u8]) -> Result<Vec<String>, String> {
    let mut dec = Decoder::new(data);
    let mut vals = Vec::new();
    loop {
        let tok = match dec.token() {
            Ok(t) => t,
            Err(e) if is_eof(&e) => break,
            Err(e) => return Err(format!("parse sharedStrings.xml: {e}")),
        };
        if let Token::Start { name, .. } = tok
            && name.local == "si"
        {
            vals.push(collect_xml_text(&mut dec)?);
        }
    }
    Ok(vals)
}

/// `workbookSheetNames` given the (optional) workbook and rels parts.
pub fn workbook_sheet_names(
    workbook: Option<&[u8]>,
    rels_data: Option<&[u8]>,
) -> Result<HashMap<String, String>, String> {
    let mut names = HashMap::new();
    let Some(data) = workbook else {
        return Ok(names);
    };
    let mut rels: HashMap<String, String> = HashMap::new();
    if let Some(rdata) = rels_data {
        let mut dec = Decoder::new(rdata);
        loop {
            let tok = match dec.token() {
                Ok(t) => t,
                Err(e) if is_eof(&e) => break,
                Err(e) => return Err(format!("parse workbook rels: {e}")),
            };
            if let Token::Start { name, attr } = tok
                && name.local == "Relationship"
            {
                let (mut id, mut target) = (String::new(), String::new());
                for a in attr {
                    match a.name.local.as_str() {
                        "Id" => id = a.value,
                        "Target" => target = a.value,
                        _ => {}
                    }
                }
                if !id.is_empty() && !target.is_empty() {
                    rels.insert(id, go_base(&target));
                }
            }
        }
    }
    let mut dec = Decoder::new(data);
    loop {
        let tok = match dec.token() {
            Ok(t) => t,
            Err(e) if is_eof(&e) => break,
            Err(e) => return Err(format!("parse workbook.xml: {e}")),
        };
        if let Token::Start { name, attr } = tok
            && name.local == "sheet"
        {
            let (mut n, mut rid) = (String::new(), String::new());
            for a in attr {
                match a.name.local.as_str() {
                    "name" => n = a.value,
                    "id" => rid = a.value,
                    _ => {}
                }
            }
            if let Some(target) = rels.get(&rid)
                && !n.is_empty()
            {
                names.insert(target.clone(), n);
            }
        }
    }
    Ok(names)
}

/// `fmt.Sscanf(strings.TrimSpace(val), "%d", &idx)` success value.
fn scan_int(val: &str) -> Option<i64> {
    let t = val.trim();
    let b = t.as_bytes();
    let mut i = 0;
    if i < b.len() && (b[i] == b'+' || b[i] == b'-') {
        i += 1;
    }
    let start = i;
    while i < b.len() && b[i].is_ascii_digit() {
        i += 1;
    }
    if i == start {
        return None;
    }
    t[..i].parse::<i64>().ok()
}

fn read_cell(dec: &mut Decoder<'_>, cell_type: &str, shared: &[String]) -> Res {
    let mut val = String::new();
    loop {
        match dec.token().map_err(|e| e.to_string())? {
            Token::Start { name, .. } => {
                if name.local == "v" || name.local == "t" {
                    val = dec.decode_element_string().map_err(|e| e.to_string())?;
                }
            }
            Token::End(name) if name.local == "c" => {
                if cell_type == "s"
                    && let Some(idx) = scan_int(&val)
                    && idx >= 0
                    && (idx as usize) < shared.len()
                {
                    return Ok(shared[idx as usize].clone());
                }
                return Ok(val);
            }
            _ => {}
        }
    }
}

/// `sheetXMLToText`.
pub fn sheet_xml_to_text(data: &[u8], shared: &[String]) -> Res {
    let mut dec = Decoder::new(data);
    let mut rows: Vec<Vec<String>> = Vec::new();
    let mut current: Vec<String> = Vec::new();
    let mut in_row = false;
    loop {
        let tok = match dec.token() {
            Ok(t) => t,
            Err(e) if is_eof(&e) => break,
            Err(e) => return Err(format!("parse sheet xml: {e}")),
        };
        match tok {
            Token::Start { name, attr } => match name.local.as_str() {
                "row" => {
                    in_row = true;
                    current = Vec::new();
                }
                "c" => {
                    let cell_type = attr
                        .iter()
                        .find(|a| a.name.local == "t")
                        .map(|a| a.value.clone())
                        .unwrap_or_default();
                    let text = read_cell(&mut dec, &cell_type, shared)?;
                    if in_row {
                        current.push(text);
                    }
                }
                _ => {}
            },
            Token::End(name) if name.local == "row" && in_row => {
                rows.push(std::mem::take(&mut current));
                in_row = false;
            }
            _ => {}
        }
    }
    gfm_table(&rows)
}

// ---------------------------------------------------------------- EML

const NEST_ERR: &str = "extract: mail multipart nesting exceeds 64 levels";

/// `readEML`: `mail.ReadMessage` plus `readMailBody`.
pub fn read_eml(data: &[u8]) -> Res {
    let (header, off) = gmime::read_message(data).map_err(|e| format!("parse eml: {e}"))?;
    let mut parts: Vec<String> = Vec::new();
    let subj = header.get("Subject");
    if !subj.is_empty() {
        parts.push(format!("Subject: {}", String::from_utf8_lossy(&subj)));
    }
    let body = Stream {
        data: Cow::Borrowed(&data[off..]),
        end: End::Eof,
    };
    let text = read_mail_body(&header, &body, 0)?;
    if !text.trim().is_empty() {
        parts.push(text);
    }
    Ok(parts.join("\n\n"))
}

fn eq_fold(a: &[u8], b: &str) -> bool {
    std::str::from_utf8(a).is_ok_and(|a| a.to_lowercase() == b.to_lowercase())
}

fn read_mail_body(header: &Header, body: &Stream<'_>, depth: usize) -> Res {
    let ct = parse_media_type(&header.get("Content-Type"));
    let media = ct.media_type.as_str();
    if media.starts_with("multipart/") {
        if depth >= gmime::MAX_NESTING {
            return Err(NEST_ERR.into());
        }
        let boundary = ct.params.get("boundary").cloned().unwrap_or_default();
        let mut mr = Multipart::new(body, &boundary);
        let (mut plain, mut html_parts): (Vec<String>, Vec<String>) = (vec![], vec![]);
        while let Some(part) = mr.next_part()? {
            let disp = parse_media_type(&part.header.get("Content-Disposition")).media_type;
            if eq_fold(disp.as_bytes(), "attachment") {
                continue;
            }
            let text = read_mail_body(&part.header, &part.body, depth + 1)?;
            let pct = parse_media_type(&part.header.get("Content-Type")).media_type;
            if pct.starts_with("text/html") {
                html_parts.push(text);
            } else if !text.trim().is_empty() {
                plain.push(text);
            }
        }
        return Ok(if plain.is_empty() {
            html_parts.join("\n\n")
        } else {
            plain.join("\n\n")
        });
    }
    if !media.is_empty() && !media.starts_with("text/plain") && !media.starts_with("text/html") {
        return Ok(String::new());
    }
    let decoded = decode_mail_transfer(body, &header.get("Content-Transfer-Encoding"))?;
    let text = String::from_utf8_lossy(&decoded).into_owned();
    if media.starts_with("text/html") {
        return Ok(crate::extract::html_to_text(&text));
    }
    Ok(text)
}

fn decode_mail_transfer(body: &Stream<'_>, encoding: &[u8]) -> Result<Vec<u8>, String> {
    let enc = String::from_utf8_lossy(encoding).trim().to_lowercase();
    let decoded = match enc.as_str() {
        "base64" => base64_stream(body),
        "quoted-printable" => quoted_printable(body),
        _ => Stream {
            data: Cow::Borrowed(&body.data),
            end: body.end.clone(),
        },
    };
    match decoded.end.error() {
        Some(e) => Err(e.to_string()),
        None => Ok(decoded.data.into_owned()),
    }
}
