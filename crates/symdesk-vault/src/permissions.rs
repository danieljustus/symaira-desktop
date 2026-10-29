//! User, group and document-rule persistence. Port of `internal/permissions`.
//!
//! The Go-generated `testdata/port/vault/permissions.json` (written by
//! `scripts/rust-port/cmd/permissionsgen`) pins every observable behavior:
//! the `users.json` / `groups.json` / `permissions.json` byte layout
//! (`json.MarshalIndent`, HTML escaping, `null` for nil slices), file and
//! directory modes, Go `encoding/json` decoding (case-folded and duplicate
//! keys, backing-array reuse of repeated list keys, first-type-error wording)
//! and the Go quirks around fail-open reads.
//!
//! Faithfulness notes:
//! * `can_read`/`can_write` ignore ACL load errors exactly like Go, so a
//!   malformed `permissions.json` is *public* through them while
//!   `can_read_many` fails closed. The self-hosted HTTP server deliberately
//!   fails closed instead (`crates/symdesk-protocol`); this module is not yet
//!   wired into it.
//! * Go caches the parsed files by mtime. The cache is transparent apart from
//!   same-mtime external rewrites, so it is not ported.

use std::fs::{self, OpenOptions};
use std::io::Write as _;
use std::path::{Path, PathBuf};
use std::sync::Mutex;

use crate::go_string::quote as go_quote;
use crate::retention::json_decoder::validate_syntax;
use crate::{go_path_error, go_read, sha256};

const ROLE_ADMIN: &str = "admin";

/// Go: `permissions.User`.
#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub struct User {
    pub name: String,
    pub token_hash: String,
    /// `None` is a nil slice (`null` on the wire).
    pub roles: Option<Vec<String>>,
}

impl User {
    #[must_use]
    pub fn has_role(&self, role: &str) -> bool {
        self.roles
            .as_ref()
            .is_some_and(|roles| roles.iter().any(|r| r == role))
    }
}

/// Go: `permissions.Group`.
#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub struct Group {
    pub name: String,
    /// `None` is a nil slice (`null` on the wire).
    pub members: Option<Vec<String>>,
}

/// Go: `permissions.DocumentRule`. The list fields are `omitempty`, so nil and
/// empty are indistinguishable on the wire and here.
#[derive(Debug, Clone, PartialEq, Eq, Default)]
pub struct DocumentRule {
    pub path: String,
    pub owner: String,
    pub read_users: Vec<String>,
    pub read_groups: Vec<String>,
    pub write_users: Vec<String>,
    pub write_groups: Vec<String>,
}

impl DocumentRule {
    fn is_blank(&self) -> bool {
        self.owner.is_empty()
            && self.read_users.is_empty()
            && self.read_groups.is_empty()
            && self.write_users.is_empty()
            && self.write_groups.is_empty()
    }
}

/// Go: `permissions.Manager`. Errors carry Go's exact message text.
#[derive(Debug)]
pub struct Manager {
    config_dir: PathBuf,
    lock: Mutex<()>,
}

type Failure = String;

impl Manager {
    /// Go: `NewManager`. Creates `config_dir` (and missing ancestors) with
    /// mode 0700, like `os.MkdirAll`.
    pub fn new(config_dir: impl Into<PathBuf>) -> Result<Self, Failure> {
        let config_dir = config_dir.into();
        go_mkdir_all(&config_dir)
            .map_err(|error| format!("permissions: create config dir: {error}"))?;
        Ok(Self {
            config_dir,
            lock: Mutex::new(()),
        })
    }

    // --- authentication --------------------------------------------------

    /// Go: `Authenticate`.
    pub fn authenticate(&self, token: &str) -> Result<User, Failure> {
        if token.is_empty() {
            return Err("authentication required".to_owned());
        }
        let hash = hash_token(token);
        let users = self.load_users()?;
        users
            .unwrap_or_default()
            .into_iter()
            .find(|user| constant_time_equal(&user.token_hash, &hash))
            .ok_or_else(|| "invalid token".to_owned())
    }

    // --- document access -------------------------------------------------

    /// Go: `CanRead`. A rule/group load error is ignored (fail-open).
    #[must_use]
    pub fn can_read(&self, user: Option<&User>, path: &str) -> bool {
        self.can_access(user, path, false)
    }

    /// Go: `CanWrite`. A rule/group load error is ignored (fail-open).
    #[must_use]
    pub fn can_write(&self, user: Option<&User>, path: &str) -> bool {
        self.can_access(user, path, true)
    }

