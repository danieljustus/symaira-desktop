#[path = "../../test-support/process_env.rs"]
mod process_env;
#[path = "support/process_root.rs"]
mod process_root;
use process_env::IsolatedEnvironment;
use std::process::Command;

#[test]
fn isolated_environment_drops_parent_state_but_keeps_windows_system_root() {
    let mut command = Command::new("unused");
    command.env("MUST_NOT_LEAK", "sentinel").isolated_env();
    let variables: Vec<_> = command.get_envs().collect();
    assert!(!variables.iter().any(|(key, _)| *key == "MUST_NOT_LEAK"));
    // RUST-018: the only variable a hermetic process carries is the opt-out of
    // the automatic pre-Rust state backup (it would add files to fixture trees).
    assert!(variables.iter().any(|(key, value)| {
        *key == "SYMDESK_PRE_RUST_BACKUP" && *value == Some(std::ffi::OsStr::new("off"))
    }));
    #[cfg(windows)]
    {
        assert_eq!(variables.len(), 2);
        assert!(variables.iter().any(|(key, value)| {
            *key == "SYSTEMROOT" && *value == std::env::var_os("SYSTEMROOT").as_deref()
        }));
    }
    #[cfg(not(windows))]
    assert_eq!(variables.len(), 1);
}

// Keep shared-helper controls outside the frozen fixture replay binaries: an
// included module's #[test] is otherwise counted once for every consumer.
#[test]
fn root_markers_use_native_separators_without_cleaning_literal_arguments() {
    let directory = std::env::current_dir().unwrap();
    let canonical = process_root::canonical_root(&directory);
    assert_eq!(
        std::fs::canonicalize(&canonical).unwrap(),
        std::fs::canonicalize(&directory).unwrap()
    );
    let root = if cfg!(windows) {
        r"C:\temp\case"
    } else {
        "/private/case"
    };
    let expected =
        format!("{root}/vault/note conflicted copy.md").replace('/', std::path::MAIN_SEPARATOR_STR);
    assert_eq!(
        process_root::expand_argument("$ROOT/vault/note conflicted copy.md", root),
        expected
    );
    assert_eq!(
        process_root::expand_argument("../literal/path", root),
        "../literal/path"
    );
    assert_eq!(
        process_root::expand_argument("https://example.invalid/path", root),
        "https://example.invalid/path"
    );
}

#[path = "support/docs_output.rs"]
mod docs_output;

#[test]
fn docs_relative_path_fields_preserve_multiline_user_text_and_json_escapes() {
    let input = concat!(
        r"[{Path:nested\beta.md Title:user\text}]",
        "\n",
        r#"[{"path":"nested\\beta.md","title":"keep\\text\n"}]"#,
        "\n"
    );
    let expected = concat!(
        r"[{Path:nested/beta.md Title:user\text}]",
        "\n",
        r#"[{"path":"nested/beta.md","title":"keep\\text\n"}]"#,
        "\n"
    );
    assert_eq!(docs_output::normalize(input), expected);
    assert_eq!(docs_output::normalize(expected), expected);
}

#[path = "support/root_paths.rs"]
mod root_paths;

#[test]
fn root_path_normalizer_preserves_unrelated_escapes() {
    let text = r#"$ROOT\home\config\symdesk\config.toml "literal\n""#;
    let normalized = root_paths::slash_root_paths(text);
    assert_eq!(
        normalized,
        r#"$ROOT/home/config/symdesk/config.toml "literal\n""#
    );
}
