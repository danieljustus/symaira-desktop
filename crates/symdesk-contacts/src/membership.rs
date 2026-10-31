//! Organization memberships, the "quick add" person shape and the privacy-safe
//! contact reference lookups (`GetRef` / `FindRefsByName`).
//!
//! A reference carries provider, schema version, id, kind and a display-name
//! cache only: both lookups read the id and display-name columns, so no
//! contact point, note, alias, tag or classification can leave through them.
use crate::contact::{
    ContactPointInput, Entity, Error, Input, Record, Service, error, sql_error, wrapped,
};
use rusqlite::{OptionalExtension, params};
use serde::{Deserialize, Serialize};

/// Issuing tool marker every reference carries.
pub const REF_PROVIDER: &str = "symrelate";
/// Current contact-reference schema version.
pub const REF_SCHEMA_VERSION: i64 = 1;

#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default, rename_all = "PascalCase")]
pub struct MembershipInput {
    pub role: String,
    pub title: String,
    pub valid_from: Option<String>,
    pub valid_to: Option<String>,
}

#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "PascalCase")]
pub struct Membership {
    #[serde(rename = "ID")]
    pub id: String,
    #[serde(rename = "PersonID")]
    pub person_id: String,
    #[serde(rename = "OrganizationID")]
    pub organization_id: String,
    pub role: String,
    pub title: String,
    pub valid_from: Option<String>,
    pub valid_to: Option<String>,
    pub created_at: String,
    pub updated_at: String,
}

/// Minimal, privacy-safe, opaque reference to a contact.
#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct Ref {
    pub provider: String,
    pub schema_version: i64,
    pub id: String,
    pub kind: String,
    pub display_name: String,
}

fn make_ref(id: String, kind: &str, display_name: String) -> Ref {
    Ref {
        provider: REF_PROVIDER.to_owned(),
        schema_version: REF_SCHEMA_VERSION,
        id,
        kind: kind.to_owned(),
        display_name,
    }
}

const MEMBERSHIP_SELECT: &str = "SELECT id, person_id, organization_id, COALESCE(role, ''), COALESCE(title, ''), valid_from, valid_to, created_at, updated_at FROM organization_memberships";

fn membership_row(r: &rusqlite::Row<'_>) -> rusqlite::Result<Membership> {
    Ok(Membership {
        id: r.get(0)?,
        person_id: r.get(1)?,
        organization_id: r.get(2)?,
        role: r.get(3)?,
        title: r.get(4)?,
        valid_from: r.get(5)?,
        valid_to: r.get(6)?,
        created_at: r.get(7)?,
        updated_at: r.get(8)?,
    })
}