    fn can_access(&self, user: Option<&User>, path: &str, write: bool) -> bool {
        let Some(user) = user else {
            return false;
        };
        if user.has_role(ROLE_ADMIN) {
            return true;
        }
        let rules = self.load_permissions().ok().flatten().unwrap_or_default();
        let Some(rule) = find_rule(&rules, path) else {
            return true;
        };
        let (users, groups) = if write {
            (&rule.write_users, &rule.write_groups)
        } else {
            (&rule.read_users, &rule.read_groups)
        };
        if rule.owner == user.name || users.contains(&user.name) {
            return true;
        }
        let all_groups = self.load_groups().ok().flatten().unwrap_or_default();
        groups
            .iter()
            .any(|group| group_contains(&all_groups, group, &user.name))
    }

    /// Go: `CanReadMany`. `None` is a nil slice (nil user or unreadable
    /// `permissions.json`); an unreadable `groups.json` only drops group
    /// grants.
    #[must_use]
    pub fn can_read_many(&self, user: Option<&User>, paths: &[String]) -> Option<Vec<String>> {
        let user = user?;
        if user.has_role(ROLE_ADMIN) {
            return Some(paths.to_vec());
        }
        let rules = self.load_permissions().ok()?.unwrap_or_default();
        let groups = self.load_groups().ok().flatten().unwrap_or_default();
        let mut result = Vec::with_capacity(paths.len());
        for path in paths {
            let allowed = match find_rule(&rules, path) {
                None => true,
                Some(rule) => {
                    rule.owner == user.name
                        || rule.read_users.contains(&user.name)
                        || rule
                            .read_groups
                            .iter()
                            .any(|group| group_contains(&groups, group, &user.name))
                }
            };
            if allowed {
                result.push(path.clone());
            }
        }
        Some(result)
    }

    // --- user management -------------------------------------------------

    /// Go: `UserAdd`. Returns the plain-text token, or an empty string when
    /// the user already exists.
    pub fn user_add(&self, name: &str, roles: &[String]) -> Result<String, Failure> {
        if name.is_empty() {
            return Err("user name is required".to_owned());
        }
        let _guard = self.guard();
        let mut users = self.load_users()?;
        if users
            .as_ref()
            .is_some_and(|users| users.iter().any(|user| user.name == name))
        {
            return Ok(String::new());
        }
        let roles = if roles.is_empty() {
            vec!["user".to_owned()]
        } else {
            roles.to_vec()
        };
        let token = generate_token()?;
        users.get_or_insert_with(Vec::new).push(User {
            name: name.to_owned(),
            token_hash: hash_token(&token),
            roles: Some(roles),
        });
        self.save_users(&users)?;
        Ok(token)
    }

    /// Go: `UserList`.
    pub fn user_list(&self) -> Result<Option<Vec<User>>, Failure> {
        self.load_users()
    }

    /// Go: `UserRemove`. No-op (and no write) when the user does not exist;
    /// otherwise the user is also removed from every group.
    pub fn user_remove(&self, name: &str) -> Result<(), Failure> {
        let _guard = self.guard();
        let Some(mut users) = self.load_users()? else {
            return Ok(());
        };
        let before = users.len();
        users.retain(|user| user.name != name);
        if users.len() == before {
            return Ok(());
        }
        self.save_users(&Some(users))?;
        let Some(mut groups) = self.load_groups()? else {
            return Ok(());
        };
        let mut dirty = false;
        for group in &mut groups {
            if let Some(members) = &mut group.members {
                let len = members.len();
                members.retain(|member| member != name);
                dirty |= members.len() != len;
            }
        }
        if dirty {
            self.save_groups(&Some(groups))?;
        }
        Ok(())
    }

    /// Go: `UserGenerateToken`. Replaces the stored hash.
    pub fn user_generate_token(&self, name: &str) -> Result<String, Failure> {
        let _guard = self.guard();
        let mut users = self.load_users()?;
        if let Some(user) = users
            .as_mut()
            .and_then(|users| users.iter_mut().find(|user| user.name == name))
        {
            let token = generate_token()?;
            user.token_hash = hash_token(&token);
            self.save_users(&users)?;
            return Ok(token);
        }
        Err(format!("user {} not found", go_quote(name)))
    }

