#![deny(unsafe_code)]

use std::{
    collections::BTreeMap,
    fs,
    path::PathBuf,
    sync::atomic::{AtomicU64, Ordering},
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;
use symdesk_vault::{
    MutationError, delete_frontmatter_value, set_frontmatter_key, set_frontmatter_value,
};

#[derive(Deserialize)]
struct Fixture {
    schema_version: u8,
    oracle: Oracle,
    source_hashes: std::collections::BTreeMap<String, String>,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Oracle {
    commit: String,
    release: String,
}

#[derive(Deserialize)]
struct Case {
    id: String,
    operation: String,
    input_base64: String,
    key: String,
    value: InputValue,
    output_base64: String,
    #[serde(default)]
    error_class: String,
    unix_mode_before: Option<u32>,
    unix_mode_after: Option<u32>,
}

#[derive(Deserialize)]
#[serde(tag = "kind", content = "value", rename_all = "lowercase")]
enum InputValue {
    Null,
    Bool(bool),
    String(String),
    I64(String),
    U64(String),
    Float64(String),
    Sequence(Vec<InputValue>),
    Mapping(BTreeMap<String, InputValue>),
}

impl InputValue {
    fn to_noyalib(&self) -> Result<noyalib::Value, String> {
        match self {
            Self::Null => Ok(noyalib::Value::Null),
            Self::Bool(value) => Ok(noyalib::Value::from(*value)),
            Self::String(value) => Ok(noyalib::Value::from(value.clone())),
            Self::I64(value) => value
                .parse::<i64>()
                .map(|value| noyalib::Value::Number(noyalib::Number::Integer(value)))
                .map_err(|error| format!("invalid i64 {value:?}: {error}")),
            Self::U64(value) => value
                .parse::<u64>()
                .map(|value| noyalib::Value::Number(noyalib::Number::Unsigned(value)))
                .map_err(|error| format!("invalid u64 {value:?}: {error}")),
            Self::Float64(value) => parse_float(value)
                .map(|value| noyalib::Value::Number(noyalib::Number::Float(value))),
            Self::Sequence(values) => values
                .iter()
                .map(Self::to_noyalib)
                .collect::<Result<Vec<_>, _>>()
                .map(noyalib::Value::Sequence),
            Self::Mapping(values) => {
                let mut mapping = noyalib::Mapping::new();
                for (key, value) in values {
                    mapping.insert(key.clone(), value.to_noyalib()?);
                }
                Ok(noyalib::Value::Mapping(mapping))
            }
        }
    }
}

fn parse_float(value: &str) -> Result<f64, String> {
    match value {
        "NaN" => Ok(f64::NAN),
        "+Inf" => Ok(f64::INFINITY),
        "-Inf" => Ok(f64::NEG_INFINITY),
        value => value
            .parse::<f64>()
            .map_err(|error| format!("invalid float64 {value:?}: {error}")),
    }
}

#[test]
fn generated_go_frontmatter_writes_match_exact_bytes_and_side_effects() {
    let fixture: Fixture = serde_json::from_str(include_str!(
        "../../../testdata/port/vault/frontmatter-write.json"
    ))
    .expect("decode frontmatter write fixture");
    assert_eq!(fixture.schema_version, 2);
    assert!(!fixture.oracle.commit.is_empty());
    assert!(!fixture.oracle.release.is_empty());
    assert!(!fixture.source_hashes.is_empty());
    assert_eq!(fixture.cases.len(), 70);
    assert!(!fixture.cases.is_empty());

    let mismatches: Vec<String> = fixture.cases.iter().flat_map(run_case).collect();
    assert!(
        mismatches.is_empty(),
        "frontmatter parity mismatches:\n{}",
        mismatches.join("\n")
    );
}

fn run_case(case: &Case) -> Vec<String> {
    let root = OwnedTempDir::new(&case.id);
    let path = if case.id == "missing-parent" {
        root.path.join("missing").join("note.md")
    } else {
        root.path.join("note.md")
    };
    let input = base64::decode(&case.input_base64).expect("decode input bytes");
    if case.id == "read-directory" {
        fs::create_dir_all(&path).expect("create directory case");
        set_initial_mode(&path, case.unix_mode_before);
    } else if !case.id.starts_with("missing-") {
        fs::write(&path, &input).expect("write case input");
        set_initial_mode(&path, case.unix_mode_before);
    }
    let actual_before = file_mode(&path);

    let result = match case.operation.as_str() {
        "set_key" => match &case.value {
            InputValue::String(value) => set_frontmatter_key(&path, &case.key, value),
            _ => panic!("{}: set_key requires a string fixture value", case.id),
        },
        "set_value" => {
            let value = case
                .value
                .to_noyalib()
                .unwrap_or_else(|error| panic!("{}: {error}", case.id));
            set_frontmatter_value(&path, &case.key, &value)
        }
        "delete_value" => delete_frontmatter_value(&path, &case.key),
        operation => panic!("{}: unknown operation {operation}", case.id),
    };

    let mut mismatches = Vec::new();
    let actual_error = result.as_ref().err().map(error_class).unwrap_or("");
    if actual_error != case.error_class {
        mismatches.push(format!(
            "{} error class: expected {:?}, got {:?}",
            case.id, case.error_class, actual_error
        ));
    }
    let output = fs::read(&path).unwrap_or_default();
    let actual_output = base64::encode(output);
    if actual_output != case.output_base64 {
        mismatches.push(format!(
            "{} output: expected {:?}, got {:?}",
            case.id, case.output_base64, actual_output
        ));
    }

    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        let actual_after = fs::metadata(&path)
            .ok()
            .map(|metadata| metadata.permissions().mode() & 0o777);
        if actual_before != case.unix_mode_before {
            mismatches.push(format!(
                "{} input mode: expected {:?}, got {:?}",
                case.id, case.unix_mode_before, actual_before
            ));
        }
        if actual_after != case.unix_mode_after {
            mismatches.push(format!(
                "{} output mode: expected {:?}, got {:?}",
                case.id, case.unix_mode_after, actual_after
            ));
        }
    }
    #[cfg(not(unix))]
    {
        let _ = actual_before;
    }
    mismatches
}

fn set_initial_mode(path: &PathBuf, mode: Option<u32>) {
    #[cfg(unix)]
    if let Some(mode) = mode {
        use std::os::unix::fs::PermissionsExt;
        fs::set_permissions(path, fs::Permissions::from_mode(mode)).expect("set case input mode");
    }

    #[cfg(not(unix))]
    {
        let _ = (path, mode);
    }
}

fn file_mode(path: &PathBuf) -> Option<u32> {
    #[cfg(unix)]
    {
        use std::os::unix::fs::PermissionsExt;
        fs::metadata(path)
            .ok()
            .map(|metadata| metadata.permissions().mode() & 0o777)
    }
    #[cfg(not(unix))]
    {
        let _ = path;
        None
    }
}

fn error_class(error: &MutationError) -> &'static str {
    match error {
        MutationError::Read { source } if source.kind() == std::io::ErrorKind::NotFound => {
            "not_found"
        }
        MutationError::Read { .. }
        | MutationError::Write { .. }
        | MutationError::TempCreate { .. }
        | MutationError::TempWrite { .. }
        | MutationError::TempSync { .. }
        | MutationError::TempRename { .. }
        | MutationError::Marshal { .. } => "filesystem",
    }
}

