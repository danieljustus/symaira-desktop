//! Directed links, immutable interactions and follow-up lifecycle.
//!
//! Timestamp inputs must be UTC RFC3339Nano values (as produced by Go's
//! formatTime); clock/ID acquisition belongs to the caller. No external data is
//! resolved or copied by these APIs.
use crate::contact::{Entity, Error, sql_error};
use rusqlite::{Connection, OptionalExtension, params, params_from_iter};
use serde::{Deserialize, Serialize};

fn error(kind: &'static str, op: &str, message: &str) -> Error {
    Error {
        kind,
        message: format!("{op}: {message}"),
    }
}
fn wrapped(kind: &'static str, op: &str, message: &str, e: rusqlite::Error) -> Error {
    error(kind, op, &format!("{message}: {}", sql_error(e)))
}
fn column(entity: Entity) -> &'static str {
    match entity {
        Entity::Person => "person_id",
        Entity::Organization => "organization_id",
    }
}
fn nonempty<T>(v: Vec<T>) -> Option<Vec<T>> {
    if v.is_empty() { None } else { Some(v) }
}
fn zero(t: &str) -> bool {
    t.is_empty() || t == "0001-01-01T00:00:00Z"
}

#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default, rename_all = "PascalCase")]
pub struct RelationshipInput {
    #[serde(rename = "FromPersonID")]
    pub from_person_id: String,
    #[serde(rename = "ToPersonID")]
    pub to_person_id: String,
    #[serde(rename = "ToOrganizationID")]
    pub to_organization_id: String,
    #[serde(rename = "Type")]
    pub relationship_type: String,
    pub valid_from: Option<String>,
    pub valid_to: Option<String>,
    pub notes: String,
}
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "PascalCase")]
pub struct Relationship {
    #[serde(rename = "ID")]
    pub id: String,
    #[serde(rename = "FromPersonID")]
    pub from_person_id: String,
    #[serde(rename = "ToPersonID")]
    pub to_person_id: String,
    #[serde(rename = "ToOrganizationID")]
    pub to_organization_id: String,
    #[serde(rename = "Type")]
    pub relationship_type: String,
    pub valid_from: Option<String>,
    pub valid_to: Option<String>,
    pub notes: String,
    pub created_at: String,
    pub updated_at: String,
}
#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default, rename_all = "PascalCase")]
pub struct InteractionInput {
    pub kind: String,
    pub occurred_at: String,
    pub summary: String,
    pub external_ref: String,
}
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "PascalCase")]
pub struct Interaction {
    #[serde(rename = "ID")]
    pub id: String,
    pub kind: String,
    pub occurred_at: String,
    pub summary: String,
    pub external_ref: String,
    pub created_at: String,
}
#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default, rename_all = "PascalCase")]
pub struct FollowUpInput {
    pub due_at: String,
    pub notes: String,
}
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "PascalCase")]
pub struct FollowUp {
    #[serde(rename = "ID")]
    pub id: String,
    pub due_at: String,
    pub notes: String,
    pub status: String,
    pub completed_at: Option<String>,
    pub cancelled_at: Option<String>,
    pub created_at: String,
    pub updated_at: String,
}
#[derive(Debug, Clone, Copy)]
pub enum Direction {
    Outgoing,
    IncomingPerson,
    IncomingOrganization,
}
#[derive(Debug, Clone, Copy)]
pub enum Transition {
    Complete,
    Cancel,
}

const REL_SELECT: &str = "SELECT id,from_person_id,to_person_id,to_organization_id,relationship_type,valid_from,valid_to,notes,created_at,updated_at FROM relationships";
const FOLLOW_SELECT: &str =
    "SELECT id,due_at,notes,status,completed_at,cancelled_at,created_at,updated_at FROM follow_ups";
const INTERACTION_SELECT: &str =
    "SELECT id,kind,occurred_at,summary,external_ref,created_at FROM interactions";
