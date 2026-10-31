//! Normalisation and comparison helpers shared by the configured-provider
//! replays (`ask_agent`, `mcp_ask_provider`, `ask_cancel`). Retrieval scores,
//! task ids, the scratch root and the loopback port vary per run or per host
//! and are masked exactly like the Go generator masks them.
#![allow(dead_code)]

use std::{
    collections::BTreeMap,
    fs,
    os::unix::fs::PermissionsExt,
    path::PathBuf,
    process::Command,
    sync::atomic::{AtomicUsize, Ordering},
    time::{SystemTime, UNIX_EPOCH},
};

use serde::Deserialize;
use serde_json::Value;

use crate::provider::{self, KEY};

#[derive(Deserialize, Clone)]
pub struct Request {
    pub method: String,
    pub path: String,
    #[serde(default)]
    pub headers: BTreeMap<String, String>,
    #[serde(default)]
    pub body: Option<Value>,
}

/// Go's timestamped slog lines are excluded from the compared stderr.
pub fn without_log_lines(text: &str) -> String {
    text.split_inclusive('\n')
        .filter(|line| !(line.starts_with("time=") && line.contains("level=INFO")))
        .collect()
}

/// `task-<nanoseconds>` run ids differ per run.
pub fn mask_task_ids(text: &str) -> String {
    let mut out = String::new();
    let mut rest = text;
    while let Some(at) = rest.find("task-") {
        out.push_str(&rest[..at]);
        let tail = &rest[at + 5..];
        let digits = tail.bytes().take_while(u8::is_ascii_digit).count();
        if digits > 0 {
            out.push_str("task-<ID>");
            rest = &tail[digits..];
        } else {
            out.push_str("task-");
            rest = tail;
        }
    }
    out.push_str(rest);
    out
}

/// `("score":|\"score\":)<number>` -> `...<SCORE>` (Go `askScoreRawRE`).
pub fn mask_raw_scores(text: &str) -> String {
    let mut out = String::new();
    let mut rest = text;
    while let Some(at) = rest.find("score") {
        let before = &rest[..at];
        let after = &rest[at + 5..];
        let opened = before.ends_with('"');
        let closed = after.starts_with("\\\":") || after.starts_with("\":");
        if !(opened && closed) {
            out.push_str(&rest[..at + 5]);
            rest = after;
            continue;
        }
        let quote_len = if after.starts_with("\\\":") { 3 } else { 2 };
        out.push_str(&rest[..at + 5 + quote_len]);
        let number = &after[quote_len..];
        let digits_start = usize::from(number.starts_with('-'));
        if number
            .as_bytes()
            .get(digits_start)
            .is_some_and(u8::is_ascii_digit)
        {
            let len = number
                .bytes()
                .skip(digits_start + 1)
                .take_while(|byte| matches!(byte, b'0'..=b'9' | b'.' | b'e' | b'E' | b'+' | b'-'))
                .count()
                + digits_start
                + 1;
            out.push_str("<SCORE>");
            rest = &number[len..];
        } else {
            rest = number;
        }
    }
    out.push_str(rest);
    out
}

/// Go `maskAskScores`: `"score":<n>` and bare `0.0…` tokens.
pub fn mask_scores(text: &str) -> String {
    const KEY_TEXT: &str = "\"score\":";
    let mut masked = String::new();
    let mut rest = text;
    while let Some(at) = rest.find(KEY_TEXT) {
        masked.push_str(&rest[..at + KEY_TEXT.len()]);
        let tail = &rest[at + KEY_TEXT.len()..];
        let end = tail.find([',', '}']).unwrap_or(tail.len());
        masked.push_str("\"<SCORE>\"");
        rest = &tail[end..];
    }
    masked.push_str(rest);
    masked
        .split(' ')
        .map(|token| {
            if token.starts_with("0.0") && token.parse::<f64>().is_ok() {
                "<SCORE>"
            } else {
                token
            }
        })
        .collect::<Vec<_>>()
        .join(" ")
}

