#![deny(unsafe_code)]

//! Signed room events: the canonical byte encoding, the signature envelope and
//! the JSON line format. Port of `internal/room/event`.

use std::fmt;
use std::io;

use serde::ser::{SerializeMap, Serializer as _};
use serde_json::value::RawValue;

use crate::identity::{self, Identity};

/// Go: `event.CurrentVersion`.
pub const CURRENT_VERSION: i64 = 1;
/// Go: `event.SigPrefix`.
pub const SIG_PREFIX: &str = "ed25519:";

/// Go: `event.ErrInvalidSignature`.
pub const INVALID_SIGNATURE: &str = "invalid event signature";

/// Every kind in Go's `event.KnownKinds`, in declaration order.
pub const KNOWN_KINDS: [&str; 20] = [
    "room.created",
    "room.renamed",
    "policy.changed",
    "member.added",
    "member.removed",
    "member.role_changed",
    "note.posted",
    "decision.recorded",
    "artifact.linked",
    "artifact.unlinked",
    "artifact.changed",
    "run.requested",
    "run.approved",
    "run.denied",
    "run.started",
    "run.finished",
    "run.failed",
    "run.cancelled",
    "checkpoint.requested",
    "checkpoint.resolved",
];

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum EventError {
    /// A signature problem, with Go's exact message.
    Signature(String),
    /// A wrapped encoding failure, already prefixed like Go.
    Message(String),
}

impl fmt::Display for EventError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Signature(message) | Self::Message(message) => f.write_str(message),
        }
    }
}

impl std::error::Error for EventError {}

/// Go: `event.Event`. The raw body preserves Go's `json.RawMessage` escape
/// spelling when signing and writing journal lines.
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct Event {
    pub v: i64,
    pub id: String,
    pub room: String,
    pub author: String,
    pub seq: u64,
    pub prev: String,
    pub lamport: u64,
    pub ts: String,
    pub kind: String,
    pub body: Box<RawValue>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub sig: Option<String>,
}

/// Go: `event.FormatTimestamp` — UTC, millisecond precision, always three
/// fractional digits.
pub fn format_timestamp(stamp: time::OffsetDateTime) -> String {
    let utc = stamp.to_offset(time::UtcOffset::UTC);
    format!(
        "{:04}-{:02}-{:02}T{:02}:{:02}:{:02}.{:03}Z",
        utc.year(),
        u8::from(utc.month()),
        utc.day(),
        utc.hour(),
        utc.minute(),
        utc.second(),
        utc.millisecond(),
    )
}

/// Go: `event.CanonicalBytes` — the exact JSON that is signed, keys in Go's
/// sorted order: `author, body, id, kind, lamport, prev, room, seq, ts, v`.
///
/// The raw body retains escape spelling as Go's `json.RawMessage` does;
/// the outer keys are emitted in sorted order by the shared serializer.
pub fn canonical_bytes(event: &Event) -> Result<Vec<u8>, EventError> {
    encode_event(event, None).map_err(|err| EventError::Message(err.to_string()))
}

impl Event {
    /// Go: `(*Event).Sign`. Fills a missing version and replaces the signature.
    pub fn sign(&mut self, signer: &Identity) -> Result<(), EventError> {
        if self.v == 0 {
            self.v = CURRENT_VERSION;
        }
        let canonical = canonical_bytes(self)
            .map_err(|err| EventError::Message(format!("canonical encoding: {err}")))?;
        let signature = identity::sign(&signer.private_key, &canonical).ok_or_else(|| {
            EventError::Message("canonical encoding: invalid private key".to_owned())
        })?;
        self.sig = Some(format!(
            "{SIG_PREFIX}{}",
            base64_encode(signature.to_bytes().as_slice())
        ));
        Ok(())
    }