fn relationship_row(r: &rusqlite::Row<'_>) -> rusqlite::Result<Relationship> {
    Ok(Relationship {
        id: r.get(0)?,
        from_person_id: r.get(1)?,
        to_person_id: r.get::<_, Option<String>>(2)?.unwrap_or_default(),
        to_organization_id: r.get::<_, Option<String>>(3)?.unwrap_or_default(),
        relationship_type: r.get(4)?,
        valid_from: r.get(5)?,
        valid_to: r.get(6)?,
        notes: r.get(7)?,
        created_at: r.get(8)?,
        updated_at: r.get(9)?,
    })
}
fn followup_row(r: &rusqlite::Row<'_>) -> rusqlite::Result<FollowUp> {
    Ok(FollowUp {
        id: r.get(0)?,
        due_at: r.get(1)?,
        notes: r.get(2)?,
        status: r.get(3)?,
        completed_at: r.get(4)?,
        cancelled_at: r.get(5)?,
        created_at: r.get(6)?,
        updated_at: r.get(7)?,
    })
}
fn interaction_row(r: &rusqlite::Row<'_>) -> rusqlite::Result<Interaction> {
    Ok(Interaction {
        id: r.get(0)?,
        kind: r.get(1)?,
        occurred_at: r.get(2)?,
        summary: r.get(3)?,
        external_ref: r.get::<_, Option<String>>(4)?.unwrap_or_default(),
        created_at: r.get(5)?,
    })
}

