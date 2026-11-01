//! `symdesk relations contact [--id] <name|id>` and `contact link|unlink`
//! (Go `cmd/symdesk/relations.go`, `internal/service/contact_references.go`,
//! `contact_links.go`): reference-only contact lookups against the local
//! contact store plus the two explicit note-frontmatter writers.
use std::{collections::BTreeMap, fs, path::Path, process::ExitCode};

use clap::{Arg, ArgAction, ArgMatches, Command};
use noyalib::{Mapping, Value};
use rusqlite::Connection;
use serde::Serialize;
use symdesk_contacts::{
    contact::Service,
    membership::{REF_PROVIDER, REF_SCHEMA_VERSION, Ref},
};
use symdesk_index::{
    Sidecar,
    docs::{DocsFilter, DocsResult},
    open_for_vault,
};
use symdesk_render::export_vault::{parse_file_in_root, read_in_root};
use symdesk_vault::{Document, go_path_error, render_go_yaml_map, secure_path, walk_markdown};

use crate::{emit_error, resolve_vault, write_go_json, write_stdout};

const NOT_FOUND: &str = "symrelate contact not found";
const STORE_UNAVAILABLE: &str = "symdesk contact store unavailable";

pub fn command() -> Command {
    Command::new("contact")
        .subcommand_precedence_over_arg(true)
        .arg(
            Arg::new("id")
                .long("id")
                .action(ArgAction::Set)
                .num_args(0..=1)
                .require_equals(true)
                .default_missing_value("true")
                .default_value("false")
                .overrides_with("id")
                .value_parser(|value: &str| match value {
                    "1" | "t" | "T" | "true" | "TRUE" | "True" => Ok(true),
                    "0" | "f" | "F" | "false" | "FALSE" | "False" => Ok(false),
                    _ => Err("invalid boolean value"),
                }),
        )
        .arg(Arg::new("name").num_args(0..).action(ArgAction::Append))
        .subcommand(Command::new("link").arg(Arg::new("args").num_args(0..)))
        .subcommand(Command::new("unlink").arg(Arg::new("args").num_args(0..)))
}

pub fn run(command: &ArgMatches, vault: Option<&str>, output_json: bool) -> ExitCode {
    let root = match resolve_vault(vault).map_err(|e| {
        if e.starts_with("vault path not configured") {
            "vault path not configured (use flag or SYMDESK_VAULT env)".to_owned()
        } else {
            crate::index_cli::index_vault_error(vault, &e)
        }
    }) {
        Ok(root) => root,
        Err(e) => return emit_error(e, output_json),
    };
    let mut sidecar = match open_for_vault(&root) {
        Ok(sidecar) => sidecar,
        Err(e) => return emit_error(e.to_string(), output_json),
    };
    let outcome = match command.subcommand() {
        Some(("link", link)) => {
            let args = values(link, "args");
            link_note(&root, &mut sidecar, &args[0], &args[1]).map(|reference| {
                Output::Map(BTreeMap::from([
                    ("path".to_owned(), args[0].clone()),
                    ("contact_id".to_owned(), reference.id),
                    ("status".to_owned(), "linked".to_owned()),
                ]))
            })
        }
        Some(("unlink", unlink)) => {
            let args = values(unlink, "args");
            unlink_note(&root, &mut sidecar, &args[0]).map(|()| {
                Output::Map(BTreeMap::from([
                    ("path".to_owned(), args[0].clone()),
                    ("status".to_owned(), "unlinked".to_owned()),
                ]))
            })
        }
        _ => {
            let name = values(command, "name").remove(0);
            let by_id = command.get_flag("id");
            if by_id {
                by_contact_id(&root, &mut sidecar, &name)
            } else {
                by_name(&root, &sidecar, &name)
            }
            .map(Output::References)
        }
    };
    if let Err(e) = sidecar.close() {
        eprintln!("warning: failed to close sidecar database: {e}");
    }
    match outcome {
        Ok(output) => output.print(output_json),
        Err(error) => emit_error(error, output_json),
    }
}

