#![deny(unsafe_code)]

use std::{
    collections::{BTreeMap, HashSet},
    fs,
    path::Path,
};

use serde::Serialize;
use thiserror::Error;
use time::{OffsetDateTime, format_description::well_known::Rfc3339};

use crate::{Base, MutationError, TypedVaultError, View, parse_base, secure_path};

const BASES_DIR: &str = "bases";
const LEGACY_VIEWS: &str = ".symdesk/views.json";
const CREATED_PLACEHOLDER: &str = "__SYMDESK_BASE_CREATED__";

#[derive(Debug, Error)]
pub enum BaseWriteError {
    #[error(transparent)]
    Path(#[from] crate::SecurePathError),
    #[error("{0}")]
    Io(#[from] std::io::Error),
    #[error(transparent)]
    Mutation(#[from] MutationError),
    #[error(transparent)]
    Contract(#[from] TypedVaultError),
    #[error("base not found")]
    BaseNotFound,
    #[error("view not found")]
    ViewNotFound,
    #[error("serialize base: {0}")]
    Serialize(String),
}

#[derive(Serialize)]
struct BaseFrontmatter<'a> {
    #[serde(rename = "type")]
    kind: &'static str,
    title: &'a str,
    created: &'a str,
    tags: &'a [String],
    base_id: &'a str,
    #[serde(skip_serializing_if = "str::is_empty")]
    description: &'a str,
    #[serde(skip_serializing_if = "std::collections::BTreeMap::is_empty")]
    properties: &'a std::collections::BTreeMap<String, crate::PropertyConfig>,
    views: &'a [View],
    #[serde(flatten)]
    extras: &'a std::collections::BTreeMap<String, noyalib::Value>,
}

/// Writes a base note, applying the Go manager's ID, path, timestamp, and tag defaults.
pub fn save_base(vault_root: impl AsRef<Path>, base: &mut Base) -> Result<(), BaseWriteError> {
    migrate_legacy_views(vault_root.as_ref());
    write_base(vault_root.as_ref(), base, None)
}

/// Writes a base and calls `snapshot_fn` with its absolute path before writing.
pub fn save_base_with_snapshot(
    vault_root: impl AsRef<Path>,
    base: &mut Base,
    mut snapshot_fn: impl FnMut(&Path),
) -> Result<(), BaseWriteError> {
    migrate_legacy_views(vault_root.as_ref());
    write_base(vault_root.as_ref(), base, Some(&mut snapshot_fn))
}

fn write_base(
    root: &Path,
    base: &mut Base,
    mut snapshot_fn: Option<&mut dyn FnMut(&Path)>,
) -> Result<(), BaseWriteError> {
    if base.id.is_empty() {
        base.id = slugify(&base.title);
    }
    if base.path.is_empty() {
        base.path = format!("{BASES_DIR}/{}.md", base.id);
    }
    if base.created.is_empty() {
        let now = OffsetDateTime::now_utc();
        base.created = OffsetDateTime::from_unix_timestamp(now.unix_timestamp())
            .unwrap_or(now)
            .format(&Rfc3339)
            .unwrap_or_default();
    }
    if base.tags.is_empty() {
        base.tags.push("base".to_owned());
    }

    let path = secure_path(root, &base.path)?;
    if let Some(snapshot_fn) = snapshot_fn.as_mut() {
        snapshot_fn(&path);
    }
    create_dir_all_0750(path.parent().ok_or_else(|| {
        BaseWriteError::Serialize("base path has no parent directory".to_owned())
    })?)?;
    let markdown = render_base(base)?;
    crate::write_atomic(&path, markdown.as_bytes())?;
    Ok(())
}

/// Resolves a base by vault-relative path, ID, or title and removes its file.
pub fn delete_base(vault_root: impl AsRef<Path>, reference: &str) -> Result<(), BaseWriteError> {
    let root = vault_root.as_ref();
    migrate_legacy_views(root);
    delete_base_inner(root, reference, None)
}

/// Removes a base and calls `snapshot_fn` with its absolute path before deletion.
pub fn delete_base_with_snapshot(
    vault_root: impl AsRef<Path>,
    reference: &str,
    mut snapshot_fn: impl FnMut(&Path),
) -> Result<(), BaseWriteError> {
    let root = vault_root.as_ref();
    migrate_legacy_views(root);
    delete_base_inner(root, reference, Some(&mut snapshot_fn))
}

fn delete_base_inner(
    root: &Path,
    reference: &str,
    mut snapshot_fn: Option<&mut dyn FnMut(&Path)>,
) -> Result<(), BaseWriteError> {
    let base = get_base(root, reference)?;
    let path = secure_path(root, &base.path)?;
    if let Some(snapshot_fn) = snapshot_fn.as_mut() {
        snapshot_fn(&path);
    }
    fs::remove_file(path)?;
    Ok(())
}

/// Saves a view into its existing base or creates a base for it.
pub fn save_view(vault_root: impl AsRef<Path>, view: View) -> Result<(), BaseWriteError> {
    let root = vault_root.as_ref();
    migrate_legacy_views(root);
    save_view_inner(root, view, None)
}

/// Saves a view and calls `snapshot_fn` before rewriting its base.
pub fn save_view_with_snapshot(
    vault_root: impl AsRef<Path>,
    view: View,
    mut snapshot_fn: impl FnMut(&Path),
) -> Result<(), BaseWriteError> {
    let root = vault_root.as_ref();
    migrate_legacy_views(root);
    save_view_inner(root, view, Some(&mut snapshot_fn))
}

fn save_view_inner(
    root: &Path,
    mut view: View,
    snapshot_fn: Option<&mut dyn FnMut(&Path)>,
) -> Result<(), BaseWriteError> {
    let mut bases = list_bases(root)?;
    if view.id.is_empty() {
        let count: usize = bases.iter().map(|base| base.views.len()).sum();
        view.id = if view.name.is_empty() {
            format!("view_{}", count + 1)
        } else {
            slugify(&view.name)
        };
    }
    for mut base in bases.iter().cloned() {
        if let Some(index) = base
            .views
            .iter()
            .position(|existing| existing.id == view.id)
        {
            base.views[index] = view;
            return write_base(root, &mut base, snapshot_fn);
        }
    }
    if !view.source.is_empty() {
        let source_slug = slugify(&view.source);
        for mut base in bases.drain(..) {
            if base.id == source_slug
                || (!base.views.is_empty() && base.views[0].source == view.source)
            {
                base.views.push(view);
                return write_base(root, &mut base, snapshot_fn);
            }
        }
    }

    let base_slug = slugify(&view.name);
    let mut slug = base_slug.clone();
    let mut suffix = 2;
    while list_bases(root)?.iter().any(|base| base.id == slug) {
        slug = format!("{base_slug}-{suffix}");
        suffix += 1;
    }
    let title = if view.name.is_empty() {
        "Saved Views".to_owned()
    } else {
        view.name.clone()
    };
    let mut base = Base {
        id: slug.clone(),
        path: format!("{BASES_DIR}/{slug}.md"),
        title,
        description: String::new(),
        created: String::new(),
        tags: Vec::new(),
        properties: Default::default(),
        views: vec![view],
        extras: Default::default(),
    };
    write_base(root, &mut base, snapshot_fn)
}

/// Removes a view from the first matching base and rewrites that base note.
pub fn delete_view(vault_root: impl AsRef<Path>, view_id: &str) -> Result<(), BaseWriteError> {
    let root = vault_root.as_ref();
    migrate_legacy_views(root);
    delete_view_inner(root, view_id, None)
}

/// Deletes a view and calls `snapshot_fn` before rewriting its base.
pub fn delete_view_with_snapshot(
    vault_root: impl AsRef<Path>,
    view_id: &str,
    mut snapshot_fn: impl FnMut(&Path),
) -> Result<(), BaseWriteError> {
    let root = vault_root.as_ref();
    migrate_legacy_views(root);
    delete_view_inner(root, view_id, Some(&mut snapshot_fn))
}

fn delete_view_inner(
    root: &Path,
    view_id: &str,
    snapshot_fn: Option<&mut dyn FnMut(&Path)>,
) -> Result<(), BaseWriteError> {
    for mut base in list_bases(root)? {
        if let Some(index) = base.views.iter().position(|view| view.id == view_id) {
            base.views.remove(index);
            return write_base(root, &mut base, snapshot_fn);
        }
    }
    Err(BaseWriteError::ViewNotFound)
}

fn get_base(root: &Path, reference: &str) -> Result<Base, BaseWriteError> {
    let reference = reference.trim();
    if reference.is_empty() {
        return Err(BaseWriteError::BaseNotFound);
    }
    let mut path = reference.to_owned();
    if !path.ends_with(".md") {
        path.push_str(".md");
    }
    if !path.starts_with("bases/") {
        path = format!(
            "{BASES_DIR}/{}",
            Path::new(&path)
                .file_name()
                .and_then(|v| v.to_str())
                .unwrap_or("")
        );
    }
    if let Ok(abs) = secure_path(root, &path)
        && let Ok(data) = fs::read(abs)
    {
        return Ok(parse_base(&path, &data)?);
    }
    for base in list_bases(root)? {
        if base.id == reference || base.title.eq_ignore_ascii_case(reference) {
            return Ok(base);
        }
    }
    Err(BaseWriteError::BaseNotFound)
}

fn list_bases(root: &Path) -> Result<Vec<Base>, BaseWriteError> {
    let dir = secure_path(root, BASES_DIR)?;
    let entries = match fs::read_dir(dir) {
        Ok(entries) => entries,
        Err(error) if error.kind() == std::io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(error) => return Err(error.into()),
    };
    let mut bases = Vec::new();
    for entry in entries {
        let entry = entry?;
        if !entry.file_type()?.is_file()
            || entry.path().extension().and_then(|value| value.to_str()) != Some("md")
        {
            continue;
        }
        let name = entry.file_name().to_string_lossy().into_owned();
        let path = format!("{BASES_DIR}/{name}");
        let Ok(abs) = secure_path(root, &path) else {
            continue;
        };
        let Ok(data) = fs::read(abs) else {
            continue;
        };
        if let Ok(base) = parse_base(&path, &data) {
            bases.push(base);
        }
    }
    bases.sort_by_key(|base| base.title.to_lowercase());
    Ok(bases)
}

fn migrate_legacy_views(root: &Path) {
    let Ok(path) = secure_path(root, LEGACY_VIEWS) else {
        return;
    };
    let Ok(data) = fs::read(path) else {
        return;
    };
    let Ok(views) = serde_json::from_slice::<Vec<View>>(&data) else {
        return;
    };
    if views.is_empty() {
        return;
    }

    let existing_ids: HashSet<String> = list_bases(root)
        .unwrap_or_default()
        .into_iter()
        .flat_map(|base| base.views.into_iter().map(|view| view.id))
        .collect();
    let mut groups: BTreeMap<String, (String, Vec<View>)> = BTreeMap::new();
    for view in views {
        if existing_ids.contains(&view.id) {
            continue;
        }
        let (slug, title) = if view.source.is_empty() {
            (slugify(&view.name), view.name.clone())
        } else {
            let slug = slugify(&view.source);
            let title = view.source.strip_suffix('/').unwrap_or(&view.source);
            let title = title.strip_prefix("tag:").unwrap_or(title);
            let title = title
                .strip_prefix("notebook:")
                .unwrap_or(title)
                .replace('-', " ");
            (slug, title_case(&title))
        };
        let title = if title.is_empty() {
            "Saved Views"
        } else {
            &title
        };
        groups
            .entry(slug)
            .or_insert_with(|| (title.to_owned(), Vec::new()))
            .1
            .push(view);
    }

    for (slug, (title, views)) in groups {
        let now = OffsetDateTime::now_utc();
        let mut base = Base {
            id: slug.clone(),
            path: format!("{BASES_DIR}/{slug}.md"),
            title,
            description: String::new(),
            created: OffsetDateTime::from_unix_timestamp(now.unix_timestamp())
                .unwrap_or(now)
                .format(&Rfc3339)
                .unwrap_or_default(),
            tags: vec!["base".to_owned()],
            properties: Default::default(),
            views,
            extras: Default::default(),
        };
        let _ = write_base(root, &mut base, None);
    }
}

fn title_case(value: &str) -> String {
    let mut output = String::with_capacity(value.len());
    let mut start_word = true;
    for character in value.chars() {
        if start_word && character.is_alphanumeric() {
            output.extend(character.to_uppercase());
            start_word = false;
        } else {
            output.push(character);
            start_word = !character.is_alphanumeric();
        }
    }
    output
}

fn render_base(base: &Base) -> Result<String, BaseWriteError> {
    let tags = if base.tags.is_empty() {
        vec!["base".to_owned()]
    } else {
        base.tags.clone()
    };
    let views = &base.views;
    let frontmatter = BaseFrontmatter {
        kind: "base",
        title: &base.title,
        // noyalib follows YAML 1.2 and leaves timestamps plain; Go's YAML
        // serializer quotes timestamp-shaped strings to preserve their type.
        created: CREATED_PLACEHOLDER,
        tags: &tags,
        base_id: &base.id,
        description: &base.description,
        properties: &base.properties,
        views,
        extras: &base.extras,
    };
    let frontmatter = noyalib::to_string_with_config(
        &frontmatter,
        &noyalib::SerializerConfig::new().indent(4),
    )
    .map_err(|error| BaseWriteError::Serialize(error.to_string()))?;
    let created = crate::mutations::render_go_yaml_string(&base.created, 4, false)
        .map_err(|error| BaseWriteError::Serialize(error.to_string()))?;
    let mut frontmatter = frontmatter.replacen(
        &format!("created: {CREATED_PLACEHOLDER}"),
        &format!("created: {created}"),
        1,
    );
    if !frontmatter.ends_with('\n') {
        frontmatter.push('\n');
    }
    let mut body = format!("# {}\n\n", base.title);
    if !base.description.is_empty() {
        body.push_str(&format!("{}\n\n", base.description));
    }
    body.push_str("## Views\n\n");
    if base.views.is_empty() {
        body.push_str("_No views yet._\n");
    } else {
        for view in &base.views {
            let view_type = if view.r#type.is_empty() {
                "table"
            } else {
                &view.r#type
            };
            let mut line = format!("- **{}** (`{view_type}`)", view.name);
            let mut details = Vec::new();
            if !view.source.is_empty() {
                if let Some(id) = view.source.strip_prefix("notebook:") {
                    details.push(format!("Source: [[{id}]]"));
                } else if view.source.starts_with("tag:") {
                    details.push(format!("Source: {}", view.source));
                } else {
                    details.push(format!("Source: `{}`", view.source));
                }
            }
            if !view.filters.is_empty() {
                let filters = view
                    .filters
                    .iter()
                    .map(|filter| {
                        if filter.operator.is_empty() || filter.operator == "equals" {
                            format!("{} = {}", filter.key, filter.value)
                        } else {
                            format!("{} {} {}", filter.key, filter.operator, filter.value)
                        }
                    })
                    .collect::<Vec<_>>();
                details.push(format!("Filters: {}", filters.join(", ")));
            }
            if !details.is_empty() {
                line.push_str(&format!(" · {}", details.join(" · ")));
            }
            body.push_str(&format!("{line}\n"));
        }
    }
    Ok(format!("---\n{frontmatter}---\n\n{body}"))
}

fn slugify(value: &str) -> String {
    let mut slug = String::new();
    let mut separator = false;
    for character in value.trim().chars().flat_map(char::to_lowercase) {
        if character.is_ascii_lowercase() || character.is_ascii_digit() {
            if separator && !slug.is_empty() {
                slug.push('-');
            }
            slug.push(character);
            separator = false;
        } else {
            separator = true;
        }
    }
    if slug.is_empty() {
        "base".to_owned()
    } else {
        slug
    }
}

fn create_dir_all_0750(path: &Path) -> Result<(), std::io::Error> {
    if path.is_dir() {
        return Ok(());
    }
    if let Some(parent) = path.parent() {
        create_dir_all_0750(parent)?;
    }
    #[cfg(unix)]
    let mut builder = fs::DirBuilder::new();
    #[cfg(not(unix))]
    let builder = fs::DirBuilder::new();
    #[cfg(unix)]
    {
        use std::os::unix::fs::DirBuilderExt;
        builder.mode(0o750);
    }
    match builder.create(path) {
        Ok(()) => Ok(()),
        Err(error) if error.kind() == std::io::ErrorKind::AlreadyExists && path.is_dir() => Ok(()),
        Err(error) => Err(error),
    }
}
