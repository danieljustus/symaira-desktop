#![no_main]
//! Untrusted-input boundary of the ingest port: content-authoritative file
//! detection, every byte-oriented extraction reader (text, HTML, RTF, DOCX,
//! XLSX, PPTX, ODF, EPUB, EML), the Go-faithful XML tokenizer, MIME header /
//! multipart / quoted-printable / base64 decoding and the ZIP reader. The
//! first byte selects how the remaining bytes are presented so that the XML
//! and mail readers are reached even though a fuzzer cannot forge ZIP CRCs.

use libfuzzer_sys::fuzz_target;
use symdesk_ingest::{extract, ghtml, gmime, gxml, gzip, structured};

const LIMIT: usize = 4 << 20;

const KINDS: &[&str] = &[
    "text/plain",
    "text/html",
    "application/rtf",
    "message/rfc822",
    "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
    "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
    "application/vnd.openxmlformats-officedocument.presentationml.presentation",
    "application/vnd.oasis.opendocument.text",
    "application/vnd.oasis.opendocument.spreadsheet",
    "application/vnd.oasis.opendocument.presentation",
    "application/epub+zip",
];

const PARTS: &[&str] = &[
    "word/document.xml",
    "xl/sharedStrings.xml",
    "xl/workbook.xml",
    "xl/_rels/workbook.xml.rels",
    "xl/worksheets/sheet1.xml",
    "ppt/slides/slide1.xml",
    "content.xml",
    "mimetype",
    "_rels/.rels",
    "[Content_Types].xml",
    "OEBPS/ch1.xhtml",
];

/// A stored (method 0) archive with correct CRCs.
fn stored_zip(entries: &[(&str, &[u8])]) -> Vec<u8> {
    let mut out = Vec::new();
    let mut central = Vec::new();
    for (name, data) in entries {
        let crc = crc32(data);
        let offset = out.len() as u32;
        out.extend_from_slice(&0x0403_4b50u32.to_le_bytes());
        out.extend_from_slice(&[20, 0, 0, 0, 0, 0, 0, 0, 0, 0]);
        out.extend_from_slice(&crc.to_le_bytes());
        out.extend_from_slice(&(data.len() as u32).to_le_bytes());
        out.extend_from_slice(&(data.len() as u32).to_le_bytes());
        out.extend_from_slice(&(name.len() as u16).to_le_bytes());
        out.extend_from_slice(&0u16.to_le_bytes());
        out.extend_from_slice(name.as_bytes());
        out.extend_from_slice(data);
        central.extend_from_slice(&0x0201_4b50u32.to_le_bytes());
        central.extend_from_slice(&[20, 0, 20, 0, 0, 0, 0, 0, 0, 0, 0, 0]);
        central.extend_from_slice(&crc.to_le_bytes());
        central.extend_from_slice(&(data.len() as u32).to_le_bytes());
        central.extend_from_slice(&(data.len() as u32).to_le_bytes());
        central.extend_from_slice(&(name.len() as u16).to_le_bytes());
        central.extend_from_slice(&[0; 12]);
        central.extend_from_slice(&offset.to_le_bytes());
        central.extend_from_slice(name.as_bytes());
    }
    let cd_offset = out.len() as u32;
    out.extend_from_slice(&central);
    out.extend_from_slice(&0x0605_4b50u32.to_le_bytes());
    out.extend_from_slice(&[0; 4]);
    out.extend_from_slice(&(entries.len() as u16).to_le_bytes());
    out.extend_from_slice(&(entries.len() as u16).to_le_bytes());
    out.extend_from_slice(&(central.len() as u32).to_le_bytes());
    out.extend_from_slice(&cd_offset.to_le_bytes());
    out.extend_from_slice(&0u16.to_le_bytes());
    out
}