pub fn normalize(text: &str, root: &str, url: &str) -> String {
    let mut text = text.replace(root, "<ROOT>");
    if !url.is_empty() {
        text = text.replace(url, "<URL>");
    }
    mask_scores(&mask_raw_scores(&mask_task_ids(&text)))
}

pub fn compare_requests(
    label: &str,
    expected: &[Request],
    actual: &[provider::Seen],
    root: &str,
    url: &str,
) -> Result<(), String> {
    if expected.len() != actual.len() {
        return Err(format!(
            "{label}: provider saw {} requests, Go saw {}",
            actual.len(),
            expected.len()
        ));
    }
    for (index, (want, got)) in expected.iter().zip(actual).enumerate() {
        if (want.method.as_str(), want.path.as_str()) != (got.method.as_str(), got.path.as_str()) {
            return Err(format!(
                "{label}: request {index} {} {} != {} {}",
                got.method, got.path, want.method, want.path
            ));
        }
        // The Go fake records headers and bodies for chat endpoints only.
        let chat = matches!(
            got.path.as_str(),
            "/api/chat" | "/api/generate" | "/v1/messages"
        );
        let mut headers = BTreeMap::new();
        for (name, value) in got.headers.iter().filter(|_| chat) {
            if matches!(
                name.as_str(),
                "x-api-key" | "anthropic-version" | "content-type"
            ) {
                let value = if value == KEY { "<KEY>" } else { value };
                headers.insert(name.clone(), value.to_owned());
            }
        }
        if headers != want.headers {
            return Err(format!(
                "{label}: request {index} headers {headers:?} != {:?}",
                want.headers
            ));
        }
        let Some(want_body) = &want.body else {
            continue;
        };
        let text = normalize(&String::from_utf8_lossy(&got.body), root, url);
        let got_body: Value = serde_json::from_str(&text)
            .map_err(|error| format!("{label}: request {index} is not JSON: {error}: {text}"))?;
        if &got_body != want_body {
            return Err(format!(
                "{label}: request {index} body\nrust: {got_body}\ngo:   {want_body}"
            ));
        }
    }
    Ok(())
}

/// `2026-01-02T03:04:05Z` -> SystemTime.
pub fn parse_time(text: &str) -> SystemTime {
    let parsed = time::OffsetDateTime::parse(text, &time::format_description::well_known::Rfc3339)
        .expect("fixture timestamp");
    UNIX_EPOCH + std::time::Duration::from_secs(u64::try_from(parsed.unix_timestamp()).unwrap())
}

static ROOT_SEQUENCE: AtomicUsize = AtomicUsize::new(0);

/// One isolated vault/home/bin tree per case; removed on drop.
pub struct Scratch {
    pub root: PathBuf,
    pub home: PathBuf,
    pub vault: PathBuf,
    pub log: PathBuf,
    bin: PathBuf,
}

impl Scratch {
    pub fn new(
        name: &str,
        files: &BTreeMap<String, String>,
        file_time: &str,
        hermes: Option<&str>,
    ) -> Self {
        let root = fs::canonicalize(std::env::temp_dir())
            .unwrap()
            .join(format!(
                "symdesk-ask-{name}-{}-{}",
                std::process::id(),
                SystemTime::now()
                    .duration_since(UNIX_EPOCH)
                    .unwrap()
                    .as_nanos()
                    + ROOT_SEQUENCE.fetch_add(1, Ordering::Relaxed) as u128
            ));
        let (home, bin, vault) = (root.join("home"), root.join("bin"), root.join("vault"));
        for dir in [&home, &bin, &vault] {
            fs::create_dir_all(dir).unwrap();
        }
        // Retrieval embeddings point at a closed port; keep the retry loop of
        // the failing embedder instant so setup does not depend on how long
        // the host takes to refuse a connection.
        let seek_config = home.join(".config/symseek");
        fs::create_dir_all(&seek_config).unwrap();
        fs::write(
            seek_config.join("config.toml"),
            "retry_count = 1\nretry_backoff_ms = 1\n",
        )
        .unwrap();
        let stamp = parse_time(file_time);
        for (file, content) in files {
            let dest = vault.join(file);
            fs::create_dir_all(dest.parent().unwrap()).unwrap();
            fs::write(&dest, content).unwrap();
            fs::File::options()
                .write(true)
                .open(&dest)
                .unwrap()
                .set_modified(stamp)
                .unwrap();
        }
        if let Some(script) = hermes {
            fs::write(bin.join("hermes"), script).unwrap();
            fs::set_permissions(bin.join("hermes"), fs::Permissions::from_mode(0o700)).unwrap();
        }
        let log = root.join("hermes.log");
        Self {
            root,
            home,
            vault,
            log,
            bin,
        }
    }

