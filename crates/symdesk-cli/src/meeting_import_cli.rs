//! `symdesk meeting available|import|refresh|segments|speakers`: finished Meet
//! recordings become vault meeting notes (issue #1404).
//!
//! These commands did not exist in the Go oracle at `36aad3d4`; Go removed its
//! versions in `686570a5` (they shelled out to the `symmeet` binary). They are
//! declared in `assets/cli-additions.json` (docs/rust-port/cli-additions.md) and
//! intercepted before the frozen Cobra front, which would print the `meeting`
//! help for them. Where the behaviour comes from:
//!
//! * argument shapes and JSON field names: the Swift caller
//!   (`Sources/SymDeskCore/DeskCore.swift`, `meetings*` and its decoders);
//! * note path, frontmatter, transcript markers, refresh preview/apply and the
//!   diff: Go `internal/service/meetings.go` before `686570a5`;
//! * store location, layout, manifest/segment/speaker-edit decoding, the
//!   Markdown transcript and the "edited segments win" rule: the Meet package
//!   (`meet/Sources/SymMeetCore`: `SymMeetPaths`, `ArtifactLayout`,
//!   `MeetingStore`, `MeetingManifest`, `Segment`, `SpeakerEditor`,
//!   `MarkdownRenderer`, `symmeet export`).
//!
//! The Meet store is input only: nothing below opens a file there for writing.
use std::{
    collections::{BTreeMap, BTreeSet},
    ffi::OsString,
    io::Read,
    path::{Path, PathBuf},
    process::ExitCode,
};

use cap_std::{ambient_authority, fs::Dir};
use serde::{Deserialize, Deserializer, Serialize};
use symdesk_vault::secure_path;
use time::{OffsetDateTime, format_description::well_known::Rfc3339};

use crate::{
    emit_error,
    ingest_cli::init_service_deps,
    mcp::read::meetings,
    meeting_cli::{NewMeetingNote, encode_new_note},
    mutate_cli::{reindex, try_snapshot},
    write_go_json, write_stdout,
};

const COMMANDS: [&str; 5] = ["available", "import", "refresh", "segments", "speakers"];
const START_MARKER: &str = "<!-- symmeet-transcript:start -->";
const END_MARKER: &str = "<!-- symmeet-transcript:end -->";
/// A manifest is a few hundred bytes; a megabyte is already hostile.
const MANIFEST_LIMIT: u64 = 1024 * 1024;
/// Segment, turn and speaker-edit logs: the vault's own read limit.
const LOG_LIMIT: u64 = 64 * 1024 * 1024;
/// Go `maxDiffCells`: bound of the O(n*m) line-diff table.
const MAX_DIFF_CELLS: usize = 4_000_000;

// ------------------------------------------------------------------ routing

struct Invocation {
    command: &'static str,
    positionals: Vec<String>,
    vault: Option<String>,
    json: bool,
    output: String,
    apply: bool,
    help: bool,
}

struct UsageError {
    message: String,
    json: bool,
}

/// pflag's boolean flag: bare means true, `=value` is `strconv.ParseBool`.
fn parse_bool(flag: &str, inline: Option<&str>) -> Result<bool, String> {
    match inline {
        None | Some("true" | "1" | "t" | "T" | "TRUE" | "True") => Ok(true),
        Some("false" | "0" | "f" | "F" | "FALSE" | "False") => Ok(false),
        Some(other) => Err(format!(
            "invalid argument {other:?} for \"{flag}\" flag: strconv.ParseBool: parsing {other:?}: invalid syntax"
        )),
    }
}

/// `Some` when `args` is `symdesk [global flags] meeting <one of the five> ...`.
/// Anything else (including every other `meeting` word) stays with Cobra.
fn parse(args: &[OsString]) -> Option<Result<Invocation, UsageError>> {
    let words: Vec<&str> = args
        .iter()
        .skip(1)
        .map(|word| word.to_str())
        .collect::<Option<_>>()?;
    let mut meeting_seen = false;
    let mut command: Option<&'static str> = None;
    let mut inv = Invocation {
        command: "",
        positionals: Vec::new(),
        vault: None,
        json: false,
        output: String::new(),
        apply: false,
        help: false,
    };
    let mut apply_seen = false;
    let mut terminator = false;
    let mut index = 0;
    while index < words.len() {
        let word = words[index];
        index += 1;
        if !terminator && command.is_some() && word == "--" {
            terminator = true;
            continue;
        }
        if terminator || word == "-" || !word.starts_with('-') {
            if !meeting_seen {
                if word != "meeting" {
                    return None;
                }
                meeting_seen = true;
            } else if command.is_none() {
                command = Some(COMMANDS.iter().copied().find(|name| *name == word)?);
            } else {
                inv.positionals.push(word.to_owned());
            }
            continue;
        }
        // A flag. Unknown flags before the command belong to Cobra.
        let (name, inline) = match word.split_once('=') {
            Some((name, value)) if word.starts_with("--") => (name, Some(value)),
            _ => (word, None),
        };
        let mut value = |inv_name: &str| -> Result<String, String> {
            if let Some(value) = inline {
                return Ok(value.to_owned());
            }
            let next = words
                .get(index)
                .ok_or_else(|| format!("flag needs an argument: {inv_name}"))?;
            index += 1;
            Ok((*next).to_owned())
        };
        let known = match name {
            "--json" | "--apply" | "--help" => match parse_bool(
                // pflag names a flag with a shorthand like `-h, --help`.
                if name == "--help" { "-h, --help" } else { name },
                inline,
            ) {
                Ok(on) => {
                    match name {
                        "--json" => inv.json = on,
                        "--apply" => {
                            apply_seen = true;
                            inv.apply = on;
                        }
                        _ => inv.help = on,
                    }
                    true
                }
                Err(message) => {
                    return command.map(|_| {
                        Err(UsageError {
                            message,
                            json: false,
                        })
                    });
                }
            },
            "--vault" | "--output" => match value(name) {
                Ok(text) => {
                    if name == "--vault" {
                        inv.vault = Some(text);
                    } else {
                        inv.output = text;
                    }
                    true
                }
                Err(message) => {
                    return command.map(|_| {
                        Err(UsageError {
                            message,
                            json: inv.json,
                        })
                    });
                }
            },
            "-h" => {
                inv.help = true;
                true
            }
            _ => false,
        };
        if !known {
            command?;
            let message = if word.starts_with("--") {
                format!("unknown flag: {name}")
            } else {
                let letter = word.chars().nth(1).unwrap_or('-');
                format!("unknown shorthand flag: '{letter}' in {word}")
            };
            return Some(Err(UsageError {
                message,
                json: inv.json,
            }));
        }
    }
    let command = command?;
    inv.command = command;
    if apply_seen && command != "refresh" {
        return Some(Err(UsageError {
            message: "unknown flag: --apply".to_owned(),
            json: inv.json,
        }));
    }
    if !inv.help && command != "available" && inv.positionals.len() != 1 {
        return Some(Err(UsageError {
            message: format!("accepts 1 arg(s), received {}", inv.positionals.len()),
            json: inv.json,
        }));
    }
    Some(Ok(inv))
}