    /// Go: `SetTokenHash`.
    pub fn set_token_hash(&self, name: &str, hash: &str) -> Result<(), Failure> {
        let _guard = self.guard();
        let mut users = self.load_users()?;
        if let Some(user) = users
            .as_mut()
            .and_then(|users| users.iter_mut().find(|user| user.name == name))
        {
            hash.clone_into(&mut user.token_hash);
            return self.save_users(&users);
        }
        Err(format!("user {} not found", go_quote(name)))
    }

    // --- group management ------------------------------------------------

    /// Go: `GroupAdd`. A new group has nil members (`"members": null`).
    pub fn group_add(&self, name: &str) -> Result<(), Failure> {
        if name.is_empty() {
            return Err("group name is required".to_owned());
        }
        let _guard = self.guard();
        let mut groups = self.load_groups()?;
        if groups
            .as_ref()
            .is_some_and(|groups| groups.iter().any(|group| group.name == name))
        {
            return Ok(());
        }
        groups.get_or_insert_with(Vec::new).push(Group {
            name: name.to_owned(),
            members: None,
        });
        self.save_groups(&groups)
    }

    /// Go: `GroupList`.
    pub fn group_list(&self) -> Result<Option<Vec<Group>>, Failure> {
        self.load_groups()
    }

    /// Go: `GroupRemove`. Always rewrites the file, even when nothing matched.
    pub fn group_remove(&self, name: &str) -> Result<(), Failure> {
        let _guard = self.guard();
        let mut groups = self.load_groups()?;
        if let Some(groups) = &mut groups {
            groups.retain(|group| group.name != name);
        }
        self.save_groups(&groups)
    }

    /// Go: `GroupAddMember` — idempotent; only the first group of that name
    /// is considered.
    pub fn group_add_member(&self, group: &str, user: &str) -> Result<(), Failure> {
        let _guard = self.guard();
        let mut groups = self.load_groups()?;
        if let Some(found) = groups
            .as_mut()
            .and_then(|groups| groups.iter_mut().find(|candidate| candidate.name == group))
        {
            if found
                .members
                .as_ref()
                .is_some_and(|members| members.iter().any(|member| member == user))
            {
                return Ok(());
            }
            found
                .members
                .get_or_insert_with(Vec::new)
                .push(user.to_owned());
            return self.save_groups(&groups);
        }
        Err(format!("group {} not found", go_quote(group)))
    }

    /// Go: `GroupRemoveMember` — always rewrites the file once the group
    /// exists; nil members stay nil.
    pub fn group_remove_member(&self, group: &str, user: &str) -> Result<(), Failure> {
        let _guard = self.guard();
        let mut groups = self.load_groups()?;
        if let Some(found) = groups
            .as_mut()
            .and_then(|groups| groups.iter_mut().find(|candidate| candidate.name == group))
        {
            if let Some(members) = &mut found.members {
                members.retain(|member| member != user);
            }
            return self.save_groups(&groups);
        }
        Err(format!("group {} not found", go_quote(group)))
    }

    // --- document rules --------------------------------------------------

    /// Go: `SetDocumentRule`. A rule with no owner and no lists deletes the
    /// existing rule; an ownerless rule that matches nothing is not stored
    /// but the file is still rewritten.
    pub fn set_document_rule(&self, rule: DocumentRule) -> Result<(), Failure> {
        if rule.path.is_empty() {
            return Err("path is required for a document rule".to_owned());
        }
        let _guard = self.guard();
        let mut rules = self.load_permissions()?;
        let mut found = false;
        if let Some(rules) = &mut rules
            && let Some(index) = rules.iter().position(|existing| existing.path == rule.path)
        {
            if rule.is_blank() {
                rules.remove(index);
            } else {
                rules[index] = rule.clone();
            }
            found = true;
        }
        if !found && !rule.owner.is_empty() {
            rules.get_or_insert_with(Vec::new).push(rule);
        }
        self.save_permissions(&rules)
    }

    /// Go: `GetDocumentRule`.
    pub fn get_document_rule(&self, path: &str) -> Result<Option<DocumentRule>, Failure> {
        let rules = self.load_permissions()?.unwrap_or_default();
        Ok(find_rule(&rules, path).cloned())
    }

    // --- persistence -----------------------------------------------------