    pub fn root_text(&self) -> String {
        self.root.to_string_lossy().into_owned()
    }

    /// The CLI under test with the hermetic environment of the Go generator.
    pub fn command(&self, mode: &str, extra: &BTreeMap<String, String>) -> Command {
        let mut command = Command::new(env!("CARGO_BIN_EXE_symdesk"));
        command
            .current_dir(&self.root)
            .env_clear()
            .env("PATH", format!("{}:/usr/bin:/bin", self.bin.display()))
            .env("HOME", &self.home)
            .env("TZ", "UTC")
            .env("XDG_CONFIG_HOME", self.home.join("config"))
            .env("XDG_CACHE_HOME", self.home.join("cache"))
            .env("XDG_DATA_HOME", self.home.join("data"))
            .env("FAKE_HERMES_LOG", &self.log)
            .env("FAKE_HERMES_MODE", mode)
            .envs(extra);
        command
    }

    /// Build the sidecar with embeddings pointed at a closed port.
    pub fn index(&self, mode: &str) {
        let closed = BTreeMap::from([(
            "SYMDESK_OLLAMA_URL".to_owned(),
            "http://127.0.0.1:9".to_owned(),
        )]);
        let setup = self
            .command(mode, &closed)
            .arg("--vault")
            .arg(&self.vault)
            .arg("index")
            .output()
            .unwrap();
        assert!(
            setup.status.success(),
            "index: {}",
            String::from_utf8_lossy(&setup.stderr)
        );
        let _ = fs::remove_file(&self.log);
    }

    pub fn transcript(&self) -> String {
        fs::read_to_string(&self.log)
            .unwrap_or_default()
            .replace(&self.root_text(), "<ROOT>")
    }
}

impl Drop for Scratch {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.root);
    }
}

/// The provider selection a case runs under (Go `providerEnv`); `{{URL}}` in
/// an override is the fake provider's base URL.
pub fn provider_env(
    provider: &str,
    url: Option<&str>,
    extra: &BTreeMap<String, String>,
) -> BTreeMap<String, String> {
    let mut env = BTreeMap::new();
    if provider != "none" {
        env.insert(
            "SYMDESK_OLLAMA_URL".to_owned(),
            "http://127.0.0.1:9".to_owned(),
        );
    }
    match (provider, url) {
        ("ollama", Some(url)) => {
            env.insert("SYMDESK_LLM_PROVIDER".to_owned(), "ollama".to_owned());
            env.insert("SYMDESK_OLLAMA_URL".to_owned(), url.to_owned());
        }
        ("anthropic", Some(url)) => {
            env.insert("SYMDESK_LLM_PROVIDER".to_owned(), "anthropic".to_owned());
            env.insert("SYMDESK_ANTHROPIC_URL".to_owned(), format!("{url}/v1"));
            env.insert("SYMDESK_LLM_API_KEY".to_owned(), KEY.to_owned());
        }
        ("hermes", _) => {
            env.insert("SYMDESK_LLM_PROVIDER".to_owned(), "hermes".to_owned());
        }
        _ => {}
    }
    for (key, value) in extra {
        env.insert(
            key.clone(),
            value.replace("{{URL}}", url.unwrap_or_default()),
        );
    }
    env
}

pub fn mcp_call_line(arguments: &str) -> String {
    format!(
        r#"{{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{{"name":"desk_ask","arguments":{arguments}}}}}"#
    ) + "\n"
}