fn values(command: &ArgMatches, name: &str) -> Vec<String> {
    command
        .get_many::<String>(name)
        .map(|values| values.cloned().collect())
        .unwrap_or_default()
}

enum Output {
    References(Box<ContactReferences>),
    Map(BTreeMap<String, String>),
}

impl Output {
    fn print(&self, output_json: bool) -> ExitCode {
        match self {
            Self::References(references) if output_json => write_go_json(references),
            Self::References(references) => write_stdout(format!("{}\n", references.go_text())),
            Self::Map(map) if output_json => write_go_json(map),
            Self::Map(map) => write_stdout(format!(
                "map[{}]\n",
                map.iter()
                    .map(|(key, value)| format!("{key}:{value}"))
                    .collect::<Vec<_>>()
                    .join(" ")
            )),
        }
    }
}

#[derive(Serialize)]
struct ContactReferences {
    name: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    contact_id: String,
    refs: Vec<ContactRef>,
    documents: Vec<DocsResult>,
    meetings: Vec<MeetingRef>,
    store_available: bool,
}

#[derive(Serialize, Clone)]
struct ContactRef {
    provider: String,
    schema_version: i64,
    id: String,
    kind: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    display_name: String,
}

impl From<Ref> for ContactRef {
    fn from(reference: Ref) -> Self {
        Self {
            provider: reference.provider,
            schema_version: reference.schema_version,
            id: reference.id,
            kind: reference.kind,
            display_name: reference.display_name,
        }
    }
}

#[derive(Serialize)]
struct MeetingRef {
    path: String,
    title: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    meeting_id: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    started_at: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    participant: String,
}

impl ContactReferences {
    fn empty(name: &str) -> Self {
        Self {
            name: name.to_owned(),
            contact_id: String::new(),
            refs: Vec::new(),
            documents: Vec::new(),
            meetings: Vec::new(),
            store_available: false,
        }
    }

    /// Go `fmt.Printf("%+v", *ContactReferences)`.
    fn go_text(&self) -> String {
        let refs = self.refs.iter().map(|r| {
            format!(
                "{{Provider:{} SchemaVersion:{} ID:{} Kind:{} DisplayName:{} Extras:map[]}}",
                r.provider, r.schema_version, r.id, r.kind, r.display_name
            )
        });
        let documents = self.documents.iter().map(|r| {
            format!(
                "{{Path:{} Title:{} Type:{} DocumentDate:{} Person:{} Status:{} DueDate:{} Confidence:{} Correspondent:{} DocumentType:{} ASN:{} Tags:[{}]}}",
                r.path,
                r.title,
                r.file_type,
                r.document_date,
                r.person,
                r.status,
                r.due_date,
                r.confidence,
                r.correspondent,
                r.document_type,
                r.asn,
                r.tags.join(" ")
            )
        });
        let meetings = self.meetings.iter().map(|m| {
            format!(
                "{{Path:{} Title:{} MeetingID:{} StartedAt:{} Participant:{}}}",
                m.path, m.title, m.meeting_id, m.started_at, m.participant
            )
        });
        format!(
            "&{{Name:{} ContactID:{} Refs:[{}] Documents:[{}] Meetings:[{}] StoreAvailable:{}}}",
            self.name,
            self.contact_id,
            refs.collect::<Vec<_>>().join(" "),
            documents.collect::<Vec<_>>().join(" "),
            meetings.collect::<Vec<_>>().join(" "),
            self.store_available
        )
    }
}

/// Go `contacts.Available()`: resolve the contact store's directories (created
/// 0700) and open and migrate its database. `None` when that fails.
fn open_store() -> Option<Connection> {
    let environment: BTreeMap<String, String> = std::env::vars().collect();
    let paths = symdesk_core::config::contacts_paths(&environment);
    for directory in [&paths.config_dir, &paths.data_dir, &paths.cache_dir] {
        if directory.is_empty() {
            continue;
        }
        let mut builder = fs::DirBuilder::new();
        builder.recursive(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::DirBuilderExt;
            builder.mode(0o700);
        }
        builder.create(directory).ok()?;
    }
    symdesk_contacts::open(&paths.db_path).ok()
}

