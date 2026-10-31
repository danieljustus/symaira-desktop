//! `net/http` `Request.ParseMultipartForm` + `FormFile("file")` over the ported
//! `mime/multipart` reader: what Go's `POST /api/v1/ingest` handler observes.
//! Memory accounting follows `multipart.Reader.ReadForm` (`maxMemory` plus 10 MiB
//! of value/header slack, 100 bytes charged per file header, at most 1000 parts).
use crate::gmime::{Header, Multipart, Stream, parse_media_type};

const MAX_PARTS: usize = 1000;
const FILE_HEADER_COST: i64 = 100;
const VALUE_SLACK: i64 = 10 << 20;

/// The first part named `file` that carries a file name.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct UploadFile {
    /// `Part.FileName()`: the `filename` parameter after `filepath.Base`.
    pub file_name: String,
    /// `FileHeader.Header.Get("Content-Type")`.
    pub content_type: String,
    pub data: Vec<u8>,
}

fn go_base(path: &[u8]) -> Vec<u8> {
    if path.is_empty() {
        return b".".to_vec();
    }
    let mut end = path.len();
    while end > 0 && path[end - 1] == b'/' {
        end -= 1;
    }
    if end == 0 {
        return b"/".to_vec();
    }
    let trimmed = &path[..end];
    match trimmed.iter().rposition(|b| *b == b'/') {
        Some(i) => trimmed[i + 1..].to_vec(),
        None => trimmed.to_vec(),
    }
}

fn disposition(header: &Header) -> (String, std::collections::HashMap<String, Vec<u8>>) {
    let media = parse_media_type(&header.get("Content-Disposition"));
    // Go ignores the parse error: ErrInvalidMediaParameter leaves nil params.
    if media.err {
        (media.media_type, Default::default())
    } else {
        (media.media_type, media.params)
    }
}

/// Go `url.ParseQuery` reports an error for malformed escapes and, since Go 1.17,
/// for semicolons; `ParseMultipartForm` returns that error after a successful parse.
pub fn query_is_invalid(raw: &str) -> bool {
    for pair in raw.split('&') {
        if pair.is_empty() {
            continue;
        }
        if pair.contains(';') {
            return true;
        }
        let (key, value) = pair.split_once('=').unwrap_or((pair, ""));
        for part in [key, value] {
            let bytes = part.as_bytes();
            let mut i = 0;
            while i < bytes.len() {
                if bytes[i] == b'%' {
                    let ok = i + 2 < bytes.len()
                        && bytes[i + 1].is_ascii_hexdigit()
                        && bytes[i + 2].is_ascii_hexdigit();
                    if !ok {
                        return true;
                    }
                    i += 3;
                } else {
                    i += 1;
                }
            }
        }
    }
    false
}

/// Parses a fully buffered request body. Any error (not multipart, missing
/// boundary, malformed framing, truncated part, size accounting) is a plain
/// `Err(text)`; the Go handler maps all of them to one 413 response.
/// `Ok(None)` is `http.ErrMissingFile`.
pub fn read_upload(
    content_type: &str,
    raw_query: &str,
    body: &[u8],
    max_memory: i64,
) -> Result<Option<UploadFile>, String> {
    let media = parse_media_type(content_type.as_bytes());
    if media.err || media.media_type != "multipart/form-data" {
        return Err("request Content-Type isn't multipart/form-data".into());
    }
    let boundary = media.params.get("boundary").cloned().unwrap_or_default();
    if boundary.is_empty() {
        return Err("no multipart boundary param in Content-Type".into());
    }
    let stream = Stream {
        data: std::borrow::Cow::Borrowed(body),
        end: crate::gmime::End::Eof,
    };
    let mut reader = Multipart::new(&stream, &boundary);
    let mut max_file_memory = max_memory;
    let mut max_memory_bytes = max_memory + VALUE_SLACK;
    let mut parts_left = MAX_PARTS;
    let mut found: Option<UploadFile> = None;
    loop {
        let Some(part) = reader.next_part()? else {
            break;
        };
        if parts_left == 0 {
            return Err("multipart: message too large".into());
        }
        parts_left -= 1;
        let (disp, params) = disposition(&part.header);
        if disp != "form-data" {
            continue;
        }
        let name = params.get("name").cloned().unwrap_or_default();
        if name.is_empty() {
            continue;
        }
        let file_name = match params.get("filename") {
            Some(f) if !f.is_empty() => go_base(f),
            _ => Vec::new(),
        };
        if let Some(e) = part.body.end.error() {
            return Err(e.to_string());
        }
        let len = part.body.data.len() as i64;
        if file_name.is_empty() {
            max_memory_bytes -= len;
            if max_memory_bytes < 0 {
                return Err("multipart: message too large".into());
            }
            continue;
        }
        max_memory_bytes -= FILE_HEADER_COST;
        if len <= max_file_memory {
            max_file_memory -= len;
            max_memory_bytes -= len;
        }
        if name == b"file" && found.is_none() {
            found = Some(UploadFile {
                file_name: String::from_utf8_lossy(&file_name).into_owned(),
                content_type: String::from_utf8_lossy(&part.header.get("Content-Type"))
                    .into_owned(),
                data: part.body.data.to_vec(),
            });
        }
    }
    if query_is_invalid(raw_query) {
        return Err("invalid URL escape in query".into());
    }
    Ok(found)
}
