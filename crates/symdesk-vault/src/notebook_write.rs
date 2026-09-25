#![deny(unsafe_code)]

use std::{
    fs::{self, OpenOptions},
    io::Write as _,
    path::Path,
};
use time::OffsetDateTime;

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
    #[error("title is required")]
    TitleRequired,
    #[error("write notebook: {0}")]
    Write(std::io::Error),
    #[error("a notebook cannot be a source of itself")]
    SourceIsSelf,
}

/// Creates a notebook note at `notebooks/<slug>.md`.
pub fn new_notebook(
    vault_root: impl AsRef<Path>,
    title: &str,
    description: &str,
) -> Result<Notebook, NotebookWriteError> {
    new_notebook_with_query(vault_root, title, description, "")
}

/// Creates a notebook note and stores the originating search query as metadata.
pub fn new_notebook_with_query(
    vault_root: impl AsRef<Path>,
    title: &str,
    description: &str,
    query: &str,
) -> Result<Notebook, NotebookWriteError> {
    let title = title.trim();
    if title.is_empty() {
        return Err(NotebookWriteError::TitleRequired);
    }

    let root = vault_root.as_ref();
    let base = notebook_slug(title);
    let mut suffix = 1;
    loop {
        let slug = if suffix == 1 {
            base.clone()
        } else {
            format!("{base}-{suffix}")
        };
        let path = format!("notebooks/{slug}.md");
        let file = secure_path(root, &path)?;
        if fs::metadata(&file).is_ok() {
            suffix += 1;
            continue;
        }

        let notebook = Notebook {
            id: slug,
            path,
            title: title.to_owned(),
            description: description.to_owned(),
            created: crate::notes::format_rfc3339(OffsetDateTime::now_utc()),
            sources: Vec::new(),
            query: query.trim().to_owned(),
        };
        let output = render_notebook(&notebook)?;
        create_dir_all_0750(file.parent().expect("notebook path has a parent"))
            .map_err(NotebookWriteError::Write)?;
        let mut options = OpenOptions::new();
        options.write(true).create_new(true);
        #[cfg(unix)]
        {
            use std::os::unix::fs::OpenOptionsExt;
            options.mode(0o600);
        }
        match options.open(&file) {
            Ok(mut handle) => {
                handle
                    .write_all(&output)
                    .map_err(NotebookWriteError::Write)?;
                return Ok(notebook);
            }
            Err(error) if error.kind() == std::io::ErrorKind::AlreadyExists => suffix += 1,
            Err(error) => return Err(NotebookWriteError::Write(error)),
        }
    }
}

fn notebook_slug(title: &str) -> String {
    let mut slug = String::new();
    let mut separator = false;
    for character in title.chars().flat_map(char::to_lowercase) {
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
        "notebook".to_owned()
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
    let mut builder = fs::DirBuilder::new();
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
