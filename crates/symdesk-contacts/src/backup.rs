//! Encrypted, self-contained backups: `SYMRB1\0 | salt(16) | nonce(12) |
//! AES-256-GCM(SQLite snapshot)` with an Argon2id (t=1, m=64 MiB, p=4) key.
//! Restore authenticates and decrypts fully in memory before touching disk.
use crate::contact::{Error, error, sql_error};
use crate::security::Service;
use aes_gcm::aead::{Aead, KeyInit};
use aes_gcm::{Aes256Gcm, Nonce};
use argon2::{Algorithm, Argon2, Params, Version};
use rusqlite::{Connection, params};
use std::io::Write;
use std::path::{Path, PathBuf};

pub const MAGIC: [u8; 7] = *b"SYMRB1\0";
pub const SALT_SIZE: usize = 16;
pub const NONCE_SIZE: usize = 12;
pub const HEADER_SIZE: usize = MAGIC.len() + SALT_SIZE + NONCE_SIZE;
const KEY_SIZE: usize = 32;
const WRONG_PASSPHRASE: &str = "security: wrong passphrase or corrupted backup";

fn cipher(passphrase: &[u8], salt: &[u8]) -> Result<Aes256Gcm, String> {
    let params = Params::new(64 * 1024, 1, 4, Some(KEY_SIZE)).map_err(|e| e.to_string())?;
    let mut key = [0u8; KEY_SIZE];
    Argon2::new(Algorithm::Argon2id, Version::V0x13, params)
        .hash_password_into(passphrase, salt, &mut key)
        .map_err(|e| e.to_string())?;
    Aes256Gcm::new_from_slice(&key).map_err(|e| e.to_string())
}

fn random<const N: usize>() -> Result<[u8; N], String> {
    let mut buf = [0u8; N];
    getrandom::fill(&mut buf).map_err(|e| e.to_string())?;
    Ok(buf)
}

/// Encrypt `plaintext`; returns `(ciphertext+tag, salt, nonce)`.
fn encrypt(
    passphrase: &[u8],
    plaintext: &[u8],
) -> Result<(Vec<u8>, [u8; SALT_SIZE], [u8; NONCE_SIZE]), String> {
    let salt = random::<SALT_SIZE>()?;
    let nonce = random::<NONCE_SIZE>()?;
    let sealed = cipher(passphrase, &salt)?
        .encrypt(&Nonce::try_from(&nonce[..]).map_err(|e| format!("{e:?}"))?, plaintext)
        .map_err(|e| e.to_string())?;
    Ok((sealed, salt, nonce))
}

fn decrypt(passphrase: &[u8], salt: &[u8], nonce: &[u8], ciphertext: &[u8]) -> Result<Vec<u8>, String> {
    cipher(passphrase, salt)?
        .decrypt(&Nonce::try_from(nonce).map_err(|e| format!("{e:?}"))?, ciphertext)
        .map_err(|e| e.to_string())
}

fn temp_snapshot_path() -> Result<PathBuf, String> {
    let suffix = u64::from_le_bytes(random::<8>()?);
    Ok(std::env::temp_dir().join(format!("symrelate-backup-{suffix:016x}.db")))
}

impl<I: FnMut() -> String, T: FnMut() -> String, A: FnMut() -> String> Service<'_, I, T, A> {
    /// Write an encrypted point-in-time snapshot (`VACUUM INTO`, safe under
    /// WAL) of the live database to `w`.
    pub fn backup(&mut self, passphrase: &[u8], w: &mut impl Write) -> Result<(), Error> {
        const OP: &str = "security.Backup";
        if passphrase.is_empty() {
            return Err(error("invalid", OP, "a passphrase is required to create a backup"));
        }
        let internal = |message: &str, cause: String| error("internal", OP, &format!("{message}: {cause}"));
        let path = temp_snapshot_path().map_err(|c| internal("failed to create temp snapshot file", c))?;
        let _cleanup = Cleanup(path.clone());
        let target = path.to_string_lossy().into_owned();
        self.db
            .execute("VACUUM INTO ?", params![target])
            .map_err(|e| internal("failed to snapshot database", sql_error(e).message))?;
        let plaintext = std::fs::read(&path).map_err(|e| internal("failed to read database snapshot", e.to_string()))?;
        let (ciphertext, salt, nonce) =
            encrypt(passphrase, &plaintext).map_err(|c| internal("failed to encrypt backup", c))?;
        for chunk in [&MAGIC[..], &salt, &nonce, &ciphertext] {
            w.write_all(chunk).map_err(|e| internal("failed to write backup", e.to_string()))?;
        }
        self.record_audit("backup_created", &format!("bytes={}", ciphertext.len()))
    }
}

struct Cleanup(PathBuf);
impl Drop for Cleanup {
    fn drop(&mut self) {
        let _ = std::fs::remove_file(&self.0);
    }
}

/// Decrypt `data` into `target`; a wrong passphrase or damaged backup leaves
/// `target` untouched. The audit event goes to the *restored* database.
pub fn restore(
    passphrase: &[u8],
    data: &[u8],
    target: &Path,
    id: impl FnMut() -> String,
    timestamp: impl FnMut() -> String,
    actor: impl FnMut() -> String,
) -> Result<(), Error> {
    const OP: &str = "security.Restore";
    if passphrase.is_empty() {
        return Err(error("invalid", OP, "a passphrase is required to restore a backup"));
    }
    if data.len() < HEADER_SIZE {
        return Err(error("invalid", OP, "backup is truncated or not a symrelate backup"));
    }
    if data[..MAGIC.len()] != MAGIC {
        return Err(error("invalid", OP, "not a symrelate backup file"));
    }
    let salt = &data[MAGIC.len()..MAGIC.len() + SALT_SIZE];
    let nonce = &data[MAGIC.len() + SALT_SIZE..HEADER_SIZE];
    let plaintext = decrypt(passphrase, salt, nonce, &data[HEADER_SIZE..])
        .map_err(|_| error("invalid", OP, &format!("wrong passphrase or corrupted backup: {WRONG_PASSPHRASE}")))?;

    let internal = |message: &str, cause: String| error("internal", OP, &format!("{message}: {cause}"));
    let mut tmp = target.as_os_str().to_owned();
    tmp.push(".restoring");
    let tmp = PathBuf::from(tmp);
    write_private(&tmp, &plaintext).map_err(|e| internal("failed to write restored database", e.to_string()))?;
    if let Err(e) = std::fs::rename(&tmp, target) {
        let _ = std::fs::remove_file(&tmp);
        return Err(internal("failed to finalize restored database", e.to_string()));
    }
    let db: Connection = crate::open(target).map_err(|e| error("internal", OP, &format!("restored database failed to open: {}", sql_error(e).message)))?;
    let mut service = Service::new(&db, id, timestamp, actor);
    service.record_audit("backup_restored", &format!("bytes={}", plaintext.len()))
}

fn write_private(path: &Path, bytes: &[u8]) -> std::io::Result<()> {
    use std::fs::OpenOptions;
    let mut options = OpenOptions::new();
    options.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    options.open(path)?.write_all(bytes)
}