struct OwnedTempDir {
    path: PathBuf,
}

static TEMP_DIR_COUNTER: AtomicU64 = AtomicU64::new(0);

impl OwnedTempDir {
    fn new(id: &str) -> Self {
        let stamp = SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .expect("clock")
            .as_nanos();
        let counter = TEMP_DIR_COUNTER.fetch_add(1, Ordering::Relaxed);
        let path = std::env::temp_dir().join(format!(
            "symdesk-frontmatter-test-{id}-{}-{stamp}-{counter}",
            std::process::id()
        ));
        fs::create_dir(&path).expect("create private case directory");
        Self { path }
    }
}

impl Drop for OwnedTempDir {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.path);
    }
}

mod base64 {
    const TABLE: &[u8; 64] = b"ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/";

    fn value(byte: u8) -> Option<u8> {
        match byte {
            b'A'..=b'Z' => Some(byte - b'A'),
            b'a'..=b'z' => Some(byte - b'a' + 26),
            b'0'..=b'9' => Some(byte - b'0' + 52),
            b'+' => Some(62),
            b'/' => Some(63),
            _ => None,
        }
    }

    pub(super) fn encode(input: Vec<u8>) -> String {
        let mut output = String::new();
        for chunk in input.chunks(3) {
            let first = chunk[0];
            output.push(TABLE[(first >> 2) as usize] as char);
            let second = chunk.get(1).copied();
            output.push(TABLE[((first & 3) << 4 | second.unwrap_or(0) >> 4) as usize] as char);
            if let Some(second) = second {
                output.push(
                    TABLE[((second & 15) << 2 | chunk.get(2).copied().unwrap_or(0) >> 6) as usize]
                        as char,
                );
            } else {
                output.push('=');
            }
            if let Some(third) = chunk.get(2) {
                output.push(TABLE[(third & 63) as usize] as char);
            } else {
                output.push('=');
            }
        }
        output
    }

    pub(super) fn decode(input: &str) -> Result<Vec<u8>, ()> {
        let bytes = input.as_bytes();
        if bytes.is_empty() {
            return Ok(Vec::new());
        }
        let (chunks, remainder) = bytes.as_chunks::<4>();
        if !remainder.is_empty() {
            return Err(());
        }
        let mut output = Vec::with_capacity(bytes.len() / 4 * 3);
        for chunk in chunks {
            let a = value(chunk[0]).ok_or(())?;
            let b = value(chunk[1]).ok_or(())?;
            output.push(a << 2 | b >> 4);
            if chunk[2] != b'=' {
                let c = value(chunk[2]).ok_or(())?;
                output.push(b << 4 | c >> 2);
                if chunk[3] != b'=' {
                    let d = value(chunk[3]).ok_or(())?;
                    output.push(c << 6 | d);
                }
            }
        }
        Ok(output)
    }
}
