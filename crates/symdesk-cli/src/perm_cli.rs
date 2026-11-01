//! `symdesk perm user|group ...` (Go `cmd/symdesk/permissions.go`): named
//! user accounts and groups stored below `<vault>/.symdesk`. The persistence
//! layer is the Go-pinned `symdesk_vault::permissions::Manager`.
use std::process::ExitCode;

use clap::{Arg, ArgAction, ArgMatches, Command};
use serde::Serialize;
use symdesk_vault::{
    go_quote,
    permissions::{Group, Manager, User},
};

use crate::{emit_error, resolve_vault_root, write_go_json, write_stdout};

pub fn cli() -> Command {
    let arguments = || Arg::new("args").num_args(0..).hide(true);
    Command::new("perm")
        .subcommand(
            Command::new("user")
                .subcommand(
                    Command::new("add").arg(arguments()).arg(
                        Arg::new("group")
                            .long("group")
                            .num_args(1)
                            .overrides_with("group"),
                    ),
                )
                .subcommand(Command::new("list").arg(arguments()))
                .subcommand(Command::new("remove").arg(arguments()))
                .subcommand(
                    Command::new("token").arg(arguments()).arg(
                        Arg::new("generate")
                            .long("generate")
                            .action(ArgAction::SetTrue),
                    ),
                ),
        )
        .subcommand(
            Command::new("group")
                .subcommand(Command::new("add").arg(arguments()))
                .subcommand(Command::new("list").arg(arguments()))
                .subcommand(Command::new("remove").arg(arguments())),
        )
}

#[derive(Serialize)]
struct UserJson<'a> {
    name: &'a str,
    token_hash: &'a str,
    roles: Option<&'a [String]>,
}

#[derive(Serialize)]
struct GroupJson<'a> {
    name: &'a str,
    members: Option<&'a [String]>,
}

#[derive(Serialize)]
struct Status<'a> {
    name: &'a str,
    status: &'a str,
    #[serde(skip_serializing_if = "Option::is_none")]
    token: Option<&'a str>,
}

/// Go `%+v` of a `[]string`.
fn go_strings(values: Option<&[String]>) -> String {
    format!("[{}]", values.unwrap_or_default().join(" "))
}

fn user_text(user: &User) -> String {
    format!(
        "{{Name:{} TokenHash:{} Roles:{}}}",
        user.name,
        user.token_hash,
        go_strings(user.roles.as_deref())
    )
}

fn group_text(group: &Group) -> String {
    format!(
        "{{Name:{} Members:{}}}",
        group.name,
        go_strings(group.members.as_deref())
    )
}

/// Go `openPermissions`: `vault.ResolveVaultRoot` and `<root>/.symdesk`.
fn open(vault: Option<&str>) -> Result<Manager, String> {
    let root = resolve_vault_root(vault)?;
    Manager::new(root.join(".symdesk"))
}

fn arguments(command: &ArgMatches) -> Vec<String> {
    command
        .get_many::<String>("args")
        .map(|values| values.cloned().collect())
        .unwrap_or_default()
}

fn status(
    output_json: bool,
    status: &str,
    name: &str,
    token: Option<&str>,
    text: String,
) -> ExitCode {
    if output_json {
        write_go_json(&Status {
            name,
            status,
            token,
        })
    } else {
        write_stdout(text)
    }
}

pub fn run(command: &ArgMatches, vault: Option<&str>, output_json: bool) -> ExitCode {
    let result = match command.subcommand() {
        Some(("user", user)) => run_user(user, vault, output_json),
        Some(("group", group)) => run_group(group, vault, output_json),
        _ => return crate::process_exit(crate::CoreExitCode::Ok),
    };
    result.unwrap_or_else(|error| emit_error(error, output_json))
}

fn run_user(
    command: &ArgMatches,
    vault: Option<&str>,
    output_json: bool,
) -> Result<ExitCode, String> {
    Ok(match command.subcommand() {
        Some(("add", add)) => {
            let name = arguments(add).remove(0);
            let permissions = open(vault)?;
            let token = permissions.user_add(&name, &["user".to_owned()])?;
            if token.is_empty() {
                return Ok(write_stdout(format!(
                    "User {} already exists.\n",
                    go_quote(&name)
                )));
            }
            let group = add
                .get_one::<String>("group")
                .filter(|group| !group.is_empty());
            if let Some(group) = group {
                permissions
                    .group_add_member(group, &name)
                    .map_err(|error| {
                        format!(
                            "user created but failed to add to group {}: {error}",
                            go_quote(group)
                        )
                    })?;
            }
            let mut text = format!("User {} created.\nToken: {token}\n", go_quote(&name));
            if let Some(group) = group {
                text.push_str(&format!("Added to group {}.\n", go_quote(group)));
            }
            status(output_json, "created", &name, Some(&token), text)
        }
        Some(("list", _)) => {
            let users = open(vault)?.user_list()?;
            if output_json {
                match users {
                    None => write_stdout("null\n".to_owned()),
                    Some(users) => write_go_json(
                        &users
                            .iter()
                            .map(|user| UserJson {
                                name: &user.name,
                                token_hash: &user.token_hash,
                                roles: user.roles.as_deref(),
                            })
                            .collect::<Vec<_>>(),
                    ),
                }
            } else {
                write_stdout(format!(
                    "[{}]\n",
                    users
                        .unwrap_or_default()
                        .iter()
                        .map(user_text)
                        .collect::<Vec<_>>()
                        .join(" ")
                ))
            }
        }
        Some(("remove", remove)) => {
            let name = arguments(remove).remove(0);
            open(vault)?.user_remove(&name)?;
            status(
                output_json,
                "removed",
                &name,
                None,
                format!("User {} removed.\n", go_quote(&name)),
            )
        }
        Some(("token", token)) => {
            let name = arguments(token).remove(0);
            if !token.get_flag("generate") {
                return Err("use --generate to confirm token rotation".to_owned());
            }
            let rotated = open(vault)?.user_generate_token(&name)?;
            status(
                output_json,
                "rotated",
                &name,
                Some(&rotated),
                format!("New token for {}: {rotated}\n", go_quote(&name)),
            )
        }
        _ => crate::process_exit(crate::CoreExitCode::Ok),
    })
}

fn run_group(
    command: &ArgMatches,
    vault: Option<&str>,
    output_json: bool,
) -> Result<ExitCode, String> {
    Ok(match command.subcommand() {
        Some(("add", add)) => {
            let name = arguments(add).remove(0);
            open(vault)?.group_add(&name)?;
            status(
                output_json,
                "created",
                &name,
                None,
                format!("Group {} created.\n", go_quote(&name)),
            )
        }
        Some(("list", _)) => {
            let groups = open(vault)?.group_list()?;
            if output_json {
                match groups {
                    None => write_stdout("null\n".to_owned()),
                    Some(groups) => write_go_json(
                        &groups
                            .iter()
                            .map(|group| GroupJson {
                                name: &group.name,
                                members: group.members.as_deref(),
                            })
                            .collect::<Vec<_>>(),
                    ),
                }
            } else {
                write_stdout(format!(
                    "[{}]\n",
                    groups
                        .unwrap_or_default()
                        .iter()
                        .map(group_text)
                        .collect::<Vec<_>>()
                        .join(" ")
                ))
            }
        }
        Some(("remove", remove)) => {
            let name = arguments(remove).remove(0);
            open(vault)?.group_remove(&name)?;
            status(
                output_json,
                "removed",
                &name,
                None,
                format!("Group {} removed.\n", go_quote(&name)),
            )
        }
        _ => crate::process_exit(crate::CoreExitCode::Ok),
    })
}