/// Runs before `cobra::front`: the recorded tree answers these words with the
/// `meeting` help (exit 0) or `unknown command`.
pub fn intercept(args: &[OsString]) -> Option<ExitCode> {
    let inv = match parse(args)? {
        Ok(inv) => inv,
        Err(error) => return Some(emit_error(error.message, error.json)),
    };
    if inv.help {
        let topics = ["meeting".to_owned(), inv.command.to_owned()];
        return Some(
            crate::help_cli::added_help(&topics)
                .expect("meeting commands are declared in assets/cli-additions.json"),
        );
    }
    let output = inv.output.as_str();
    if !output.is_empty() && !matches!(output, "text" | "json" | "yaml") {
        return Some(emit_error(
            format!("invalid --output value {output:?} (want text|json|yaml)"),
            false,
        ));
    }
    let json = match output {
        "json" => true,
        "text" | "yaml" => false,
        _ => inv.json,
    };
    if let Err(code) = crate::backup_cli::gate(args) {
        return Some(code);
    }
    let result = init_service_deps(inv.vault.as_deref()).and_then(|(root, mut sidecar)| {
        let result = dispatch(&inv, json, &root, &mut sidecar);
        if let Err(error) = sidecar.close() {
            eprintln!("warning: failed to close sidecar database: {error}");
        }
        result
    });
    Some(result.unwrap_or_else(|message| emit_error(message, json)))
}

fn dispatch(
    inv: &Invocation,
    json: bool,
    root: &Path,
    db: &mut symdesk_index::Sidecar,
) -> Result<ExitCode, String> {
    let note = inv.positionals.first().map_or("", String::as_str);
    match inv.command {
        "available" => available(root, json),
        "import" => import(root, db, note, json),
        "refresh" => refresh(root, db, note, inv.apply, json),
        "segments" => segments(root, note, json),
        _ => speakers(root, note, json),
    }
}

// ------------------------------------------------------------ Meet store

/// `SymMeetPaths.dataDirectory`: `$XDG_DATA_HOME/symmeet`, else
/// `$HOME/.local/share/symmeet`; an empty variable counts as unset.
fn store_root() -> Result<PathBuf, String> {
    let variable = |name: &str| std::env::var_os(name).filter(|value| !value.is_empty());
    let root = if let Some(data) = variable("XDG_DATA_HOME") {
        PathBuf::from(data)
    } else if let Some(home) = variable("HOME") {
        PathBuf::from(home).join(".local/share")
    } else {
        return Err("cannot locate the Meet store: set HOME or XDG_DATA_HOME".to_owned());
    };
    let root = root.join("symmeet");
    if root.is_absolute() {
        Ok(root)
    } else {
        std::env::current_dir()
            .map(|cwd| cwd.join(root))
            .map_err(|error| format!("cannot locate the Meet store: {error}"))
    }
}

/// `Ok(false)` when `path` does not exist; a symbolic link is never followed.
fn directory_exists(path: &Path) -> Result<bool, String> {
    match std::fs::symlink_metadata(path) {
        Ok(meta) if meta.file_type().is_symlink() => Err(format!(
            "{}: refusing to follow a symbolic link",
            path.display()
        )),
        Ok(meta) if meta.is_dir() => Ok(true),
        Ok(_) => Err(format!("{}: not a directory", path.display())),
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(false),
        Err(error) => Err(format!("{}: {error}", path.display())),
    }
}

/// `meetings/` below the store root, `None` when the store has none yet.
fn meetings_dir() -> Result<Option<PathBuf>, String> {
    let root = store_root()?;
    if !directory_exists(&root)? {
        return Ok(None);
    }
    let meetings = root.join("meetings");
    Ok(directory_exists(&meetings)?.then_some(meetings))
}

/// `UUID(uuidString:)` followed by `uuidString.lowercased()`: the 8-4-4-4-12 form.
fn uuid_text(text: &str) -> Option<String> {
    let bytes = text.as_bytes();
    let valid = bytes.len() == 36
        && bytes.iter().enumerate().all(|(index, byte)| {
            if matches!(index, 8 | 13 | 18 | 23) {
                *byte == b'-'
            } else {
                byte.is_ascii_hexdigit()
            }
        });
    valid.then(|| text.to_ascii_lowercase())
}

