//! Room artifact links, hashes and their journal projection.

use std::{
    collections::BTreeMap,
    fs,
    io::Read,
    path::{Component, Path, PathBuf},
};

use serde::{Deserialize, Serialize};
use serde_json::value::RawValue;
use sha2::{Digest, Sha256};

use crate::{
    desk_watch::EventStreamItem,
    event::{self, Event},
    identity::Identity,
    journal,
};

const OUTSIDE_ROOT: &str = "path is outside artifact root";

#[derive(Debug, Clone, PartialEq, Eq, Serialize)]
pub struct ArtifactRef {
    pub id: String,
    pub path: String,
    pub sha256: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub title: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub symdesk_id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub status: String,
}

#[derive(Debug, thiserror::Error)]
pub enum ArtifactError {
    #[error("{OUTSIDE_ROOT}")]
    OutsideRoot,
    #[error("{0}")]
    Message(String),
}

#[derive(Serialize)]
struct LinkedBody<'a> {
    artifact_id: &'a str,
    path: &'a str,
    sha256: &'a str,
    symdesk_id: &'a str,
    title: &'a str,
}

#[derive(Deserialize)]
struct LinkedBodyOwned {
    #[serde(default)]
    artifact_id: String,
    #[serde(default)]
    path: String,
    #[serde(default)]
    sha256: String,
    #[serde(default)]
    symdesk_id: String,
    #[serde(default)]
    title: String,
}

#[derive(Deserialize)]
struct UnlinkedBody {
    #[serde(default)]
    artifact_id: String,
}

pub fn link(
    room_dir: &Path,
    file_path: &Path,
    title: &str,
    signer: &Identity,
) -> Result<Event, ArtifactError> {
    let rel = make_relative_path(file_path, room_dir)?;
    let abs_path = room_dir.join(&rel);
    let hash = hash_file(&abs_path)
        .map_err(|error| ArtifactError::Message(format!("compute sha256: {error}")))?;
    let digest = Sha256::digest(format!("{}{}", rel.to_string_lossy(), hash).as_bytes());
    let artifact_id = format!("art_{}", hex::encode(digest)[..16].to_owned());
    let rel_text = rel.to_string_lossy().replace('\\', "/");
    let title = if title.is_empty() {
        rel.file_name()
            .unwrap_or_default()
            .to_string_lossy()
            .into_owned()
    } else {
        title.to_owned()
    };
    let body = LinkedBody {
        artifact_id: &artifact_id,
        path: &rel_text,
        sha256: &hash,
        symdesk_id: "",
        title: &title,
    };
    let raw_body = json_body(&body)?;
    let id_suffix = &artifact_id[4..];
    let event = append_signed_event(
        room_dir,
        signer,
        format!("ev_{id_suffix}"),
        "artifact.linked",
        raw_body,
    )?;
    Ok(event)
}

pub fn unlink(
    room_dir: &Path,
    artifact_id: &str,
    signer: &Identity,
) -> Result<Event, ArtifactError> {
    let body = serde_json::json!({"artifact_id": artifact_id});
    let raw_body = json_body(&body)?;
    let digest = Sha256::digest(artifact_id.as_bytes());
    let event_id = format!("ev_{}", &hex::encode(digest)[..16]);
    append_signed_event(room_dir, signer, event_id, "artifact.unlinked", raw_body)
}

/// Port of Go `artifact.HandleDeskEvent` for the `symdesk events` watcher.
pub fn handle_desk_event(
    room_dir: &Path,
    artifact_root: &Path,
    item: &EventStreamItem,
    signer: &Identity,
) -> Result<(), ArtifactError> {
    let root = if artifact_root.as_os_str().is_empty() {
        room_dir
    } else {
        artifact_root
    };
    let artifacts = list(room_dir)?;
    let relative = make_relative_path(Path::new(&item.path), root)
        .unwrap_or_else(|_| PathBuf::from(&item.path));
    let relative = clean_path(&relative);
    let Some(target) = artifacts.into_iter().find(|artifact| {
        artifact.path == relative.to_string_lossy()
            || clean_path(Path::new(&artifact.path)) == relative
    }) else {
        return Ok(());
    };

    let path = root.join(&target.path);
    let hash = match hash_file(&path) {
        Ok(hash) => hash,
        Err(_) if item.event == "file_removed" => String::new(),
        Err(_) => return Ok(()),
    };
    let body = BTreeMap::from([
        ("artifact_id", target.id.as_str()),
        ("event_type", item.event.as_str()),
        ("path", target.path.as_str()),
        ("sha256", hash.as_str()),
    ]);
    let raw_body = json_body(&body)?;
    let digest = Sha256::digest(format!("{}{}{}", target.id, hash, item.event).as_bytes());
    append_signed_event(
        room_dir,
        signer,
        format!("ev_{}", &hex::encode(digest)[..16]),
        "artifact.changed",
        raw_body,
    )?;
    Ok(())
}

