//! Filesystem-backed notebook reads shared by CLI and protocol surfaces.
use std::{
    io::{self, Read},
    path::Path,
};

use cap_std::{ambient_authority, fs::Dir};
use serde::Serialize;

use crate::{Notebook, go_path_error, parse_bytes, parse_notebook, secure_path};

/// A source's current title, or an explicit dangling-reference marker.
#[derive(Clone, Debug, Serialize)]
pub struct NotebookSource {
    pub path: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub title: String,
    #[serde(skip_serializing_if = "std::ops::Not::not")]
    pub missing: bool,
}

fn read_source(root: &Path, relative: &str) -> Result<Vec<u8>, String> {
    let absolute = secure_path(root, relative).map_err(|error| error.to_string())?;
    let canonical_root = root.canonicalize().map_err(|error| error.to_string())?;
    let relative = absolute
        .strip_prefix(&canonical_root)
        .map_err(|error| error.to_string())?;
    let dir = Dir::open_ambient_dir(&canonical_root, ambient_authority())
        .map_err(|error| error.to_string())?;
    let mut file = dir.open(relative).map_err(|error| {
        if error.kind() == io::ErrorKind::NotFound {
            "notebook not found".to_owned()
        } else {
            go_path_error("open", &absolute, &error)
        }
    })?;
    let mut bytes = Vec::new();
    file.read_to_end(&mut bytes)
        .map_err(|error| go_path_error("read", &absolute, &error))?;
    Ok(bytes)
}

/// Loads one notebook by slug or vault-relative path.
///
/// # Errors
/// Rejects a missing notebook, invalid frontmatter, or a path outside the vault.
pub fn load_notebook(root: &Path, reference: &str) -> Result<Notebook, String> {
    let mut relative = reference.trim().to_owned();
    if relative.is_empty() {
        return Err("notebook reference is required".to_owned());
    }
    if !relative.ends_with(".md") {
        relative.push_str(".md");
    }
    if !relative
        .replace(std::path::MAIN_SEPARATOR, "/")
        .starts_with("notebooks/")
    {
        let name = Path::new(&relative).file_name().unwrap_or_default();
        relative = Path::new("notebooks")
            .join(name)
            .to_string_lossy()
            .into_owned();
    }
    let data = read_source(root, &relative)?;
    parse_notebook(&relative, &data).map_err(|error| error.to_string())
}

/// Lists immediate, lowercase-.md notebook notes, skipping ordinary/broken notes.
///
/// # Errors
/// Returns directory access and vault-confinement errors.
pub fn list_notebooks(root: &Path) -> Result<Vec<Notebook>, String> {
    let absolute = secure_path(root, "notebooks").map_err(|error| error.to_string())?;
    let dir =
        Dir::open_ambient_dir(root, ambient_authority()).map_err(|error| error.to_string())?;
    let entries = match dir.read_dir("notebooks") {
        Ok(entries) => entries,
        Err(error) if error.kind() == io::ErrorKind::NotFound => return Ok(Vec::new()),
        Err(error) => return Err(go_path_error("open", &absolute, &error)),
    };
    let mut names = Vec::new();
    for entry in entries {
        let entry = entry.map_err(|error| error.to_string())?;
        if entry
            .file_type()
            .map_err(|error| error.to_string())?
            .is_dir()
        {
            continue;
        }
        let path = Path::new("notebooks").join(entry.file_name());
        if path.extension().is_some_and(|ext| ext == "md") {
            names.push(path);
        }
    }
    names.sort();
    let mut notebooks = Vec::new();
    for path in names {
        if let Ok(notebook) = load_notebook(root, &path.to_string_lossy()) {
            notebooks.push(notebook);
        }
    }
    notebooks.sort_by_key(|notebook| crate::go_lowercase(&notebook.title));
    Ok(notebooks)
}

/// Resolves current source titles without discarding missing or duplicate refs.
#[must_use]
pub fn resolve_notebook_sources(root: &Path, notebook: &Notebook) -> Vec<NotebookSource> {
    notebook
        .sources
        .iter()
        .map(|path| {
            let document = read_source(root, path).and_then(|data| {
                let absolute = secure_path(root, path).map_err(|error| error.to_string())?;
                parse_bytes(&absolute.to_string_lossy(), &data).map_err(|error| error.to_string())
            });
            match document {
                Ok(document) => NotebookSource {
                    path: path.clone(),
                    title: document.title,
                    missing: false,
                },
                Err(_) => NotebookSource {
                    path: path.clone(),
                    title: String::new(),
                    missing: true,
                },
            }
        })
        .collect()
}
