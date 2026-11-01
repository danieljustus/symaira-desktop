//! PostgreSQL `StorageBackend`: the `storage_entries` key/value table.
//!
//! Statement texts are byte-for-byte those of Go `internal/storage/postgres.go`
//! so server-side errors (and the DDL the fixture introspects) are identical.

use std::error::Error as StdError;
use std::sync::atomic::{AtomicBool, Ordering};
use std::sync::{Arc, Mutex, MutexGuard, PoisonError};
use std::time::Duration;

use bytes::BytesMut;
use tokio::sync::{OwnedSemaphorePermit, Semaphore};
use tokio::task::JoinHandle;
use tokio_postgres::types::{FromSql, IsNull, ToSql, Type, to_sql_checked};
use tokio_postgres::{CancelToken, Client, Config, NoTls, Statement};

use crate::{Entry, StorageError, dsn, go_quote};

const DDL: &str = "
		CREATE TABLE IF NOT EXISTS storage_entries (
			storage_key TEXT PRIMARY KEY NOT NULL,
			value BYTEA NOT NULL
		)";
const SQL_GET: &str = "SELECT value FROM storage_entries WHERE storage_key = $1";
const SQL_SET: &str = "
		INSERT INTO storage_entries (storage_key, value) VALUES ($1, $2)
		ON CONFLICT(storage_key) DO UPDATE SET value = EXCLUDED.value";
const SQL_REMOVE: &str = "DELETE FROM storage_entries WHERE storage_key = $1";
const SQL_QUERY: &str = "
		SELECT storage_key, value
		FROM storage_entries
		WHERE substr(storage_key, 1, length($1)) = $1
		ORDER BY storage_key COLLATE \"C\" ASC";

/// A value column read or written as raw bytes. Go's pgx converts between
/// `[]byte` and `bytea` *or* `text`; a store opened on an older table whose
/// value column is `text` keeps working the same way.
#[derive(Debug)]
struct Raw(Vec<u8>);

impl ToSql for Raw {
    fn to_sql(
        &self,
        _ty: &Type,
        out: &mut BytesMut,
    ) -> Result<IsNull, Box<dyn StdError + Sync + Send>> {
        out.extend_from_slice(&self.0);
        Ok(IsNull::No)
    }

    fn accepts(ty: &Type) -> bool {
        matches!(*ty, Type::BYTEA | Type::TEXT | Type::VARCHAR)
    }

    to_sql_checked!();
}

impl<'a> FromSql<'a> for Raw {
    fn from_sql(_ty: &Type, raw: &'a [u8]) -> Result<Self, Box<dyn StdError + Sync + Send>> {
        Ok(Self(raw.to_vec()))
    }

    fn accepts(ty: &Type) -> bool {
        matches!(*ty, Type::BYTEA | Type::TEXT | Type::VARCHAR)
    }
}

#[derive(Clone, Copy)]
enum Kind {
    Get,
    Set,
    Remove,
    Query,
}

#[derive(Default)]
struct Statements {
    get: Option<Statement>,
    set: Option<Statement>,
    remove: Option<Statement>,
    query: Option<Statement>,
}

struct Conn {
    client: Client,
    statements: Statements,
}

impl Conn {
    async fn prepare(&mut self, kind: Kind) -> Result<Statement, tokio_postgres::Error> {
        let (slot, sql) = match kind {
            Kind::Get => (&mut self.statements.get, SQL_GET),
            Kind::Set => (&mut self.statements.set, SQL_SET),
            Kind::Remove => (&mut self.statements.remove, SQL_REMOVE),
            Kind::Query => (&mut self.statements.query, SQL_QUERY),
        };
        if let Some(statement) = slot {
            return Ok(statement.clone());
        }
        let statement = self.client.prepare(sql).await?;
        *slot = Some(statement.clone());
        Ok(statement)
    }
}

