//! Replays the Go-built `testdata/port/render/press-render.json` against the
//! Rust press pipeline: the strict frontmatter contract and meta.json bytes
//! (`meta` cases) and full renders through the installed Typst binary
//! (`render` cases: typed errors, resolved options, PDF digest or canonical
//! structure). Needs python3, qpdf and typst on PATH.

use std::{
    fs,
    path::{Path, PathBuf},
    process::Command,
    time::Duration,
};

use base64::Engine as _;
use regex::Regex;
use serde::Deserialize;
use serde_json::Value;
use sha2::{Digest, Sha256};
use symdesk_render::{
    press_front::{self, Issue},
    press_render::{self, PressError, Request},
};

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Fixture {
    schema_version: u32,
    oracle: Oracle,
    meta: Vec<MetaCase>,
    render: Vec<RenderCase>,
}

#[derive(Deserialize)]
#[serde(deny_unknown_fields)]
struct Oracle {
    typst_version: String,
    #[allow(dead_code)]
    commit: String,
    go_version: String,
    #[allow(dead_code)]
    canon_tool: String,
    #[allow(dead_code)]
    canon_tool_sha256: String,
    #[allow(dead_code)]
    normalisation: String,
}

#[derive(Deserialize, Clone)]
#[serde(deny_unknown_fields)]
struct MetaCase {
    id: String,
    source: String,
    profile: String,
    want: MetaWant,
}

#[derive(Deserialize, Clone, Default)]
#[serde(deny_unknown_fields)]
struct GoIssue {
    severity: String,
    field: String,
    message: String,
}

#[derive(Deserialize, Clone, Default)]
#[serde(deny_unknown_fields)]
struct GoContract {
    #[serde(default)]
    reason: String,
    #[serde(default)]
    detail: String,
    #[serde(default)]
    issues: Vec<GoIssue>,
}

#[derive(Deserialize, Clone)]
#[serde(deny_unknown_fields)]
struct MetaWant {
    parse_error: Option<GoContract>,
    #[serde(default)]
    syntax_error: bool,
    body: String,
    #[serde(default)]
    meta_json: String,
    #[serde(default)]
    resolved_profile: String,
    #[serde(default)]
    resolved_form: String,
    #[serde(default)]
    issues: Vec<GoIssue>,
}

#[derive(Deserialize, Clone)]
#[serde(deny_unknown_fields)]
struct FileSpec {
    path: String,
    #[serde(default)]
    data_b64: String,
    #[serde(default)]
    link_to: String,
    #[serde(default)]
    dir: bool,
}

#[derive(Deserialize, Clone, Default)]
#[serde(deny_unknown_fields)]
struct RenderReq {
    source: String,
    #[serde(default)]
    files: Vec<FileSpec>,
    #[serde(default)]
    no_source_dir: bool,
    #[serde(default)]
    profile_override: String,
    #[serde(default)]
    standard_override: Vec<String>,
    reproducible: Option<bool>,
    #[serde(default)]
    default_profile: String,
    #[serde(default)]
    short_diagnostics: bool,
    #[serde(default)]
    typst_bin: String,
    #[serde(default)]
    ignore_system_fonts: bool,
    #[serde(default)]
    timeout_ms: u64,
    #[serde(default)]
    preexisting_output: String,
    #[serde(default)]
    overwrite: bool,
}

#[derive(Deserialize, Clone)]
#[serde(deny_unknown_fields)]
struct GoRenderError {
    stage: String,
    message: String,
    #[serde(default)]
    detail: String,
    #[serde(default)]
    hint: String,
}

#[derive(Deserialize, Clone)]
#[serde(deny_unknown_fields)]
struct GoResult {
    profile: String,
    engine: String,
    engine_version: String,
    #[serde(default)]
    pdf_standard: Vec<String>,
    reproducible: bool,
}

#[derive(Deserialize, Clone)]
#[serde(deny_unknown_fields)]
struct RenderWant {
    ok: bool,
    #[serde(default)]
    error_kind: String,
    #[serde(default)]
    error_text: String,
    contract: Option<GoContract>,
    render_error: Option<GoRenderError>,
    result: Option<GoResult>,
    #[serde(default)]
    pdf_sha256: String,
    #[serde(default)]
    pdf_size: usize,
    canon: Option<Value>,
}

