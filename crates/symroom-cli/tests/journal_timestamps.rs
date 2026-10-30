#![deny(unsafe_code)]
#[path = "../../test-support/process_env.rs"]
mod process_env;
use process_env::IsolatedEnvironment;

use serde_json::Value;
use std::{fs, path::Path, process::Command};

#[test]
fn unstamped_cli_event_kinds_keep_empty_signed_timestamps() {
    let fixture: Value = serde_json::from_slice(
        &fs::read(
            Path::new(env!("CARGO_MANIFEST_DIR"))
                .join("../../testdata/port/room/run-approval-cli.json"),
        )
        .expect("fixture"),
    )
    .expect("parse");
    let root = std::env::temp_dir().join(format!("room-ts-{}", std::process::id()));
    fs::create_dir(&root).expect("fresh root");
    // Artifact paths are checked against the CLI's canonical room root.
    // Preserve Windows' non-verbatim spelling.
    #[cfg(unix)]
    let root = fs::canonicalize(root).expect("canonical test root");
    struct Cleanup(std::path::PathBuf);
    impl Drop for Cleanup {
        fn drop(&mut self) {
            let _ = fs::remove_dir_all(&self.0);
        }
    }
    let _cleanup = Cleanup(root.clone());
    for (index, (kind, args, code)) in [
        ("run.started", vec!["run", "start", "approval-approved"], 0),
        (
            "run.cancelled",
            vec!["run", "cancel", "approval-pending"],
            0,
        ),
        (
            "run.approved",
            vec!["run", "approve", "approval-pending"],
            0,
        ),
        (
            "run.denied",
            vec!["run", "deny", "--reason=no", "approval-pending"],
            0,
        ),
        ("artifact.linked", vec!["artifact", "link", "f.txt"], 0),
        (
            "checkpoint.requested",
            vec![
                "checkpoint",
                "request",
                "--run=approval-pending",
                "--question=Proceed?",
                "--timeout=1ms",
            ],
            10,
        ),
    ]
    .into_iter()
    .enumerate()
    {
        let room = root.join(index.to_string());
        fs::create_dir_all(room.join("journal")).expect("journal");
        fs::create_dir(room.join("home")).expect("home");
        fs::write(
            room.join("room.toml"),
            format!(
                "id = \"rm_run_approval_fixture\"\nroot_event = \"{}\"\nroot_pubkey = \"{}\"\n",
                fixture["root_event"].as_str().unwrap(),
                fixture["root_pubkey"].as_str().unwrap()
            ),
        )
        .expect("config");
        // Use the recorded room ID rather than duplicating its spelling.
        let first: Value = serde_json::from_str(
            fixture["initial_journal"][0]["content"]
                .as_str()
                .unwrap()
                .split('\n')
                .next()
                .unwrap(),
        )
        .unwrap();
        let config = fs::read_to_string(room.join("room.toml"))
            .unwrap()
            .replace("rm_run_approval_fixture", first["room"].as_str().unwrap());
        fs::write(room.join("room.toml"), config).unwrap();
        for file in fixture["initial_journal"].as_array().unwrap() {
            fs::write(
                room.join("journal").join(file["name"].as_str().unwrap()),
                file["content"].as_str().unwrap(),
            )
            .unwrap();
        }
        fs::write(room.join("f.txt"), "artifact").unwrap();
        let run = |args: &[&str]| {
            Command::new(env!("CARGO_BIN_EXE_symroom"))
                .args(args)
                .current_dir(&room)
                .isolated_env()
                .env("HOME", room.join("home"))
                .env("USERPROFILE", room.join("home"))
                .env("XDG_DATA_HOME", room.join("home/data"))
                .env("TMPDIR", &room)
                .env("PATH", &room)
                .env("SYMROOM_DEFAULT_IDENTITY", "owner")
                .env("SYMROOM_ROOM_DIR", &room)
                .env(
                    "SYMROOM_IDENTITY_KEY",
                    fixture["identity_keys"]["owner"].as_str().unwrap(),
                )
                .output()
                .unwrap()
        };
        let output = run(&args);
        assert_eq!(
            output.status.code(),
            Some(code),
            "{kind}: {}",
            String::from_utf8_lossy(&output.stderr)
        );
        let mut found = Vec::new();
        for entry in fs::read_dir(room.join("journal")).unwrap() {
            for line in fs::read_to_string(entry.unwrap().path())
                .unwrap()
                .split('\n')
                .filter(|s| !s.is_empty())
            {
                let event: Value = serde_json::from_str(line).unwrap();
                if event["kind"] == kind && event["ts"] == "" {
                    found.push(event);
                }
            }
        }
        assert_eq!(found.len(), 1, "new {kind} must carry literal empty ts");
        let event = symroom_core::event::Event::unmarshal_json_line(
            serde_json::to_string(&found[0]).unwrap().as_bytes(),
        )
        .unwrap();
        let identity = symroom_core::identity::identity_from_private_key(
            "owner",
            &hex::decode(fixture["identity_keys"]["owner"].as_str().unwrap()).unwrap(),
        )
        .unwrap();
        event
            .verify_signature(&identity.public_key)
            .expect("empty TS is signed");
        if kind == "artifact.linked" {
            let id = found[0]["body"]["artifact_id"].as_str().unwrap();
            assert!(run(&["artifact", "unlink", id]).status.success());
            let content = fs::read_to_string(
                room.join("journal")
                    .join(format!("{}.jsonl", identity.member_id)),
            )
            .unwrap();
            let last: Value =
                serde_json::from_str(content.trim_end().rsplit('\n').next().unwrap()).unwrap();
            assert_eq!(last["kind"], "artifact.unlinked");
            assert_eq!(last["ts"], "");
        }
        if kind == "checkpoint.requested" {
            let id = found[0]["body"]["checkpoint_id"].as_str().unwrap();
            assert!(
                run(&["checkpoint", "resolve", "--answer=yes", id])
                    .status
                    .success()
            );
            let content = fs::read_to_string(
                room.join("journal")
                    .join(format!("{}.jsonl", identity.member_id)),
            )
            .unwrap();
            let last: Value =
                serde_json::from_str(content.trim_end().rsplit('\n').next().unwrap()).unwrap();
            assert_eq!(last["kind"], "checkpoint.resolved");
            assert_eq!(last["ts"], "");
        }
    }
}
