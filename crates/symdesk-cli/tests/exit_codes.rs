use std::process::Command;

#[test]
fn representative_exit_codes_remain_unchanged() {
    let success = Command::new(env!("CARGO_BIN_EXE_symdesk"))
        .arg("--version")
        .output()
        .expect("run symdesk --version");
    assert_eq!(success.status.code(), Some(0));

    let error = Command::new(env!("CARGO_BIN_EXE_symdesk"))
        .args(["--unknown-flag"])
        .output()
        .expect("run symdesk with an unknown flag");
    assert_eq!(error.status.code(), Some(1));
}