type StoreService<'a> = Service<'a, fn() -> String, fn() -> String>;

fn with_service<T>(store: &Connection, action: impl FnOnce(&StoreService<'_>) -> T) -> T {
    let service: StoreService<'_> = Service::new(store, String::new, String::new);
    action(&service)
}

fn resolve_ref(store: &Connection, id: &str) -> Result<Ref, String> {
    if id.is_empty() {
        return Err("a symrelate contact id is required".to_owned());
    }
    with_service(store, |service| service.get_ref(id)).map_err(|error| {
        if error.kind == "not_found" {
            NOT_FOUND.to_owned()
        } else {
            error.message
        }
    })
}

fn resolve_for_write(contact_id: &str) -> Result<Ref, String> {
    let store = open_store().ok_or_else(|| STORE_UNAVAILABLE.to_owned())?;
    resolve_ref(&store, contact_id.trim())
}

/// Go `Service.ResolveContactReferences`.
fn by_name(root: &Path, sidecar: &Sidecar, name: &str) -> Result<Box<ContactReferences>, String> {
    let trimmed = name.trim();
    let mut out = Box::new(ContactReferences::empty(trimmed));
    if trimmed.is_empty() {
        return Ok(out);
    }
    let mut rows = sidecar
        .docs_list(&DocsFilter {
            correspondent: trimmed.to_owned(),
            ..DocsFilter::default()
        })
        .map_err(|error| error.to_string())?;
    for row in &mut rows {
        row.path = crate::native_relative_path(root, &row.path);
    }
    out.documents = rows;
    let Some(store) = open_store() else {
        return Ok(out);
    };
    out.store_available = true;
    let found = with_service(&store, |service| service.find_refs_by_name(trimmed))
        .map_err(|error| error.message)?
        .unwrap_or_default();
    out.refs = found.iter().cloned().map(ContactRef::from).collect();
    if found.is_empty() {
        return Ok(out);
    }
    let wanted: Vec<&str> = found
        .iter()
        .map(|reference| reference.id.as_str())
        .collect();
    out.meetings = meetings_referencing(root, &wanted)?;
    Ok(out)
}

/// Go `Service.ResolveContactReferencesByID`.
fn by_contact_id(
    root: &Path,
    sidecar: &mut Sidecar,
    contact_id: &str,
) -> Result<Box<ContactReferences>, String> {
    let contact_id = contact_id.trim();
    if contact_id.is_empty() {
        return Err("a contact id is required".to_owned());
    }
    let mut out = Box::new(ContactReferences::empty(contact_id));
    contact_id.clone_into(&mut out.contact_id);
    let Some(store) = open_store() else {
        collect_references(root, &mut out, contact_id, None)?;
        return Ok(out);
    };
    out.store_available = true;
    let reference = resolve_ref(&store, contact_id)?;
    out.name.clone_from(&reference.display_name);
    out.refs.push(reference.clone().into());
    refresh_references(root, sidecar, &reference)?;
    collect_references(root, &mut out, contact_id, Some(&reference))?;
    Ok(out)
}

fn walk(root: &Path) -> Result<Vec<String>, String> {
    walk_markdown(root)
        .map(|paths| {
            paths
                .iter()
                .map(|path| path.to_string_lossy().replace('\\', "/"))
                .collect()
        })
        .map_err(|error| format!("walk vault: {error}"))
}

fn parse_in_vault(root: &Path, rel: &str) -> Option<Document> {
    let canonical = fs::canonicalize(root).ok()?;
    parse_file_in_root(root, &canonical.join(rel)).ok()
}

/// Go `contacts.ReferencesInFrontmatter`: every structurally valid reference
/// (provider, id and kind are non-empty strings) as `(provider, id, kind)`.
fn references_in(value: &Value, found: &mut Vec<(String, String, String)>) {
    match value {
        Value::Mapping(mapping) => {
            let text = |key: &str| match mapping.get(key) {
                Some(Value::String(value)) if !value.is_empty() => Some(value.clone()),
                _ => None,
            };
            if let (Some(provider), Some(id), Some(kind)) =
                (text("provider"), text("id"), text("kind"))
            {
                found.push((provider, id, kind));
                return;
            }
            for (_, child) in mapping.iter() {
                references_in(child, found);
            }
        }
        Value::Sequence(items) => {
            for child in items {
                references_in(child, found);
            }
        }
        _ => {}
    }
}

fn frontmatter_references(document: &Document) -> Vec<(String, String, String)> {
    let mut found = Vec::new();
    for value in document.frontmatter.values() {
        references_in(value, &mut found);
    }
    found
}

struct Participant {
    label: String,
    contact_ref_id: Option<String>,
}

struct MeetingFrontmatter {
    meeting_id: String,
    started_at: String,
    participants: Vec<Participant>,
}

/// A scalar the way yaml.v3 decodes it into a Go `string`.
fn scalar_text(value: &Value) -> Option<String> {
    match value {
        Value::Null => Some(String::new()),
        Value::String(text) => Some(text.clone()),
        Value::Bool(flag) => Some(flag.to_string()),
        Value::Number(number) => Some(number.to_string()),
        _ => None,
    }
}

fn optional_text(mapping: &Mapping, key: &str) -> Option<String> {
    mapping.get(key).map_or(Some(String::new()), scalar_text)
}

/// Go `loadMeetingFrontmatter`: the typed view of a meeting note, `None` when
/// the note is not a meeting or its frontmatter does not decode.
fn load_meeting(document: &Document) -> Option<MeetingFrontmatter> {
    if document.frontmatter.get("type").and_then(Value::as_str) != Some("meeting") {
        return None;
    }
    let get = |key: &str| document.frontmatter.get(key);
    for key in [
        "title",
        "created",
        "meeting_id",
        "started_at",
        "ended_at",
        "language",
    ] {
        if let Some(value) = get(key) {
            scalar_text(value)?;
        }
    }
    let text = |key: &str| match get(key) {
        None => Some(String::new()),
        Some(value) => scalar_text(value),
    };
    match get("tags") {
        None | Some(Value::Null) => {}
        Some(Value::Sequence(items)) => {
            for item in items {
                scalar_text(item)?;
            }
        }
        Some(_) => return None,
    }
    match get("duration_ms") {
        None | Some(Value::Null) | Some(Value::Number(_)) => {}
        Some(_) => return None,
    }
    match get("symmeet_source") {
        None | Some(Value::Null) | Some(Value::Mapping(_)) => {}
        Some(_) => return None,
    }
    let mut participants = Vec::new();
    match get("participants") {
        None | Some(Value::Null) => {}
        Some(Value::Sequence(items)) => {
            for item in items {
                let mapping = match item {
                    Value::Mapping(mapping) => mapping,
                    Value::Null => {
                        participants.push(Participant {
                            label: String::new(),
                            contact_ref_id: None,
                        });
                        continue;
                    }
                    _ => return None,
                };
                let label = optional_text(mapping, "label")?;
                match mapping.get("speaker_ids") {
                    None | Some(Value::Null) => {}
                    Some(Value::Sequence(ids)) => {
                        for id in ids {
                            scalar_text(id)?;
                        }
                    }
                    Some(_) => return None,
                }
                optional_text(mapping, "entity_id")?;
                let contact_ref_id = match mapping.get("contact_ref") {
                    None | Some(Value::Null) => None,
                    Some(Value::Mapping(reference)) => {
                        for key in ["provider", "kind", "display_name"] {
                            optional_text(reference, key)?;
                        }
                        match reference.get("schema_version") {
                            None | Some(Value::Null) | Some(Value::Number(_)) => {}
                            Some(_) => return None,
                        }
                        Some(optional_text(reference, "id")?)
                    }
                    Some(_) => return None,
                };
                participants.push(Participant {
                    label,
                    contact_ref_id,
                });
            }
        }
        Some(_) => return None,
    }
    let started_at = match get("started_at") {
        Some(Value::String(text)) => text.clone(),
        _ => document
            .yaml_timestamps
            .get("started_at")
            .cloned()
            .or_else(|| text("started_at"))?,
    };
    Some(MeetingFrontmatter {
        meeting_id: text("meeting_id")?,
        started_at,
        participants,
    })
}

/// Go `meetingsReferencing`: the first participant per meeting that carries one
/// of the given contact ids.
fn meetings_referencing(root: &Path, ids: &[&str]) -> Result<Vec<MeetingRef>, String> {
    let mut found = Vec::new();
    for rel in walk(root)? {
        let Some(document) = parse_in_vault(root, &rel) else {
            continue;
        };
        if document.frontmatter.get("type").and_then(Value::as_str) != Some("meeting") {
            continue;
        }
        let Some(meeting) = load_meeting(&document) else {
            continue;
        };
        if let Some(participant) = meeting.participants.iter().find(|participant| {
            participant
                .contact_ref_id
                .as_deref()
                .is_some_and(|id| ids.contains(&id))
        }) {
            found.push(MeetingRef {
                path: rel,
                title: document.title.clone(),
                meeting_id: meeting.meeting_id.clone(),
                started_at: meeting.started_at.clone(),
                participant: participant.label.clone(),
            });
        }
    }
    Ok(found)
}

/// Go `collectContactReferences`.
fn collect_references(
    root: &Path,
    out: &mut ContactReferences,
    contact_id: &str,
    identity: Option<&Ref>,
) -> Result<(), String> {
    for rel in walk(root)? {
        let Some(document) = parse_in_vault(root, &rel) else {
            continue;
        };
        let matched = frontmatter_references(&document)
            .iter()
            .any(|(provider, id, kind)| {
                id == contact_id
                    && identity.is_none_or(|identity| {
                        *provider == identity.provider && *kind == identity.kind
                    })
            });
        if !matched {
            continue;
        }
        if document.document_type == "meeting" {
            let Some(meeting) = load_meeting(&document) else {
                continue;
            };
            for participant in &meeting.participants {
                if participant.contact_ref_id.as_deref() == Some(contact_id) {
                    out.meetings.push(MeetingRef {
                        path: rel.clone(),
                        title: document.title.clone(),
                        meeting_id: meeting.meeting_id.clone(),
                        started_at: meeting.started_at.clone(),
                        participant: participant.label.clone(),
                    });
                }
            }
            continue;
        }
        out.documents.push(DocsResult {
            path: rel,
            title: document.title.clone(),
            file_type: document.document_type.clone(),
            document_date: String::new(),
            person: String::new(),
            status: String::new(),
            due_date: String::new(),
            confidence: 0,
            correspondent: String::new(),
            document_type: String::new(),
            asn: 0,
            tags: Vec::new(),
        });
    }
    Ok(())
}

/// Go `refreshContactMaps`: update the cached display name of every matching
/// reference below `value`; reports whether anything changed.
fn refresh_maps(value: &mut Value, reference: &Ref) -> bool {
    match value {
        Value::Mapping(mapping) => {
            let text = |key: &str| match mapping.get(key) {
                Some(Value::String(value)) => Some(value.clone()),
                _ => None,
            };
            if text("id").as_deref() == Some(reference.id.as_str())
                && text("provider").as_deref() == Some(reference.provider.as_str())
                && text("kind").as_deref() == Some(reference.kind.as_str())
            {
                if text("display_name").as_deref() == Some(reference.display_name.as_str()) {
                    return false;
                }
                mapping.insert(
                    "display_name".to_owned(),
                    Value::String(reference.display_name.clone()),
                );
                return true;
            }
            let mut changed = false;
            let keys: Vec<String> = mapping.keys().cloned().collect();
            for key in keys {
                if let Some(child) = mapping.get_mut(&key)
                    && refresh_maps(child, reference)
                {
                    changed = true;
                }
            }
            changed
        }
        Value::Sequence(items) => {
            let mut changed = false;
            for child in items {
                if refresh_maps(child, reference) {
                    changed = true;
                }
            }
            changed
        }
        _ => false,
    }
}

/// Go `refreshContactReferences`.
fn refresh_references(root: &Path, sidecar: &mut Sidecar, reference: &Ref) -> Result<(), String> {
    for rel in walk(root)? {
        let Some(mut document) = parse_in_vault(root, &rel) else {
            continue;
        };
        let mut changed = false;
        for value in document.frontmatter.values_mut() {
            if refresh_maps(value, reference) {
                changed = true;
            }
        }
        if changed {
            write_frontmatter(root, sidecar, &rel, &document)?;
        }
    }
    Ok(())
}

fn load_note(root: &Path, note: &str) -> Result<Document, String> {
    let path = secure_path(root, note).map_err(|error| error.to_string())?;
    parse_file_in_root(root, &path)
}

/// Go `writeVaultFrontmatter`: re-encode the whole frontmatter map, keep the
/// body, snapshot, write 0600 and re-index.
fn write_frontmatter(
    root: &Path,
    sidecar: &mut Sidecar,
    note: &str,
    document: &Document,
) -> Result<(), String> {
    let path = secure_path(root, note).map_err(|error| error.to_string())?;
    let frontmatter = render_go_yaml_map(&document.frontmatter)
        .map_err(|error| format!("encode frontmatter: {error}"))?;
    let content = format!("---\n{frontmatter}---\n{}", document.body);
    let raw = read_in_root(root, &path).map_err(|error| format!("read {note}: {error}"))?;
    let digest: String = symdesk_vault::sha256::digest(&raw)
        .iter()
        .map(|byte| format!("{byte:02x}"))
        .collect();
    if digest != document.sha256 {
        return Err(format!("{note} changed on disk since it was read; re-run"));
    }
    crate::mutate_cli::snapshot(root, &path);
    fs::write(&path, content.as_bytes())
        .map_err(|error| format!("write {note}: {}", go_path_error("open", &path, &error)))?;
    crate::mutate_cli::reindex(sidecar, &path)?;
    Ok(())
}

/// Go `LinkNoteContact`.
fn link_note(
    root: &Path,
    sidecar: &mut Sidecar,
    note: &str,
    contact_id: &str,
) -> Result<Ref, String> {
    let reference = resolve_for_write(contact_id)?;
    let mut document = load_note(root, note)?;
    let mut value = match document.frontmatter.get("contact") {
        Some(Value::Mapping(existing)) => existing.clone(),
        _ => Mapping::new(),
    };
    value.insert(
        "provider".to_owned(),
        Value::String(REF_PROVIDER.to_owned()),
    );
    value.insert("schema_version".to_owned(), Value::from(REF_SCHEMA_VERSION));
    value.insert("id".to_owned(), Value::String(reference.id.clone()));
    value.insert("kind".to_owned(), Value::String(reference.kind.clone()));
    value.insert(
        "display_name".to_owned(),
        Value::String(reference.display_name.clone()),
    );
    document
        .frontmatter
        .insert("contact".to_owned(), Value::Mapping(value));
    write_frontmatter(root, sidecar, note, &document)?;
    Ok(reference)
}

/// Go `UnlinkNoteContact`.
fn unlink_note(root: &Path, sidecar: &mut Sidecar, note: &str) -> Result<(), String> {
    let mut document = load_note(root, note)?;
    document.frontmatter.remove("contact");
    write_frontmatter(root, sidecar, note, &document)
}