fn normalized_id(text: &str) -> Result<String, String> {
    uuid_text(text).ok_or_else(|| format!("invalid meeting id {text:?}: expected a UUID"))
}

/// The recording directory of `id`, opened as a capability: nothing read
/// through it can leave the directory. `None` when it does not exist.
fn open_recording(id: &str) -> Result<Option<Dir>, String> {
    let Some(meetings) = meetings_dir()? else {
        return Ok(None);
    };
    let path = meetings.join(id);
    if !directory_exists(&path)? {
        return Ok(None);
    }
    Dir::open_ambient_dir(&path, ambient_authority())
        .map(Some)
        .map_err(|error| format!("{}: {error}", path.display()))
}

/// One regular file of a recording: symlinks are refused, the size is bounded.
fn read_member(dir: &Dir, name: &str, limit: u64) -> Result<Option<Vec<u8>>, String> {
    match dir.symlink_metadata(name) {
        Ok(meta) if meta.file_type().is_symlink() => {
            Err(format!("{name}: refusing to follow a symbolic link"))
        }
        Ok(meta) if !meta.is_file() => Err(format!("{name}: not a regular file")),
        Ok(meta) if meta.len() > limit => {
            Err(format!("{name}: larger than the {limit} byte limit"))
        }
        Ok(_) => {
            let file = dir.open(name).map_err(|error| format!("{name}: {error}"))?;
            let mut bytes = Vec::new();
            file.take(limit + 1)
                .read_to_end(&mut bytes)
                .map_err(|error| format!("{name}: {error}"))?;
            if bytes.len() as u64 > limit {
                return Err(format!("{name}: larger than the {limit} byte limit"));
            }
            Ok(Some(bytes))
        }
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => Ok(None),
        Err(error) => Err(format!("{name}: {error}")),
    }
}

// ------------------------------------------------------- Swift Codable mirror

#[derive(Debug)]
struct Uuid(String);

impl<'de> Deserialize<'de> for Uuid {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        let text = String::deserialize(deserializer)?;
        uuid_text(&text)
            .map(|_| Self(text.to_ascii_uppercase()))
            .ok_or_else(|| serde::de::Error::custom(format!("{text:?} is not a UUID")))
    }
}

/// `ContractCodec`: ISO-8601 without fractional seconds.
#[derive(Debug, Clone, Copy)]
struct Stamp(OffsetDateTime);

impl<'de> Deserialize<'de> for Stamp {
    fn deserialize<D: Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        let text = String::deserialize(deserializer)?;
        let time_part = text.split_once(['T', 't']).map_or("", |(_, rest)| rest);
        if time_part.contains('.') {
            return Err(serde::de::Error::custom(format!(
                "{text:?} has fractional seconds"
            )));
        }
        OffsetDateTime::parse(&text, &Rfc3339)
            .map(Self)
            .map_err(|error| serde::de::Error::custom(format!("{text:?}: {error}")))
    }
}

#[derive(Deserialize)]
#[serde(rename_all = "snake_case")]
enum Source {
    Imported,
    LiveCapture,
    Recovery,
}

impl Source {
    fn name(&self) -> &'static str {
        match self {
            Self::Imported => "imported",
            Self::LiveCapture => "live_capture",
            Self::Recovery => "recovery",
        }
    }
}

#[derive(Deserialize)]
#[serde(rename_all = "snake_case")]
enum TrackKind {
    Microphone,
    System,
    Original,
}

#[derive(Deserialize)]
struct Track {
    #[allow(dead_code)]
    track_id: Uuid,
    #[allow(dead_code)]
    kind: TrackKind,
    relative_path: String,
}

#[derive(Deserialize)]
#[serde(rename_all = "snake_case")]
enum JobState {
    Queued,
    Processing,
    Completed,
    Failed,
    Cancelled,
}

impl JobState {
    /// `MeetingJobState.rawValue`.
    fn name(&self) -> &'static str {
        match self {
            Self::Queued => "queued",
            Self::Processing => "processing",
            Self::Completed => "completed",
            Self::Failed => "failed",
            Self::Cancelled => "cancelled",
        }
    }
}

#[derive(Deserialize)]
#[allow(dead_code)]
struct Engine {
    engine_id: String,
    model_id: String,
    model_version: String,
}

#[derive(Deserialize)]
#[allow(dead_code)]
struct Job {
    job_id: Uuid,
    state: JobState,
    engine: Option<Engine>,
}

#[derive(Deserialize)]
#[serde(rename_all = "snake_case")]
enum ConsentStatus {
    Required,
    Authorized,
    Expired,
    Revoked,
}

#[derive(Deserialize)]
#[allow(dead_code)]
struct Consent {
    status: ConsentStatus,
    authorized_at: Option<Stamp>,
    expires_at: Option<Stamp>,
}

#[derive(Deserialize)]
#[serde(rename_all = "snake_case")]
enum RetentionPolicy {
    Keep,
    DeleteAfterDate,
    DeleteAfterExport,
}

#[derive(Deserialize)]
#[allow(dead_code)]
struct Retention {
    policy: RetentionPolicy,
    delete_after: Option<Stamp>,
}

#[derive(Deserialize)]
#[allow(dead_code)]
struct AssetMetadata {
    container: String,
    duration_ms: i64,
    channel_count: i64,
    sample_rate: f64,
    byte_size: i64,
    sha256: String,
}

