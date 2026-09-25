#![deny(unsafe_code)]

use std::{
    fs::{self, OpenOptions},
    io::Write as _,
    path::Path,
};

use thiserror::Error;

use crate::{MutationError, Notebook, TypedVaultError, parse_notebook, secure_path};

#[derive(Debug, Error)]
pub enum NotebookWriteError {
    #[error(transparent)]
    Path(#[from] crate::SecurePathError),
    #[error("read notebook: {0}")]
    Read(#[from] std::io::Error),
    #[error(transparent)]
    Mutation(#[from] MutationError),
    #[error(transparent)]
    Contract(#[from] TypedVaultError),
    #[error("source path is required")]
    EmptySource,
    #[error("a notebook cannot be a source of itself")]
    SourceIsSelf,
}

/// Adds a source to a notebook note, keeping sources sorted and unique.
///
/// The Markdown body is regenerated with the same rendering rules as Go.
pub fn add_notebook_source(
    vault_root: impl AsRef<Path>,
    notebook_path: &str,
    source_path: &str,
) -> Result<Notebook, NotebookWriteError> {
    change_notebook_source(vault_root.as_ref(), notebook_path, source_path, true)
}

/// Removes a source from a notebook note. An absent source is a no-op.
pub fn remove_notebook_source(
    vault_root: impl AsRef<Path>,
    notebook_path: &str,
    source_path: &str,
) -> Result<Notebook, NotebookWriteError> {
    change_notebook_source(vault_root.as_ref(), notebook_path, source_path, false)
}

fn change_notebook_source(
    vault_root: &Path,
    notebook_path: &str,
    source_path: &str,
    add: bool,
) -> Result<Notebook, NotebookWriteError> {
    let notebook_file = secure_path(vault_root, notebook_path)?;
    let notebook_data = fs::read(&notebook_file)?;
    let root = fs::canonicalize(vault_root)?;
    let notebook_rel = relative_slash_path(&root, &notebook_file)?;
    let mut notebook = parse_notebook(&notebook_rel, &notebook_data)?;

    let source_path = source_path.trim();
    if source_path.is_empty() {
        return Err(NotebookWriteError::EmptySource);
    }
    let source_file = secure_path(vault_root, source_path)?;
    let source_rel = relative_slash_path(&root, &source_file)?;
    if source_rel == notebook.path {
        return Err(NotebookWriteError::SourceIsSelf);
    }

    let original_len = notebook.sources.len();
    if add {
        if !notebook.sources.contains(&source_rel) {
            notebook.sources.push(source_rel.clone());
            notebook.sources.sort();
        }
    } else {
        notebook.sources.retain(|source| source != &source_rel);
    }
    if notebook.sources.len() == original_len {
        return Ok(notebook);
    }

    let output = render_notebook(&notebook)?;
    let mut options = OpenOptions::new();
    options.write(true).create(true).truncate(true);
    #[cfg(unix)]
    {
        use std::os::unix::fs::OpenOptionsExt;
        options.mode(0o600);
    }
    let mut file = options.open(&notebook_file)?;
    file.write_all(&output)?;
    Ok(notebook)
}

fn render_notebook(notebook: &Notebook) -> Result<Vec<u8>, NotebookWriteError> {
    let yaml = |value: &str| crate::mutations::render_go_yaml_string(value, 4, false);
    let mut frontmatter = format!(
        "type: notebook\ntitle: {}\ncreated: {}\ntags:\n    - notebook\nnotebook_id: {}\n",
        yaml(&notebook.title)?,
        yaml(&notebook.created)?,
        yaml(&notebook.id)?,
    );
    if !notebook.description.is_empty() {
        frontmatter.push_str(&format!("description: {}\n", yaml(&notebook.description)?));
    }
    if notebook.sources.is_empty() {
        frontmatter.push_str("sources: []\n");
    } else {
        frontmatter.push_str("sources:\n");
        for source in &notebook.sources {
            frontmatter.push_str(&format!("    - {}\n", yaml(source)?));
        }
    }
    if !notebook.query.is_empty() {
        frontmatter.push_str(&format!("query: {}\n", yaml(&notebook.query)?));
    }

    let mut body = format!("# {}\n\n", notebook.title);
    if !notebook.description.is_empty() {
        body.push_str(&format!("{}\n\n", notebook.description));
    }
    body.push_str("## Sources\n\n");
    if notebook.sources.is_empty() {
        body.push_str("_No sources yet._\n");
    }
    for source in &notebook.sources {
        let base = source.rsplit('/').next().unwrap_or(source);
        let name = base.rfind('.').map_or(base, |index| &base[..index]);
        body.push_str(&format!("- [[{name}]] (`{source}`)\n"));
    }
    Ok(format!("---\n{frontmatter}---\n\n{body}").into_bytes())
}

fn relative_slash_path(root: &Path, path: &Path) -> Result<String, NotebookWriteError> {
    let relative = path
        .strip_prefix(root)
        .map_err(|_| crate::SecurePathError::Traversal(path.display().to_string()))?;
    Ok(relative
        .components()
        .map(|part| part.as_os_str().to_string_lossy())
        .collect::<Vec<_>>()
        .join("/"))
}
