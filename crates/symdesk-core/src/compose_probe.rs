//! Go `HasTool`/`ResetCache`: read-only version probing with a one-second
//! deadline. This adapter never imports a separately released sibling crate.
use crate::compose::Resolver;
use std::{
    collections::HashMap,
    fs::{self, File, OpenOptions},
    io::{self, Read, Seek, SeekFrom},
    os::unix::fs::OpenOptionsExt,
    path::PathBuf,
    process::{Child, Command, Stdio},
    sync::{
        Mutex,
        atomic::{AtomicU64, Ordering},
    },
    thread,
    time::{Duration, Instant},
};

#[derive(Clone, Debug, PartialEq, Eq)]
pub struct ToolInfo {
    pub available: bool,
    pub version: String,
}
/// Cache both success and absence by binary name until explicitly reset.
/// An instance is the cache ownership boundary; share it across callers.
#[derive(Default)]
pub struct ToolCache(Mutex<HashMap<String, ToolInfo>>);
impl ToolCache {
    pub fn reset(&self) {
        self.0.lock().unwrap_or_else(|p| p.into_inner()).clear();
    }
    pub fn has_tool(&self, resolver: &Resolver, name: &str) -> ToolInfo {
        let mut cache = self.0.lock().unwrap_or_else(|p| p.into_inner());
        cache
            .entry(name.to_owned())
            .or_insert_with(|| match resolver.resolve(name) {
                Err(_) => ToolInfo {
                    available: false,
                    version: "not_found".to_owned(),
                },
                Ok(path) => ToolInfo {
                    available: true,
                    version: probe(resolver, &path).unwrap_or_else(|_| "unknown".to_owned()),
                },
            })
            .clone()
    }
}

fn probe(resolver: &Resolver, path: &str) -> io::Result<String> {
    let mut output = ProbeOutput::new()?;
    let start = Instant::now();
    let child = Command::new(resolver.cwd.join(path))
        .current_dir(&resolver.cwd)
        .env("HOME", &resolver.home)
        .env("SYMAIRA_BIN", &resolver.symaira_bin)
        .env("PATH", &resolver.path)
        .args(["version", "--json"])
        .stdin(Stdio::null())
        .stdout(output.file.try_clone()?)
        .stderr(Stdio::null())
        .spawn()?;
    let mut child = ProbeChild {
        child,
        reaped: false,
    };
    loop {
        if let Some(status) = child.child.try_wait()? {
            child.reaped = true;
            if !status.success() {
                return Err(io::Error::other("version probe failed"));
            }
            break;
        }
        if start.elapsed() >= Duration::from_secs(1) {
            return Err(io::Error::new(
                io::ErrorKind::TimedOut,
                "version probe timed out",
            ));
        }
        thread::sleep(Duration::from_millis(2));
    }
    output.file.seek(SeekFrom::Start(0))?;
    let mut bytes = Vec::new();
    output.file.read_to_end(&mut bytes)?;
    let value: ProbeVersion = serde_json::from_slice(&bytes).map_err(io::Error::other)?;
    Ok(value.0)
}
// Decode in wire order, as Go encoding/json does: matching is case-insensitive,
// duplicate fields update earlier values, and null leaves a scalar untouched.
struct ProbeVersion(String);
impl<'de> serde::Deserialize<'de> for ProbeVersion {
    fn deserialize<D: serde::Deserializer<'de>>(deserializer: D) -> Result<Self, D::Error> {
        struct VersionVisitor;
        impl<'de> serde::de::Visitor<'de> for VersionVisitor {
            type Value = ProbeVersion;
            fn expecting(&self, f: &mut std::fmt::Formatter<'_>) -> std::fmt::Result {
                f.write_str("version object or null")
            }
            fn visit_unit<E: serde::de::Error>(self) -> Result<Self::Value, E> {
                Ok(ProbeVersion(String::new()))
            }
            fn visit_map<M: serde::de::MapAccess<'de>>(
                self,
                mut map: M,
            ) -> Result<Self::Value, M::Error> {
                let mut version = String::new();
                while let Some(key) = map.next_key::<String>()? {
                    if key.eq_ignore_ascii_case("version") {
                        if let Some(value) = map.next_value::<Option<String>>()? {
                            version = value;
                        }
                    } else if key.eq_ignore_ascii_case("schema_version") {
                        let _ = map.next_value::<Option<i64>>()?;
                    } else {
                        let _ = map.next_value::<serde::de::IgnoredAny>()?;
                    }
                }
                Ok(ProbeVersion(version))
            }
        }
        deserializer.deserialize_any(VersionVisitor)
    }
}
struct ProbeChild {
    child: Child,
    reaped: bool,
}
impl Drop for ProbeChild {
    fn drop(&mut self) {
        if !self.reaped {
            let _ = self.child.kill();
            let _ = self.child.wait();
        }
    }
}
// Regular-file output avoids joining a pipe-reader that a descendant could
// retain. It contains no surfaced stderr and is private from creation onward.
struct ProbeOutput {
    path: PathBuf,
    file: File,
}
impl ProbeOutput {
    fn new() -> io::Result<Self> {
        static NEXT: AtomicU64 = AtomicU64::new(0);
        loop {
            let path = std::env::temp_dir().join(format!(
                "symdesk-version-probe-{}-{}",
                std::process::id(),
                NEXT.fetch_add(1, Ordering::Relaxed)
            ));
            match OpenOptions::new()
                .read(true)
                .write(true)
                .create_new(true)
                .mode(0o600)
                .open(&path)
            {
                Ok(file) => return Ok(Self { path, file }),
                Err(error) if error.kind() == io::ErrorKind::AlreadyExists => (),
                Err(error) => return Err(error),
            }
        }
    }
}
impl Drop for ProbeOutput {
    fn drop(&mut self) {
        let _ = fs::remove_file(&self.path);
    }
}
