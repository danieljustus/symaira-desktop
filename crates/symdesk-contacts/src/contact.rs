//! Contact CRUD and child tables. IDs and UTC RFC3339Nano timestamps are
//! supplied by the caller; this layer never consults credentials or a clock.
use rusqlite::{Connection, OptionalExtension, params, params_from_iter};
use serde::{Deserialize, Serialize};
use std::fmt;

#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Error {
    pub kind: &'static str,
    pub message: String,
}
impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.write_str(&self.message)
    }
}
impl std::error::Error for Error {}
fn error(kind: &'static str, op: &str, message: &str) -> Error {
    Error {
        kind,
        message: format!("{op}: {message}"),
    }
}
fn sql_error(e: rusqlite::Error) -> Error {
    let message = match &e {
        rusqlite::Error::SqliteFailure(code, Some(message))
            if code.code == rusqlite::ErrorCode::ConstraintViolation =>
        {
            format!("constraint failed: {message} ({})", code.extended_code)
        }
        _ => e.to_string(),
    };
    Error {
        kind: "internal",
        message,
    }
}
fn wrapped(op: &str, message: &str, e: rusqlite::Error) -> Error {
    error("internal", op, &format!("{message}: {}", sql_error(e)))
}

#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum Entity {
    Person,
    Organization,
}
impl Entity {
    fn table(self) -> &'static str {
        match self {
            Self::Person => "persons",
            Self::Organization => "organizations",
        }
    }
    fn column(self) -> &'static str {
        match self {
            Self::Person => "person_id",
            Self::Organization => "organization_id",
        }
    }
    fn name(self) -> &'static str {
        match self {
            Self::Person => "display_name",
            Self::Organization => "name",
        }
    }
    fn noun(self) -> &'static str {
        match self {
            Self::Person => "person",
            Self::Organization => "organization",
        }
    }
    fn op(self, action: &str) -> String {
        format!(
            "contact.{action}{}",
            match self {
                Self::Person => "Person",
                Self::Organization => "Organization",
            }
        )
    }
    fn name_error(self) -> &'static str {
        match self {
            Self::Person => "display name must not be empty",
            Self::Organization => "name must not be empty",
        }
    }
}