pub fn list(room_dir: &Path) -> Result<Vec<ArtifactRef>, ArtifactError> {
    let events =
        journal::merge_all(room_dir).map_err(|error| ArtifactError::Message(error.to_string()))?;
    let mut active = BTreeMap::<String, ArtifactRef>::new();
    for event in events {
        match event.kind.as_str() {
            "artifact.linked" => {
                if let Ok(body) = serde_json::from_str::<LinkedBodyOwned>(event.body.get()) {
                    active.insert(
                        body.artifact_id.clone(),
                        ArtifactRef {
                            id: body.artifact_id,
                            path: body.path,
                            sha256: body.sha256,
                            title: body.title,
                            symdesk_id: body.symdesk_id,
                            status: String::new(),
                        },
                    );
                }
            }
            "artifact.unlinked" => {
                if let Ok(body) = serde_json::from_str::<UnlinkedBody>(event.body.get()) {
                    active.remove(&body.artifact_id);
                }
            }
            _ => {}
        }
    }
    let mut result = Vec::with_capacity(active.len());
    for mut artifact in active.into_values() {
        match hash_file(&room_dir.join(&artifact.path)) {
            Ok(hash) if hash == artifact.sha256 => artifact.status = "ok".to_owned(),
            Ok(_) => artifact.status = "modified".to_owned(),
            Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
                artifact.status = "missing".to_owned()
            }
            Err(_) => artifact.status = "error".to_owned(),
        }
        result.push(artifact);
    }
    Ok(result)
}

fn append_signed_event(
    room_dir: &Path,
    signer: &Identity,
    id: String,
    kind: &str,
    body: Box<RawValue>,
) -> Result<Event, ArtifactError> {
    let author = journal::author_stats(room_dir, &signer.member_id)
        .map_err(|error| ArtifactError::Message(error.to_string()))?;
    let stats = journal::read_journal_stats(room_dir)
        .map_err(|error| ArtifactError::Message(error.to_string()))?;
    let mut event = Event {
        v: event::CURRENT_VERSION,
        id,
        room: "rm_test".to_owned(),
        author: signer.member_id.clone(),
        seq: author.seq + 1,
        prev: author.prev,
        lamport: stats.max_lamport + 1,
        ts: event::current_timestamp(),
        kind: kind.to_owned(),
        body,
        sig: None,
    };
    event
        .sign(signer)
        .map_err(|error| ArtifactError::Message(error.to_string()))?;
    journal::append_event(room_dir, &event)
        .map_err(|error| ArtifactError::Message(error.to_string()))?;
    Ok(event)
}

fn hash_file(path: &Path) -> Result<String, std::io::Error> {
    let mut file = fs::File::open(path)?;
    let mut hash = Sha256::new();
    let mut buffer = [0_u8; 8192];
    loop {
        let count = file.read(&mut buffer)?;
        if count == 0 {
            break;
        }
        hash.update(&buffer[..count]);
    }
    Ok(hex::encode(hash.finalize()))
}

fn make_relative_path(target: &Path, root: &Path) -> Result<PathBuf, ArtifactError> {
    let cwd = std::env::current_dir().map_err(|error| ArtifactError::Message(error.to_string()))?;
    let target = normalize_absolute(if target.is_absolute() {
        target.to_path_buf()
    } else {
        cwd.join(target)
    });
    let root = normalize_absolute(if root.is_absolute() {
        root.to_path_buf()
    } else {
        cwd.join(root)
    });
    let relative = target
        .strip_prefix(&root)
        .map_err(|_| ArtifactError::OutsideRoot)?;
    if relative
        .components()
        .any(|component| matches!(component, Component::ParentDir))
    {
        return Err(ArtifactError::OutsideRoot);
    }
    Ok(relative.to_path_buf())
}

fn normalize_absolute(path: PathBuf) -> PathBuf {
    let mut normalized = PathBuf::new();
    for component in path.components() {
        match component {
            Component::CurDir => {}
            Component::ParentDir => {
                normalized.pop();
            }
            other => normalized.push(other.as_os_str()),
        }
    }
    normalized
}

fn clean_path(path: &Path) -> PathBuf {
    let mut clean = PathBuf::new();
    for component in path.components() {
        match component {
            Component::CurDir => {}
            Component::ParentDir => {
                clean.pop();
            }
            other => clean.push(other.as_os_str()),
        }
    }
    clean
}

fn json_body(value: &impl Serialize) -> Result<Box<RawValue>, ArtifactError> {
    let encoded = serde_json::to_string(value)
        .map_err(|error| ArtifactError::Message(format!("marshal event body: {error}")))?;
    let encoded = encoded
        .replace('&', "\\u0026")
        .replace('<', "\\u003c")
        .replace('>', "\\u003e")
        .replace('\u{2028}', "\\u2028")
        .replace('\u{2029}', "\\u2029");
    RawValue::from_string(encoded)
        .map_err(|error| ArtifactError::Message(format!("marshal event body: {error}")))
}