#[derive(Deserialize, Clone)]
#[serde(deny_unknown_fields)]
struct RenderCase {
    id: String,
    request: RenderReq,
    want: RenderWant,
}

fn root() -> PathBuf {
    Path::new(env!("CARGO_MANIFEST_DIR")).join("../..")
}

fn fixture() -> Fixture {
    let text = fs::read_to_string(root().join("testdata/port/render/press-render.json"))
        .expect("read press fixture");
    let f: Fixture = serde_json::from_str(&text).expect("decode press fixture");
    assert_eq!(f.schema_version, 1);
    assert_eq!(f.oracle.go_version, "go1.26.10");
    f
}

fn issues_equal(got: &[Issue], want: &[GoIssue]) -> bool {
    got.len() == want.len()
        && got
            .iter()
            .zip(want)
            .all(|(g, w)| g.severity == w.severity && g.field == w.field && g.message == w.message)
}

fn check_meta(c: &MetaCase) -> Result<(), String> {
    let parsed = press_front::parse(c.source.as_bytes());
    match (&c.want.parse_error, parsed) {
        (Some(want), Err(got)) => {
            if got.reason != want.reason {
                return Err(format!(
                    "{}: reason {:?}, want {:?}",
                    c.id, got.reason, want.reason
                ));
            }
            if !c.want.syntax_error && got.detail != want.detail {
                return Err(format!(
                    "{}: detail {:?}, want {:?}",
                    c.id, got.detail, want.detail
                ));
            }
            Ok(())
        }
        (Some(want), Ok(_)) => Err(format!(
            "{}: Go rejected ({}), Rust accepted",
            c.id, want.detail
        )),
        (None, Err(got)) => Err(format!("{}: Go accepted, Rust rejected: {}", c.id, got)),
        (None, Ok(doc)) => {
            let mut front = doc.front.clone();
            let body = String::from_utf8_lossy(&doc.body).into_owned();
            if body != c.want.body {
                return Err(format!("{}: body {:?}, want {:?}", c.id, body, c.want.body));
            }
            let mut resolved = String::new();
            let mut form = String::new();
            let mut issues = Vec::new();
            if let Some(prof) = symdesk_render::press::lookup(&c.profile) {
                if front.form.is_empty() {
                    front.form = prof.form.to_owned();
                }
                front.profile = prof.name.to_owned();
                resolved = prof.name.to_owned();
                form = front.form.clone();
                issues = press_front::Document {
                    front: front.clone(),
                    body: doc.body.clone(),
                }
                .validate(prof);
            }
            if resolved != c.want.resolved_profile || form != c.want.resolved_form {
                return Err(format!(
                    "{}: resolved {resolved:?}/{form:?}, want {:?}/{:?}",
                    c.id, c.want.resolved_profile, c.want.resolved_form
                ));
            }
            if !issues_equal(&issues, &c.want.issues) {
                return Err(format!(
                    "{}: issues {issues:?}, want {:?}",
                    c.id,
                    c.want
                        .issues
                        .iter()
                        .map(|i| (&i.field, &i.message))
                        .collect::<Vec<_>>()
                ));
            }
            let meta = match press_front::meta_json(&front) {
                Ok(m) => m,
                Err(e) => format!("ERROR: {e}"),
            };
            if meta != c.want.meta_json {
                return Err(format!(
                    "{}: meta.json differs\n--- rust\n{meta}\n--- go\n{}",
                    c.id, c.want.meta_json
                ));
            }
            Ok(())
        }
    }
}