/// `MeetingManifest` (schema 1). Unknown keys are tolerated, as the Swift
/// decoder keeps them in `additionalFields`.
#[derive(Deserialize)]
#[allow(dead_code)]
struct Manifest {
    schema_version: i64,
    meeting_id: Uuid,
    source: Source,
    created_at: Stamp,
    updated_at: Stamp,
    original_asset: Option<String>,
    original_asset_metadata: Option<AssetMetadata>,
    audio_tracks: Option<Vec<Track>>,
    language: Option<String>,
    job: Option<Job>,
    consent: Consent,
    retention: Retention,
}

/// `MeetingStore.validateRelativePath`.
fn check_relative_path(value: &str) -> Result<(), String> {
    let bad = value.is_empty()
        || value.starts_with('/')
        || value.starts_with('~')
        || value.split('/').any(|part| part.is_empty() || part == "..");
    if bad {
        Err(format!("invalid relative path {value:?} in manifest.json"))
    } else {
        Ok(())
    }
}

fn load_manifest(dir: &Dir, id: &str) -> Result<Manifest, String> {
    let bytes = read_member(dir, "manifest.json", MANIFEST_LIMIT)?
        .ok_or_else(|| "manifest.json is missing".to_owned())?;
    let manifest: Manifest = serde_json::from_slice(&bytes)
        .map_err(|error| format!("invalid manifest.json: {error}"))?;
    if manifest.schema_version != 1 {
        return Err(format!(
            "unsupported meeting artifact schema version {} (symdesk supports 1)",
            manifest.schema_version
        ));
    }
    if manifest.meeting_id.0.to_ascii_lowercase() != id {
        return Err("manifest.json names a different meeting id than its directory".to_owned());
    }
    if let Some(asset) = &manifest.original_asset {
        check_relative_path(asset)?;
    }
    for track in manifest.audio_tracks.iter().flatten() {
        check_relative_path(&track.relative_path)?;
    }
    Ok(manifest)
}

#[derive(Deserialize)]
#[serde(rename_all = "snake_case")]
enum Revision {
    Engine,
    UserCorrected,
}

#[derive(Deserialize)]
#[allow(dead_code)]
struct SegmentRecord {
    schema_version: i64,
    segment_id: Uuid,
    track_id: Uuid,
    speaker_id: String,
    start_ms: i64,
    end_ms: i64,
    engine_text: String,
    edited_text: Option<String>,
    confidence: Option<f64>,
    revision: Revision,
}

#[derive(Deserialize)]
#[allow(dead_code)]
struct TurnRecord {
    schema_version: i64,
    turn_id: Uuid,
    speaker_id: String,
    start_ms: i64,
    end_ms: i64,
    confidence: f64,
    is_overlapping: bool,
    provenance: Revision,
}

#[derive(Deserialize)]
#[serde(rename_all = "snake_case")]
enum EditKind {
    Label,
    Merge,
    Split,
    Reset,
}

#[derive(Deserialize)]
#[allow(dead_code)]
struct EditEvent {
    schema_version: i64,
    event_id: Uuid,
    meeting_id: Uuid,
    kind: EditKind,
    speaker_id: Option<String>,
    label: Option<String>,
    target_id: Option<String>,
    segment_id: Option<Uuid>,
    sequence_number: i64,
    occurred_at: Stamp,
}

/// JSON Lines as `MeetingStore` reads them: empty lines are skipped, every
/// other line must decode.
fn read_log<T: for<'de> Deserialize<'de>>(
    dir: &Dir,
    name: &str,
    check: impl Fn(&T) -> Result<(), String>,
) -> Result<Vec<T>, String> {
    let Some(bytes) = read_member(dir, name, LOG_LIMIT)? else {
        return Ok(Vec::new());
    };
    let mut records = Vec::new();
    for (number, line) in bytes.split(|byte| *byte == b'\n').enumerate() {
        if line.iter().all(u8::is_ascii_whitespace) {
            continue;
        }
        let record: T = serde_json::from_slice(line)
            .map_err(|error| format!("malformed {name} line {}: {error}", number + 1))?;
        check(&record).map_err(|error| format!("malformed {name} line {}: {error}", number + 1))?;
        records.push(record);
    }
    Ok(records)
}

fn check_segment(segment: &SegmentRecord) -> Result<(), String> {
    if segment.schema_version != 1 {
        return Err(format!(
            "unsupported schema version {}",
            segment.schema_version
        ));
    }
    if !(segment.speaker_id.starts_with("speaker_") && segment.speaker_id.len() > "speaker_".len())
    {
        return Err(format!("invalid speaker_id {:?}", segment.speaker_id));
    }
    if segment.start_ms < 0 || segment.end_ms <= segment.start_ms {
        return Err(format!(
            "invalid time range {}..{}",
            segment.start_ms, segment.end_ms
        ));
    }
    Ok(())
}

fn check_turn(turn: &TurnRecord) -> Result<(), String> {
    if turn.schema_version != 1 {
        return Err(format!(
            "unsupported schema version {}",
            turn.schema_version
        ));
    }
    if turn.start_ms < 0 || turn.end_ms <= turn.start_ms {
        return Err(format!(
            "invalid time range {}..{}",
            turn.start_ms, turn.end_ms
        ));
    }
    Ok(())
}

/// `symmeet export`'s default: `segments.edited.jsonl` when it holds segments,
/// otherwise the raw evidence. Chronological order, like the Markdown export.
fn load_segments(dir: &Dir) -> Result<Vec<SegmentRecord>, String> {
    let edited = read_log::<SegmentRecord>(dir, "segments.edited.jsonl", check_segment)?;
    let mut segments = if edited.is_empty() {
        read_log::<SegmentRecord>(dir, "segments.raw.jsonl", check_segment)?
    } else {
        edited
    };
    segments.sort_by(|left, right| {
        (
            left.start_ms,
            left.end_ms,
            &left.speaker_id,
            &left.segment_id.0,
        )
            .cmp(&(
                right.start_ms,
                right.end_ms,
                &right.speaker_id,
                &right.segment_id.0,
            ))
    });
    Ok(segments)
}

