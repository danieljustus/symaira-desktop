#![deny(unsafe_code)]

//! Ed25519 identities: member ids, key material, the identity file and the
//! resolution chains. Port of `internal/room/identity`.

use std::fmt;
use std::{fs, path::PathBuf, process::Command};

use ed25519_dalek::{Signature, Signer, SigningKey, Verifier, VerifyingKey};
use sha2::{Digest, Sha256};

/// Go: `identity.ErrIdentityNotFound`.
pub const IDENTITY_NOT_FOUND: &str = "identity not found";
/// Go: `identity.ErrInvalidKey`.
pub const INVALID_KEY: &str = "invalid key data";

const PRIVATE_KEY_SIZE: usize = 64;
const SEED_SIZE: usize = 32;
const PUBLIC_KEY_SIZE: usize = 32;

#[derive(Debug, Clone, PartialEq, Eq)]
pub enum IdentityError {
    /// No chain produced key material.
    NotFound,
    /// Key material existed but was not usable.
    InvalidKey,
    /// A wrapped filesystem or encoding failure, already prefixed like Go.
    Message(String),
}

impl fmt::Display for IdentityError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::NotFound => f.write_str(IDENTITY_NOT_FOUND),
            Self::InvalidKey => f.write_str(INVALID_KEY),
            Self::Message(message) => f.write_str(message),
        }
    }
}

impl std::error::Error for IdentityError {}

/// Go: `identity.Identity`.
#[derive(Debug, Clone, PartialEq, Eq)]
pub struct Identity {
    pub name: String,
    pub member_id: String,
    pub public_key: Vec<u8>,
    pub private_key: Vec<u8>,
}

/// Go: `identity.StoredIdentity`. Field order matters: Go marshals structs in
/// declaration order, and the identity file is compared byte for byte.
///
/// Every field defaults on read, like Go's `json.Unmarshal` into a struct: a
/// file missing `member_id` is not a decode error there, it reaches the key
/// validation and reports `invalid key data`.
#[derive(Debug, Clone, serde::Serialize, serde::Deserialize)]
pub struct StoredIdentity {
    #[serde(default)]
    pub name: String,
    #[serde(default)]
    pub member_id: String,
    #[serde(default)]
    pub public_key: String,
    #[serde(default)]
    pub private_key: String,
}

/// Go: `identity.ComputeMemberID` — the first eight bytes of the SHA-256 of the
/// public key, hex encoded behind a `mem_` prefix.
pub fn compute_member_id(public_key: &[u8]) -> String {
    let digest = Sha256::digest(public_key);
    format!("mem_{}", hex::encode(&digest[..8]))
}

/// Go: `identity.IdentitiesDir`.
pub fn identities_dir() -> PathBuf {
    if let Ok(data_home) = std::env::var("XDG_DATA_HOME")
        && !data_home.is_empty()
    {
        return PathBuf::from(data_home).join("symroom").join("identities");
    }
    let home = std::env::var("HOME").unwrap_or_else(|_| ".".to_owned());
    PathBuf::from(home)
        .join(".local")
        .join("share")
        .join("symroom")
        .join("identities")
}

/// Build an identity from raw private key bytes (32-byte seed or 64-byte key).
pub fn identity_from_private_key(name: &str, private_key: &[u8]) -> Option<Identity> {
    let signing_key = match private_key.len() {
        SEED_SIZE => SigningKey::from_bytes(
            &<[u8; SEED_SIZE]>::try_from(private_key).expect("length checked above"),
        ),
        PRIVATE_KEY_SIZE => {
            // Go stores seed || public key; verify the public half matches.
            let seed: [u8; SEED_SIZE] = private_key[..SEED_SIZE].try_into().ok()?;
            let candidate = SigningKey::from_bytes(&seed);
            if candidate.verifying_key().to_bytes().as_slice() != &private_key[SEED_SIZE..] {
                return None;
            }
            candidate
        }
        _ => return None,
    };
    let public_key = signing_key.verifying_key().to_bytes().to_vec();
    // Go's ed25519.PrivateKey is seed || public key (64 bytes); ed25519-dalek
    // keeps only the seed, so the public half is appended here.
    let mut private_key = signing_key.to_bytes().to_vec();
    private_key.extend_from_slice(&public_key);
    Some(Identity {
        name: name.to_owned(),
        member_id: compute_member_id(&public_key),
        public_key,
        private_key,
    })
}

/// Generate an identity with the operating system CSPRNG.
///
/// Go: `identity.Generate`. `getrandom` delegates to the platform CSPRNG.
pub fn generate(name: &str) -> Result<Identity, IdentityError> {
    let mut seed = [0_u8; SEED_SIZE];
    getrandom::fill(&mut seed)
        .map_err(|error| IdentityError::Message(format!("generate ed25519 key: {error}")))?;
    identity_from_private_key(name, &seed).ok_or(IdentityError::InvalidKey)
}

/// Go: `identity.Sign`.
pub fn sign(private_key: &[u8], message: &[u8]) -> Option<Signature> {
    let signing_key = signing_key_from(private_key)?;
    Some(signing_key.sign(message))
}

/// Go: `identity.Verify`. Uses the permissive RFC 8032 check, like
/// `crypto/ed25519.Verify`.
pub fn verify(public_key: &[u8], message: &[u8], signature: &[u8]) -> bool {
    let Ok(key_bytes) = <[u8; PUBLIC_KEY_SIZE]>::try_from(public_key) else {
        return false;
    };
    let Ok(key) = VerifyingKey::from_bytes(&key_bytes) else {
        return false;
    };
    let Ok(signature) = Signature::from_slice(signature) else {
        return false;
    };
    key.verify(message, &signature).is_ok()
}

