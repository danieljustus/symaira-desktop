//! Go `internal/room/members` role decisions and journal projection.
//! This is a projection of already accepted events, not signature validation.

use std::borrow::Cow;
use std::collections::BTreeMap;
use std::{fmt, path::Path};

use serde::de::{DeserializeSeed, IgnoredAny, MapAccess, Visitor};
use serde_json::value::RawValue;

use crate::{
    event::{self, Event},
    identity::Identity,
    journal,
};

#[derive(Debug, Clone, PartialEq, Eq, serde::Serialize, serde::Deserialize)]
pub struct Member {
    pub id: String,
    pub name: String,
    pub public_key: String,
    pub role: String,
    pub kind: String,
}

impl Member {
    /// Match Go `(*Member).CanPerform`, including the owner's unknown-action behavior.
    pub fn can_perform(&self, action: &str) -> bool {
        match self.role.as_str() {
            "owner" => true,
            "member" => matches!(action, "approve" | "post_link" | "request_run"),
            "agent" => matches!(action, "post_link" | "request_run"),
            _ => false,
        }
    }
}

#[derive(Clone, Debug, Default, Eq, PartialEq)]
pub struct State {
    pub members: BTreeMap<String, Member>,
}

#[derive(Default)]
struct MemberBody {
    id: String,
    name: String,
    public_key: String,
    role: String,
    kind: String,
}

struct MemberBodySeed<'a>(&'a str);

impl<'de> DeserializeSeed<'de> for MemberBodySeed<'_> {
    type Value = MemberBody;

    fn deserialize<D: serde::Deserializer<'de>>(self, decoder: D) -> Result<Self::Value, D::Error> {
        decoder.deserialize_any(MemberBodyVisitor(self.0))
    }
}

struct MemberBodyVisitor<'a>(&'a str);

impl<'de> Visitor<'de> for MemberBodyVisitor<'_> {
    type Value = MemberBody;

    fn expecting(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str("a membership event body or null")
    }

    fn visit_unit<E: serde::de::Error>(self) -> Result<Self::Value, E> {
        Ok(MemberBody::default())
    }

    fn visit_map<M: MapAccess<'de>>(self, mut map: M) -> Result<Self::Value, M::Error> {
        let mut body = MemberBody::default();
        while let Some(key) = map.next_key::<String>()? {
            // Go folds JSON struct field names and the last matching key wins,
            // even when its casing differs from an earlier key.
            let field = key.to_lowercase();
            let relevant = match self.0 {
                "room.created" => matches!(field.as_str(), "name" | "public_key"),
                "member.added" => {
                    matches!(
                        field.as_str(),
                        "id" | "name" | "public_key" | "role" | "kind"
                    )
                }
                "member.removed" => field == "id",
                "member.role_changed" => matches!(field.as_str(), "id" | "role"),
                _ => false,
            };
            if !relevant {
                let _: IgnoredAny = map.next_value()?;
                continue;
            }
            // Go leaves an existing string untouched when a duplicate key is null.
            let Some(value): Option<String> = map.next_value()? else {
                continue;
            };
            match field.as_str() {
                "id" => body.id = value,
                "name" => body.name = value,
                "public_key" => body.public_key = value,
                "role" => body.role = value,
                "kind" => body.kind = value,
                _ => unreachable!(),
            }
        }
        Ok(body)
    }
}