    fn guard(&self) -> std::sync::MutexGuard<'_, ()> {
        self.lock
            .lock()
            .unwrap_or_else(std::sync::PoisonError::into_inner)
    }

    fn file(&self, name: &str) -> PathBuf {
        self.config_dir.join(name)
    }

    fn load_users(&self) -> Result<Option<Vec<User>>, Failure> {
        match read_file(&self.file("users.json"))? {
            None => Ok(None),
            Some(data) => decode::decode_users(&data),
        }
    }

    fn load_groups(&self) -> Result<Option<Vec<Group>>, Failure> {
        match read_file(&self.file("groups.json"))? {
            None => Ok(None),
            Some(data) => decode::decode_groups(&data),
        }
    }

    fn load_permissions(&self) -> Result<Option<Vec<DocumentRule>>, Failure> {
        match read_file(&self.file("permissions.json"))? {
            None => Ok(None),
            Some(data) => decode::decode_rules(&data),
        }
    }

    fn save_users(&self, users: &Option<Vec<User>>) -> Result<(), Failure> {
        write_file(&self.file("users.json"), &encode::users(users))
    }

    fn save_groups(&self, groups: &Option<Vec<Group>>) -> Result<(), Failure> {
        write_file(&self.file("groups.json"), &encode::groups(groups))
    }

    fn save_permissions(&self, rules: &Option<Vec<DocumentRule>>) -> Result<(), Failure> {
        write_file(&self.file("permissions.json"), &encode::rules(rules))
    }
}

/// Go: `GenerateToken` — 32 random bytes as lowercase hex.
pub fn generate_token() -> Result<String, Failure> {
    let mut buf = [0u8; 32];
    getrandom::fill(&mut buf).map_err(|error| format!("generate token: {error}"))?;
    Ok(hex(&buf))
}

/// Go: `HashToken` — SHA-256 hex digest.
#[must_use]
pub fn hash_token(token: &str) -> String {
    hex(&sha256::digest(token.as_bytes()))
}

/// Go: `ConstantTimeEqual`.
#[must_use]
pub fn constant_time_equal(a: &str, b: &str) -> bool {
    a.len() == b.len()
        && a.bytes()
            .zip(b.bytes())
            .fold(0u8, |acc, (x, y)| acc | (x ^ y))
            == 0
}

fn hex(bytes: &[u8]) -> String {
    const DIGITS: &[u8; 16] = b"0123456789abcdef";
    let mut out = String::with_capacity(bytes.len() * 2);
    for byte in bytes {
        out.push(char::from(DIGITS[usize::from(byte >> 4)]));
        out.push(char::from(DIGITS[usize::from(byte & 0x0f)]));
    }
    out
}

fn find_rule<'a>(rules: &'a [DocumentRule], path: &str) -> Option<&'a DocumentRule> {
    rules
        .iter()
        .find(|rule| rule.path == path)
        .or_else(|| rules.iter().find(|rule| rule.path == "*"))
}

fn group_contains(groups: &[Group], group: &str, user: &str) -> bool {
    groups
        .iter()
        .find(|candidate| candidate.name == group)
        .and_then(|found| found.members.as_ref())
        .is_some_and(|members| members.iter().any(|member| member == user))
}

/// Go: `readJSONFile` I/O half — a missing or empty file is the empty state.
fn read_file(path: &Path) -> Result<Option<Vec<u8>>, Failure> {
    match go_read(path) {
        Ok(data) if data.is_empty() => Ok(None),
        Ok(data) => Ok(Some(data)),
        Err((_, error)) if error.kind() == std::io::ErrorKind::NotFound => Ok(None),
        Err((operation, error)) => Err(format!(
            "read {}: {}",
            path.display(),
            go_path_error(operation, path, &error)
        )),
    }
}

/// Go: `writeJSONFile` — `os.WriteFile(path, data, 0600)`; the mode applies
/// only when the file is created.
fn write_file(path: &Path, data: &str) -> Result<(), Failure> {
    let mut options = OpenOptions::new();
    options.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    let mut file = options.open(path).map_err(|error| {
        format!(
            "write {}: {}",
            path.display(),
            go_path_error("open", path, &error)
        )
    })?;
    file.write_all(data.as_bytes()).map_err(|error| {
        format!(
            "write {}: {}",
            path.display(),
            go_path_error("write", path, &error)
        )
    })
}