    /// Go: `(*Event).VerifySignature` — the prefix check, then base64, then the
    /// Ed25519 check. The kind is not validated here.
    pub fn verify_signature(&self, public_key: &[u8]) -> Result<(), EventError> {
        let Some(raw) = self.sig.as_deref() else {
            return Err(EventError::Signature(format!(
                "{INVALID_SIGNATURE}: missing ed25519: prefix"
            )));
        };
        let Some(encoded) = raw.strip_prefix(SIG_PREFIX) else {
            return Err(EventError::Signature(format!(
                "{INVALID_SIGNATURE}: missing ed25519: prefix"
            )));
        };
        let Some(signature) = base64_decode(encoded) else {
            return Err(EventError::Signature(format!(
                "{INVALID_SIGNATURE}: invalid base64 signature"
            )));
        };
        let canonical = canonical_bytes(self)
            .map_err(|err| EventError::Message(format!("canonical encoding: {err}")))?;
        if !identity::verify(public_key, &canonical, &signature) {
            return Err(EventError::Signature(INVALID_SIGNATURE.to_owned()));
        }
        Ok(())
    }

    /// Go: `(*Event).MarshalJSONLine` — one JSON object with the signature, keys
    /// sorted the same way, terminated by a newline.
    pub fn marshal_json_line(&self) -> Result<Vec<u8>, EventError> {
        let mut data = encode_event(self, Some(self.sig.as_deref().unwrap_or_default()))
            .map_err(|err| EventError::Message(err.to_string()))?;
        data.push(b'\n');
        Ok(data)
    }

    /// Go: `event.UnmarshalJSONLine`.
    pub fn unmarshal_json_line(data: &[u8]) -> Result<Self, EventError> {
        check_json_depth(data).map_err(|err| EventError::Message(err.to_string()))?;
        serde_json::from_slice(data).map_err(|err| EventError::Message(err.to_string()))
    }
}

/// Serialise Go's sorted map keys while retaining `json.RawMessage` bytes.
fn encode_event(event: &Event, signature: Option<&str>) -> Result<Vec<u8>, serde_json::Error> {
    check_json_depth(event.body.get().as_bytes()).map_err(|err| {
        serde_json::Error::io(io::Error::new(
            io::ErrorKind::InvalidData,
            format!("json: error calling MarshalJSON for type json.RawMessage: {err}"),
        ))
    })?;
    // Go's RawMessage marshaler compacts whitespace outside JSON strings while
    // preserving escape spelling. The fixture itself is pretty-printed JSON.
    let body = RawValue::from_string(compact_json(event.body.get()))?;
    let mut output = Vec::new();
    let mut serializer = serde_json::Serializer::new(&mut output);
    let mut map = serializer.serialize_map(Some(if signature.is_some() { 11 } else { 10 }))?;
    map.serialize_entry("author", &event.author)?;
    map.serialize_entry("body", &body)?;
    map.serialize_entry("id", &event.id)?;
    map.serialize_entry("kind", &event.kind)?;
    map.serialize_entry("lamport", &event.lamport)?;
    map.serialize_entry("prev", &event.prev)?;
    map.serialize_entry("room", &event.room)?;
    map.serialize_entry("seq", &event.seq)?;
    if let Some(signature) = signature {
        map.serialize_entry("sig", signature)?;
    }
    map.serialize_entry("ts", &event.ts)?;
    map.serialize_entry("v", &event.v)?;
    map.end()?;
    Ok(escape_go_json(output))
}

/// Go's JSON validator rejects nesting beyond 10,000 containers. RawValue's
/// skip parser does not enforce that limit; count only delimiters outside strings.
fn check_json_depth(data: &[u8]) -> Result<(), serde_json::Error> {
    let mut depth: usize = 0;
    let mut in_string = false;
    let mut escaped = false;
    for &byte in data {
        if in_string {
            if escaped {
                escaped = false;
            } else if byte == b'\\' {
                escaped = true;
            } else if byte == b'"' {
                in_string = false;
            }
            continue;
        }
        match byte {
            b'"' => in_string = true,
            b'[' | b'{' => {
                depth += 1;
                if depth > 10_000 {
                    return Err(serde_json::Error::io(io::Error::new(
                        io::ErrorKind::InvalidData,
                        format!("invalid character '{}' exceeded max depth", byte as char),
                    )));
                }
            }
            b']' | b'}' => depth = depth.saturating_sub(1),
            _ => {}
        }
    }
    Ok(())
}