/// `(speaker_id, label)` for every known speaker, sorted by id: the raw diarization
/// speakers plus the speakers of the segments, labelled by replaying
/// `speaker_edits.jsonl` like `SpeakerEditor.replay`.
fn load_speakers(dir: &Dir, segments: &[SegmentRecord]) -> Result<Vec<(String, String)>, String> {
    let mut known: BTreeSet<String> = segments
        .iter()
        .map(|segment| segment.speaker_id.clone())
        .collect();
    for turn in read_log::<TurnRecord>(dir, "turns.raw.jsonl", check_turn)? {
        known.insert(turn.speaker_id);
    }
    let events = read_log::<EditEvent>(dir, "speaker_edits.jsonl", |event| {
        if event.schema_version == 1 {
            Ok(())
        } else {
            Err(format!(
                "unsupported schema version {}",
                event.schema_version
            ))
        }
    })?;
    let labels = replay(&events, &known)?;
    Ok(known
        .into_iter()
        .map(|id| {
            let label = labels.get(&id).cloned().unwrap_or_else(|| id.clone());
            (id, label)
        })
        .collect())
}

/// `SpeakerEditor.replay`, limited to what the labels need. A `split` event is
/// validated but not applied (it never changes a label); unlike the Swift list
/// command, which passes no segment ids, it is not rejected for lack of them.
fn replay(
    events: &[EditEvent],
    known: &BTreeSet<String>,
) -> Result<BTreeMap<String, String>, String> {
    const MAX_LABEL: usize = 128;
    let corrupt = || "speaker_edits.jsonl is corrupt".to_owned();
    let unknown = |id: &str| format!("speaker_edits.jsonl names unknown speaker {id:?}");
    let mut labels: BTreeMap<String, String> = BTreeMap::new();
    let mut merged: BTreeMap<String, Vec<String>> = BTreeMap::new();
    let mut last = 0;
    for event in events {
        if event.sequence_number <= last {
            continue;
        }
        match event.kind {
            EditKind::Label => {
                let (Some(speaker), Some(label)) = (&event.speaker_id, &event.label) else {
                    return Err(corrupt());
                };
                if label.is_empty() || label.chars().count() > MAX_LABEL {
                    return Err(format!(
                        "speaker_edits.jsonl has an invalid label {label:?}"
                    ));
                }
                if !known.contains(speaker) {
                    return Err(unknown(speaker));
                }
                labels.insert(speaker.clone(), label.clone());
            }
            EditKind::Merge => {
                let (Some(from), Some(into)) = (&event.speaker_id, &event.target_id) else {
                    return Err(corrupt());
                };
                if from == into {
                    return Err(format!("speaker_edits.jsonl merges {from:?} into itself"));
                }
                for id in [from, into] {
                    if !known.contains(id) {
                        return Err(unknown(id));
                    }
                }
                if merged.get(from).is_some_and(|set| set.contains(into)) {
                    return Err(format!(
                        "speaker_edits.jsonl merges {from:?} and {into:?} in a circle"
                    ));
                }
                let mut target = merged.remove(into).unwrap_or_default();
                target.push(from.clone());
                if let Some(source) = merged.remove(from) {
                    target.extend(source);
                }
                merged.insert(into.clone(), target);
                if let Some(label) = labels.remove(from) {
                    labels.insert(into.clone(), label);
                }
            }
            EditKind::Split => {
                let (Some(speaker), Some(_)) = (&event.speaker_id, &event.segment_id) else {
                    return Err(corrupt());
                };
                if !known.contains(speaker) {
                    return Err(unknown(speaker));
                }
            }
            EditKind::Reset => {
                labels.clear();
                merged.clear();
            }
        }
        last = event.sequence_number;
    }
    Ok(labels)
}

struct Recording {
    manifest: Manifest,
    dir: Dir,
}

/// The recording `id`, or an error naming it.
fn load_recording(id: &str) -> Result<Recording, String> {
    let dir =
        open_recording(id)?.ok_or_else(|| format!("recording {id} not found in the Meet store"))?;
    let manifest = load_manifest(&dir, id).map_err(|error| format!("recording {id}: {error}"))?;
    Ok(Recording { manifest, dir })
}

/// Like [`load_recording`], for the commands that read a recording to build a
/// note (`available`, `import`, `refresh`): its transcription job must have
/// completed. This is the rule of `symmeet export` without `--allow-incomplete`
/// (`meet/Sources/symmeet/Commands/ExportCommand.swift:147-149`): refused when
/// `manifest.job?.state != .completed`, and a manifest without a job counts as
/// `not_started` (`ExportError.meetingIncomplete`, `ExportError.swift:35-38`).
fn load_completed_recording(id: &str) -> Result<Recording, String> {
    let recording = load_recording(id)?;
    let state = match &recording.manifest.job {
        Some(job) if matches!(job.state, JobState::Completed) => return Ok(recording),
        Some(job) => job.state.name(),
        None => "not_started",
    };
    Err(format!(
        "recording {id}: the transcription job has not completed (current state: {state})"
    ))
}

// ------------------------------------------------------------- transcript

/// `TimestampFormatter.format(_, separator: ".")`.
fn timestamp(milliseconds: i64) -> String {
    let clamped = milliseconds.max(0);
    format!(
        "{:02}:{:02}:{:02}.{:03}",
        clamped / 3_600_000,
        (clamped / 60_000) % 60,
        (clamped / 1_000) % 60,
        clamped % 1_000
    )
}

