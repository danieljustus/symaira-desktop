#![cfg(unix)]
use serde::Deserialize;
use std::{
    fs,
    os::unix::fs::PermissionsExt,
    path::{Path, PathBuf},
    sync::atomic::{AtomicU64, Ordering},
    time::{Duration, Instant},
};
use symdesk_core::{compose::Resolver, compose_probe::ToolCache};
#[derive(Deserialize)]
struct Corpus {
    schema_version: u32,
    platform: String,
    cases: Vec<Case>,
}
#[derive(Deserialize)]
struct Case {
    id: String,
    output: String,
    exit: u8,
    missing: bool,
    timeout: bool,
    replace: String,
    observations: Vec<Observation>,
}
#[derive(Deserialize)]
struct Observation {
    available: bool,
    version: String,
    argv: String,
}
struct Root(PathBuf);
impl Root {
    fn new() -> Self {
        static NEXT: AtomicU64 = AtomicU64::new(0);
        let root = Self(std::env::temp_dir().join(format!(
            "symdesk-probe-replay-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        )));
        fs::create_dir(&root.0).unwrap();
        root
    }
}
impl Drop for Root {
    fn drop(&mut self) {
        fs::remove_dir_all(&self.0).unwrap();
    }
}
fn script(path: &Path, transcript: &Path, output: &str, exit: u8, timeout: bool) {
    let mut content = format!(
        "#!/bin/sh\nprintf '%s\\n' \"$@\" >> '{}'\n",
        transcript.display()
    );
    if timeout {
        content.push_str("while :; do :; done\n");
    } else {
        content.push_str(&format!("printf '%s' '{}'\nexit {exit}\n", output));
    }
    fs::write(path, content).unwrap();
    fs::set_permissions(path, fs::Permissions::from_mode(0o700)).unwrap();
}
#[test]
fn replay_go_version_probes_and_cache() {
    let path = std::env::var_os("PORT_FIXTURE_PATH")
        .map(PathBuf::from)
        .unwrap_or_else(|| {
            Path::new(env!("CARGO_MANIFEST_DIR")).join("../../testdata/port/ai/compose-probe.json")
        });
    let corpus: Corpus = serde_json::from_slice(&fs::read(path).unwrap()).unwrap();
    assert_eq!(corpus.schema_version, 1);
    assert_eq!(corpus.platform, "unix");
    assert_eq!(corpus.cases.len(), 19);
    for case in corpus.cases {
        let root = Root::new();
        let bin = root.0.join("bin");
        fs::create_dir(&bin).unwrap();
        let runner = bin.join("symprobe");
        let transcript = root.0.join("argv");
        if !case.missing {
            script(&runner, &transcript, &case.output, case.exit, case.timeout);
        }
        let resolver = Resolver {
            home: root.0.join("home").to_str().unwrap().to_owned(),
            symaira_bin: bin.to_str().unwrap().to_owned(),
            path: String::new(),
            cwd: root.0.clone(),
            allow_relative_path: false,
        };
        let cache = ToolCache::default();
        for (i, expected) in case.observations.iter().enumerate() {
            if i == 1 && !case.replace.is_empty() {
                script(&runner, &transcript, &case.replace, 0, false);
            }
            if i == 2 {
                cache.reset();
            }
            let start = Instant::now();
            let actual = cache.has_tool(&resolver, "symprobe");
            if case.timeout && i == 0 {
                assert!(start.elapsed() >= Duration::from_secs(1));
                assert!(start.elapsed() < Duration::from_secs(3));
            }
            assert_eq!(
                actual.available, expected.available,
                "{}: availability step {i}",
                case.id
            );
            assert_eq!(
                actual.version, expected.version,
                "{}: version step {i}",
                case.id
            );
            let argv = fs::read_to_string(&transcript).unwrap_or_default();
            assert_eq!(
                argv, expected.argv,
                "{}: exact executed argv and cache probe count step {i}",
                case.id
            );
        }
    }
}
