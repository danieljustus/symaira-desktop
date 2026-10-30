#![cfg(unix)]
use serde::Deserialize;
use std::{
    fs,
    os::unix::fs::{PermissionsExt, symlink},
    path::{Path, PathBuf},
    sync::atomic::{AtomicU64, Ordering},
};
use symdesk_core::compose::Resolver;

#[derive(Deserialize)]
struct Corpus {
    schema_version: u32,
    platform: String,
    cases: Vec<Case>,
}
#[derive(Deserialize)]
struct File {
    path: String,
    kind: String,
    mode: u32,
    target: String,
}
#[derive(Deserialize)]
struct Case {
    id: String,
    name: String,
    home: String,
    bin: String,
    path_env: String,
    execerrdot: String,
    files: Option<Vec<File>>,
    path: String,
    origin: String,
    error: String,
    managed: String,
    managed_exists: bool,
}
struct Root(PathBuf);
impl Root {
    fn new() -> Self {
        static NEXT: AtomicU64 = AtomicU64::new(0);
        let path = std::env::temp_dir().join(format!(
            "compose-resolve-{}-{}",
            std::process::id(),
            NEXT.fetch_add(1, Ordering::Relaxed)
        ));
        fs::create_dir(&path).unwrap();
        Self(path)
    }
}
impl Drop for Root {
    fn drop(&mut self) {
        fs::remove_dir_all(&self.0).unwrap();
    }
}
fn corpus() -> Corpus {
    let path = std::env::var_os("PORT_FIXTURE_PATH")
        .map(PathBuf::from)
        .unwrap_or_else(|| {
            Path::new(env!("CARGO_MANIFEST_DIR"))
                .join("../../testdata/port/ai/compose-resolve.json")
        });
    serde_json::from_slice(&fs::read(path).unwrap()).unwrap()
}

#[test]
fn replay_go_compose_resolution() {
    let corpus = corpus();
    assert_eq!(corpus.schema_version, 1);
    assert_eq!(corpus.platform, "unix");
    assert_eq!(
        corpus.cases.len(),
        38,
        "never accept an empty or partial corpus"
    );
    for case in corpus.cases {
        let root = Root::new();
        let root_text = root.0.to_str().unwrap();
        let expand = |value: &str| value.replace("{root}", root_text);
        let normalize = |value: &str| value.replace(root_text, "{root}");
        for file in case.files.unwrap_or_default() {
            let path = root.0.join(&file.path);
            fs::create_dir_all(path.parent().unwrap()).unwrap();
            match file.kind.as_str() {
                "dir" => fs::create_dir_all(&path).unwrap(),
                "symlink" => symlink(expand(&file.target), &path).unwrap(),
                "file" => {
                    fs::write(&path, "#!/bin/sh\nexit 99\n").unwrap();
                    fs::set_permissions(&path, fs::Permissions::from_mode(file.mode)).unwrap();
                }
                kind => panic!("unexpected fixture entry {kind}"),
            }
        }
        let resolver = Resolver {
            home: expand(&case.home),
            symaira_bin: expand(&case.bin),
            path: expand(&case.path_env),
            cwd: root.0.clone(),
            allow_relative_path: case.execerrdot == "0",
        };
        let (path, origin, error) = match resolver.resolve_with_origin(&expand(&case.name)) {
            Ok((path, origin)) => (normalize(&path), origin.as_str().to_owned(), String::new()),
            Err(error) => (String::new(), String::new(), normalize(&error)),
        };
        assert_eq!(path, case.path, "{}: path", case.id);
        assert_eq!(origin, case.origin, "{}: origin", case.id);
        assert_eq!(error, case.error, "{}: exact error bytes", case.id);
        assert_eq!(
            normalize(&resolver.managed_runtime_dir()),
            case.managed,
            "{}: managed directory",
            case.id
        );
        assert_eq!(
            resolver.managed_runtime_dir_exists(),
            case.managed_exists,
            "{}: managed existence",
            case.id
        );
    }
}