fn display_text(segment: &SegmentRecord) -> &str {
    segment
        .edited_text
        .as_deref()
        .unwrap_or(&segment.engine_text)
}

/// `MarkdownRenderer.render` through `MarkdownProjection`.
fn render_markdown(segments: &[SegmentRecord]) -> String {
    let participants: BTreeSet<&str> = segments
        .iter()
        .map(|segment| segment.speaker_id.as_str())
        .collect();
    let list = if participants.is_empty() {
        "- None".to_owned()
    } else {
        participants
            .iter()
            .map(|speaker| format!("- {speaker}"))
            .collect::<Vec<_>>()
            .join("\n")
    };
    let transcript = segments
        .iter()
        .map(|segment| {
            format!(
                "**{}** [{} - {}]\n\n{}",
                segment.speaker_id,
                timestamp(segment.start_ms),
                timestamp(segment.end_ms),
                display_text(segment)
            )
        })
        .collect::<Vec<_>>()
        .join("\n\n");
    format!(
        "## Summary\n\n\n\n## Decisions\n\n- None\n\n## Action Items\n\n- None\n\n## Participants\n\n{list}\n\n## Transcript\n\n{transcript}"
    )
}

/// Go `wrapTranscript`.
fn wrap_transcript(markdown: &str) -> String {
    format!(
        "{START_MARKER}\n{}\n{END_MARKER}\n",
        markdown.trim_end_matches('\n')
    )
}

/// Go `findTranscriptMarkers`: the span strictly between the markers.
fn find_markers(body: &str) -> Result<(usize, usize), String> {
    let start = body
        .find(START_MARKER)
        .ok_or_else(|| format!("missing {START_MARKER} marker"))?
        + START_MARKER.len();
    let end = body[start..]
        .find(END_MARKER)
        .ok_or_else(|| format!("missing {END_MARKER} marker"))?;
    Ok((start, start + end))
}

/// Go `diffLines`: `"  "` unchanged, `"- "` removed, `"+ "` added.
fn diff_lines(old: &str, new: &str) -> Vec<String> {
    let old: Vec<&str> = old.split('\n').collect();
    let new: Vec<&str> = new.split('\n').collect();
    let (n, m) = (old.len(), new.len());
    let width = m + 1;
    let mut lcs = vec![0_u32; (n + 1) * width];
    for i in (0..n).rev() {
        for j in (0..m).rev() {
            lcs[i * width + j] = if old[i] == new[j] {
                lcs[(i + 1) * width + j + 1] + 1
            } else {
                lcs[(i + 1) * width + j].max(lcs[i * width + j + 1])
            };
        }
    }
    let mut out = Vec::new();
    let (mut i, mut j) = (0, 0);
    while i < n && j < m {
        if old[i] == new[j] {
            out.push(format!("  {}", old[i]));
            i += 1;
            j += 1;
        } else if lcs[(i + 1) * width + j] >= lcs[i * width + j + 1] {
            out.push(format!("- {}", old[i]));
            i += 1;
        } else {
            out.push(format!("+ {}", new[j]));
            j += 1;
        }
    }
    out.extend(old[i..].iter().map(|line| format!("- {line}")));
    out.extend(new[j..].iter().map(|line| format!("+ {line}")));
    out
}

// ----------------------------------------------------------------- commands

#[derive(Serialize)]
struct AvailableMeeting {
    meeting_id: String,
    source: String,
}

fn imported_ids(root: &Path) -> Result<BTreeMap<String, String>, String> {
    let (summaries, _) = meetings::summaries(root)?;
    Ok(summaries
        .into_iter()
        .map(|summary| (summary.meeting_id.to_ascii_lowercase(), summary.path))
        .collect())
}

fn available(root: &Path, json: bool) -> Result<ExitCode, String> {
    let imported = imported_ids(root)?;
    let mut found: Vec<AvailableMeeting> = Vec::new();
    if let Some(meetings) = meetings_dir()? {
        let mut names: Vec<String> = Vec::new();
        for entry in std::fs::read_dir(&meetings)
            .map_err(|error| format!("{}: {error}", meetings.display()))?
        {
            let entry = entry.map_err(|error| format!("{}: {error}", meetings.display()))?;
            if let Some(name) = entry.file_name().to_str() {
                // `.skipsHiddenFiles` in the Swift store listing.
                if !name.starts_with('.') {
                    names.push(name.to_owned());
                }
            }
        }
        names.sort();
        for name in names {
            let skip = |reason: String| {
                if !json {
                    eprintln!("skipped recording {name}: {reason}");
                }
            };
            let Some(id) = uuid_text(&name) else {
                skip("not a meeting directory".to_owned());
                continue;
            };
            if id != name {
                skip("directory name is not a lowercase UUID".to_owned());
                continue;
            }
            match load_completed_recording(&id) {
                Ok(recording) if !imported.contains_key(&id) => found.push(AvailableMeeting {
                    meeting_id: id,
                    source: recording.manifest.source.name().to_owned(),
                }),
                Ok(_) => {}
                Err(reason) => skip(reason),
            }
        }
    }
    if json {
        return Ok(write_go_json(&found));
    }
    if found.is_empty() {
        return Ok(write_stdout("No recordings to import.\n".to_owned()));
    }
    let lines: String = found
        .iter()
        .map(|item| format!("{}  {}\n", item.meeting_id, item.source))
        .collect();
    Ok(write_stdout(lines))
}

#[derive(Serialize)]
struct ImportResult<'a> {
    path: &'a str,
    status: &'static str,
}