fn work_path_re() -> Regex {
    Regex::new(r#"(?:/[^\s:"'()]*)?/symprint-[0-9]+"#).unwrap()
}

fn normalise_text(s: &str) -> String {
    work_path_re().replace_all(s, "{WORK}").into_owned()
}

fn mask_dates(s: &str) -> String {
    let a = Regex::new(r"D:\d{14}[Z+\-][0-9'Z]*").unwrap();
    let b =
        Regex::new(r"\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(?:\.\d+)?(?:Z|[+\-]\d{2}:\d{2})").unwrap();
    b.replace_all(&a.replace_all(s, "{DATE}"), "{DATE}")
        .into_owned()
}

struct TempDir(PathBuf);
impl TempDir {
    fn new() -> Self {
        let p = std::env::temp_dir().join(format!(
            "press-port-{}-{}",
            std::process::id(),
            std::time::SystemTime::now()
                .duration_since(std::time::UNIX_EPOCH)
                .unwrap()
                .as_nanos()
        ));
        fs::create_dir_all(&p).unwrap();
        Self(p)
    }
}
impl Drop for TempDir {
    fn drop(&mut self) {
        let _ = fs::remove_dir_all(&self.0);
    }
}

fn contract_equal(got: &press_front::ContractError, want: &GoContract) -> bool {
    got.reason == want.reason
        && got.detail == want.detail
        && issues_equal(&got.issues, &want.issues)
}

fn canon(pdf: &Path) -> Value {
    let tool = root().join("scripts/rust-port/pdfops/pdfcanon.py");
    let out = Command::new("python3")
        .arg("-I")
        .arg(tool)
        .arg(pdf)
        .output()
        .expect("run pdfcanon.py");
    assert!(
        out.status.success(),
        "pdfcanon: {}",
        String::from_utf8_lossy(&out.stderr)
    );
    let masked = mask_dates(&String::from_utf8(out.stdout).expect("utf-8 canon"));
    serde_json::from_str(&masked).expect("decode canon")
}

fn check_render(c: &RenderCase) -> Result<(), String> {
    let r = &c.request;
    let tmp = TempDir::new();
    let src_dir = tmp.0.join("src");
    let outside = tmp.0.join("outside");
    fs::create_dir_all(&src_dir).unwrap();
    fs::create_dir_all(&outside).unwrap();
    // The outside image is the PNG the generator writes; any valid file serves
    // because the symlink-escape case must never read it.
    fs::write(outside.join("secret.png"), b"outside").unwrap();
    for f in &r.files {
        let p = src_dir.join(&f.path);
        fs::create_dir_all(p.parent().unwrap()).unwrap();
        if f.dir {
            fs::create_dir_all(&p).unwrap();
        } else if !f.link_to.is_empty() {
            #[cfg(unix)]
            std::os::unix::fs::symlink(
                f.link_to.replace("{OUTSIDE}", &outside.to_string_lossy()),
                &p,
            )
            .unwrap();
        } else {
            fs::write(
                &p,
                base64::engine::general_purpose::STANDARD
                    .decode(&f.data_b64)
                    .unwrap(),
            )
            .unwrap();
        }
    }
    let out = tmp.0.join("out").join("result.pdf");
    if !r.preexisting_output.is_empty() {
        fs::create_dir_all(out.parent().unwrap()).unwrap();
        if r.preexisting_output == "dir" {
            fs::create_dir_all(&out).unwrap();
        } else {
            fs::write(&out, &r.preexisting_output).unwrap();
        }
    }
    let want = &c.want;
    if let Err(e) = press_render::check_overwrite(&out.to_string_lossy(), r.overwrite) {
        if want.error_kind != "overwrite" {
            return Err(format!("{}: unexpected overwrite refusal {e}", c.id));
        }
        let text = e.replace(&*out.to_string_lossy(), "{OUT}");
        return if text == want.error_text {
            Ok(())
        } else {
            Err(format!(
                "{}: overwrite text {text:?}, want {:?}",
                c.id, want.error_text
            ))
        };
    }
    if want.error_kind == "overwrite" {
        return Err(format!("{}: Go refused to overwrite, Rust did not", c.id));
    }
    let req = Request {
        source: r.source.clone().into_bytes(),
        source_name: "doc.md".into(),
        source_dir: if r.no_source_dir {
            String::new()
        } else {
            src_dir.to_string_lossy().into_owned()
        },
        output_path: out.to_string_lossy().into_owned(),
        profile_override: r.profile_override.clone(),
        standard_override: r.standard_override.clone(),
        reproducible: r.reproducible,
        default_profile: r.default_profile.clone(),
        short_diagnostics: r.short_diagnostics,
        engine: press_render::EngineConfig {
            typst_bin: r.typst_bin.clone(),
            font_paths: vec![],
            ignore_system_fonts: r.ignore_system_fonts,
            timeout: Some(if r.timeout_ms > 0 {
                Duration::from_millis(r.timeout_ms)
            } else {
                Duration::from_secs(60)
            }),
        },
    };
    symdesk_render::press::reset_probe_cache();
    let got = press_render::render(&req);
    match got {
        Err(err) => {
            if want.ok {
                return Err(format!("{}: Go succeeded, Rust failed: {err}", c.id));
            }
            let text = normalise_text(
                &err.to_string()
                    .replace(&*src_dir.to_string_lossy(), "{SRC}"),
            );
            // YAML syntax errors carry the yaml.v3 scanner wording and positions,
            // which the port does not reproduce: the reason must match, the rest
            // is not compared (declared limit, see press_yaml.rs).
            let syntax = matches!(&err, PressError::Contract(g) if g.reason == "invalid frontmatter" && !text.contains("cannot unmarshal") && !text.contains("not found in type") && !text.contains("already defined"));
            let text_ok = if syntax {
                text.split(':').next() == want.error_text.split(':').next()
            } else {
                text == want.error_text
            };
            if !text_ok {
                return Err(format!(
                    "{}: error text\n--- rust\n{text}\n--- go\n{}",
                    c.id, want.error_text
                ));
            }
            match (&err, want.error_kind.as_str()) {
                (PressError::Contract(g), "contract") => {
                    let w = want.contract.as_ref().ok_or("missing contract")?;
                    if !(syntax && g.reason == w.reason) && !contract_equal(g, w) {
                        return Err(format!(
                            "{}: contract {g:?}, want reason={:?} detail={:?}",
                            c.id, w.reason, w.detail
                        ));
                    }
                }
                (PressError::Render(g), "render") => {
                    let w = want.render_error.as_ref().ok_or("missing render_error")?;
                    let (m, d, h) = (
                        normalise_text(&g.message),
                        normalise_text(&g.detail),
                        normalise_text(&g.hint),
                    );
                    if g.stage != w.stage || m != w.message || d != w.detail || h != w.hint {
                        return Err(format!(
                            "{}: render error stage={:?} message={m:?} detail={d:?} hint={h:?}, want {:?}/{:?}/{:?}/{:?}",
                            c.id, g.stage, w.stage, w.message, w.detail, w.hint
                        ));
                    }
                }
                (e, kind) => return Err(format!("{}: error kind {e:?}, want {kind}", c.id)),
            }
            Ok(())
        }
        Ok(res) => {
            if !want.ok {
                return Err(format!(
                    "{}: Go failed ({}), Rust succeeded",
                    c.id, want.error_text
                ));
            }
            let w = want.result.as_ref().ok_or("missing result")?;
            if res.profile != w.profile
                || res.engine != w.engine
                || res.engine_version != w.engine_version
                || res.pdf_standard != w.pdf_standard
                || res.reproducible != w.reproducible
                || res.output_path != out.to_string_lossy()
            {
                return Err(format!(
                    "{}: result {res:?}, want {}/{}/{}/{:?}/{}",
                    c.id, w.profile, w.engine, w.engine_version, w.pdf_standard, w.reproducible
                ));
            }
            let data = fs::read(&out).map_err(|e| format!("{}: read output: {e}", c.id))?;
            if data.len() as u64 != res.bytes {
                return Err(format!(
                    "{}: bytes {} vs file {}",
                    c.id,
                    res.bytes,
                    data.len()
                ));
            }
            if res.reproducible {
                let sum = hex(&Sha256::digest(&data));
                if sum != want.pdf_sha256 || data.len() != want.pdf_size {
                    return Err(format!(
                        "{}: reproducible PDF differs from Go ({} bytes sha {sum}, want {} bytes sha {})",
                        c.id,
                        data.len(),
                        want.pdf_size,
                        want.pdf_sha256
                    ));
                }
            } else if &canon(&out) != want.canon.as_ref().ok_or("missing canon")? {
                return Err(format!("{}: canonical PDF differs from Go", c.id));
            }
            Ok(())
        }
    }
}

fn hex(b: &[u8]) -> String {
    b.iter().map(|x| format!("{x:02x}")).collect()
}

fn tools() {
    for (tool, arg) in [
        ("python3", "--version"),
        ("qpdf", "--version"),
        ("typst", "--version"),
    ] {
        let ok = Command::new(tool)
            .arg(arg)
            .output()
            .is_ok_and(|o| o.status.success());
        assert!(
            ok,
            "{tool} is required on PATH for the press render contract"
        );
    }
}

#[test]
fn meta_cases_match_go_press_contract() {
    let f = fixture();
    assert!(
        f.meta.len() >= 80,
        "fixture shrank: {} meta cases",
        f.meta.len()
    );
    let failures: Vec<String> = f.meta.iter().filter_map(|c| check_meta(c).err()).collect();
    assert!(
        failures.is_empty(),
        "{} of {} meta cases differ:\n{}",
        failures.len(),
        f.meta.len(),
        failures.join("\n")
    );
}

#[test]
fn render_cases_match_go_press_through_typst() {
    tools();
    let f = fixture();
    assert!(
        f.render.len() >= 60,
        "fixture shrank: {} render cases",
        f.render.len()
    );
    let version = Command::new("typst")
        .arg("--version")
        .output()
        .expect("typst");
    let version = String::from_utf8_lossy(&version.stdout)
        .split_whitespace()
        .nth(1)
        .unwrap_or_default()
        .to_owned();
    assert_eq!(
        version, f.oracle.typst_version,
        "the fixture was recorded with typst {}; pin the engine or regenerate",
        f.oracle.typst_version
    );
    let failures: Vec<String> = f
        .render
        .iter()
        .filter_map(|c| check_render(c).err())
        .collect();
    assert!(
        failures.is_empty(),
        "{} of {} render cases differ:\n{}",
        failures.len(),
        f.render.len(),
        failures.join("\n")
    );
}

/// Mutation controls: one changed character in a recorded expectation is a mismatch.
#[test]
fn mutated_expectations_are_rejected() {
    tools();
    let f = fixture();
    let meta = f
        .meta
        .iter()
        .find(|c| c.id == "all-fields-report")
        .expect("meta case")
        .clone();
    check_meta(&meta).expect("unmutated meta case passes");
    let mut bad = meta.clone();
    bad.want.meta_json = bad.want.meta_json.replacen("Titel", "Titeq", 1);
    assert!(check_meta(&bad).is_err());
    let rc = f
        .render
        .iter()
        .find(|c| c.id == "report-basic-repro")
        .expect("render case")
        .clone();
    check_render(&rc).expect("unmutated render case passes");
    let mut bad = rc.clone();
    bad.want.pdf_sha256 = bad
        .want
        .pdf_sha256
        .replacen(&bad.want.pdf_sha256[..1], "z", 1);
    assert!(check_render(&bad).is_err());
    let nr = f
        .render
        .iter()
        .find(|c| c.id == "report-basic-nonrepro")
        .expect("render case")
        .clone();
    check_render(&nr).expect("unmutated non-reproducible case passes");
    let mut bad = nr.clone();
    let text = bad
        .want
        .canon
        .as_ref()
        .unwrap()
        .to_string()
        .replacen("/Lang", "/Lanq", 1);
    bad.want.canon = Some(serde_json::from_str(&text).unwrap());
    assert!(check_render(&bad).is_err());
    let er = f
        .render
        .iter()
        .find(|c| c.id == "behoerde-image-no-alt-ua1")
        .expect("render case")
        .clone();
    check_render(&er).expect("unmutated gate case passes");
    let mut bad = er.clone();
    bad.want.error_text = bad
        .want
        .error_text
        .replacen("missing alt text", "missing alt tex", 1);
    assert!(check_render(&bad).is_err());
}
