//! Go `internal/room/members` role decisions and journal projection.
//! This is a projection of already accepted events, not signature validation.

use std::collections::BTreeMap;

use serde::Deserialize;
use serde_json::value::RawValue;

use crate::event::Event;

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

#[derive(Debug, Default)]
pub struct State {
    pub members: BTreeMap<String, Member>,
}

#[derive(Default, Deserialize)]
#[serde(default)]
struct MemberBody {
    #[serde(deserialize_with = "null_as_empty")]
    id: String,
    #[serde(deserialize_with = "null_as_empty")]
    name: String,
    #[serde(deserialize_with = "null_as_empty")]
    public_key: String,
    #[serde(deserialize_with = "null_as_empty")]
    role: String,
    #[serde(deserialize_with = "null_as_empty")]
    kind: String,
}

fn null_as_empty<'de, D: serde::Deserializer<'de>>(decoder: D) -> Result<String, D::Error> {
    Ok(Option::<String>::deserialize(decoder)?.unwrap_or_default())
}

impl State {
    /// Replay a Go room event after the caller has authenticated its signature.
    pub fn apply_event(&mut self, event: &Event) -> Result<(), String> {
        match event.kind.as_str() {
            "room.created" => {
                let body: MemberBody = parse_body(&event.body, "room.created")?;
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
                let body: MemberBody = parse_body(&event.body, &event.kind)?;
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
    serde_json::from_str(body.get()).map_err(|error| format!("unmarshal {kind} body: {error}"))
}

fn decode_key(text: &str, field: &str) -> Result<String, String> {
    let bytes = hex::decode(text).map_err(|error| format!("invalid {field} pubkey: {error}"))?;
    if bytes.len() != 32 {
        return Err(format!("invalid {field} pubkey: %!w(<nil>)"));
    }
    Ok(hex::encode(bytes))
}