/// Go: `os.MkdirAll(path, 0700)`, including its `ENOTDIR` text for a
/// non-directory in the way.
fn go_mkdir_all(path: &Path) -> Result<(), Failure> {
    if let Ok(metadata) = fs::metadata(path) {
        return if metadata.is_dir() {
            Ok(())
        } else {
            Err(format!("mkdir {}: not a directory", path.display()))
        };
    }
    if let Some(parent) = path.parent()
        && !parent.as_os_str().is_empty()
    {
        go_mkdir_all(parent)?;
    }
    #[cfg_attr(not(unix), allow(unused_mut))]
    let mut builder = fs::DirBuilder::new();
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        builder.mode(0o700);
    }
    match builder.create(path) {
        Ok(()) => Ok(()),
        Err(error) => {
            if fs::symlink_metadata(path).is_ok_and(|metadata| metadata.is_dir()) {
                Ok(())
            } else {
                Err(go_path_error("mkdir", path, &error))
            }
        }
    }
}

/// Go `json.MarshalIndent(value, "", "  ")` plus the trailing newline, for the
/// three persisted shapes.
mod encode {
    use super::{DocumentRule, Group, User};

    enum Json {
        Null,
        Str(String),
        Array(Vec<Json>),
        Object(Vec<(&'static str, Json)>),
    }

    fn strings(list: &[String]) -> Json {
        Json::Array(list.iter().map(|s| Json::Str(s.clone())).collect())
    }

    fn nilable(list: Option<&Vec<String>>) -> Json {
        list.map_or(Json::Null, |list| strings(list))
    }

    pub(super) fn users(users: &Option<Vec<User>>) -> String {
        finish(users.as_ref().map(|users| {
            users
                .iter()
                .map(|user| {
                    Json::Object(vec![
                        ("name", Json::Str(user.name.clone())),
                        ("token_hash", Json::Str(user.token_hash.clone())),
                        ("roles", nilable(user.roles.as_ref())),
                    ])
                })
                .collect()
        }))
    }

    pub(super) fn groups(groups: &Option<Vec<Group>>) -> String {
        finish(groups.as_ref().map(|groups| {
            groups
                .iter()
                .map(|group| {
                    Json::Object(vec![
                        ("name", Json::Str(group.name.clone())),
                        ("members", nilable(group.members.as_ref())),
                    ])
                })
                .collect()
        }))
    }

    pub(super) fn rules(rules: &Option<Vec<DocumentRule>>) -> String {
        finish(rules.as_ref().map(|rules| {
            rules
                .iter()
                .map(|rule| {
                    let mut members = vec![
                        ("path", Json::Str(rule.path.clone())),
                        ("owner", Json::Str(rule.owner.clone())),
                    ];
                    for (key, list) in [
                        ("read_users", &rule.read_users),
                        ("read_groups", &rule.read_groups),
                        ("write_users", &rule.write_users),
                        ("write_groups", &rule.write_groups),
                    ] {
                        if !list.is_empty() {
                            members.push((key, strings(list)));
                        }
                    }
                    Json::Object(members)
                })
                .collect()
        }))
    }

    fn finish(items: Option<Vec<Json>>) -> String {
        let root = items.map_or(Json::Null, Json::Array);
        let mut out = String::new();
        write(&root, 0, &mut out);
        out.push('\n');
        out
    }

    fn write(value: &Json, depth: usize, out: &mut String) {
        match value {
            Json::Null => out.push_str("null"),
            Json::Str(text) => string(text, out),
            Json::Array(items) if items.is_empty() => out.push_str("[]"),
            Json::Array(items) => {
                out.push_str("[\n");
                for (index, item) in items.iter().enumerate() {
                    indent(depth + 1, out);
                    write(item, depth + 1, out);
                    out.push_str(if index + 1 < items.len() { ",\n" } else { "\n" });
                }
                indent(depth, out);
                out.push(']');
            }
            Json::Object(members) => {
                out.push_str("{\n");
                for (index, (key, item)) in members.iter().enumerate() {
                    indent(depth + 1, out);
                    string(key, out);
                    out.push_str(": ");
                    write(item, depth + 1, out);
                    out.push_str(if index + 1 < members.len() {
                        ",\n"
                    } else {
                        "\n"
                    });
                }
                indent(depth, out);
                out.push('}');
            }
        }
    }

    fn indent(depth: usize, out: &mut String) {
        for _ in 0..depth {
            out.push_str("  ");
        }
    }

    /// Go's default encoder: HTML-safe (`<`, `>`, `&`), U+2028/U+2029 escaped,
    /// short escapes for `\n \r \t \b \f`, `\u00XX` for other controls.
    fn string(text: &str, out: &mut String) {
        out.push('"');
        for character in text.chars() {
            match character {
                '"' => out.push_str("\\\""),
                '\\' => out.push_str("\\\\"),
                '\n' => out.push_str("\\n"),
                '\r' => out.push_str("\\r"),
                '\t' => out.push_str("\\t"),
                '\u{8}' => out.push_str("\\b"),
                '\u{c}' => out.push_str("\\f"),
                '<' | '>' | '&' | '\u{2028}' | '\u{2029}' => {
                    out.push_str(&format!("\\u{:04x}", u32::from(character)));
                }
                control if u32::from(control) < 0x20 => {
                    out.push_str(&format!("\\u{:04x}", u32::from(control)));
                }
                other => out.push(other),
            }
        }
        out.push('"');
    }
}

/// Go `json.Unmarshal` into `[]User` / `[]Group` / `[]DocumentRule`.
///
/// Syntax is validated first with Go's wording, then the document is walked in
/// wire order. Only the first type error matters because Go saves it and keeps
/// decoding without changing the returned error.
mod decode {
    use super::{DocumentRule, Failure, Group, User, validate_syntax};

