#![deny(unsafe_code)]

//! Ed25519 identities: member ids, key material, the identity file and the
//! resolution chains. Port of `internal/room/identity`.

use std::fmt;
use std::{fs, path::PathBuf, process::Command};

use crate::lookpath::look_path;
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
///
/// `Debug` is written by hand so a stray `{:?}` or a failed `assert_eq!` never
/// prints the private key.
#[derive(Clone, PartialEq, Eq)]
pub struct Identity {
    pub name: String,
    pub member_id: String,
    pub public_key: Vec<u8>,
    pub private_key: Vec<u8>,
}

impl fmt::Debug for Identity {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("Identity")
            .field("name", &self.name)
            .field("member_id", &self.member_id)
            .field("public_key", &hex::encode(&self.public_key))
            .field("private_key", &REDACTED)
            .finish()
    }
}

const REDACTED: &str = "<redacted>";

/// Go: `identity.StoredIdentity`. Field order matters: Go marshals structs in
/// declaration order, and the identity file is compared byte for byte.
///
/// Every field defaults on read, like Go's `json.Unmarshal` into a struct: a
/// file missing `member_id` is not a decode error there, it reaches the key
/// validation and reports `invalid key data`.
#[derive(Clone, serde::Serialize, serde::Deserialize)]
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

