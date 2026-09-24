//! Append-only SymRoom journal, ported from Go's `internal/room/room`
//! (contract row ROOM-002).
//!
//! Reads journal files into a Lamport ceiling and membership projection, tracks
//! per-author sequence/hash chains, and appends events. Like Go, the projection
//! consumes decodable events without authenticating signatures; verification of
//! the journal is a separate RUST-016 contract.
//!
//! Three Go behaviours are easy to "repair" by accident and are therefore
//! reproduced literally, pinned by `testdata/port/room/journal.json`:
//!
//! * `bufio.Scanner` strips a trailing `\r`, so the hashed bytes of a CRLF file
//!   exclude it.
//! * `ReadJournalStats` skips undecodable lines, while `GetAuthorStats` counts
//!   every non-blank line — a corrupt line still advances the sequence.
//! * `AppendEvent` writes the marshalled line as-is; if the previous file had no
//!   trailing newline, Go produces a joined physical line and does not insert a
//!   separator.

use std::{
    collections::BTreeMap,
    fs,
    io::{BufRead, BufReader, Write},
    path::{Path, PathBuf},
};

use sha2::{Digest, Sha256};

use crate::{
    event::{Event, EventError},
    members::State,
};

/// The `prev` value Go reports for an author that has not written yet.
pub const ZERO_HASH: &str =
    "sha256:0000000000000000000000000000000000000000000000000000000000000000";

const JOURNAL_DIR: &str = "journal";
const JOURNAL_SUFFIX: &str = ".jsonl";

#[derive(Clone, Debug, Default, Eq, PartialEq)]
pub struct JournalStats {
    /// The highest Lamport clock observed across every decodable event.
    pub max_lamport: u64,
    /// Membership after replaying the decodable lines in author-file order.
    pub member_state: State,
}

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct AuthorStats {
    /// The number of non-blank lines the author has written.
    pub seq: u64,
    /// `sha256:` over the last non-blank line, or [`ZERO_HASH`] when empty.
    pub prev: String,
}

/// Go `journal.Merge`: total-order events from all author segments. A stable
/// sort retains segment order for events with identical complete sort keys.
pub fn merge(segments: BTreeMap<String, Vec<Event>>) -> Vec<Event> {
    let mut events: Vec<Event> = segments.into_values().flatten().collect();
    events.sort_by(|left, right| {
        (left.lamport, &left.ts, &left.author, left.seq, &left.id).cmp(&(
            right.lamport,
            &right.ts,
            &right.author,
            right.seq,
            &right.id,
        ))
    });
    events
}

/// Reads the Lamport ceiling and member state of a room journal.
///
/// A missing journal directory, an unreadable file and an undecodable line are
/// all tolerated exactly as Go tolerates them.
///
/// # Errors
/// Returns the filesystem error when the journal directory exists but cannot be
/// listed.
pub fn read_journal_stats(room_dir: &Path) -> Result<JournalStats, std::io::Error> {
    let journal_dir = room_dir.join(JOURNAL_DIR);
    let entries = match fs::read_dir(&journal_dir) {
        Ok(entries) => entries,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
            return Ok(JournalStats::default());
        }
        Err(error) => return Err(error),
    };

    let mut entries: Vec<_> = entries.collect::<Result<_, _>>()?;
    // Go os.ReadDir returns name-sorted entries; role changes can depend on
    // whether a room-created event in another author's file was seen first.
    entries.sort_by_key(|entry| entry.file_name());
    let mut max_lamport = 0_u64;
    let mut member_state = State::default();
    for entry in entries {
        let name = entry.file_name().to_string_lossy().into_owned();
        if entry.path().is_dir() || !name.ends_with(JOURNAL_SUFFIX) {
            continue;
        }
        let Ok(contents) = fs::read(entry.path()) else {
            // Go skips a file it cannot open.
            continue;
        };
        for line in scan_lines(&contents) {
            if is_blank(line) {
                continue;
            }
            let Ok(event) = Event::unmarshal_json_line(line) else {
                continue;
            };
            if event.lamport > max_lamport {
                max_lamport = event.lamport;
            }
            let _ = member_state.apply_event(&event);
        }
    }
    Ok(JournalStats {
        max_lamport,
        member_state,
    })
}

