//! Relate's own storage of reviewed SymMemory entity links. This module only
//! owns the local mapping (an opaque entity id and type, never a name or
//! memory content); it never talks to SymMemory itself.
use crate::contact::{Entity, Error, error, sql_error, wrapped};
use rusqlite::{Connection, OptionalExtension, params};
use serde::{Deserialize, Serialize};

#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default, rename_all = "PascalCase")]
pub struct LinkInput {
    #[serde(rename = "MemoryEntityID")]
    pub memory_entity_id: String,
    pub memory_entity_type: String,
    pub linked_by: String,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "PascalCase")]
pub struct Link {
    #[serde(rename = "ID")]
    pub id: String,
    #[serde(rename = "PersonID")]
    pub person_id: String,
    #[serde(rename = "OrganizationID")]
    pub organization_id: String,
    #[serde(rename = "MemoryEntityID")]
    pub memory_entity_id: String,
    pub memory_entity_type: String,
    pub linked_by: String,
    pub created_at: String,
}

pub struct Service<'a, I, T> {
    db: &'a Connection,
    id: I,
    timestamp: T,
}

fn column(entity: Entity) -> &'static str {
    match entity {
        Entity::Person => "person_id",
        Entity::Organization => "organization_id",
    }
}

impl<'a, I: FnMut() -> String, T: FnMut() -> String> Service<'a, I, T> {
    pub fn new(db: &'a Connection, id: I, timestamp: T) -> Self {
        Self { db, id, timestamp }
    }

    /// Records a reviewed link. A contact has at most one link: unlink first
    /// to replace it, so a link is never silently overwritten.
    ///
    /// # Errors
    /// Returns Go's invalid/conflict/internal errors.
    pub fn link(&mut self, entity: Entity, id: &str, input: &LinkInput) -> Result<Link, Error> {
        let op = "memorylink.link";
        if input.memory_entity_id.trim().is_empty() {
            return Err(error("invalid", op, "memory entity id must not be empty"));
        }
        if input.linked_by.trim().is_empty() {
            return Err(error("invalid", op, "linked_by must not be empty"));
        }
        let link_id = (self.id)();
        let ts = (self.timestamp)();
        let column = column(entity);
        let outcome = self.db.execute(
            &format!("INSERT INTO memory_links (id, {column}, memory_entity_id, memory_entity_type, linked_by, created_at) VALUES (?, ?, ?, ?, ?, ?)"),
            params![link_id, id, input.memory_entity_id, input.memory_entity_type, input.linked_by, ts],
        );
        if let Err(e) = outcome {
            let cause = sql_error(e);
            return Err(if cause.message.contains("UNIQUE constraint failed") {
                error(
                    "conflict",
                    op,
                    &format!(
                        "already linked to a SymMemory entity; unlink first to replace it: {cause}"
                    ),
                )
            } else if cause.message.contains("FOREIGN KEY constraint failed") {
                error("invalid", op, &format!("entity does not exist: {cause}"))
            } else {
                error(
                    "internal",
                    op,
                    &format!("failed to insert memory link: {cause}"),
                )
            });
        }
        self.get(entity, id)?
            .ok_or_else(|| error("internal", op, "link vanished after insert"))
    }

    /// The link for one contact, or `None` when unlinked.
    ///
    /// # Errors
    /// Returns the internal load error.
    pub fn get(&self, entity: Entity, id: &str) -> Result<Option<Link>, Error> {
        self.db
            .query_row(
                &format!("SELECT id, COALESCE(person_id, ''), COALESCE(organization_id, ''), memory_entity_id, memory_entity_type, linked_by, created_at FROM memory_links WHERE {} = ?", column(entity)),
                [id],
                |r| {
                    Ok(Link {
                        id: r.get(0)?,
                        person_id: r.get(1)?,
                        organization_id: r.get(2)?,
                        memory_entity_id: r.get(3)?,
                        memory_entity_type: r.get(4)?,
                        linked_by: r.get(5)?,
                        created_at: r.get(6)?,
                    })
                },
            )
            .optional()
            .map_err(|e| wrapped("memorylink.get", "failed to load memory link", e))
    }

    /// Removes a contact's link; unlinking an unlinked contact is a no-op.
    ///
    /// # Errors
    /// Returns the internal delete error.
    pub fn unlink(&self, entity: Entity, id: &str) -> Result<(), Error> {
        self.db
            .execute(
                &format!("DELETE FROM memory_links WHERE {} = ?", column(entity)),
                [id],
            )
            .map(|_| ())
            .map_err(|e| wrapped("memorylink.unlink", "failed to remove memory link", e))
    }
}
