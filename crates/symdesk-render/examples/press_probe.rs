//! Test-only driver of the production profile and Typst discovery APIs.
use serde::Deserialize;
use serde_json::{Value, json};
use std::{
    fs, io, thread,
    time::{Duration, Instant},
};
use symdesk_render::{pdfops::Context, press};

#[derive(Deserialize)]
#[serde(default, deny_unknown_fields)]
#[derive(Default)]
struct Step {
    operation: String,
    name: String,
    path: String,
    source: String,
    cancel: bool,
    timeout_ms: u64,
    expired_deadline: bool,
    cancel_marker: String,
}
fn main() {
    let steps: Vec<Step> = serde_json::from_reader(io::stdin()).expect("steps");
    let mut results = Vec::new();
    for step in steps {
        results.push(match step.operation.as_str() {
            "profiles" => json!({
                "full":press::all(), "facade":press::profiles(),
                "capabilities":press::all().iter().map(|p| json!({
                    "name":p.name,"capability":p.capability(),"accessibility":p.requires_accessibility(),
                })).collect::<Vec<_>>(),
            }),
            "lookup" => json!(press::lookup(&step.name)),
            "detect" => {
                let mut ctx = Context::default();
                if step.timeout_ms > 0 { ctx.deadline = Some(Instant::now() + Duration::from_millis(step.timeout_ms)); }
                if step.cancel { ctx.cancel(); }
                if step.expired_deadline { ctx.deadline = Some(Instant::now()); }
                let monitor = if step.cancel_marker.is_empty() { None } else {
                    let ctx = ctx.clone();
                    Some(thread::spawn(move || {
                        let deadline = Instant::now() + Duration::from_secs(10);
                        while Instant::now() < deadline {
                            if std::path::Path::new(&step.cancel_marker).exists() { ctx.cancel(); return; }
                            thread::sleep(Duration::from_millis(1));
                        }
                        panic!("tool did not signal readiness");
                    }))
                };
                let result = press::detect_typst(&ctx, &step.name);
                if let Some(monitor) = monitor { monitor.join().expect("cancel monitor"); }
                json!(result)
            }
            "reset" => { press::reset_probe_cache(); Value::Null }
            "remove" => { fs::remove_file(&step.path).expect("remove isolated tool"); Value::Null }
            "install" => {
                fs::copy(&step.source, &step.path).expect("install isolated tool");
                #[cfg(unix)] {
                    use std::os::unix::fs::PermissionsExt;
                    fs::set_permissions(&step.path, fs::Permissions::from_mode(0o700)).unwrap();
                }
                Value::Null
            }
            _ => panic!("unknown operation"),
        });
    }
    println!("{}", json!(results));
}
