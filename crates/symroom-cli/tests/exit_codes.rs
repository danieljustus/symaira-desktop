use std::process::Command;

#[test]
fn representative_exit_codes_remain_unchanged() {
    let success = Command::new(env!("CARGO_BIN_EXE_symroom"))
        .args(["version"])
        .output()
        .expect("run symroom version");
    assert_eq!(success.status.code(), Some(0));

    let error = Command::new(env!("CARGO_BIN_EXE_symroom"))
        .output()
        .expect("run symroom without a subcommand");
    assert_eq!(error.status.code(), Some(2));
}