impl<I: FnMut() -> String, T: FnMut() -> String> Service<'_, I, T> {
    /// Links a person to an organization; several memberships between the same
    /// pair are allowed so history is preserved.
    ///
    /// # Errors
    /// Returns Go's invalid/internal errors for a missing parent or a period
    /// ending before it starts.
    pub fn add_membership(
        &mut self,
        person_id: &str,
        organization_id: &str,
        input: &MembershipInput,
    ) -> Result<Membership, Error> {
        let op = "contact.AddMembership";
        let id = (self.id)();
        let ts = (self.timestamp)();
        let valid_from = input.valid_from.as_deref().map(crate::utc_rfc3339nano);
        let valid_to = input.valid_to.as_deref().map(crate::utc_rfc3339nano);
        let outcome = self.db.execute(
            "INSERT INTO organization_memberships (id, person_id, organization_id, role, title, valid_from, valid_to, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
            params![id, person_id, organization_id, input.role, input.title, valid_from, valid_to, ts, ts],
        );
        if let Err(e) = outcome {
            let cause = sql_error(e);
            return Err(if cause.message.contains("FOREIGN KEY constraint failed") {
                error(
                    "invalid",
                    op,
                    &format!("person or organization does not exist: {cause}"),
                )
            } else if cause.message.contains("CHECK constraint failed") {
                error(
                    "invalid",
                    op,
                    &format!("valid_to must not be before valid_from: {cause}"),
                )
            } else {
                error(
                    "internal",
                    op,
                    &format!("failed to insert membership: {cause}"),
                )
            });
        }
        self.get_membership(&id)
    }

    fn get_membership(&self, id: &str) -> Result<Membership, Error> {
        let op = "contact.getMembership";
        self.db
            .query_row(
                &format!("{MEMBERSHIP_SELECT} WHERE id = ?"),
                [id],
                membership_row,
            )
            .optional()
            .map_err(|e| wrapped(op, "failed to load membership", e))?
            .ok_or_else(|| error("not_found", op, "membership not found"))
    }

    /// Every membership (past and present) for a person, newest start first.
    ///
    /// # Errors
    /// Returns the internal list error.
    pub fn list_memberships_by_person(
        &self,
        person_id: &str,
    ) -> Result<Option<Vec<Membership>>, Error> {
        self.list_memberships("person_id", person_id)
    }

    /// Every membership for an organization.
    ///
    /// # Errors
    /// Returns the internal list error.
    pub fn list_memberships_by_organization(
        &self,
        organization_id: &str,
    ) -> Result<Option<Vec<Membership>>, Error> {
        self.list_memberships("organization_id", organization_id)
    }

    fn list_memberships(&self, column: &str, id: &str) -> Result<Option<Vec<Membership>>, Error> {
        let op = "contact.listMemberships";
        let rows = self
            .db
            .prepare(&format!(
                "{MEMBERSHIP_SELECT} WHERE {column} = ? ORDER BY COALESCE(valid_from, created_at) DESC, id"
            ))
            .map_err(|e| wrapped(op, "failed to list memberships", e))?
            .query_map([id], membership_row)
            .map_err(|e| wrapped(op, "failed to list memberships", e))?
            .collect::<rusqlite::Result<Vec<_>>>()
            .map_err(|e| wrapped(op, "failed to scan membership", e))?;
        Ok(if rows.is_empty() { None } else { Some(rows) })
    }

    /// Closes a role without deleting its history.
    ///
    /// # Errors
    /// Returns Go's invalid/not-found/internal errors.
    pub fn end_membership(
        &mut self,
        membership_id: &str,
        valid_to: &str,
    ) -> Result<Membership, Error> {
        let op = "contact.EndMembership";
        let ts = (self.timestamp)();
        let valid_to = crate::utc_rfc3339nano(valid_to);
        let changed = self.db.execute(
            "UPDATE organization_memberships SET valid_to = ?, updated_at = ? WHERE id = ?",
            params![valid_to, ts, membership_id],
        );
        match changed {
            Err(e) => {
                let cause = sql_error(e);
                Err(if cause.message.contains("CHECK constraint failed") {
                    error(
                        "invalid",
                        op,
                        &format!("valid_to must not be before valid_from: {cause}"),
                    )
                } else {
                    error("internal", op, &format!("failed to end membership: {cause}"))
                })
            }
            Ok(0) => Err(error("not_found", op, "membership not found")),
            Ok(_) => self.get_membership(membership_id),
        }
    }

    /// Creates a person and, when non-empty, an email and/or phone contact
    /// point in one transaction: any failure rolls the whole call back.
    ///
    /// # Errors
    /// Returns Go's validation error or the first failing statement.
    pub fn create_person_with_contact_points(
        &mut self,
        input: &Input,
        email: &str,
        phone: &str,
    ) -> Result<Record, Error> {
        let op = "contact.CreatePersonWithContactPoints";
        if input.display_name.trim().is_empty() {
            return Err(error("invalid", op, "display name must not be empty"));
        }
        self.db.execute_batch("BEGIN").map_err(sql_error)?;
        let outcome = self.quick_add_inside(input, email, phone);
        match outcome {
            Ok(id) => {
                self.db.execute_batch("COMMIT").map_err(sql_error)?;
                self.get(Entity::Person, &id)
            }
            Err(e) => {
                let _ = self.db.execute_batch("ROLLBACK");
                Err(e)
            }
        }
    }

    fn quick_add_inside(&mut self, input: &Input, email: &str, phone: &str) -> Result<String, Error> {
        let id = (self.id)();
        let ts = (self.timestamp)();
        self.db
            .execute(
                "INSERT INTO persons (id, display_name, given_name, family_name, notes, source, source_ref, created_at, updated_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
                params![id, input.display_name, input.given_name, input.family_name, input.notes, input.source, input.source_ref, ts, ts],
            )
            .map_err(sql_error)?;
        for (kind, value) in [("email", email), ("phone", phone)] {
            if !value.is_empty() {
                self.add_contact_point(
                    Entity::Person,
                    &id,
                    &ContactPointInput {
                        kind: kind.to_owned(),
                        raw_value: value.to_owned(),
                        ..ContactPointInput::default()
                    },
                )?;
            }
        }
        Ok(id)
    }

    /// Resolves any contact id to its minimal reference.
    ///
    /// # Errors
    /// Returns `not_found` for an unknown or erased id.
    pub fn get_ref(&self, id: &str) -> Result<Ref, Error> {
        let op = "contact.GetRef";
        for (sql, kind) in [
            ("SELECT display_name FROM persons WHERE id = ?", "person"),
            ("SELECT name FROM organizations WHERE id = ?", "organization"),
        ] {
            match self.db.query_row(sql, [id], |r| r.get::<_, String>(0)) {
                Ok(name) => return Ok(make_ref(id.to_owned(), kind, name)),
                Err(rusqlite::Error::QueryReturnedNoRows) => {}
                Err(e) => return Err(wrapped(op, "failed to resolve contact reference", e)),
            }
        }
        Err(error("not_found", op, "contact not found"))
    }

    /// Resolves a display name to the references that carry it (persons first,
    /// then organizations). Matching is exact but ASCII-case-insensitive.
    /// `None` for a blank name, `Some(vec![])` for a name nobody carries.
    ///
    /// # Errors
    /// Returns the internal resolve error.
    pub fn find_refs_by_name(&self, name: &str) -> Result<Option<Vec<Ref>>, Error> {
        let op = "contact.FindRefsByName";
        let trimmed = name.trim();
        if trimmed.is_empty() {
            return Ok(None);
        }
        let mut refs = Vec::new();
        for (sql, kind) in [
            (
                "SELECT id, display_name FROM persons WHERE display_name = ? COLLATE NOCASE ORDER BY display_name, id",
                "person",
            ),
            (
                "SELECT id, name FROM organizations WHERE name = ? COLLATE NOCASE ORDER BY name, id",
                "organization",
            ),
        ] {
            let found = self
                .db
                .prepare(sql)
                .map_err(|e| wrapped(op, "failed to resolve contact references", e))?
                .query_map([trimmed], |r| Ok((r.get::<_, String>(0)?, r.get::<_, String>(1)?)))
                .map_err(|e| wrapped(op, "failed to resolve contact references", e))?
                .collect::<rusqlite::Result<Vec<_>>>()
                .map_err(|e| wrapped(op, "failed to resolve contact references", e))?;
            refs.extend(found.into_iter().map(|(id, name)| make_ref(id, kind, name)));
        }
        Ok(Some(refs))
    }
}
