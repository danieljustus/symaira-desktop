//! Transactional privacy erasure. Audit entries carry only identifiers and
//! removal counts; actor, timestamp and ID acquisition are injected.
use crate::contact::{Entity, Error, sql_error};
use rusqlite::{Connection, params, params_from_iter};
use serde::Serialize;

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "PascalCase")]
pub struct EraseSummary {
    pub entity_type: String,
    #[serde(rename = "EntityID")]
    pub entity_id: String,
    pub contact_points_removed: i64,
    pub aliases_removed: i64,
    pub memberships_removed: i64,
    pub relationships_removed: i64,
    pub interactions_removed: i64,
    pub follow_ups_removed: i64,
}
impl EraseSummary {
    fn detail(&self) -> String {
        format!(
            "contact_points={} aliases={} memberships={} relationships={} interactions={} follow_ups={}",
            self.contact_points_removed,
            self.aliases_removed,
            self.memberships_removed,
            self.relationships_removed,
            self.interactions_removed,
            self.follow_ups_removed
        )
    }
}
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "PascalCase")]
pub struct AuditEvent {
    #[serde(rename = "ID")]
    pub id: String,
    pub occurred_at: String,
    pub operation: String,
    pub actor: String,
    pub entity_type: String,
    #[serde(rename = "EntityID")]
    pub entity_id: String,
    pub detail: String,
}

pub struct Service<'a, I, T, A> {
    db: &'a Connection,
    id: I,
    timestamp: T,
    actor: A,
}
impl<'a, I: FnMut() -> String, T: FnMut() -> String, A: FnMut() -> String> Service<'a, I, T, A> {
    pub fn new(db: &'a Connection, id: I, timestamp: T, actor: A) -> Self {
        Self {
            db,
            id,
            timestamp,
            actor,
        }
    }
    pub fn erase(&mut self, entity: Entity, id: &str) -> Result<EraseSummary, Error> {
        let (table, column, noun, op) = match entity {
            Entity::Person => ("persons", "person_id", "person", "security.EraseContact"),
            Entity::Organization => (
                "organizations",
                "organization_id",
                "organization",
                "security.EraseOrganization",
            ),
        };
        let wrap = |e: rusqlite::Error| Error {
            kind: "internal",
            message: format!("{op}: failed to erase {noun}: {}", sql_error(e)),
        };
        let tx = self.db.unchecked_transaction().map_err(wrap)?;
        let count = |table: &str| -> rusqlite::Result<i64> {
            tx.query_row(
                &format!("SELECT COUNT(*) FROM {table} WHERE {column}=?"),
                [id],
                |r| r.get(0),
            )
        };
        let (condition, ids) = if entity == Entity::Person {
            ("from_person_id=? OR to_person_id=?", vec![id, id])
        } else {
            ("to_organization_id=?", vec![id])
        };
        let relationships_removed = tx
            .query_row(
                &format!("SELECT COUNT(*) FROM relationships WHERE {condition}"),
                params_from_iter(ids),
                |r| r.get(0),
            )
            .map_err(wrap)?;
        let summary = EraseSummary {
            entity_type: noun.to_owned(),
            entity_id: id.to_owned(),
            contact_points_removed: count("contact_points").map_err(wrap)?,
            aliases_removed: count("aliases").map_err(wrap)?,
            memberships_removed: count("organization_memberships").map_err(wrap)?,
            relationships_removed,
            interactions_removed: count("interactions").map_err(wrap)?,
            follow_ups_removed: count("follow_ups").map_err(wrap)?,
        };
        let removed = tx
            .execute(&format!("DELETE FROM {table} WHERE id=?"), [id])
            .map_err(wrap)?;
        if removed == 0 {
            return Err(Error {
                kind: "not_found",
                message: format!("{op}: {noun} not found"),
            });
        }
        tx.execute("INSERT INTO audit_events(id,occurred_at,operation,actor,entity_type,entity_id,detail) VALUES(?,?,'erase_contact',?,?,?,?)", params![(self.id)(),(self.timestamp)(),(self.actor)(),noun,id,summary.detail()]).map_err(wrap)?;
        tx.commit().map_err(wrap)?;
        Ok(summary)
    }
    pub fn list_audit_events(&self) -> Result<Option<Vec<AuditEvent>>, Error> {
        let mut stmt = self.db.prepare("SELECT id,occurred_at,operation,actor,entity_type,entity_id,detail FROM audit_events ORDER BY occurred_at DESC,id DESC").map_err(sql_error)?;
        let events = stmt
            .query_map([], |r| {
                Ok(AuditEvent {
                    id: r.get(0)?,
                    occurred_at: r.get(1)?,
                    operation: r.get(2)?,
                    actor: r.get(3)?,
                    entity_type: r.get::<_, Option<String>>(4)?.unwrap_or_default(),
                    entity_id: r.get::<_, Option<String>>(5)?.unwrap_or_default(),
                    detail: r.get::<_, Option<String>>(6)?.unwrap_or_default(),
                })
            })
            .map_err(sql_error)?
            .collect::<rusqlite::Result<Vec<_>>>()
            .map_err(sql_error)?;
        Ok(if events.is_empty() {
            None
        } else {
            Some(events)
        })
    }
}