    #[derive(Clone, Copy, PartialEq, Eq)]
    enum Kind {
        Str,
        List,
    }

    /// A `[]string` field with Go slice semantics: `null` drops the backing
    /// array, `[]` replaces it with a fresh empty one, and a longer earlier
    /// array stays visible to later `null` elements of a repeated key.
    #[derive(Default)]
    struct List {
        backing: Vec<String>,
        len: usize,
        nil: bool,
    }

    impl List {
        fn nil() -> Self {
            Self {
                nil: true,
                ..Self::default()
            }
        }

        fn into_option(self) -> Option<Vec<String>> {
            if self.nil {
                None
            } else {
                Some(self.backing.into_iter().take(self.len).collect())
            }
        }

        fn into_vec(self) -> Vec<String> {
            self.into_option().unwrap_or_default()
        }
    }

    struct Fields {
        strs: Vec<String>,
        lists: Vec<List>,
    }

    impl Fields {
        fn new(specs: &[(&str, Kind)]) -> Self {
            Self {
                strs: vec![String::new(); specs.len()],
                lists: specs.iter().map(|_| List::nil()).collect(),
            }
        }
    }

    pub(super) fn decode_users(data: &[u8]) -> Result<Option<Vec<User>>, Failure> {
        const SPECS: &[(&str, Kind)] = &[
            ("name", Kind::Str),
            ("token_hash", Kind::Str),
            ("roles", Kind::List),
        ];
        decode_array(data, "User", SPECS, |mut fields| User {
            name: std::mem::take(&mut fields.strs[0]),
            token_hash: std::mem::take(&mut fields.strs[1]),
            roles: std::mem::take(&mut fields.lists[2]).into_option(),
        })
    }

    pub(super) fn decode_groups(data: &[u8]) -> Result<Option<Vec<Group>>, Failure> {
        const SPECS: &[(&str, Kind)] = &[("name", Kind::Str), ("members", Kind::List)];
        decode_array(data, "Group", SPECS, |mut fields| Group {
            name: std::mem::take(&mut fields.strs[0]),
            members: std::mem::take(&mut fields.lists[1]).into_option(),
        })
    }

    pub(super) fn decode_rules(data: &[u8]) -> Result<Option<Vec<DocumentRule>>, Failure> {
        const SPECS: &[(&str, Kind)] = &[
            ("path", Kind::Str),
            ("owner", Kind::Str),
            ("read_users", Kind::List),
            ("read_groups", Kind::List),
            ("write_users", Kind::List),
            ("write_groups", Kind::List),
        ];
        decode_array(data, "DocumentRule", SPECS, |mut fields| DocumentRule {
            path: std::mem::take(&mut fields.strs[0]),
            owner: std::mem::take(&mut fields.strs[1]),
            read_users: std::mem::take(&mut fields.lists[2]).into_vec(),
            read_groups: std::mem::take(&mut fields.lists[3]).into_vec(),
            write_users: std::mem::take(&mut fields.lists[4]).into_vec(),
            write_groups: std::mem::take(&mut fields.lists[5]).into_vec(),
        })
    }

