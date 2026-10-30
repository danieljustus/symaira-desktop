#[path = "../../test-support/process_env.rs"]
mod process_env;
use process_env::IsolatedEnvironment;

#[path = "support/root_paths.rs"]
mod root_paths;

use serde::Deserialize;
use std::{
    collections::BTreeMap,
    fs,
    path::Path,
    process::Command,
    time::{SystemTime, UNIX_EPOCH},
};
#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    cases: Vec<Case>,
}
#[derive(Deserialize)]
struct Case {
    name: String,
    #[serde(default)]
    config: String,
    #[serde(default)]
    environment: BTreeMap<String, String>,
    #[serde(default)]
    ollama_response: String,
    steps: Vec<Step>,
}
#[derive(Deserialize)]
struct Step {
    args: Vec<String>,
    exit_code: i32,
    stdout: String,
    stderr: String,
    files: BTreeMap<String, File>,
    #[serde(default)]
    requests: Vec<String>,
}
#[derive(Deserialize, Debug, PartialEq)]
struct File {
    content: String,
    mode: u32,
}
fn normalize(value: &str, root: &Path) -> String {
    let text = value
        .replace(&root.to_string_lossy().replace('\\', "\\\\"), "$ROOT")
        .replace(root.to_string_lossy().as_ref(), "$ROOT");
    root_paths::slash_root_paths(&text)
}
fn snapshot(path: &Path, root: &Path, out: &mut BTreeMap<String, File>) {
    for entry in fs::read_dir(path).unwrap() {
        let entry = entry.unwrap();
        let path = entry.path();
        if path.is_dir() {
            snapshot(&path, root, out);
            continue;
        }
        #[cfg(unix)]
        let mode = {
            use std::os::unix::fs::PermissionsExt;
            entry.metadata().unwrap().permissions().mode() & 0o777
        };
        #[cfg(not(unix))]
        let mode = 0;
        out.insert(
            path.strip_prefix(root)
                .unwrap()
                .to_string_lossy()
                .replace('\\', "/"),
            File {
                content: normalize(&fs::read_to_string(path).unwrap(), root),
                mode,
            },
        );
    }
}
#[test]
fn config_and_ai_config_match_real_go_process_and_filesystem() {
    let fixture: Fixture = serde_json::from_str(include_str!(
        "../../../testdata/port/cli/config-process.json"
    ))
    .unwrap();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.cases.len(), 10);
    assert_eq!(
        fixture.cases.iter().map(|c| c.steps.len()).sum::<usize>(),
        141
    );
    let base = std::env::temp_dir().join(format!(
        "symdesk-config-{}-{}",
        std::process::id(),
        SystemTime::now()
            .duration_since(UNIX_EPOCH)
            .unwrap()
            .as_nanos()
    ));
    let mut executed = 0;
    for case in fixture.cases {
        let root = base.join(&case.name);
        let home = root.join("home");
        fs::create_dir_all(&home).unwrap();
        let root = fs::canonicalize(root).unwrap();
        let home = root.join("home");
        if !case.config.is_empty() {
            let path = home.join("config/symdesk/config.toml");
            fs::create_dir_all(path.parent().unwrap()).unwrap();
            fs::write(&path, case.config).unwrap();
            #[cfg(unix)]
            {
                use std::os::unix::fs::PermissionsExt;
                fs::set_permissions(&path, fs::Permissions::from_mode(0o600)).unwrap();
            }
        }
        let mut environment = case.environment;
        let server =
            (!case.ollama_response.is_empty()).then(|| Server::start(case.ollama_response));
        if let Some(server) = &server {
            environment.insert("SYMDESK_OLLAMA_URL".to_owned(), server.url.clone());
        }
        for step in case.steps {
            let args: Vec<_> = step
                .args
                .iter()
                .map(|a| a.replace("$ROOT", &root.to_string_lossy()))
                .collect();
            let output = Command::new(env!("CARGO_BIN_EXE_symdesk"))
                .current_dir(&root)
                .isolated_env()
                .env("HOME", &home)
                .env("USERPROFILE", &home)
                .env("XDG_CONFIG_HOME", home.join("config"))
                .env("XDG_DATA_HOME", home.join("data"))
                .env("XDG_CACHE_HOME", home.join("cache"))
                .env("TZ", "UTC")
                .env("LANG", "C")
                .env("LC_ALL", "C")
                .env("PATH", &root)
                .envs(&environment)
                .args(args)
                .output()
                .unwrap();
            let label = format!("{} {:?}", case.name, step.args);
            assert_eq!(
                normalize(&String::from_utf8(output.stdout).unwrap(), &root),
                step.stdout,
                "{label} stdout"
            );
            assert_eq!(
                normalize(&String::from_utf8(output.stderr).unwrap(), &root),
                step.stderr,
                "{label} stderr"
            );
            assert_eq!(output.status.code(), Some(step.exit_code), "{label} exit");
            let mut files = BTreeMap::new();
            snapshot(&home, &root, &mut files);
            let mut expected = step.files;
            if cfg!(windows) {
                for file in expected.values_mut() {
                    file.mode = 0;
                }
            }
            assert_eq!(files, expected, "{label} filesystem");
            let requests = server
                .as_ref()
                .map(|server| std::mem::take(&mut *server.requests.lock().unwrap()))
                .unwrap_or_default();
            assert_eq!(requests, step.requests, "{label} HTTP requests");
            executed += 1;
        }
    }
    assert_eq!(executed, 141);
    fs::remove_dir_all(base).unwrap();
}