impl State {
    /// Replay a Go room event after the caller has authenticated its signature.
    pub fn apply_event(&mut self, event: &Event) -> Result<(), String> {
        match event.kind.as_str() {
            "room.created" => {
                let body = parse_body(&event.body, "room.created")?;
                let public_key = decode_key(&body.public_key, "root")?;
                self.members.insert(
                    event.author.clone(),
                    Member {
                        id: event.author.clone(),
                        name: body.name,
                        public_key,
                        role: "owner".into(),
                        kind: "human".into(),
                    },
                );
            }
            "member.added" | "member.removed" | "member.role_changed" => {
                if self
                    .members
                    .get(&event.author)
                    .is_none_or(|m| m.role != "owner")
                {
                    return Err("only room owners can perform member management".into());
                }
                let body = parse_body(&event.body, &event.kind)?;
                match event.kind.as_str() {
                    "member.added" => {
                        let public_key = decode_key(&body.public_key, "member")?;
                        self.members.insert(
                            body.id.clone(),
                            Member {
                                id: body.id,
                                name: body.name,
                                public_key,
                                role: body.role,
                                kind: body.kind,
                            },
                        );
                    }
                    "member.removed" => {
                        self.members.remove(&body.id);
                    }
                    "member.role_changed" => {
                        if let Some(member) = self.members.get_mut(&body.id) {
                            member.role = body.role;
                        }
                    }
                    _ => unreachable!(),
                }
            }
            "run.approved" | "checkpoint.resolved" => match self.members.get(&event.author) {
                None => return Err("member not found".into()),
                Some(member) if member.role == "agent" => {
                    return Err(
                        "agent role is strictly forbidden from signing approval events".into(),
                    );
                }
                Some(member) if member.role == "observer" => {
                    return Err("observer role has read-only access".into());
                }
                _ => {}
            },
            _ => {}
        }
        Ok(())
    }
}

fn parse_body(body: &RawValue, kind: &str) -> Result<MemberBody, String> {
    // Go's encoding/json replaces lone surrogate escapes with U+FFFD while
    // decoding strings. Keep the signed RawMessage untouched: this copy is
    // only for interpreting membership fields after signature verification.
    let decoded = replace_unpaired_surrogates(body.get());
    let mut decoder = serde_json::Deserializer::from_str(&decoded);
    let parsed = MemberBodySeed(kind)
        .deserialize(&mut decoder)
        .map_err(|error| format!("unmarshal {kind} body: {error}"))?;
    decoder
        .end()
        .map_err(|error| format!("unmarshal {kind} body: {error}"))?;
    Ok(parsed)
}

pub(crate) fn replace_unpaired_surrogates(raw: &str) -> Cow<'_, str> {
    let bytes = raw.as_bytes();
    let mut positions = Vec::new();
    let mut in_string = false;
    let mut i = 0;
    while i < bytes.len() {
        match bytes[i] {
            b'"' => {
                in_string = !in_string;
                i += 1;
            }
            b'\\' if in_string => {
                if bytes.get(i + 1) == Some(&b'u')
                    && let Some(unit) = bytes.get(i + 2..i + 6).and_then(hex_unit)
                {
                    let paired = (0xd800..=0xdbff).contains(&unit)
                        && bytes.get(i + 6..i + 8) == Some(br"\u")
                        && bytes
                            .get(i + 8..i + 12)
                            .and_then(hex_unit)
                            .is_some_and(|low| (0xdc00..=0xdfff).contains(&low));
                    if paired {
                        i += 12;
                        continue;
                    }
                    if (0xd800..=0xdfff).contains(&unit) {
                        positions.push(i);
                    }
                    i += 6;
                    continue;
                }
                i += 2; // Also skips an escaped backslash before literal `u`.
            }
            _ => i += 1,
        }
    }
    if positions.is_empty() {
        return Cow::Borrowed(raw);
    }
    let mut output = bytes.to_vec();
    for position in positions {
        output[position..position + 6].copy_from_slice(br"\ufffd");
    }
    Cow::Owned(String::from_utf8(output).expect("ASCII replacement preserves UTF-8"))
}

fn hex_unit(bytes: &[u8]) -> Option<u16> {
    u16::from_str_radix(std::str::from_utf8(bytes).ok()?, 16).ok()
}

fn decode_key(text: &str, field: &str) -> Result<String, String> {
    // Go reports the first invalid byte even when the hex string is odd-length.
    let decoded = match text.bytes().position(|byte| !byte.is_ascii_hexdigit()) {
        Some(index) => Err(hex::FromHexError::InvalidHexCharacter {
            c: char::from(text.as_bytes()[index]),
            index,
        }),
        None => hex::decode(text),
    };
    let bytes = decoded.map_err(|error| {
        let detail = match error {
            hex::FromHexError::OddLength => "encoding/hex: odd length hex string".to_owned(),
            hex::FromHexError::InvalidHexCharacter { c, .. } => {
                if c.is_control() || c == '\u{ad}' {
                    format!("encoding/hex: invalid byte: U+{:04X}", c as u32)
                } else {
                    format!("encoding/hex: invalid byte: U+{:04X} '{c}'", c as u32)
                }
            }
            hex::FromHexError::InvalidStringLength => error.to_string(),
        };
        format!("invalid {field} pubkey: {detail}")
    })?;
    if bytes.len() != 32 {
        return Err(format!("invalid {field} pubkey: %!w(<nil>)"));
    }
    Ok(hex::encode(bytes))
}