    fn decode_array<T>(
        data: &[u8],
        struct_name: &str,
        specs: &[(&str, Kind)],
        build: impl Fn(Fields) -> T,
    ) -> Result<Option<Vec<T>>, Failure> {
        validate_syntax(data)?;
        let mut scanner = Scanner { data, pos: 0 };
        scanner.skip_space();
        match scanner.peek() {
            b'n' => Ok(None),
            b'[' => {
                scanner.pos += 1;
                let mut items = Vec::new();
                scanner.skip_space();
                if scanner.peek() == b']' {
                    return Ok(Some(items));
                }
                loop {
                    scanner.skip_space();
                    match scanner.peek() {
                        b'n' => {
                            scanner.skip_value();
                            items.push(build(Fields::new(specs)));
                        }
                        b'{' => items.push(build(scanner.object(struct_name, specs)?)),
                        _ => {
                            return Err(format!(
                                "json: cannot unmarshal {} into Go value of type permissions.{struct_name}",
                                scanner.kind()
                            ));
                        }
                    }
                    scanner.skip_space();
                    let delimiter = scanner.peek();
                    scanner.pos += 1;
                    if delimiter == b']' {
                        return Ok(Some(items));
                    }
                }
            }
            _ => Err(format!(
                "json: cannot unmarshal {} into Go value of type []permissions.{struct_name}",
                scanner.kind()
            )),
        }
    }

    struct Scanner<'a> {
        data: &'a [u8],
        pos: usize,
    }

    impl Scanner<'_> {
        fn peek(&self) -> u8 {
            self.data[self.pos]
        }

        fn skip_space(&mut self) {
            while matches!(self.data.get(self.pos), Some(b' ' | b'\t' | b'\n' | b'\r')) {
                self.pos += 1;
            }
        }

        fn kind(&self) -> &'static str {
            match self.peek() {
                b'{' => "object",
                b'[' => "array",
                b'"' => "string",
                b't' | b'f' => "bool",
                b'n' => "null",
                _ => "number",
            }
        }

        /// Skips one syntactically valid value without recursion.
        fn skip_value(&mut self) {
            let mut depth = 0usize;
            loop {
                self.skip_space();
                match self.peek() {
                    b'"' => {
                        self.skip_string();
                        if depth == 0 {
                            return;
                        }
                    }
                    b'{' | b'[' => {
                        self.pos += 1;
                        depth += 1;
                    }
                    b'}' | b']' => {
                        self.pos += 1;
                        depth -= 1;
                        if depth == 0 {
                            return;
                        }
                    }
                    b',' | b':' => self.pos += 1,
                    _ => {
                        while self.pos < self.data.len()
                            && !matches!(
                                self.data[self.pos],
                                b',' | b']' | b'}' | b' ' | b'\t' | b'\n' | b'\r'
                            )
                        {
                            self.pos += 1;
                        }
                        if depth == 0 {
                            return;
                        }
                    }
                }
            }
        }

        fn skip_string(&mut self) {
            self.pos += 1;
            while self.data[self.pos] != b'"' {
                self.pos += if self.data[self.pos] == b'\\' { 2 } else { 1 };
            }
            self.pos += 1;
        }

        /// Reads a string token like Go's `unquote`: invalid UTF-8 becomes one
        /// U+FFFD per byte and unpaired surrogate escapes become U+FFFD.
        fn string(&mut self) -> String {
            self.pos += 1;
            let mut out = String::new();
            let mut run = Vec::new();
            loop {
                let byte = self.data[self.pos];
                self.pos += 1;
                match byte {
                    b'"' => break,
                    b'\\' => {
                        flush_run(&mut out, &mut run);
                        let escape = self.data[self.pos];
                        self.pos += 1;
                        match escape {
                            b'b' => out.push('\u{8}'),
                            b'f' => out.push('\u{c}'),
                            b'n' => out.push('\n'),
                            b'r' => out.push('\r'),
                            b't' => out.push('\t'),
                            b'u' => self.unicode_escape(&mut out),
                            other => out.push(char::from(other)),
                        }
                    }
                    other => run.push(other),
                }
            }
            flush_run(&mut out, &mut run);
            out
        }

        fn hex4(&mut self) -> u32 {
            let value = std::str::from_utf8(&self.data[self.pos..self.pos + 4])
                .ok()
                .and_then(|digits| u32::from_str_radix(digits, 16).ok())
                .unwrap_or(0);
            self.pos += 4;
            value
        }

