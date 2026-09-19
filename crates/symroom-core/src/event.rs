#![deny(unsafe_code)]

//! Signed room events: the canonical byte encoding, the signature envelope and
//! the JSON line format. Port of `internal/room/event`.

use std::fmt;

use serde_json::{Map, Value};

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

/// Go: `event.Event`. The body is kept as parsed JSON so the canonical encoder
/// can re-emit it verbatim.
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
    pub body: Value,
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
/// `serde_json` is built with `preserve_order` so the body keeps the key order
/// it was parsed with, exactly like Go's `json.RawMessage` does; the outer keys
/// are therefore inserted in sorted order by hand and must stay that way.
///
/// ponytail: Go's encoder rewrites `<`, `>` and `&` inside string values to
/// `\u003c` and friends; the port keeps the bytes it was given. Extend this with
/// Go's HTML escaping when a room, id or body may carry those characters
/// (nothing in the current event kinds does).
pub fn canonical_bytes(event: &Event) -> Result<Vec<u8>, EventError> {
    let map = canonical_map(event, None);
    serde_json::to_vec(&Value::Object(map))
        .map_err(|err| EventError::Message(format!("canonical encoding: {err}")))
}

impl Event {
    /// Go: `(*Event).Sign`. Fills a missing version and replaces the signature.
    pub fn sign(&mut self, signer: &Identity) -> Result<(), EventError> {
        if self.v == 0 {
            self.v = CURRENT_VERSION;
        }
        let canonical = canonical_bytes(self)?;
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
        let canonical = canonical_bytes(self)?;
        if !identity::verify(public_key, &canonical, &signature) {
            return Err(EventError::Signature(INVALID_SIGNATURE.to_owned()));
        }
        Ok(())
    }

    /// Go: `(*Event).MarshalJSONLine` — one JSON object with the signature, keys
    /// sorted the same way, terminated by a newline.
    pub fn marshal_json_line(&self) -> Result<Vec<u8>, EventError> {
        let map = canonical_map(self, Some(self.sig.clone().unwrap_or_default()));
        let mut data = serde_json::to_vec(&Value::Object(map))
            .map_err(|err| EventError::Message(err.to_string()))?;
        data.push(b'\n');
        Ok(data)
    }

    /// Go: `event.UnmarshalJSONLine`.
    pub fn unmarshal_json_line(data: &[u8]) -> Result<Self, EventError> {
        serde_json::from_slice(data).map_err(|err| EventError::Message(err.to_string()))
    }
}

/// Go's `encoding/json` emits struct fields in declaration order for the event
/// itself, but the canonical and line encodings are built from a map and are
/// therefore sorted. `preserve_order` keeps that order explicit here.
fn canonical_map(event: &Event, signature: Option<String>) -> Map<String, Value> {
    let mut map = Map::new();
    map.insert("author".to_owned(), Value::from(event.author.clone()));
    map.insert("body".to_owned(), event.body.clone());
    map.insert("id".to_owned(), Value::from(event.id.clone()));
    map.insert("kind".to_owned(), Value::from(event.kind.clone()));
    map.insert("lamport".to_owned(), Value::from(event.lamport));
    map.insert("prev".to_owned(), Value::from(event.prev.clone()));
    map.insert("room".to_owned(), Value::from(event.room.clone()));
    map.insert("seq".to_owned(), Value::from(event.seq));
    if let Some(signature) = signature {
        map.insert("sig".to_owned(), Value::from(signature));
    }
    map.insert("ts".to_owned(), Value::from(event.ts.clone()));
    map.insert("v".to_owned(), Value::from(event.v));
    map
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
    let bytes = text.as_bytes();
    if !bytes.len().is_multiple_of(4) {
        return None;
    }
    let mut out = Vec::with_capacity(bytes.len() / 4 * 3);
    for chunk in bytes.chunks(4) {
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