struct Server {
    url: String,
    requests: std::sync::Arc<std::sync::Mutex<Vec<String>>>,
    stop: std::sync::Arc<std::sync::atomic::AtomicBool>,
    thread: Option<std::thread::JoinHandle<()>>,
}
impl Server {
    fn start(body: String) -> Self {
        use std::{
            io::{Read, Write},
            net::TcpListener,
            sync::{
                Arc, Mutex,
                atomic::{AtomicBool, Ordering},
            },
            time::Duration,
        };
        let listener = TcpListener::bind("127.0.0.1:0").unwrap();
        let url = format!("http://{}", listener.local_addr().unwrap());
        listener.set_nonblocking(true).unwrap();
        let requests = Arc::new(Mutex::new(Vec::new()));
        let recorded = requests.clone();
        let stop = Arc::new(AtomicBool::new(false));
        let stopped = stop.clone();
        let thread = std::thread::spawn(move || {
            while !stopped.load(Ordering::Acquire) {
                match listener.accept() {
                    Ok((mut stream, _)) => {
                        // Darwin may inherit the listener's nonblocking mode.
                        stream.set_nonblocking(false).unwrap();
                        stream
                            .set_read_timeout(Some(Duration::from_secs(2)))
                            .unwrap();
                        stream
                            .set_write_timeout(Some(Duration::from_secs(2)))
                            .unwrap();
                        let mut bytes = Vec::new();
                        let mut byte = [0];
                        while !bytes.ends_with(b"\r\n\r\n") && bytes.len() < 16384 {
                            match stream.read(&mut byte) {
                                Ok(1) => bytes.push(byte[0]),
                                _ => break,
                            }
                        }
                        let request = String::from_utf8(bytes).unwrap();
                        if request.is_empty() {
                            continue;
                        }
                        let first = request.lines().next().unwrap_or_default();
                        let mut parts = first.split_whitespace();
                        recorded.lock().unwrap().push(format!(
                            "{} {}",
                            parts.next().unwrap_or_default(),
                            parts.next().unwrap_or_default()
                        ));
                        let response = format!(
                            "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\nContent-Length: {}\r\nConnection: close\r\n\r\n{body}",
                            body.len()
                        );
                        stream.write_all(response.as_bytes()).unwrap();
                    }
                    Err(e) if e.kind() == std::io::ErrorKind::WouldBlock => {
                        std::thread::sleep(Duration::from_millis(2))
                    }
                    Err(e) => panic!("listener: {e}"),
                }
            }
        });
        Self {
            url,
            requests,
            stop,
            thread: Some(thread),
        }
    }
}
impl Drop for Server {
    fn drop(&mut self) {
        self.stop.store(true, std::sync::atomic::Ordering::Release);
        if let Some(thread) = self.thread.take() {
            thread.join().unwrap();
        }
    }
}
