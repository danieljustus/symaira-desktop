use crate::{Result, Store, error};
use rusqlite::{Row, params};
use serde::{Deserialize, Serialize};

#[derive(Clone, Debug, Default, Deserialize, Serialize, PartialEq, Eq)]
#[serde(rename_all = "PascalCase")]
pub struct Document {
    #[serde(rename = "ID")]
    pub id: i64,
    pub source_path: String,
    #[serde(rename = "SHA256")]
    pub sha256: String,
    #[serde(rename = "MIME")]
    pub mime: String,
    pub status: String,
    pub vault_path: Option<String>,
    pub archive_path: Option<String>,
    pub category: String,
    pub tags: Option<Vec<String>>,
    pub correspondent: String,
    pub document_type: String,
    #[serde(rename = "SourceMailID")]
    pub source_mail_id: Option<String>,
    pub vault_root: String,
}
const COLUMNS: &str = "id,source_path,sha256,mime,status,vault_path,archive_path,category,tags,correspondent,document_type,source_mail_id,vault_root";
fn scan(r: &Row<'_>) -> rusqlite::Result<Document> {
    let tags: Option<String> = r.get(8)?;
    Ok(Document {
        id: r.get(0)?,
        source_path: r.get(1)?,
        sha256: r.get(2)?,
        mime: r.get(3)?,
        status: r.get(4)?,
        vault_path: r.get(5)?,
        archive_path: r.get(6)?,
        category: r.get::<_, Option<String>>(7)?.unwrap_or_default(),
        tags: tags.and_then(|s| serde_json::from_str(&s).ok()),
        correspondent: r.get::<_, Option<String>>(9)?.unwrap_or_default(),
        document_type: r.get::<_, Option<String>>(10)?.unwrap_or_default(),
        source_mail_id: r.get(11)?,
        vault_root: r.get::<_, Option<String>>(12)?.unwrap_or_default(),
    })
}
impl Store {
    pub fn by_id(&self, id: i64) -> Result<Document> {
        let mut doc = self
            .connection
            .query_row(
                &format!("SELECT {COLUMNS} FROM documents WHERE id=?"),
                [id],
                scan,
            )
            .map_err(|e| error("", e))?;
        // Go ByID omits this field even though ByHash/ListDocuments return it.
        doc.source_mail_id = None;
        Ok(doc)
    }
    pub fn by_hash(&self, hash: &str) -> Result<Document> {
        self.lookup_hash(hash, None)
    }
    pub fn by_hash_in_vault(&self, hash: &str, vault: &str) -> Result<Document> {
        self.lookup_hash(hash, Some(vault))
    }
    fn lookup_hash(&self, hash: &str, vault: Option<&str>) -> Result<Document> {
        let mut query = format!("SELECT {COLUMNS} FROM documents WHERE sha256=?");
        let mut args = vec![hash.to_owned()];
        if let Some(v) = vault {
            query.push_str(
                " AND (vault_root = ? OR (vault_root = '' AND instr(vault_path, ?) = 1))",
            );
            args.push(v.into());
            args.push(format!("{v}{}", std::path::MAIN_SEPARATOR));
        }
        query.push_str(" ORDER BY id ASC LIMIT 1");
        self.connection
            .query_row(&query, rusqlite::params_from_iter(args), scan)
            .map_err(|e| error("", e))
    }
    pub fn create_or_get(
        &self,
        source: &str,
        hash: &str,
        mime: &str,
        vault: &str,
    ) -> Result<(Document, bool)> {
        match self.by_hash_in_vault(hash, vault) {
            Ok(d) => return Ok((d, false)),
            Err(e) if e == "sql: no rows in result set" => {}
            Err(e) => return Err(e),
        }
        if let Err(e)=self.connection.execute("INSERT INTO documents (source_path,sha256,mime,vault_root,status) VALUES (?,?,?,?,'pending')",params![source,hash,mime,vault]) {
            if let Ok(doc)=self.by_hash_in_vault(hash,vault){return Ok((doc,false));}return Err(error("insert document",e));
        }
        Ok((
            Document {
                id: self.connection.last_insert_rowid(),
                source_path: source.into(),
                sha256: hash.into(),
                mime: mime.into(),
                status: "pending".into(),
                vault_root: vault.into(),
                ..Document::default()
            },
            true,
        ))
    }
    pub fn by_archive_path(&self, path: &str) -> Result<Document> {
        let id = self
            .connection
            .query_row(
                "SELECT id FROM documents WHERE archive_path=?",
                [path],
                |r| r.get(0),
            )
            .map_err(|e| error("", e))?;
        self.by_id(id)
    }
    pub fn list_documents(&self) -> Result<Option<Vec<Document>>> {
        let mut stmt=self.connection.prepare(&format!("SELECT {COLUMNS} FROM documents WHERE vault_path IS NOT NULL AND vault_path <> '' ORDER BY vault_path ASC,id ASC")).map_err(|e|error("query documents",e))?;
        let docs = stmt
            .query_map([], scan)
            .map_err(|e| error("query documents", e))?
            .collect::<std::result::Result<Vec<_>, _>>()
            .map_err(|e| error("scan document", e))?;
        Ok(if docs.is_empty() { None } else { Some(docs) })
    }
    pub fn set_provenance(&self, id: i64, mail: &str, correspondent: &str) -> Result<()> {
        self.connection
            .execute(
                "UPDATE documents SET source_mail_id=?,correspondent=? WHERE id=?",
                params![mail, correspondent, id],
            )
            .map_err(|e| error("", e))?;
        Ok(())
    }
    pub fn set_vault_and_archive_path(&self, id: i64, metadata: &DocumentPaths<'_>) -> Result<()> {
        let nullable = |s: &str| {
            if s.is_empty() {
                None
            } else {
                Some(s.to_owned())
            }
        };
        let tags = if metadata.tags.is_empty() {
            None
        } else {
            Some(
                serde_json::to_string(metadata.tags)
                    .map_err(|e| format!("marshal tags: {e}"))?
                    .replace('&', "\\u0026")
                    .replace('<', "\\u003c")
                    .replace('>', "\\u003e")
                    .replace('\u{2028}', "\\u2028")
                    .replace('\u{2029}', "\\u2029"),
            )
        };
        self.connection.execute("UPDATE documents SET vault_path=?,archive_path=?,category=?,tags=?,correspondent=?,document_type=?,status='done',updated_at=CURRENT_TIMESTAMP WHERE id=?",params![metadata.vault_path,metadata.archive_path,nullable(metadata.category),tags,nullable(metadata.correspondent),nullable(metadata.document_type),id]).map_err(|e|error("update document",e))?;
        Ok(())
    }
    pub fn has_mail_message(&self, id: &str) -> Result<bool> {
        self.connection
            .query_row(
                "SELECT COUNT(*) FROM mail_messages WHERE message_id=?",
                [id],
                |r| r.get::<_, i64>(0).map(|n| n > 0),
            )
            .map_err(|e| error("", e))
    }
    pub fn track_mail_message(&self, id: &str, mailbox: &str) -> Result<()> {
        self.connection
            .execute(
                "INSERT OR IGNORE INTO mail_messages(message_id,source_mailbox) VALUES (?,?)",
                params![id, mailbox],
            )
            .map_err(|e| error("", e))?;
        Ok(())
    }
}
#[derive(Clone, Debug, Default)]
pub struct DocumentPaths<'a> {
    pub vault_path: &'a str,
    pub archive_path: &'a str,
    pub category: &'a str,
    pub tags: &'a [String],
    pub correspondent: &'a str,
    pub document_type: &'a str,
}