/// Go `room.AddMember`: validate the request, authorize the caller, then append
/// one owner-signed `member.added` event.
pub fn add_member(
    room_dir: &Path,
    name: &str,
    public_key_hex: &str,
    role: &str,
    kind: &str,
    signer: &Identity,
) -> Result<Event, MemberMutationError> {
    let public_key = hex::decode(public_key_hex).map_err(|error| {
        MemberMutationError::InvalidPublicKeyHex(go_hex_error(error, public_key_hex))
    })?;
    if public_key.len() != 32 {
        return Err(MemberMutationError::InvalidPublicKeyLength(
            public_key.len(),
        ));
    }
    if !valid_role(role) {
        return Err(MemberMutationError::InvalidRole);
    }
    if !valid_kind(kind) {
        return Err(MemberMutationError::InvalidKind);
    }

    let stats = journal::read_journal_stats(room_dir)?;
    require_owner(&stats, signer)?;
    let mut body = BTreeMap::new();
    body.insert("id", crate::identity::compute_member_id(&public_key));
    body.insert("name", name.to_owned());
    body.insert("public_key", public_key_hex.to_owned());
    body.insert("role", role.to_owned());
    body.insert("kind", kind.to_owned());
    let body = go_json(&body)?;
    append_member_event(room_dir, signer, &stats, "member.added", body)
}

/// Go `room.RemoveMember`: require an owner and an existing target, then append
/// `member.removed`.
pub fn remove_member(
    room_dir: &Path,
    member_id: &str,
    signer: &Identity,
) -> Result<Event, MemberMutationError> {
    let stats = journal::read_journal_stats(room_dir)?;
    require_owner(&stats, signer)?;
    if !stats.member_state.members.contains_key(member_id) {
        return Err(MemberMutationError::NotFound);
    }
    let body = go_json(&BTreeMap::from([("id", member_id)]))?;
    append_member_event(room_dir, signer, &stats, "member.removed", body)
}

/// Go `room.SetMemberRole`: validate role before owner and target checks, then
/// append `member.role_changed`.
pub fn set_member_role(
    room_dir: &Path,
    member_id: &str,
    role: &str,
    signer: &Identity,
) -> Result<Event, MemberMutationError> {
    if !valid_role(role) {
        return Err(MemberMutationError::InvalidRole);
    }
    let stats = journal::read_journal_stats(room_dir)?;
    require_owner(&stats, signer)?;
    if !stats.member_state.members.contains_key(member_id) {
        return Err(MemberMutationError::NotFound);
    }
    let body = go_json(&BTreeMap::from([("id", member_id), ("role", role)]))?;
    append_member_event(room_dir, signer, &stats, "member.role_changed", body)
}

/// Go `room.ListMembers` projection.
pub fn list_members(room_dir: &Path) -> Result<State, std::io::Error> {
    Ok(journal::read_journal_stats(room_dir)?.member_state)
}

fn require_owner(
    stats: &journal::JournalStats,
    signer: &Identity,
) -> Result<(), MemberMutationError> {
    if stats
        .member_state
        .members
        .get(&signer.member_id)
        .is_none_or(|member| member.role != "owner")
    {
        return Err(MemberMutationError::Unauthorized);
    }
    Ok(())
}