/// Reads one author's sequence number and previous-line hash.
///
/// # Errors
/// Returns the filesystem error when the author's journal file exists but
/// cannot be read.
pub fn author_stats(room_dir: &Path, author: &str) -> Result<AuthorStats, std::io::Error> {
    let path = author_journal_path(room_dir, author);
    let contents = match fs::read(&path) {
        Ok(contents) => contents,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => {
            return Ok(AuthorStats {
                seq: 0,
                prev: ZERO_HASH.to_owned(),
            });
        }
        Err(error) => return Err(error),
    };

    let mut count = 0_u64;
    let mut last: Option<&[u8]> = None;
    for line in scan_lines(&contents) {
        if is_blank(line) {
            continue;
        }
        count += 1;
        last = Some(line);
    }

    match last {
        None => Ok(AuthorStats {
            seq: 0,
            prev: ZERO_HASH.to_owned(),
        }),
        Some(line) => {
            let digest = Sha256::digest(line);
            Ok(AuthorStats {
                seq: count,
                prev: format!("sha256:{}", hex::encode(digest)),
            })
        }
    }
}

/// Go `Journal.VerifyChain`: checks the non-blank lines of one author's
/// segment in sequence, hashing each stored JSON line without its delimiter.
/// Signatures and cross-author membership belong to `Journal.Verify`, not here.
///
/// # Errors
/// Returns the first decoding, sequence or previous-hash error, or an I/O error.
pub fn verify_chain(room_dir: &Path, author: &str) -> Result<(), VerifyChainError> {
    let path = author_journal_path(room_dir, author);
    let file = match fs::File::open(&path) {
        Ok(file) => file,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(()),
        Err(error) => return Err(error.into()),
    };
    let mut reader = BufReader::new(file);
    let mut events = Vec::new();
    // Go ReadSegment ignores Scanner errors after returning decoded events.
    while let Ok(Some(line)) = read_scanner_line(&mut reader) {
        if !is_blank(&line) {
            events.push(Event::unmarshal_json_line(&line).map_err(VerifyChainError::Parse)?);
        }
    }
    let file = match fs::File::open(&path) {
        Ok(file) => file,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound && events.is_empty() => {
            return Ok(());
        }
        Err(error) => return Err(error.into()),
    };
    let mut reader = BufReader::new(file);
    let mut lines = Vec::new();
    while let Some(line) = read_scanner_line(&mut reader)? {
        if !is_blank(&line) {
            lines.push(line);
        }
    }
    let mut previous = ZERO_HASH.to_owned();
    for (index, (line, event)) in lines.iter().zip(events.iter()).enumerate() {
        let expected = index as u64 + 1;
        if event.seq != expected {
            return Err(VerifyChainError::Sequence {
                author: author.to_owned(),
                expected,
                actual: event.seq,
            });
        }
        if event.prev != previous {
            return Err(VerifyChainError::Previous {
                author: author.to_owned(),
                seq: event.seq,
                expected: previous,
                actual: event.prev.clone(),
            });
        }
        previous = format!("sha256:{}", hex::encode(Sha256::digest(line)));
    }
    Ok(())
}

// Go bufio.Scanner's default buffer is 64 KiB, including the delimiter.
// Bound each physical line before allocating it.
fn read_scanner_line(reader: &mut impl BufRead) -> Result<Option<Vec<u8>>, VerifyChainError> {
    const MAX_TOKEN_BUFFER: usize = 64 * 1024;
    let mut line = Vec::new();
    loop {
        let available = reader.fill_buf()?;
        if available.is_empty() {
            if line.is_empty() {
                return Ok(None);
            }
            if line.last() == Some(&b'\r') {
                line.pop();
            }
            return Ok(Some(line));
        }
        let newline = available.iter().position(|byte| *byte == b'\n');
        let length = newline.map_or(available.len(), |index| index + 1);
        if line.len() + length > MAX_TOKEN_BUFFER
            || (newline.is_none() && line.len() + length == MAX_TOKEN_BUFFER)
        {
            return Err(VerifyChainError::ScannerTooLong);
        }
        line.extend_from_slice(&available[..length]);
        reader.consume(length);
        if newline.is_some() {
            line.pop();
            if line.last() == Some(&b'\r') {
                line.pop();
            }
            return Ok(Some(line));
        }
    }
}