impl fmt::Debug for StoredIdentity {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        f.debug_struct("StoredIdentity")
            .field("name", &self.name)
            .field("member_id", &self.member_id)
            .field("public_key", &self.public_key)
            .field("private_key", &REDACTED)
            .finish()
    }
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
///
/// A 64-byte key is taken as Go's `ed25519.PrivateKey(keyBytes)` conversion
/// does: the public half is whatever the last 32 bytes say, with no check that
/// it belongs to the seed (`Public()` returns those bytes verbatim). Go only
/// notices the mismatch, as an unverifiable signature, when it signs; Rust
/// refuses to sign with such a key (see [`sign`]).
pub fn identity_from_private_key(name: &str, private_key: &[u8]) -> Option<Identity> {
    let (public_key, private_key) = match private_key.len() {
        SEED_SIZE => {
            let signing_key = SigningKey::from_bytes(
                &<[u8; SEED_SIZE]>::try_from(private_key).expect("length checked above"),
            );
            let public_key = signing_key.verifying_key().to_bytes().to_vec();
            // Go's ed25519.PrivateKey is seed || public key (64 bytes);
            // ed25519-dalek keeps only the seed, so the public half is
            // appended here.
            let mut expanded = signing_key.to_bytes().to_vec();
            expanded.extend_from_slice(&public_key);
            (public_key, expanded)
        }
        PRIVATE_KEY_SIZE => (private_key[SEED_SIZE..].to_vec(), private_key.to_vec()),
        _ => return None,
    };
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

/// Why [`sign`] refused a key.
#[derive(Debug, Clone, Copy, PartialEq, Eq)]
pub enum SignError {
    /// Neither a 32-byte seed nor a 64-byte key (Go's `ed25519.Sign` panics).
    BadKeyLength,
    /// A 64-byte key whose public half does not belong to its seed.
    MismatchedPublicHalf,
}

impl fmt::Display for SignError {
    fn fmt(&self, f: &mut fmt::Formatter<'_>) -> fmt::Result {
        match self {
            Self::BadKeyLength => {
                f.write_str("refusing to sign: private key must be a 32-byte seed or a 64-byte key")
            }
            Self::MismatchedPublicHalf => f.write_str(
                "refusing to sign: the private key's public half does not match its seed",
            ),
        }
    }
}

impl std::error::Error for SignError {}

/// Go: `identity.Sign`.
///
/// Documented deviation: a 64-byte key whose public half does not belong to its
/// seed is refused ([`SignError::MismatchedPublicHalf`]). Go signs it anyway,
/// hashing the *stored* public half into the signature, which yields a
/// signature that verifies against neither key; Rust returns an error instead
/// of journaling an unverifiable event.
pub fn sign(private_key: &[u8], message: &[u8]) -> Result<Signature, SignError> {
    let signing_key = signing_key_from(private_key)?;
    Ok(signing_key.sign(message))
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

fn signing_key_from(private_key: &[u8]) -> Result<SigningKey, SignError> {
    match private_key.len() {
        SEED_SIZE => Ok(SigningKey::from_bytes(
            &<[u8; SEED_SIZE]>::try_from(private_key).expect("length checked above"),
        )),
        PRIVATE_KEY_SIZE => {
            let seed: [u8; SEED_SIZE] = private_key[..SEED_SIZE]
                .try_into()
                .map_err(|_| SignError::BadKeyLength)?;
            let key = SigningKey::from_bytes(&seed);
            if key.verifying_key().to_bytes().as_slice() != &private_key[SEED_SIZE..] {
                return Err(SignError::MismatchedPublicHalf);
            }
            Ok(key)
        }
        _ => Err(SignError::BadKeyLength),
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

    // Go resolves both providers with `exec.LookPath`, which refuses a hit found
    // through a relative or empty PATH entry (`ErrDot`); the chain then simply
    // falls through, so the spawn below only ever runs an absolute path.
    let vault_key = format!("symroom/identities/{name}");
    if let Some(symvault) = look_path("symvault")
        && let Ok(output) = Command::new(symvault)
            .args(["get", vault_key.as_str()])
            .output()
        && output.status.success()
        && let Some(identity) = identity_from_provider_output(name, &output.stdout)
    {
        return Ok(identity);
    }

    #[cfg(target_os = "macos")]
    if let Some(security) = look_path("security")
        && let Ok(output) = Command::new(security)
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

    let stored = decode_stored(&data)
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

/// Go: `json.Unmarshal(data, &StoredIdentity)` (see [`crate::gojson`]).
fn decode_stored(data: &[u8]) -> Result<StoredIdentity, String> {
    let mut fields = crate::gojson::decode_string_struct(
        data,
        "StoredIdentity",
        "identity.StoredIdentity",
        &["name", "member_id", "public_key", "private_key"],
    )?
    .into_iter()
    .map(Option::unwrap_or_default);
    Ok(StoredIdentity {
        name: fields.next().unwrap_or_default(),
        member_id: fields.next().unwrap_or_default(),
        public_key: fields.next().unwrap_or_default(),
        private_key: fields.next().unwrap_or_default(),
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

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn mismatched_public_half_loads_like_go_but_refuses_to_sign() {
        let seed = [7_u8; SEED_SIZE];
        let other = identity_from_private_key("other", &[9_u8; SEED_SIZE]).expect("other");
        let mut key = seed.to_vec();
        key.extend_from_slice(&other.public_key);

        let loaded = identity_from_private_key("probe", &key).expect("Go accepts the key");
        assert_eq!(loaded.public_key, other.public_key);
        assert_eq!(loaded.member_id, other.member_id);
        // Documented deviation: Go would sign with the stored public half and
        // emit a signature that verifies against neither key.
        assert_eq!(
            sign(&loaded.private_key, b"message"),
            Err(SignError::MismatchedPublicHalf)
        );

        let consistent = identity_from_private_key("seed", &seed).expect("seed");
        assert!(sign(&consistent.private_key, b"message").is_ok());
        assert_eq!(sign(&[1_u8; 5], b"message"), Err(SignError::BadKeyLength));
    }

    #[test]
    fn debug_output_never_contains_the_private_key() {
        let identity = identity_from_private_key("probe", &[7_u8; SEED_SIZE]).expect("identity");
        let private_hex = hex::encode(&identity.private_key);
        let seed_hex = hex::encode([7_u8; SEED_SIZE]);
        let stored = StoredIdentity {
            name: identity.name.clone(),
            member_id: identity.member_id.clone(),
            public_key: hex::encode(&identity.public_key),
            private_key: private_hex.clone(),
        };
        for text in [
            format!("{identity:?}"),
            format!("{stored:?}"),
            format!("{identity:#?}"),
        ] {
            assert!(!text.contains(&private_hex), "{text}");
            assert!(!text.contains(&seed_hex), "{text}");
            assert!(text.contains("<redacted>"), "{text}");
            assert!(text.contains("probe"), "{text}");
        }
    }

    #[test]
    fn other_key_lengths_are_rejected() {
        for length in [0, 16, 31, 33, 63, 65] {
            assert!(identity_from_private_key("x", &vec![1_u8; length]).is_none());
        }
    }
}