fn compact_json(raw: &str) -> String {
    let mut compact = String::with_capacity(raw.len());
    let mut in_string = false;
    let mut escaped = false;
    for ch in raw.chars() {
        if in_string {
            compact.push(ch);
            if escaped {
                escaped = false;
            } else if ch == '\\' {
                escaped = true;
            } else if ch == '"' {
                in_string = false;
            }
        } else if ch == '"' {
            in_string = true;
            compact.push(ch);
        } else if !matches!(ch, ' ' | '\n' | '\r' | '\t') {
            compact.push(ch);
        }
    }
    compact
}

/// Go `encoding/json` escapes HTML-sensitive bytes and Unicode line separators
/// even in a raw JSON body. These sequences only appear inside JSON strings.
fn escape_go_json(data: Vec<u8>) -> Vec<u8> {
    let mut escaped = Vec::with_capacity(data.len());
    let mut i = 0;
    while i < data.len() {
        let replacement: Option<&[u8]> = match data[i] {
            b'<' => Some(br"\u003c"),
            b'>' => Some(br"\u003e"),
            b'&' => Some(br"\u0026"),
            0xe2 if data.get(i..i + 3) == Some(&[0xe2, 0x80, 0xa8]) => Some(br"\u2028"),
            0xe2 if data.get(i..i + 3) == Some(&[0xe2, 0x80, 0xa9]) => Some(br"\u2029"),
            _ => None,
        };
        if let Some(replacement) = replacement {
            escaped.extend_from_slice(replacement);
            i += if data[i] == 0xe2 { 3 } else { 1 };
        } else {
            escaped.push(data[i]);
            i += 1;
        }
    }
    escaped
}

/// The standard base64 alphabet with padding, matching Go's `base64.StdEncoding`.
fn base64_encode(data: &[u8]) -> String {
    const ALPHABET: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";
    let mut out = String::with_capacity(data.len().div_ceil(3) * 4);
    for chunk in data.chunks(3) {
        let b0 = chunk[0] as u32;
        let b1 = *chunk.get(1).unwrap_or(&0) as u32;
        let b2 = *chunk.get(2).unwrap_or(&0) as u32;
        let triple = (b0 << 16) | (b1 << 8) | b2;
        out.push(ALPHABET[(triple >> 18) as usize & 0x3f] as char);
        out.push(ALPHABET[(triple >> 12) as usize & 0x3f] as char);
        if chunk.len() > 1 {
            out.push(ALPHABET[(triple >> 6) as usize & 0x3f] as char);
        } else {
            out.push('=');
        }
        if chunk.len() > 2 {
            out.push(ALPHABET[triple as usize & 0x3f] as char);
        } else {
            out.push('=');
        }
    }
    out
}

fn base64_decode(text: &str) -> Option<Vec<u8>> {
    let table = |byte: u8| -> Option<u32> {
        match byte {
            b'A'..=b'Z' => Some(u32::from(byte - b'A')),
            b'a'..=b'z' => Some(u32::from(byte - b'a') + 26),
            b'0'..=b'9' => Some(u32::from(byte - b'0') + 52),
            b'+' => Some(62),
            b'/' => Some(63),
            _ => None,
        }
    };
    // Go's StdEncoding ignores CR/LF anywhere in the encoded text.
    let bytes: Vec<u8> = text
        .bytes()
        .filter(|byte| *byte != b'\r' && *byte != b'\n')
        .collect();
    if !bytes.len().is_multiple_of(4) {
        return None;
    }
    let mut out = Vec::with_capacity(bytes.len() / 4 * 3);
    for (chunk_index, chunk) in bytes.chunks(4).enumerate() {
        let mut values = [0u32; 4];
        let mut padding = 0;
        for (index, byte) in chunk.iter().enumerate() {
            if *byte == b'=' {
                if index < 2 {
                    return None;
                }
                padding += 1;
                values[index] = 0;
                continue;
            }
            if padding > 0 {
                return None;
            }
            values[index] = table(*byte)?;
        }
        // Padding is only legal in the last quartet; otherwise the same
        // signature bytes can be written with multiple envelopes.
        if padding > 0 && chunk_index + 1 != bytes.len() / 4 {
            return None;
        }
        let triple = (values[0] << 18) | (values[1] << 12) | (values[2] << 6) | values[3];
        out.push((triple >> 16) as u8);
        if padding < 2 {
            out.push((triple >> 8) as u8);
        }
        if padding == 0 {
            out.push(triple as u8);
        }
    }
    Some(out)
}