struct Shared {
    config: Config,
    permits: Arc<Semaphore>,
    idle: Mutex<Vec<Conn>>,
    closed: AtomicBool,
}

impl Shared {
    fn idle(&self) -> MutexGuard<'_, Vec<Conn>> {
        self.idle.lock().unwrap_or_else(PoisonError::into_inner)
    }
}

/// PostgreSQL-backed key/value store. Cloning shares the pool.
#[derive(Clone)]
pub struct PostgresBackend {
    shared: Arc<Shared>,
}

enum Op<'a> {
    Get(&'a str),
    Set(&'a str, &'a [u8]),
    Remove(&'a str),
    Query(&'a str),
}

enum Out {
    Value(Vec<u8>),
    Done,
    Entries(Vec<Entry>),
}

/// Aborts the statement running on a connection when the operation is
/// dropped before it finished (deadline or caller cancellation), like pgx
/// does when its context ends.
struct CancelOnDrop(Option<CancelToken>);

impl CancelOnDrop {
    fn disarm(&mut self) {
        self.0 = None;
    }
}

impl Drop for CancelOnDrop {
    fn drop(&mut self) {
        if let (Some(token), Ok(handle)) = (self.0.take(), tokio::runtime::Handle::try_current()) {
            handle.spawn(async move {
                let _ = token.cancel_query(NoTls).await;
            });
        }
    }
}

struct Lease {
    conn: Conn,
    _permit: OwnedSemaphorePermit,
}

fn default_pool_size() -> usize {
    std::thread::available_parallelism().map_or(4, |n| n.get().max(4))
}

fn server_error(context: String, error: &tokio_postgres::Error) -> StorageError {
    match error.as_db_error() {
        Some(db) => StorageError::Server {
            context,
            severity: db.severity().to_string(),
            message: db.message().to_string(),
            sqlstate: db.code().code().to_string(),
        },
        None => StorageError::Client {
            context,
            detail: error.to_string(),
        },
    }
}

fn connect_error(error: &tokio_postgres::Error) -> StorageError {
    let db = error.as_db_error();
    StorageError::Connect {
        detail: error.to_string(),
        sqlstate: db.map(|db| db.code().code().to_string()),
        server_message: db.map(|db| db.message().to_string()),
    }
}

async fn connect(config: &Config) -> Result<Conn, StorageError> {
    let (client, connection) = config.connect(NoTls).await.map_err(|e| connect_error(&e))?;
    let _driver: JoinHandle<()> = tokio::spawn(async move {
        let _ = connection.await;
    });
    Ok(Conn {
        client,
        statements: Statements::default(),
    })
}

impl PostgresBackend {
    /// Connects, pings and ensures the `storage_entries` table exists
    /// (Go `OpenPostgres`). `dsn` is a pgx URL or keyword/value string.
    pub async fn open(dsn: &str) -> Result<Self, StorageError> {
        if dsn.is_empty() {
            return Err(StorageError::EmptyDsn);
        }
        let parsed = dsn::parse(dsn).map_err(StorageError::Dsn)?;
        let size = parsed.pool_max_conns.unwrap_or_else(default_pool_size);
        let first = connect(&parsed.config).await?;
        first
            .client
            .batch_execute(DDL)
            .await
            .map_err(|e| server_error("initialize PostgreSQL storage table".to_string(), &e))?;
        Ok(Self {
            shared: Arc::new(Shared {
                config: parsed.config,
                permits: Arc::new(Semaphore::new(size)),
                idle: Mutex::new(vec![first]),
                closed: AtomicBool::new(false),
            }),
        })
    }

    /// Closes the pool. Idempotent; later operations fail with
    /// [`StorageError::Closed`].
    pub fn close(&self) {
        self.shared.closed.store(true, Ordering::SeqCst);
        self.shared.permits.close();
        self.shared.idle().clear();
    }

    /// The value stored under `key`, or [`StorageError::NotFound`].
    pub async fn get(&self, key: &str) -> Result<Vec<u8>, StorageError> {
        self.get_within(key, None).await
    }

    /// Stores `value` under `key`, replacing any existing value.
    pub async fn set(&self, key: &str, value: &[u8]) -> Result<(), StorageError> {
        self.set_within(key, value, None).await
    }

    /// Deletes `key`; an absent key is not an error.
    pub async fn remove(&self, key: &str) -> Result<(), StorageError> {
        self.remove_within(key, None).await
    }

    /// Entries whose key starts with `prefix` in byte order (`COLLATE "C"`).
    pub async fn query(&self, prefix: &str) -> Result<Vec<Entry>, StorageError> {
        self.query_within(prefix, None).await
    }

    /// A view whose operations fail with [`StorageError::Timeout`] when they
    /// (including the wait for a pooled connection) exceed `deadline`; the
    /// running statement is cancelled server-side and its connection dropped.
    #[must_use]
    pub fn bounded(&self, deadline: Duration) -> Bounded<'_> {
        Bounded {
            backend: self,
            deadline,
        }
    }

    async fn get_within(
        &self,
        key: &str,
        deadline: Option<Duration>,
    ) -> Result<Vec<u8>, StorageError> {
        match self.run(Op::Get(key), deadline).await? {
            Out::Value(value) => Ok(value),
            _ => Err(internal("get")),
        }
    }

    async fn set_within(
        &self,
        key: &str,
        value: &[u8],
        deadline: Option<Duration>,
    ) -> Result<(), StorageError> {
        match self.run(Op::Set(key, value), deadline).await? {
            Out::Done => Ok(()),
            _ => Err(internal("set")),
        }
    }

    async fn remove_within(
        &self,
        key: &str,
        deadline: Option<Duration>,
    ) -> Result<(), StorageError> {
        match self.run(Op::Remove(key), deadline).await? {
            Out::Done => Ok(()),
            _ => Err(internal("remove")),
        }
    }

    async fn query_within(
        &self,
        prefix: &str,
        deadline: Option<Duration>,
    ) -> Result<Vec<Entry>, StorageError> {
        match self.run(Op::Query(prefix), deadline).await? {
            Out::Entries(entries) => Ok(entries),
            _ => Err(internal("query")),
        }
    }

    async fn run(&self, op: Op<'_>, deadline: Option<Duration>) -> Result<Out, StorageError> {
        if let Op::Get("") | Op::Set("", _) | Op::Remove("") = op {
            return Err(StorageError::InvalidKey);
        }
        if self.shared.closed.load(Ordering::SeqCst) {
            return Err(StorageError::Closed);
        }
        match deadline {
            Some(limit) => tokio::time::timeout(limit, self.run_leased(&op))
                .await
                .map_err(|_| StorageError::Timeout)?,
            None => self.run_leased(&op).await,
        }
    }

    async fn run_leased(&self, op: &Op<'_>) -> Result<Out, StorageError> {
        let mut lease = self.checkout().await?;
        let mut cancel = CancelOnDrop(Some(lease.conn.client.cancel_token()));
        let result = exec(&mut lease.conn, op).await;
        cancel.disarm();
        // A server error leaves the connection healthy; anything else is not
        // trusted. A dropped (cancelled) call never reaches this point, so its
        // connection is discarded with the lease.
        let keep = matches!(result, Ok(_) | Err(StorageError::Server { .. }));
        self.checkin(lease, keep);
        result
    }

    async fn checkout(&self) -> Result<Lease, StorageError> {
        let permit = Arc::clone(&self.shared.permits)
            .acquire_owned()
            .await
            .map_err(|_| StorageError::Closed)?;
        loop {
            let candidate = self.shared.idle().pop();
            match candidate {
                Some(conn) if conn.client.is_closed() => {}
                Some(conn) => {
                    return Ok(Lease {
                        conn,
                        _permit: permit,
                    });
                }
                None => break,
            }
        }
        let conn = connect(&self.shared.config).await?;
        Ok(Lease {
            conn,
            _permit: permit,
        })
    }

    fn checkin(&self, lease: Lease, keep: bool) {
        if keep && !lease.conn.client.is_closed() && !self.shared.closed.load(Ordering::SeqCst) {
            self.shared.idle().push(lease.conn);
        }
    }
}

fn internal(op: &str) -> StorageError {
    StorageError::Client {
        context: op.to_string(),
        detail: "internal result mismatch".to_string(),
    }
}

async fn exec(conn: &mut Conn, op: &Op<'_>) -> Result<Out, StorageError> {
    match *op {
        Op::Get(key) => {
            let context = || format!("get storage key {}", go_quote(key));
            let statement = conn
                .prepare(Kind::Get)
                .await
                .map_err(|e| server_error(context(), &e))?;
            let rows = conn
                .client
                .query(&statement, &[&key])
                .await
                .map_err(|e| server_error(context(), &e))?;
            let Some(row) = rows.first() else {
                return Err(StorageError::NotFound(key.to_string()));
            };
            let value: Raw = row.try_get(0).map_err(|e| server_error(context(), &e))?;
            Ok(Out::Value(value.0))
        }
        Op::Set(key, value) => {
            let context = || format!("set storage key {}", go_quote(key));
            let statement = conn
                .prepare(Kind::Set)
                .await
                .map_err(|e| server_error(context(), &e))?;
            let value = Raw(value.to_vec());
            conn.client
                .execute(&statement, &[&key, &value])
                .await
                .map_err(|e| server_error(context(), &e))?;
            Ok(Out::Done)
        }
        Op::Remove(key) => {
            let context = || format!("remove storage key {}", go_quote(key));
            let statement = conn
                .prepare(Kind::Remove)
                .await
                .map_err(|e| server_error(context(), &e))?;
            conn.client
                .execute(&statement, &[&key])
                .await
                .map_err(|e| server_error(context(), &e))?;
            Ok(Out::Done)
        }
        Op::Query(prefix) => {
            // pgx reports statement preparation failures from Query and
            // everything the server says while executing from rows.Err().
            let statement = conn.prepare(Kind::Query).await.map_err(|e| {
                server_error(
                    format!("query storage keys with prefix {}", go_quote(prefix)),
                    &e,
                )
            })?;
            let rows = conn
                .client
                .query(&statement, &[&prefix])
                .await
                .map_err(|e| server_error("iterate storage entries".to_string(), &e))?;
            let mut entries = Vec::with_capacity(rows.len());
            for row in &rows {
                let key: String = row
                    .try_get(0)
                    .map_err(|e| server_error("scan storage entry".to_string(), &e))?;
                let value: Raw = row
                    .try_get(1)
                    .map_err(|e| server_error("scan storage entry".to_string(), &e))?;
                entries.push(Entry {
                    key,
                    value: value.0,
                });
            }
            Ok(Out::Entries(entries))
        }
    }
}

/// Operations with a deadline; see [`PostgresBackend::bounded`].
pub struct Bounded<'a> {
    backend: &'a PostgresBackend,
    deadline: Duration,
}

impl Bounded<'_> {
    pub async fn get(&self, key: &str) -> Result<Vec<u8>, StorageError> {
        self.backend.get_within(key, Some(self.deadline)).await
    }

    pub async fn set(&self, key: &str, value: &[u8]) -> Result<(), StorageError> {
        self.backend
            .set_within(key, value, Some(self.deadline))
            .await
    }

    pub async fn remove(&self, key: &str) -> Result<(), StorageError> {
        self.backend.remove_within(key, Some(self.deadline)).await
    }

    pub async fn query(&self, prefix: &str) -> Result<Vec<Entry>, StorageError> {
        self.backend.query_within(prefix, Some(self.deadline)).await
    }
}
