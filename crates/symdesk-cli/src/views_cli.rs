//! Saved-view manager reads. Storage/parsing stays in symdesk-vault.
use std::process::ExitCode;

use clap::{Arg, ArgMatches, Command};
use serde_json::Value;
use symdesk_vault::{Base, View, get_base, list_bases, migrate_legacy_views};

pub fn cli() -> Command {
    Command::new("views")
        .subcommand(read_command("list"))
        .subcommand(read_command("get"))
        .subcommand(read_command("siblings"))
        .subcommand(
            Command::new("base")
                .subcommand(read_command("list"))
                .subcommand(read_command("get")),
        )
}

fn read_command(name: &'static str) -> Command {
    Command::new(name).arg(Arg::new("args").num_args(0..))
}

pub fn run(command: &ArgMatches, vault: Option<&str>, json: bool) -> ExitCode {
    let Some((mut name, mut args)) = command.subcommand() else {
        return crate::process_exit(crate::CoreExitCode::Ok);
    };
    let base = name == "base";
    if base {
        let Some((child, matches)) = args.subcommand() else {
            return crate::process_exit(crate::CoreExitCode::Ok);
        };
        name = child;
        args = matches;
    }
    let refs: Vec<_> = args
        .get_many::<String>("args")
        .map(|v| v.cloned().collect())
        .unwrap_or_default();
    if name != "list" && refs.len() != 1 {
        return crate::emit_error(format!("accepts 1 arg(s), received {}", refs.len()), json);
    }
    let root = match crate::resolve_vault(vault) {
        Ok(root) => root,
        Err(error) => {
            return crate::emit_error(
                if error.starts_with("vault path not configured") {
                    "vault path not configured (use flag or SYMDESK_VAULT env)".to_owned()
                } else {
                    crate::index_cli::index_vault_error(vault, &error)
                },
                json,
            );
        }
    };
    let mut db = match symdesk_index::open_for_vault(&root) {
        Ok(db) => db,
        Err(error) => return crate::emit_error(error.to_string(), json),
    };
    migrate_legacy_views(&root);
    let result = (|| -> Result<ExitCode, String> {
        if base {
            if name == "list" {
                let bases = list_bases(&root).map_err(|e| e.to_string())?;
                return Ok(if json {
                    crate::write_go_json(&bases)
                } else {
                    crate::write_stdout(format!(
                        "[{}]\n",
                        bases
                            .iter()
                            .map(|b| format!("{b:p}"))
                            .collect::<Vec<_>>()
                            .join(" ")
                    ))
                });
            }
            if refs[0].trim().is_empty() {
                return Err("base reference is required".to_owned());
            }
            let b = get_base(&root, &refs[0]).map_err(|e| e.to_string())?;
            return Ok(if json {
                crate::write_go_json(&b)
            } else {
                crate::write_stdout(format!("&{}\n", base_text(&b)))
            });
        }
        let views: Vec<View> = list_bases(&root)
            .map_err(|e| e.to_string())?
            .into_iter()
            .flat_map(|b| b.views)
            .collect();
        if name == "list" {
            return Ok(render_views(&views, json));
        }
        let view = views
            .iter()
            .find(|v| v.id == refs[0])
            .ok_or("view not found")?;
        if name == "siblings" {
            let siblings = views
                .iter()
                .filter(|v| {
                    if view.source.is_empty() {
                        v.id == view.id
                    } else {
                        v.source == view.source
                    }
                })
                .cloned()
                .collect::<Vec<_>>();
            return Ok(render_views(&siblings, json));
        }
        Ok(if json {
            crate::write_go_json(view)
        } else {
            crate::write_stdout(format!("&{}\n", view_text(view)))
        })
    })();
    if let Err(error) = db.close() {
        eprintln!("warning: failed to close sidecar database: {error}");
    }
    match result {
        Ok(exit) => exit,
        Err(error) => crate::emit_error(error, json),
    }
}

fn render_views(views: &[View], json: bool) -> ExitCode {
    if json {
        crate::write_go_json(&views)
    } else {
        crate::write_stdout(format!(
            "[{}]\n",
            views.iter().map(view_text).collect::<Vec<_>>().join(" ")
        ))
    }
}

fn seq<T>(values: &[T], render: impl Fn(&T) -> String) -> String {
    format!(
        "[{}]",
        values.iter().map(render).collect::<Vec<_>>().join(" ")
    )
}

fn view_text(v: &View) -> String {
    let filters = seq(&v.filters, |f| {
        format!(
            "{{Key:{} Operator:{} Value:{}}}",
            f.key, f.operator, f.value
        )
    });
    let sorts = seq(&v.sorts, |s| {
        format!("{{Key:{} Ascending:{}}}", s.key, s.ascending)
    });
    let computed = format!(
        "map[{}]",
        v.computed
            .iter()
            .map(|(k, c)| format!("{k}:{{Formula:{} Rollup:{}}}", c.formula, c.rollup))
            .collect::<Vec<_>>()
            .join(" ")
    );
    let group = v
        .filter_group
        .as_ref()
        .map_or_else(|| "<nil>".to_owned(), |g| format!("{g:p}"));
    let template = v
        .template
        .as_ref()
        .map_or_else(|| "<nil>".to_owned(), |t| format!("{t:p}"));
    format!(
        "{{ID:{} Name:{} Type:{} GroupBy:{} DateProperty:{} Computed:{} Filters:{} FilterGroup:{} Sorts:{} Columns:{} Source:{} Template:{}}}",
        v.id,
        v.name,
        v.r#type,
        v.group_by,
        v.date_property,
        computed,
        filters,
        group,
        sorts,
        seq(&v.columns, Clone::clone),
        v.source,
        template
    )
}

fn base_text(b: &Base) -> String {
    let properties = format!(
        "map[{}]",
        b.properties
            .iter()
            .map(|(key, p)| format!(
                "{key}:{{Type:{} Label:{} Options:{} Description:{} Default:{}}}",
                p.r#type,
                p.label,
                seq(&p.options, Clone::clone),
                p.description,
                p.default
            ))
            .collect::<Vec<_>>()
            .join(" ")
    );
    let extras = serde_json::to_value(&b.extras).unwrap_or(Value::Null);
    format!(
        "{{ID:{} Path:{} Title:{} Description:{} Created:{} Tags:{} Properties:{} Views:{} Extras:{}}}",
        b.id,
        b.path,
        b.title,
        b.description,
        b.created,
        seq(&b.tags, Clone::clone),
        properties,
        seq(&b.views, view_text),
        go_value(&extras)
    )
}

fn go_value(v: &Value) -> String {
    match v {
        Value::Null => "<nil>".to_owned(),
        Value::String(v) => v.clone(),
        Value::Array(v) => seq(v, go_value),
        Value::Object(v) => format!(
            "map[{}]",
            v.iter()
                .map(|(k, v)| format!("{k}:{}", go_value(v)))
                .collect::<Vec<_>>()
                .join(" ")
        ),
        other => other.to_string(),
    }
}