pub struct Service<'a, I, T> {
    db: &'a Connection,
    id: I,
    timestamp: T,
}
impl<'a, I: FnMut() -> String, T: FnMut() -> String> Service<'a, I, T> {
    pub fn new(db: &'a Connection, id: I, timestamp: T) -> Self {
        Self { db, id, timestamp }
    }
    pub fn add_relationship(&mut self, input: &RelationshipInput) -> Result<Relationship, Error> {
        let op = "relationship.AddRelationship";
        if input.from_person_id.trim().is_empty() {
            return Err(error("invalid", op, "from person id must not be empty"));
        }
        if input.relationship_type.trim().is_empty() {
            return Err(error("invalid", op, "relationship type must not be empty"));
        }
        if input.to_person_id.is_empty() == input.to_organization_id.is_empty() {
            return Err(error(
                "invalid",
                op,
                "exactly one of to-person or to-organization must be set",
            ));
        }
        let id = (self.id)();
        let ts = (self.timestamp)();
        let result=self.db.execute("INSERT INTO relationships(id,from_person_id,to_person_id,to_organization_id,relationship_type,valid_from,valid_to,notes,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)",params![id,input.from_person_id,if input.to_person_id.is_empty(){None}else{Some(&input.to_person_id)},if input.to_organization_id.is_empty(){None}else{Some(&input.to_organization_id)},input.relationship_type,input.valid_from,input.valid_to,input.notes,ts,ts]);
        if let Err(e) = result {
            let cause = sql_error(e);
            if cause.message.contains("UNIQUE constraint failed") {
                let (target, id) = if input.to_person_id.is_empty() {
                    ("to_organization_id", &input.to_organization_id)
                } else {
                    ("to_person_id", &input.to_person_id)
                };
                return self.db.query_row(&format!("{REL_SELECT} WHERE from_person_id=? AND relationship_type=? AND COALESCE(valid_from,'')=COALESCE(?,'') AND {target}=?"),params![input.from_person_id,input.relationship_type,input.valid_from,id],relationship_row).map_err(|e|wrapped("internal","relationship.findExistingRelationship","failed to load existing relationship",e));
            }
            let (kind, message) = if cause.message.contains("FOREIGN KEY constraint failed") {
                ("invalid", "from/to entity does not exist")
            } else if cause.message.contains("CHECK constraint failed") {
                (
                    "invalid",
                    "invalid relationship (self-reference or valid_to before valid_from)",
                )
            } else {
                ("internal", "failed to insert relationship")
            };
            return Err(error(kind, op, &format!("{message}: {cause}")));
        }
        self.db
            .query_row(&format!("{REL_SELECT} WHERE id=?"), [id], relationship_row)
            .map_err(|e| {
                wrapped(
                    "internal",
                    "relationship.getRelationship",
                    "failed to load relationship",
                    e,
                )
            })
    }
    pub fn list_relationships(
        &self,
        direction: Direction,
        id: &str,
    ) -> Result<Option<Vec<Relationship>>, Error> {
        let col = match direction {
            Direction::Outgoing => "from_person_id",
            Direction::IncomingPerson => "to_person_id",
            Direction::IncomingOrganization => "to_organization_id",
        };
        let rows = self
            .db
            .prepare(&format!(
                "{REL_SELECT} WHERE {col}=? ORDER BY created_at,id"
            ))
            .map_err(sql_error)?
            .query_map([id], relationship_row)
            .map_err(sql_error)?
            .collect::<rusqlite::Result<Vec<_>>>()
            .map_err(sql_error)?;
        Ok(nonempty(rows))
    }
    pub fn add_follow_up(
        &mut self,
        entity: Entity,
        parent: &str,
        input: &FollowUpInput,
    ) -> Result<FollowUp, Error> {
        let op = "relationship.addFollowUp";
        if zero(&input.due_at) {
            return Err(error("invalid", op, "due date must be set"));
        }
        let id = (self.id)();
        let ts = (self.timestamp)();
        self.db.execute(&format!("INSERT INTO follow_ups(id,{},due_at,notes,status,created_at,updated_at) VALUES(?,?,?,?,'open',?,?)",column(entity)),params![id,parent,input.due_at,input.notes,ts,ts]).map_err(|e|{
            let cause=sql_error(e);let (kind,message)=if cause.message.contains("FOREIGN KEY constraint failed"){("invalid","entity does not exist")}else{("internal","failed to insert follow-up")};error(kind,op,&format!("{message}: {cause}"))
        })?;
        self.get_follow_up(&id)
    }
    pub fn get_follow_up(&self, id: &str) -> Result<FollowUp, Error> {
        self.db
            .query_row(&format!("{FOLLOW_SELECT} WHERE id=?"), [id], followup_row)
            .optional()
            .map_err(|e| {
                wrapped(
                    "internal",
                    "relationship.getFollowUp",
                    "failed to load follow-up",
                    e,
                )
            })?
            .ok_or_else(|| {
                error(
                    "not_found",
                    "relationship.getFollowUp",
                    "follow-up not found",
                )
            })
    }
    pub fn transition_follow_up(
        &mut self,
        id: &str,
        transition: Transition,
    ) -> Result<FollowUp, Error> {
        let (status, col) = match transition {
            Transition::Complete => ("completed", "completed_at"),
            Transition::Cancel => ("cancelled", "cancelled_at"),
        };
        let ts = (self.timestamp)();
        let n=self.db.execute(&format!("UPDATE follow_ups SET status=?,{col}=?,updated_at=? WHERE id=? AND status='open'"),params![status,ts,ts,id]).map_err(|e|wrapped("internal","relationship.transitionFollowUp","failed to update follow-up",e))?;
        let existing = self.get_follow_up(id)?;
        if n == 0 {
            return Err(error(
                "conflict",
                "relationship.transitionFollowUp",
                &format!("follow-up is already {}", existing.status),
            ));
        }
        Ok(existing)
    }
    pub fn list_follow_ups(
        &mut self,
        entity: Entity,
        parent: &str,
        filter: &str,
    ) -> Result<Option<Vec<FollowUp>>, Error> {
        let mut sql = format!("{FOLLOW_SELECT} WHERE {}=?", column(entity));
        let mut args = vec![parent.to_owned()];
        match filter {
            "open" => sql.push_str(" AND status='open'"),
            "overdue" | "upcoming" => {
                sql.push_str(if filter == "overdue" {
                    " AND status='open' AND due_at < ?"
                } else {
                    " AND status='open' AND due_at >= ?"
                });
                args.push((self.timestamp)());
            }
            "all" | "" => {}
            _ => {
                return Err(error(
                    "invalid",
                    "relationship.listFollowUps",
                    &format!("unknown follow-up filter: {filter}"),
                ));
            }
        }
        sql.push_str(" ORDER BY due_at,id");
        let rows = self
            .db
            .prepare(&sql)
            .map_err(sql_error)?
            .query_map(params_from_iter(args), followup_row)
            .map_err(sql_error)?
            .collect::<rusqlite::Result<Vec<_>>>()
            .map_err(sql_error)?;
        Ok(nonempty(rows))
    }
    pub fn add_interaction(
        &mut self,
        entity: Entity,
        parent: &str,
        input: &InteractionInput,
    ) -> Result<Interaction, Error> {
        let op = "relationship.addInteraction";
        if !["note", "call", "email", "meeting", "external_reference"]
            .contains(&input.kind.as_str())
        {
            return Err(error(
                "invalid",
                op,
                &format!("unknown interaction kind: {}", input.kind),
            ));
        }
        if input.summary.trim().is_empty() {
            return Err(error("invalid", op, "summary must not be empty"));
        }
        if input.kind == "external_reference" && input.external_ref.trim().is_empty() {
            return Err(error(
                "invalid",
                op,
                "external_reference interactions require an external_ref",
            ));
        }
        let occurred_at = if zero(&input.occurred_at) {
            (self.timestamp)()
        } else {
            input.occurred_at.clone()
        };
        let id = (self.id)();
        let created_at = (self.timestamp)();
        self.db.execute(&format!("INSERT INTO interactions(id,{},kind,occurred_at,summary,external_ref,created_at) VALUES(?,?,?,?,?,?,?)",column(entity)),params![id,parent,input.kind,occurred_at,input.summary,input.external_ref,created_at]).map_err(|e|{
            let cause=sql_error(e);let (kind,message)=if cause.message.contains("CHECK constraint failed"){("invalid","external_reference interactions require an external_ref")}else if cause.message.contains("FOREIGN KEY constraint failed"){("invalid","entity does not exist")}else{("internal","failed to insert interaction")};error(kind,op,&format!("{message}: {cause}"))
        })?;
        Ok(Interaction {
            id,
            kind: input.kind.clone(),
            occurred_at,
            summary: input.summary.clone(),
            external_ref: input.external_ref.clone(),
            created_at,
        })
    }
    pub fn list_interactions(
        &self,
        entity: Entity,
        parent: &str,
    ) -> Result<Option<Vec<Interaction>>, Error> {
        let rows = self
            .db
            .prepare(&format!(
                "{INTERACTION_SELECT} WHERE {}=? ORDER BY occurred_at,id",
                column(entity)
            ))
            .map_err(sql_error)?
            .query_map([parent], interaction_row)
            .map_err(sql_error)?
            .collect::<rusqlite::Result<Vec<_>>>()
            .map_err(sql_error)?;
        Ok(nonempty(rows))
    }
    pub fn last_interaction(
        &self,
        entity: Entity,
        parent: &str,
    ) -> Result<Option<Interaction>, Error> {
        self.db
            .query_row(
                &format!(
                    "{INTERACTION_SELECT} WHERE {}=? ORDER BY occurred_at DESC,id DESC LIMIT 1",
                    column(entity)
                ),
                [parent],
                interaction_row,
            )
            .optional()
            .map_err(|e| {
                wrapped(
                    "internal",
                    "relationship.lastInteraction",
                    "failed to load last interaction",
                    e,
                )
            })
    }
}