fn append_member_event(
    room_dir: &Path,
    signer: &Identity,
    stats: &journal::JournalStats,
    kind: &str,
    body: String,
) -> Result<Event, MemberMutationError> {
    let room_config = std::fs::read_to_string(room_dir.join("room.toml"))
        .map_err(|error| MemberMutationError::Message(format!("read room.toml: {error}")))?;
    let room_id = room_config
        .lines()
        .find_map(|line| {
            let (key, value) = line.split_once('=')?;
            (key.trim() == "id").then(|| value.trim().trim_matches('"').to_owned())
        })
        .unwrap_or_default();
    let author = journal::author_stats(room_dir, &signer.member_id)?;
    let mut event_id = [0_u8; 10];
    getrandom::fill(&mut event_id)
        .map_err(|error| MemberMutationError::Message(format!("generate event id: {error}")))?;
    let event_id = format!("ev_{}", hex::encode(event_id));
    let body = RawValue::from_string(body)
        .map_err(|error| MemberMutationError::Message(format!("marshal {kind} body: {error}")))?;
    let mut event = Event {
        v: event::CURRENT_VERSION,
        id: event_id,
        room: room_id,
        author: signer.member_id.clone(),
        seq: author.seq.saturating_add(1),
        prev: author.prev,
        lamport: stats.max_lamport.saturating_add(1),
        ts: event::current_timestamp(),
        kind: kind.to_owned(),
        body,
        sig: None,
    };
    event.sign(signer)?;
    journal::append_event(room_dir, &event)?;
    Ok(event)
}

fn valid_role(role: &str) -> bool {
    matches!(role, "owner" | "member" | "agent" | "observer")
}

fn valid_kind(kind: &str) -> bool {
    matches!(kind, "human" | "agent")
}

fn go_json(value: &impl serde::Serialize) -> Result<String, MemberMutationError> {
    let rendered = serde_json::to_string(value)
        .map_err(|error| MemberMutationError::Message(error.to_string()))?;
    Ok(rendered
        .replace('&', "\\u0026")
        .replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029"))
}

fn go_hex_error(error: hex::FromHexError, text: &str) -> String {
    match error {
        hex::FromHexError::OddLength => "encoding/hex: odd length hex string".to_owned(),
        hex::FromHexError::InvalidHexCharacter { c, index } => {
            let is_control = text.as_bytes().get(index).is_some_and(u8::is_ascii_control);
            if is_control || c == '\u{00ad}' {
                format!("encoding/hex: invalid byte: U+{:04X}", c as u32)
            } else {
                format!("encoding/hex: invalid byte: U+{:04X} '{c}'", c as u32)
            }
        }
        hex::FromHexError::InvalidStringLength => "encoding/hex: invalid string length".to_owned(),
    }
}

#[derive(Debug)]
pub enum MemberMutationError {
    InvalidPublicKeyHex(String),
    InvalidPublicKeyLength(usize),
    InvalidRole,
    InvalidKind,
    Unauthorized,
    NotFound,
    Io(std::io::Error),
    Journal(journal::JournalError),
    Event(crate::event::EventError),
    Message(String),
}

impl fmt::Display for MemberMutationError {
    fn fmt(&self, formatter: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::InvalidPublicKeyHex(detail) => {
                write!(formatter, "invalid member public key hex: {detail}")
            }
            Self::InvalidPublicKeyLength(length) => write!(
                formatter,
                "invalid member public key: expected 32 bytes, got {length}"
            ),
            Self::InvalidRole => formatter.write_str("invalid member role"),
            Self::InvalidKind => formatter.write_str("invalid member kind"),
            Self::Unauthorized => {
                formatter.write_str("only room owners can perform member management")
            }
            Self::NotFound => formatter.write_str("member not found"),
            Self::Io(error) => error.fmt(formatter),
            Self::Journal(error) => error.fmt(formatter),
            Self::Event(error) => error.fmt(formatter),
            Self::Message(message) => formatter.write_str(message),
        }
    }
}

impl std::error::Error for MemberMutationError {}

impl From<std::io::Error> for MemberMutationError {
    fn from(error: std::io::Error) -> Self {
        Self::Io(error)
    }
}

impl From<journal::JournalError> for MemberMutationError {
    fn from(error: journal::JournalError) -> Self {
        Self::Journal(error)
    }
}

impl From<crate::event::EventError> for MemberMutationError {
    fn from(error: crate::event::EventError) -> Self {
        Self::Event(error)
    }
}
