#[path = "../../test-support/process_env.rs"]
mod process_env;
use process_env::IsolatedEnvironment;
use std::process::Command;

#[test]
fn isolated_environment_drops_parent_state_but_keeps_windows_system_root() {
    let mut command = Command::new("unused");
    command.env("MUST_NOT_LEAK", "sentinel").isolated_env();
    let variables: Vec<_> = command.get_envs().collect();
    assert!(!variables.iter().any(|(key, _)| *key == "MUST_NOT_LEAK"));
    #[cfg(windows)]
    {
        assert_eq!(variables.len(), 1);
        assert_eq!(variables[0].0, "SYSTEMROOT");
        assert_eq!(variables[0].1, std::env::var_os("SYSTEMROOT").as_deref());
    }
    #[cfg(not(windows))]
    assert!(variables.is_empty());
}