fn signing_key_from(private_key: &[u8]) -> Option<SigningKey> {
    match private_key.len() {
        SEED_SIZE => Some(SigningKey::from_bytes(
            &<[u8; SEED_SIZE]>::try_from(private_key).expect("length checked above"),
        )),
        PRIVATE_KEY_SIZE => {
            let seed: [u8; SEED_SIZE] = private_key[..SEED_SIZE].try_into().ok()?;
            let key = SigningKey::from_bytes(&seed);
            if key.verifying_key().to_bytes().as_slice() != &private_key[SEED_SIZE..] {
                return None;
            }
            Some(key)
        }
        _ => None,
    }
}

/// Go: `identity.Save` — two-space indented JSON in a `0700` directory, the file
/// itself written with `0600`.
pub fn save(identity: &Identity) -> Result<(), IdentityError> {
    let dir = identities_dir();
    fs::create_dir_all(&dir)
        .map_err(|err| IdentityError::Message(format!("create identities dir: {err}")))?;
    restrict_mode(&dir, 0o700);

    let stored = StoredIdentity {
        name: identity.name.clone(),
        member_id: identity.member_id.clone(),
        public_key: hex::encode(&identity.public_key),
        private_key: hex::encode(&identity.private_key),
    };
    let data = serde_json::to_string_pretty(&stored)
        .map_err(|err| IdentityError::Message(format!("marshal identity: {err}")))?;

    let path = dir.join(format!("{}.json", identity.name));
    fs::write(&path, data)
        .map_err(|err| IdentityError::Message(format!("write identity file: {err}")))?;
    restrict_mode(&path, 0o600);
    Ok(())
}

/// Go: `identity.Load` — environment, optional `symvault`, optional macOS
/// Keychain, then the identity file. Provider lookups are runtime-only so
/// standalone consumers do not need either provider installed.
pub fn load(name: &str) -> Result<Identity, IdentityError> {
    if let Ok(raw) = std::env::var("SYMROOM_IDENTITY_KEY")
        && let Ok(bytes) = hex::decode(raw.trim())
        && matches!(bytes.len(), SEED_SIZE | PRIVATE_KEY_SIZE)
        && let Some(identity) = identity_from_private_key(name, &bytes)
    {
        return Ok(identity);
    }

    let vault_key = format!("symroom/identities/{name}");
    if let Ok(output) = Command::new("symvault")
        .args(["get", vault_key.as_str()])
        .output()
        && output.status.success()
        && let Some(identity) = identity_from_provider_output(name, &output.stdout)
    {
        return Ok(identity);
    }

    #[cfg(target_os = "macos")]
    if let Ok(output) = Command::new("security")
        .args([
            "find-generic-password",
            "-s",
            "symroom-identity",
            "-a",
            name,
            "-w",
        ])
        .output()
        && output.status.success()
        && let Some(identity) = identity_from_provider_output(name, &output.stdout)
    {
        return Ok(identity);
    }

    let path = identities_dir().join(format!("{name}.json"));
    let data = match fs::read(&path) {
        Ok(data) => data,
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => {
            return Err(IdentityError::NotFound);
        }
        Err(err) => return Err(IdentityError::Message(format!("read identity file: {err}"))),
    };

    let stored: StoredIdentity = serde_json::from_slice(&data)
        .map_err(|err| IdentityError::Message(format!("unmarshal identity file: {err}")))?;

    let private_key = hex::decode(&stored.private_key).map_err(|_| IdentityError::InvalidKey)?;
    if private_key.len() != PRIVATE_KEY_SIZE {
        return Err(IdentityError::InvalidKey);
    }
    let public_key = hex::decode(&stored.public_key).map_err(|_| IdentityError::InvalidKey)?;
    if public_key.len() != PUBLIC_KEY_SIZE {
        return Err(IdentityError::InvalidKey);
    }

    Ok(Identity {
        name: stored.name,
        member_id: stored.member_id,
        public_key,
        private_key,
    })
}

fn identity_from_provider_output(name: &str, output: &[u8]) -> Option<Identity> {
    let value = std::str::from_utf8(output).ok()?.trim();
    let bytes = hex::decode(value).ok()?;
    (bytes.len() == PRIVATE_KEY_SIZE)
        .then(|| identity_from_private_key(name, &bytes))
        .flatten()
}

/// Go: `identity.List` — every `*.json` in the identities directory, in the
/// byte order Go's `os.ReadDir` returns (sorted by name); a missing directory is
/// an empty list.
pub fn list() -> Result<Vec<String>, IdentityError> {
    let dir = identities_dir();
    let entries = match fs::read_dir(&dir) {
        Ok(entries) => entries,
        Err(err) if err.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(err) => return Err(IdentityError::Message(err.to_string())),
    };

    let mut names = Vec::new();
    for entry in entries {
        let entry = entry.map_err(|err| IdentityError::Message(err.to_string()))?;
        let file_name = entry.file_name().to_string_lossy().into_owned();
        if entry.path().is_dir() {
            continue;
        }
        if let Some(stripped) = file_name.strip_suffix(".json") {
            names.push(stripped.to_owned());
        }
    }
    names.sort();
    Ok(names)
}

#[cfg(unix)]
fn restrict_mode(path: &std::path::Path, mode: u32) {
    use std::os::unix::fs::PermissionsExt;
    let _ = fs::set_permissions(path, fs::Permissions::from_mode(mode));
}

#[cfg(not(unix))]
fn restrict_mode(_path: &std::path::Path, _mode: u32) {
    // Windows carries no Unix permission bits; Go writes the same bytes there.
}