/// Go `meetingNotePath`: re-importing the same id addresses the same note.
fn note_path(id: &str) -> String {
    format!("meetings/meeting-{id}.md")
}

/// Go `time.Format(time.RFC3339)`: whole seconds, `Z` or `±hh:mm`.
fn rfc3339(time: OffsetDateTime) -> String {
    let (hours, minutes, _) = time.offset().as_hms();
    let zone = if time.offset().is_utc() {
        "Z".to_owned()
    } else {
        format!(
            "{}{:02}:{:02}",
            if hours < 0 || minutes < 0 { '-' } else { '+' },
            hours.abs(),
            minutes.abs()
        )
    };
    format!(
        "{:04}-{:02}-{:02}T{:02}:{:02}:{:02}{zone}",
        time.year(),
        u8::from(time.month()),
        time.day(),
        time.hour(),
        time.minute(),
        time.second()
    )
}

fn build_note(id: &str, recording: &Recording) -> Result<String, String> {
    let manifest = &recording.manifest;
    let segments =
        load_segments(&recording.dir).map_err(|error| format!("recording {id}: {error}"))?;
    let speakers = load_speakers(&recording.dir, &segments)
        .map_err(|error| format!("recording {id}: {error}"))?;
    let started = manifest.created_at.0;
    let updated = manifest.updated_at.0;
    let (ended, duration_ms) = if updated > started {
        let millis = (updated - started).whole_milliseconds();
        (updated, i64::try_from(millis).unwrap_or(i64::MAX))
    } else {
        (started, 0)
    };
    let title = format!(
        "Meeting {:04}-{:02}-{:02} {:02}:{:02}",
        started.year(),
        u8::from(started.month()),
        started.day(),
        started.hour(),
        started.minute()
    );
    let frontmatter = encode_new_note(&NewMeetingNote {
        title,
        created: rfc3339(OffsetDateTime::now_utc()),
        meeting_id: id.to_owned(),
        started_at: rfc3339(started),
        ended_at: rfc3339(ended),
        duration_ms,
        language: manifest.language.clone().unwrap_or_default(),
        participants: speakers
            .into_iter()
            .map(|(speaker, label)| (label, speaker))
            .collect(),
        artifact_schema_version: manifest.schema_version,
    })?;
    Ok(format!(
        "---\n{frontmatter}---\n\n{}",
        wrap_transcript(&render_markdown(&segments))
    ))
}

fn import(
    root: &Path,
    db: &mut symdesk_index::Sidecar,
    requested: &str,
    json: bool,
) -> Result<ExitCode, String> {
    let id = normalized_id(requested)?;
    // Idempotent: a recording that any vault note already names is left alone,
    // so reviewed participants and hand-written notes can never be clobbered.
    if let Some(path) = imported_ids(root)?.get(&id) {
        return Ok(import_result(path, "already_imported", &id, json));
    }
    let recording = load_completed_recording(&id)?;
    let content = build_note(&id, &recording)?;
    let relative = note_path(&id);
    let absolute = secure_path(root, &relative).map_err(|error| error.to_string())?;
    if let Some(parent) = absolute.parent() {
        std::fs::create_dir_all(parent)
            .map_err(|error| format!("failed to create the meetings directory: {error}"))?;
    }
    write_new_note(&absolute, &content).map_err(|error| {
        if error.kind() == std::io::ErrorKind::AlreadyExists {
            format!("refusing to overwrite the existing file {relative}")
        } else {
            format!("failed to write meeting note {relative}: {error}")
        }
    })?;
    reindex(db, &absolute).map_err(|error| format!("failed to index meeting note: {error}"))?;
    Ok(import_result(&relative, "imported", &id, json))
}

/// Atomic replacement of an existing note (`symdesk_ingest::writer::atomic_write_file`:
/// 0600 temp file next to it, fsync, rename; the temp file is removed on any
/// error). Transcripts are sensitive, so the mode never widens: the note keeps
/// its owner bits (0600 at most), group/other bits are dropped.
fn replace_note(path: &Path, content: &str) -> Result<(), String> {
    #[cfg(unix)]
    let mode = {
        use std::os::unix::fs::PermissionsExt;
        std::fs::metadata(path)
            .map_err(|error| error.to_string())?
            .permissions()
            .mode()
            & 0o600
    };
    #[cfg(not(unix))]
    let mode = 0o600;
    let path = path
        .to_str()
        .ok_or_else(|| format!("{}: path is not valid UTF-8", path.display()))?;
    symdesk_ingest::writer::atomic_write_file(path, content.as_bytes(), mode)
}

/// Meeting transcripts are sensitive: owner-only, and never over an existing file.
fn write_new_note(path: &Path, content: &str) -> std::io::Result<()> {
    use std::io::Write;
    let mut options = std::fs::OpenOptions::new();
    options.write(true).create_new(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    let mut file = options.open(path)?;
    let written = file.write_all(content.as_bytes());
    if written.is_err() {
        let _ = std::fs::remove_file(path);
    }
    written
}

fn import_result(path: &str, status: &'static str, id: &str, json: bool) -> ExitCode {
    if json {
        write_go_json(&ImportResult { path, status })
    } else if status == "imported" {
        write_stdout(format!("Imported {id} as {path}\n"))
    } else {
        write_stdout(format!("Already imported: {id} is {path}\n"))
    }
}

/// The note's meeting id and the note itself.
fn note_recording(root: &Path, note: &str) -> Result<(symdesk_vault::Document, String), String> {
    let doc = meetings::show_document(root, note)?;
    let meeting_id = doc
        .frontmatter
        .get("meeting_id")
        .and_then(noyalib::Value::as_str)
        .unwrap_or_default()
        .to_owned();
    if meeting_id.is_empty() {
        return Err(format!("{note} has no meeting_id in frontmatter"));
    }
    let id = normalized_id(&meeting_id)?;
    Ok((doc, id))
}

#[derive(Serialize)]
struct RefreshResult<'a> {
    path: &'a str,
    changed: bool,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    diff_lines: Vec<String>,
    applied: bool,
}