fn crc32(data: &[u8]) -> u32 {
    let mut crc = !0u32;
    for b in data {
        crc ^= u32::from(*b);
        for _ in 0..8 {
            crc = (crc >> 1) ^ (0xEDB8_8320 & 0u32.wrapping_sub(crc & 1));
        }
    }
    !crc
}

fn exercise_all(name: &str, data: &[u8]) {
    let _ = extract::detect_bytes(name, data);
    for kind in KINDS {
        let _ = extract::extract_bytes(data, kind, LIMIT);
    }
}

fuzz_target!(|input: &[u8]| {
    let Some((&mode, data)) = input.split_first() else {
        return;
    };
    match mode % 8 {
        // Raw bytes as a file, all readers.
        0 => exercise_all("fuzz.bin", data),
        // Raw bytes as one archive part; the second byte picks the part name
        // and the output of detection feeds the matching reader.
        1 | 2 => {
            let Some((&sel, body)) = data.split_first() else {
                return;
            };
            let part = PARTS[usize::from(sel) % PARTS.len()];
            let mut entries: Vec<(&str, &[u8])> = vec![(part, body)];
            if mode % 8 == 2 {
                entries.push(("mimetype", b"application/vnd.oasis.opendocument.text"));
            }
            let zip = stored_zip(&entries);
            let kind = extract::detect_bytes("fuzz.bin", &zip).unwrap_or_default();
            if !kind.is_empty() {
                let _ = extract::extract_bytes(&zip, &kind, LIMIT);
            }
            exercise_all("fuzz.docx", &zip);
        }
        // Raw bytes straight into the Go-compatible XML readers.
        3 => {
            let _ = structured::word_xml_to_text(data);
            let _ = structured::odf_xml_to_text(data);
            let _ = structured::pptx_xml_to_text(data);
            let shared = vec!["a".to_string(), "b".to_string()];
            let _ = structured::sheet_xml_to_text(data, &shared);
            let _ = structured::read_shared_strings(data);
            let _ = structured::workbook_sheet_names(Some(data), Some(data));
            let mut dec = gxml::Decoder::new(data);
            while dec.token().is_ok() {}
        }
        // Mail: headers, media types, multipart, quoted-printable, base64.
        4 => {
            let _ = structured::read_eml(data);
            let _ = gmime::read_message(data);
            let _ = gmime::parse_media_type(data);
            let stream = gmime::Stream {
                data: std::borrow::Cow::Borrowed(data),
                end: gmime::End::Eof,
            };
            let _ = gmime::quoted_printable(&stream);
            let _ = gmime::base64_stream(&stream);
        }
        // A message whose body is the fuzz data under the common transfer
        // encodings and a multipart wrapper.
        5 => {
            for head in [
                &b"Subject: s\r\nContent-Transfer-Encoding: base64\r\n\r\n"[..],
                b"Subject: s\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n",
                b"Subject: s\r\nContent-Type: multipart/mixed; boundary=B\r\n\r\n",
                b"Subject: s\r\nContent-Type: multipart/alternative; boundary=\"B\"\r\n\r\n--B\r\nContent-Type: text/html\r\n\r\n",
            ] {
                let mut msg = head.to_vec();
                msg.extend_from_slice(data);
                let _ = structured::read_eml(&msg);
            }
        }
        // HTML / RTF / text helpers.
        6 => {
            let text = String::from_utf8_lossy(data);
            let _ = extract::html_to_text(&text);
            let _ = ghtml::unescape(&text);
            let _ = extract::normalize_text(&text);
            let _ = extract::extract_bytes(data, "text/html", LIMIT);
            let _ = extract::extract_bytes(data, "application/rtf", LIMIT);
        }
        // The ZIP reader on its own: structure, then every entry.
        _ => {
            if let Ok(reader) = gzip::Reader::new(data, 4097) {
                for entry in &reader.files {
                    let _ = reader.read(entry, LIMIT);
                }
            }
            exercise_all("fuzz.zip", data);
        }
    }
});