#[derive(Debug, thiserror::Error)]
pub enum VerifyChainError {
    #[error(transparent)]
    Io(#[from] std::io::Error),
    #[error("unmarshal line: {0}")]
    Parse(EventError),
    #[error("bufio.Scanner: token too long")]
    ScannerTooLong,
    #[error(
        "journal sequence number mismatch: author {author} expected seq {expected}, got {actual}"
    )]
    Sequence {
        author: String,
        expected: u64,
        actual: u64,
    },
    #[error(
        "journal hash chain broken: author {author} seq {seq} expected prev {expected}, got {actual}"
    )]
    Previous {
        author: String,
        seq: u64,
        expected: String,
        actual: String,
    },
}

/// Appends a marshalled event to its author's journal file.
///
/// # Errors
/// Returns the marshalling error, or the filesystem error of the directory
/// creation, open or write.
pub fn append_event(room_dir: &Path, event: &Event) -> Result<(), JournalError> {
    let journal_dir = room_dir.join(JOURNAL_DIR);
    create_journal_dir(&journal_dir)?;
    let line = event.marshal_json_line()?;
    let path = journal_dir.join(format!("{}{JOURNAL_SUFFIX}", event.author));
    let mut file = open_append(&path)?;
    file.write_all(&line)?;
    Ok(())
}

#[derive(Debug, thiserror::Error)]
pub enum JournalError {
    #[error(transparent)]
    Io(#[from] std::io::Error),
    #[error(transparent)]
    Event(#[from] EventError),
}

fn author_journal_path(room_dir: &Path, author: &str) -> PathBuf {
    room_dir
        .join(JOURNAL_DIR)
        .join(format!("{author}{JOURNAL_SUFFIX}"))
}

/// Splits like Go's `bufio.ScanLines`: on `\n`, dropping one trailing `\r`,
/// yielding a final unterminated chunk and nothing after a final newline.
fn scan_lines(contents: &[u8]) -> Vec<&[u8]> {
    let mut lines = Vec::new();
    let mut rest = contents;
    loop {
        if rest.is_empty() {
            return lines;
        }
        match rest.iter().position(|byte| *byte == b'\n') {
            Some(index) => {
                lines.push(strip_carriage_return(&rest[..index]));
                rest = &rest[index + 1..];
            }
            None => {
                lines.push(strip_carriage_return(rest));
                return lines;
            }
        }
    }
}

fn strip_carriage_return(line: &[u8]) -> &[u8] {
    match line.last() {
        Some(b'\r') => &line[..line.len() - 1],
        _ => line,
    }
}

/// Mirrors Go's `strings.TrimSpace(string(line)) == ""`.
fn is_blank(line: &[u8]) -> bool {
    String::from_utf8_lossy(line).trim().is_empty()
}

#[cfg(unix)]
fn create_journal_dir(path: &Path) -> Result<(), std::io::Error> {
    use std::os::unix::fs::DirBuilderExt;
    if path.is_dir() {
        return Ok(());
    }
    fs::DirBuilder::new()
        .recursive(true)
        .mode(0o700)
        .create(path)
}

#[cfg(not(unix))]
fn create_journal_dir(path: &Path) -> Result<(), std::io::Error> {
    fs::create_dir_all(path)
}

#[cfg(unix)]
fn open_append(path: &Path) -> Result<fs::File, std::io::Error> {
    use std::os::unix::fs::OpenOptionsExt;
    fs::OpenOptions::new()
        .create(true)
        .append(true)
        .mode(0o600)
        .open(path)
}

#[cfg(not(unix))]
fn open_append(path: &Path) -> Result<fs::File, std::io::Error> {
    fs::OpenOptions::new().create(true).append(true).open(path)
}
