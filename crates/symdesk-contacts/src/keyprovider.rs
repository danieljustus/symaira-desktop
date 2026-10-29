//! Injectable key-provider policy. External command, environment and terminal
//! access are owned by caller adapters; this module never touches an OS store.
use std::fmt;

pub const DEFAULT_ENV_VAR: &str = "SYMRELATE_BACKUP_PASSPHRASE";
pub const DEFAULT_KEY_NAME: &str = "symrelate-backup";

// Deliberately not Debug/Serialize: key material must not enter diagnostics.
pub struct Key {
    pub passphrase: Vec<u8>,
    pub source: String,
}
#[derive(Debug, Clone, PartialEq, Eq)]
pub enum Error {
    Unavailable,
    Failure(String),
}
impl fmt::Display for Error {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::Unavailable => f.write_str("security: key unavailable"),
            Self::Failure(message) => f.write_str(message),
        }
    }
}
impl std::error::Error for Error {}

pub fn resolve_static(passphrase: &[u8]) -> Result<Key, Error> {
    if passphrase.is_empty() {
        return Err(Error::Unavailable);
    }
    Ok(Key {
        passphrase: passphrase.to_vec(),
        source: "explicit".to_owned(),
    })
}
pub fn resolve_env(
    name: &str,
    mut lookup: impl FnMut(&str) -> Option<Vec<u8>>,
) -> Result<Key, Error> {
    let name = if name.is_empty() {
        DEFAULT_ENV_VAR
    } else {
        name
    };
    let passphrase = lookup(name)
        .filter(|x| !x.is_empty())
        .ok_or(Error::Unavailable)?;
    Ok(Key {
        passphrase,
        source: format!("env:{name}"),
    })
}
/// The adapter must resolve `symvault` and execute `get`, with the requested
/// key name as a distinct argv element. Absence, process failure and empty
/// trimmed stdout all mean unavailable, not a fatal chain error.
pub fn resolve_symvault(
    name: &str,
    mut run: impl FnMut(&str, &str) -> Result<Vec<u8>, Error>,
) -> Result<Key, Error> {
    let name = if name.is_empty() {
        DEFAULT_KEY_NAME
    } else {
        name
    };
    let bytes = run("get", name).map_err(|_| Error::Unavailable)?;
    let passphrase = trim_space(&bytes);
    if passphrase.is_empty() {
        return Err(Error::Unavailable);
    }
    Ok(Key {
        passphrase: passphrase.to_vec(),
        source: "symvault".to_owned(),
    })
}
/// A typed Unavailable error is the only error which permits fallback.
pub fn resolve_chain<'a>(
    providers: impl IntoIterator<Item = &'a mut dyn FnMut() -> Result<Key, Error>>,
) -> Result<Key, Error> {
    for provider in providers {
        match provider() {
            Err(Error::Unavailable) => {}
            result => return result,
        }
    }
    Err(Error::Unavailable)
}
/// Default policy keeps the terminal last and never calls later adapters once
/// a provider succeeds. Confirmation only affects that last adapter.
pub fn resolve_default(
    explicit: &[u8],
    lookup: impl FnMut(&str) -> Option<Vec<u8>>,
    run: impl FnMut(&str, &str) -> Result<Vec<u8>, Error>,
    terminal: impl FnOnce(bool) -> Result<Key, Error>,
    confirm: bool,
) -> Result<Key, Error> {
    if let Ok(key) = resolve_static(explicit) {
        return Ok(key);
    }
    if let Ok(key) = resolve_env("", lookup) {
        return Ok(key);
    }
    if let Ok(key) = resolve_symvault("", run) {
        return Ok(key);
    }
    terminal(confirm)
}

pub trait Terminal {
    fn is_terminal(&self) -> bool;
    /// The adapter prints this prompt to stderr and reads without echo. It
    /// must emit a trailing newline only on a successful read, just as Go does.
    fn read_password(&mut self, prompt: &str) -> Result<Vec<u8>, String>;
}
pub fn resolve_terminal(
    terminal: &mut impl Terminal,
    prompt: &str,
    confirm: bool,
) -> Result<Key, Error> {
    if !terminal.is_terminal() {
        return Err(Error::Unavailable);
    }
    let prompt = if prompt.is_empty() {
        "Passphrase: "
    } else {
        prompt
    };
    let read_error = |e| Error::Failure(format!("terminal read failed: {e}"));
    let passphrase = terminal.read_password(prompt).map_err(read_error)?;
    if confirm {
        let confirmation = terminal
            .read_password("Confirm passphrase: ")
            .map_err(read_error)?;
        if passphrase != confirmation {
            return Err(Error::Failure("passphrases do not match".to_owned()));
        }
    }
    if passphrase.is_empty() {
        return Err(Error::Unavailable);
    }
    Ok(Key {
        passphrase,
        source: "terminal".to_owned(),
    })
}

// bytes.TrimSpace preserves invalid UTF-8 bytes rather than replacing them.
fn trim_space(mut bytes: &[u8]) -> &[u8] {
    while !bytes.is_empty() {
        let width = match bytes[0] {
            0..=127 => 1,
            194..=223 => 2,
            224..=239 => 3,
            240..=244 => 4,
            _ => break,
        };
        let Some(prefix) = bytes.get(..width) else {
            break;
        };
        if !std::str::from_utf8(prefix)
            .ok()
            .is_some_and(|s| s.chars().all(char::is_whitespace))
        {
            break;
        }
        bytes = &bytes[width..];
    }
    while !bytes.is_empty() {
        let start = bytes.iter().rposition(|b| b & 0xc0 != 0x80).unwrap_or(0);
        if !std::str::from_utf8(&bytes[start..])
            .ok()
            .is_some_and(|s| s.chars().all(char::is_whitespace))
        {
            break;
        }
        bytes = &bytes[..start];
    }
    bytes
}