        fn unicode_escape(&mut self, out: &mut String) {
            let first = self.hex4();
            if (0xd800..0xdc00).contains(&first) {
                let rest = &self.data[self.pos..];
                if rest.starts_with(b"\\u") {
                    let saved = self.pos;
                    self.pos += 2;
                    let second = self.hex4();
                    if (0xdc00..0xe000).contains(&second) {
                        let combined = 0x10000 + ((first - 0xd800) << 10) + (second - 0xdc00);
                        out.push(char::from_u32(combined).unwrap_or('\u{fffd}'));
                        return;
                    }
                    self.pos = saved;
                }
                out.push('\u{fffd}');
            } else {
                out.push(char::from_u32(first).unwrap_or('\u{fffd}'));
            }
        }

        fn object(&mut self, struct_name: &str, specs: &[(&str, Kind)]) -> Result<Fields, Failure> {
            self.pos += 1;
            let mut fields = Fields::new(specs);
            self.skip_space();
            if self.peek() == b'}' {
                self.pos += 1;
                return Ok(fields);
            }
            loop {
                self.skip_space();
                let key = self.string();
                self.skip_space();
                self.pos += 1; // ':'
                self.skip_space();
                match specs
                    .iter()
                    .position(|(name, _)| fold(&key) == name.to_ascii_uppercase())
                {
                    None => self.skip_value(),
                    Some(index) => {
                        let (tag, kind) = specs[index];
                        match kind {
                            Kind::Str => {
                                self.string_field(struct_name, tag, &mut fields.strs[index])?
                            }
                            Kind::List => {
                                self.list_field(struct_name, tag, &mut fields.lists[index])?;
                            }
                        }
                    }
                }
                self.skip_space();
                let delimiter = self.peek();
                self.pos += 1;
                if delimiter == b'}' {
                    return Ok(fields);
                }
            }
        }

        fn mismatch(&self, struct_name: &str, tag: &str, go_type: &str) -> Failure {
            format!(
                "json: cannot unmarshal {} into Go struct field {struct_name}.{tag} of type {go_type}",
                self.kind()
            )
        }

        fn string_field(
            &mut self,
            struct_name: &str,
            tag: &str,
            slot: &mut String,
        ) -> Result<(), Failure> {
            match self.peek() {
                b'"' => *slot = self.string(),
                b'n' => self.skip_value(),
                _ => return Err(self.mismatch(struct_name, tag, "string")),
            }
            Ok(())
        }

        fn list_field(
            &mut self,
            struct_name: &str,
            tag: &str,
            slot: &mut List,
        ) -> Result<(), Failure> {
            match self.peek() {
                b'n' => {
                    self.skip_value();
                    *slot = List::nil();
                }
                b'[' => {
                    self.pos += 1;
                    self.skip_space();
                    if self.peek() == b']' {
                        self.pos += 1;
                        *slot = List::default();
                        return Ok(());
                    }
                    slot.nil = false;
                    let mut count = 0usize;
                    loop {
                        self.skip_space();
                        if count >= slot.backing.len() {
                            slot.backing.push(String::new());
                        }
                        match self.peek() {
                            b'"' => slot.backing[count] = self.string(),
                            b'n' => self.skip_value(),
                            _ => return Err(self.mismatch(struct_name, tag, "string")),
                        }
                        count += 1;
                        self.skip_space();
                        let delimiter = self.peek();
                        self.pos += 1;
                        if delimiter == b']' {
                            break;
                        }
                    }
                    slot.len = count;
                }
                _ => return Err(self.mismatch(struct_name, tag, "[]string")),
            }
            Ok(())
        }
    }

    /// Go's `foldName` restricted to the characters that can fold onto the
    /// ASCII tag names: ASCII upper-casing plus U+017F (ſ → S) and U+212A
    /// (K → K).
    fn fold(key: &str) -> String {
        key.chars()
            .map(|character| match character {
                '\u{17f}' => 'S',
                '\u{212a}' => 'K',
                other => other.to_ascii_uppercase(),
            })
            .collect()
    }

    fn flush_run(out: &mut String, run: &mut Vec<u8>) {
        let mut rest: &[u8] = run;
        while !rest.is_empty() {
            match std::str::from_utf8(rest) {
                Ok(text) => {
                    out.push_str(text);
                    break;
                }
                Err(error) => {
                    let (valid, tail) = rest.split_at(error.valid_up_to());
                    out.push_str(std::str::from_utf8(valid).unwrap_or_default());
                    let bad = error.error_len().unwrap_or(tail.len());
                    for _ in 0..bad {
                        out.push('\u{fffd}');
                    }
                    rest = &tail[bad..];
                }
            }
        }
        run.clear();
    }
}