fn refresh(
    root: &Path,
    db: &mut symdesk_index::Sidecar,
    note: &str,
    apply: bool,
    json: bool,
) -> Result<ExitCode, String> {
    let (doc, id) = note_recording(root, note)?;
    let (start, end) = find_markers(&doc.body)
        .map_err(|error| format!("cannot safely refresh {note}: {error}"))?;
    let old = &doc.body[start..end];
    let recording = load_completed_recording(&id)?;
    let segments =
        load_segments(&recording.dir).map_err(|error| format!("recording {id}: {error}"))?;
    let new = format!("\n{}\n", render_markdown(&segments).trim_end_matches('\n'));
    let changed = old != new;
    let mut result = RefreshResult {
        path: note,
        changed,
        diff_lines: Vec::new(),
        applied: false,
    };
    if changed
        && (old.matches('\n').count() + 1) * (new.matches('\n').count() + 1) <= MAX_DIFF_CELLS
    {
        result.diff_lines = diff_lines(old, &new);
    }
    if apply && changed {
        let path = secure_path(root, note).map_err(|error| error.to_string())?;
        let raw =
            std::fs::read(&path).map_err(|error| format!("failed to re-read {note}: {error}"))?;
        if symdesk_vault::sha256_hex(&raw) != doc.sha256 {
            return Err(format!(
                "{note} changed on disk since it was read; re-run refresh"
            ));
        }
        let raw = String::from_utf8(raw).map_err(|error| error.to_string())?;
        let prefix = raw
            .strip_suffix(&doc.body)
            .ok_or_else(|| format!("{note} changed on disk since it was read; re-run refresh"))?;
        let content = format!("{prefix}{}{new}{}", &doc.body[..start], &doc.body[end..]);
        // The previous note must be recoverable before it is replaced.
        try_snapshot(root, &path).map_err(|error| format!("{note} not refreshed: {error}"))?;
        replace_note(&path, &content)
            .map_err(|error| format!("failed to write refreshed meeting note: {error}"))?;
        reindex(db, &path)
            .map_err(|error| format!("failed to re-index refreshed meeting note: {error}"))?;
        result.applied = true;
    }
    if json {
        return Ok(write_go_json(&result));
    }
    if !changed {
        return Ok(write_stdout("No changes.\n".to_owned()));
    }
    let mut text: String = result
        .diff_lines
        .iter()
        .filter(|line| line.starts_with("- ") || line.starts_with("+ "))
        .map(|line| format!("{line}\n"))
        .collect();
    text.push_str(if result.applied {
        "Applied.\n"
    } else {
        "Preview only; re-run with --apply to write these changes.\n"
    });
    Ok(write_stdout(text))
}

/// `service.MeetingSegment` / Swift `MeetingSegment`.
#[derive(Serialize)]
struct SegmentOut {
    segment_id: String,
    speaker_id: String,
    start_ms: i64,
    end_ms: i64,
    engine_text: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    edited_text: String,
    revision: &'static str,
}

fn segments(root: &Path, note: &str, json: bool) -> Result<ExitCode, String> {
    let (_, id) = note_recording(root, note)?;
    let recording = load_recording(&id)?;
    let loaded =
        load_segments(&recording.dir).map_err(|error| format!("recording {id}: {error}"))?;
    if !json {
        let lines: String = loaded
            .iter()
            .map(|segment| {
                format!(
                    "[{} - {}] {}: {}\n",
                    timestamp(segment.start_ms),
                    timestamp(segment.end_ms),
                    segment.speaker_id,
                    display_text(segment)
                )
            })
            .collect();
        return Ok(write_stdout(lines));
    }
    let out: Vec<SegmentOut> = loaded
        .into_iter()
        .map(|segment| SegmentOut {
            segment_id: segment.segment_id.0,
            speaker_id: segment.speaker_id,
            start_ms: segment.start_ms,
            end_ms: segment.end_ms,
            engine_text: segment.engine_text,
            edited_text: segment.edited_text.unwrap_or_default(),
            revision: match segment.revision {
                Revision::Engine => "engine",
                Revision::UserCorrected => "user_corrected",
            },
        })
        .collect();
    Ok(write_go_json(&out))
}

/// `service.MeetingSpeaker` / Swift `MeetingSpeaker`.
#[derive(Serialize)]
struct SpeakerOut {
    speaker_id: String,
    label: String,
}

fn speakers(root: &Path, note: &str, json: bool) -> Result<ExitCode, String> {
    let (_, id) = note_recording(root, note)?;
    let recording = load_recording(&id)?;
    let loaded =
        load_segments(&recording.dir).map_err(|error| format!("recording {id}: {error}"))?;
    let list = load_speakers(&recording.dir, &loaded)
        .map_err(|error| format!("recording {id}: {error}"))?;
    if json {
        let out: Vec<SpeakerOut> = list
            .into_iter()
            .map(|(speaker_id, label)| SpeakerOut { speaker_id, label })
            .collect();
        return Ok(write_go_json(&out));
    }
    let lines: String = list
        .iter()
        .map(|(speaker, label)| format!("{speaker}\t{label}\n"))
        .collect();
    Ok(write_stdout(lines))
}