#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default, rename_all = "PascalCase")]
pub struct Input {
    pub display_name: String,
    pub name: String,
    pub given_name: String,
    pub family_name: String,
    pub notes: String,
    pub source: String,
    pub source_ref: String,
}
#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default, rename_all = "PascalCase")]
pub struct Update {
    pub display_name: Option<String>,
    pub name: Option<String>,
    pub given_name: Option<String>,
    pub family_name: Option<String>,
    pub notes: Option<String>,
}
#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default, rename_all = "PascalCase")]
pub struct ListOptions {
    pub query: String,
    pub classification: String,
    pub limit: i64,
    pub offset: i64,
}
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "PascalCase")]
pub struct Page {
    pub items: Option<Vec<Record>>,
    pub has_more: bool,
}
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "PascalCase")]
pub struct Record {
    #[serde(rename = "ID")]
    pub id: String,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub display_name: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub given_name: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub family_name: Option<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub name: Option<String>,
    pub notes: String,
    pub source: String,
    pub source_ref: String,
    pub created_at: String,
    pub updated_at: String,
    pub aliases: Option<Vec<String>>,
    pub tags: Option<Vec<String>>,
    pub classifications: Option<Vec<String>>,
    pub contact_points: Option<Vec<ContactPoint>>,
}
#[derive(Debug, Clone, Serialize)]
#[serde(rename_all = "PascalCase")]
pub struct ContactPoint {
    #[serde(rename = "ID")]
    pub id: String,
    pub kind: String,
    pub raw_value: String,
    pub normalized_value: String,
    pub label: String,
    pub is_preferred: bool,
    pub is_verified: bool,
    pub created_at: String,
    pub updated_at: String,
}
#[derive(Debug, Clone, Default, Deserialize)]
#[serde(default, rename_all = "PascalCase")]
pub struct ContactPointInput {
    pub kind: String,
    pub raw_value: String,
    pub label: String,
    pub is_preferred: bool,
    pub is_verified: bool,
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
    pub fn create(&mut self, entity: Entity, input: &Input) -> Result<Record, Error> {
        let name = match entity {
            Entity::Person => &input.display_name,
            Entity::Organization => &input.name,
        };
        let op = entity.op("Create");
        if name.trim().is_empty() {
            return Err(error("invalid", &op, entity.name_error()));
        }
        let id = (self.id)();
        let ts = (self.timestamp)();
        let result = match entity {
            Entity::Person => self.db.execute("INSERT INTO persons(id,display_name,given_name,family_name,notes,source,source_ref,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)", params![id,name,input.given_name,input.family_name,input.notes,input.source,input.source_ref,ts,ts]),
            Entity::Organization => self.db.execute("INSERT INTO organizations(id,name,notes,source,source_ref,created_at,updated_at) VALUES(?,?,?,?,?,?,?)", params![id,name,input.notes,input.source,input.source_ref,ts,ts]),
        };
        result.map_err(|e| wrapped(&op, &format!("failed to insert {}", entity.noun()), e))?;
        self.get(entity, &id)
    }
    fn select(entity: Entity) -> String {
        let fields = if entity == Entity::Person {
            "display_name,COALESCE(given_name,''),COALESCE(family_name,'')"
        } else {
            "name,NULL,NULL"
        };
        format!(
            "SELECT id,{fields},COALESCE(notes,''),COALESCE(source,''),COALESCE(source_ref,''),created_at,updated_at FROM {}",
            entity.table()
        )
    }
    fn row(entity: Entity, r: &rusqlite::Row<'_>) -> rusqlite::Result<Record> {
        Ok(Record {
            id: r.get(0)?,
            display_name: if entity == Entity::Person {
                Some(r.get(1)?)
            } else {
                None
            },
            given_name: r.get(2)?,
            family_name: r.get(3)?,
            name: if entity == Entity::Organization {
                Some(r.get(1)?)
            } else {
                None
            },
            notes: r.get(4)?,
            source: r.get(5)?,
            source_ref: r.get(6)?,
            created_at: r.get(7)?,
            updated_at: r.get(8)?,
            aliases: None,
            tags: None,
            classifications: None,
            contact_points: None,
        })
    }
    pub fn get(&self, entity: Entity, id: &str) -> Result<Record, Error> {
        let op = entity.op("Get");
        let mut record = self
            .db
            .query_row(&format!("{} WHERE id=?", Self::select(entity)), [id], |r| {
                Self::row(entity, r)
            })
            .optional()
            .map_err(|e| wrapped(&op, &format!("failed to load {}", entity.noun()), e))?
            .ok_or_else(|| error("not_found", &op, &format!("{} not found", entity.noun())))?;
        let column = entity.column();
        record.aliases = self.strings(
            &format!("SELECT alias FROM aliases WHERE {column}=? ORDER BY created_at,id"),
            id,
        )?;
        record.tags=self.strings(&format!("SELECT t.name FROM tags t JOIN entity_tags et ON et.tag_id=t.id WHERE et.{column}=? ORDER BY t.name"),id)?;
        record.classifications=self.strings(&format!("SELECT classification FROM entity_classifications WHERE {column}=? ORDER BY classification"),id)?;
        let mut stmt=self.db.prepare(&format!("SELECT id,kind,raw_value,normalized_value,COALESCE(label,''),is_preferred,is_verified,created_at,updated_at FROM contact_points WHERE {column}=? ORDER BY is_preferred DESC,created_at,id")).map_err(sql_error)?;
        let points = stmt
            .query_map([id], |r| {
                Ok(ContactPoint {
                    id: r.get(0)?,
                    kind: r.get(1)?,
                    raw_value: r.get(2)?,
                    normalized_value: r.get(3)?,
                    label: r.get(4)?,
                    is_preferred: r.get::<_, i64>(5)? == 1,
                    is_verified: r.get::<_, i64>(6)? == 1,
                    created_at: r.get(7)?,
                    updated_at: r.get(8)?,
                })
            })
            .map_err(sql_error)?
            .collect::<rusqlite::Result<Vec<_>>>()
            .map_err(sql_error)?;
        record.contact_points = nonempty(points);
        Ok(record)
    }
    fn strings(&self, sql: &str, id: &str) -> Result<Option<Vec<String>>, Error> {
        let rows = self
            .db
            .prepare(sql)
            .map_err(sql_error)?
            .query_map([id], |r| r.get(0))
            .map_err(sql_error)?
            .collect::<rusqlite::Result<Vec<_>>>()
            .map_err(sql_error)?;
        Ok(nonempty(rows))
    }
    pub fn list(&self, entity: Entity, opts: &ListOptions) -> Result<Page, Error> {
        let limit = if opts.limit <= 0 {
            50
        } else {
            opts.limit.min(200)
        };
        let offset = opts.offset.max(0);
        let mut sql = Self::select(entity);
        let mut clauses = Vec::new();
        let mut args = Vec::new();
        if !opts.classification.is_empty() {
            clauses.push(format!(
                "id IN (SELECT {} FROM entity_classifications WHERE classification=?)",
                entity.column()
            ));
            args.push(rusqlite::types::Value::Text(opts.classification.clone()));
        }
        let q = opts.query.trim();
        if !q.is_empty() {
            let names = if entity == Entity::Person {
                vec!["display_name", "given_name", "family_name"]
            } else {
                vec!["name"]
            };
            clauses.push(format!(
                "({})",
                names
                    .iter()
                    .map(|n| format!("{n} LIKE ? ESCAPE '\\' COLLATE NOCASE"))
                    .collect::<Vec<_>>()
                    .join(" OR ")
            ));
            let pattern = format!(
                "%{}%",
                q.replace('\\', "\\\\")
                    .replace('%', "\\%")
                    .replace('_', "\\_")
            );
            for _ in names {
                args.push(rusqlite::types::Value::Text(pattern.clone()));
            }
        }
        if !clauses.is_empty() {
            sql.push_str(&format!(" WHERE {}", clauses.join(" AND ")));
        }
        sql.push_str(&format!(
            " ORDER BY {} COLLATE NOCASE,id LIMIT ? OFFSET ?",
            entity.name()
        ));
        args.push((limit + 1).into());
        args.push(offset.into());
        let mut records = self
            .db
            .prepare(&sql)
            .map_err(sql_error)?
            .query_map(params_from_iter(args), |r| Self::row(entity, r))
            .map_err(sql_error)?
            .collect::<rusqlite::Result<Vec<_>>>()
            .map_err(sql_error)?;
        let has_more = records.len() > limit as usize;
        records.truncate(limit as usize);
        Ok(Page {
            items: nonempty(records),
            has_more,
        })
    }
    pub fn update(&mut self, entity: Entity, id: &str, input: &Update) -> Result<Record, Error> {
        let op = entity.op("Update");
        let mut sets = vec!["updated_at=?"];
        let mut args = vec![(self.timestamp)()];
        let name = if entity == Entity::Person {
            &input.display_name
        } else {
            &input.name
        };
        if let Some(name) = name {
            if name.trim().is_empty() {
                return Err(error("invalid", &op, entity.name_error()));
            }
            sets.push(if entity == Entity::Person {
                "display_name=?"
            } else {
                "name=?"
            });
            args.push(name.clone());
        }
        if entity == Entity::Person {
            for (column, value) in [
                ("given_name=?", &input.given_name),
                ("family_name=?", &input.family_name),
            ] {
                if let Some(value) = value {
                    sets.push(column);
                    args.push(value.clone());
                }
            }
        }
        if let Some(notes) = &input.notes {
            sets.push("notes=?");
            args.push(notes.clone());
        }
        args.push(id.to_owned());
        let n = self
            .db
            .execute(
                &format!(
                    "UPDATE {} SET {} WHERE id=?",
                    entity.table(),
                    sets.join(",")
                ),
                params_from_iter(args),
            )
            .map_err(|e| wrapped(&op, &format!("failed to update {}", entity.noun()), e))?;
        if n == 0 {
            return Err(error(
                "not_found",
                &op,
                &format!("{} not found", entity.noun()),
            ));
        }
        self.get(entity, id)
    }
    pub fn delete(&self, entity: Entity, id: &str) -> Result<(), Error> {
        let op = entity.op("Delete");
        let n = self
            .db
            .execute(&format!("DELETE FROM {} WHERE id=?", entity.table()), [id])
            .map_err(|e| wrapped(&op, &format!("failed to delete {}", entity.noun()), e))?;
        if n == 0 {
            return Err(error(
                "not_found",
                &op,
                &format!("{} not found", entity.noun()),
            ));
        }
        Ok(())
    }
    pub fn add_alias(&mut self, entity: Entity, id: &str, alias: &str) -> Result<(), Error> {
        let op = entity.op("Add") + "Alias";
        if alias.trim().is_empty() {
            return Err(error("invalid", &op, "alias must not be empty"));
        }
        self.db
            .execute(
                &format!(
                    "INSERT INTO aliases(id,{},alias,created_at) VALUES(?,?,?,?)",
                    entity.column()
                ),
                params![(self.id)(), id, alias, (self.timestamp)()],
            )
            .map_err(|e| wrapped(&op, "failed to add alias", e))?;
        Ok(())
    }
    pub fn add_tag(&mut self, entity: Entity, id: &str, name: &str) -> Result<(), Error> {
        let op = entity.op("Add") + "Tag";
        if name.trim().is_empty() {
            return Err(error("invalid", &op, "tag must not be empty"));
        }
        let tx = self.db.unchecked_transaction().map_err(sql_error)?;
        let tag_id = match tx
            .query_row("SELECT id FROM tags WHERE name=?", [name], |r| {
                r.get::<_, String>(0)
            })
            .optional()
            .map_err(sql_error)?
        {
            Some(id) => id,
            None => {
                let id = (self.id)();
                tx.execute("INSERT INTO tags(id,name) VALUES(?,?)", params![id, name])
                    .map_err(sql_error)?;
                id
            }
        };
        tx.execute(
            &format!(
                "INSERT OR IGNORE INTO entity_tags(id,tag_id,{}) VALUES(?,?,?)",
                entity.column()
            ),
            params![(self.id)(), tag_id, id],
        )
        .map_err(sql_error)?;
        tx.commit().map_err(sql_error)
    }
    pub fn set_classification(
        &mut self,
        entity: Entity,
        id: &str,
        value: &str,
    ) -> Result<(), Error> {
        if !["personal", "business", "customer", "lead", "partner"].contains(&value) {
            return Err(error(
                "invalid",
                "contact.setClassification",
                &format!("unknown classification: {value}"),
            ));
        }
        self.db.execute(&format!("INSERT OR IGNORE INTO entity_classifications(id,{},classification,created_at) VALUES(?,?,?,?)",entity.column()),params![(self.id)(),id,value,(self.timestamp)()]).map_err(sql_error)?;
        Ok(())
    }
    pub fn remove_classification(
        &self,
        entity: Entity,
        id: &str,
        value: &str,
    ) -> Result<(), Error> {
        self.db
            .execute(
                &format!(
                    "DELETE FROM entity_classifications WHERE {}=? AND classification=?",
                    entity.column()
                ),
                params![id, value],
            )
            .map_err(sql_error)?;
        Ok(())
    }
    pub fn add_contact_point(
        &mut self,
        entity: Entity,
        parent: &str,
        input: &ContactPointInput,
    ) -> Result<ContactPoint, Error> {
        let op = "contact.addContactPoint";
        if !["email", "phone", "address", "url", "handle"].contains(&input.kind.as_str()) {
            return Err(error(
                "invalid",
                op,
                &format!("unknown contact point kind: {}", input.kind),
            ));
        }
        if input.raw_value.is_empty() {
            return Err(error(
                "invalid",
                op,
                "contact point value must not be empty",
            ));
        }
        let id = (self.id)();
        let ts = (self.timestamp)();
        let normalized = crate::pipeline::normalize(&input.kind, &input.raw_value);
        self.db.execute(&format!("INSERT INTO contact_points(id,{},kind,raw_value,normalized_value,label,is_preferred,is_verified,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?)",entity.column()),params![id,parent,input.kind,input.raw_value,normalized,input.label,input.is_preferred,input.is_verified,ts,ts]).map_err(|e| {
            let cause=sql_error(e);
            if cause.message.contains("UNIQUE constraint failed") {
                error("conflict",op,&format!("an identical contact point already exists on this entity: {cause}"))
            } else { cause }
        })?;
        Ok(ContactPoint {
            id,
            kind: input.kind.clone(),
            raw_value: input.raw_value.clone(),
            normalized_value: normalized,
            label: input.label.clone(),
            is_preferred: input.is_preferred,
            is_verified: input.is_verified,
            created_at: ts.clone(),
            updated_at: ts,
        })
    }
    pub fn remove_contact_point(
        &self,
        entity: Entity,
        parent: &str,
        id: &str,
    ) -> Result<(), Error> {
        let n = self
            .db
            .execute(
                &format!(
                    "DELETE FROM contact_points WHERE id=? AND {}=?",
                    entity.column()
                ),
                params![id, parent],
            )
            .map_err(sql_error)?;
        if n == 0 {
            return Err(error(
                "not_found",
                "contact.removeContactPoint",
                "contact point not found",
            ));
        }
        Ok(())
    }
}
fn nonempty<T>(v: Vec<T>) -> Option<Vec<T>> {
    if v.is_empty() { None } else { Some(v) }
}
