#![deny(unsafe_code)]

use std::{collections::BTreeMap, path::Path};

use noyalib::Value;
use serde::{Deserialize, Serialize, de::DeserializeOwned};
use thiserror::Error;
use unicode_general_category::{GeneralCategory, get_general_category};

use crate::{Document, VaultError, parse_bytes, split_frontmatter};

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct Filter {
    pub key: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub operator: String,
    pub value: String,
}

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct FilterGroup {
    pub operator: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub filters: Vec<Filter>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub groups: Vec<FilterGroup>,
}

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct Template {
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub r#ref: String,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub defaults: BTreeMap<String, String>,
}

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct Sort {
    pub key: String,
    pub ascending: bool,
}

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct ComputedColumn {
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub formula: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub rollup: String,
}

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct View {
    pub id: String,
    pub name: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub r#type: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub group_by: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub date_property: String,
    #[serde(default, skip_serializing_if = "BTreeMap::is_empty")]
    pub computed: BTreeMap<String, ComputedColumn>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub filters: Vec<Filter>,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub filter_group: Option<FilterGroup>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub sorts: Vec<Sort>,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub columns: Vec<String>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub source: String,
    #[serde(default, skip_serializing_if = "Option::is_none")]
    pub template: Option<Template>,
}

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct PropertyConfig {
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub r#type: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub label: String,
    #[serde(default, skip_serializing_if = "Vec::is_empty")]
    pub options: Vec<String>,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub description: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub default: String,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct Base {
    pub id: String,
    pub path: String,
    pub title: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
    pub created: String,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub tags: Vec<String>,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub properties: BTreeMap<String, PropertyConfig>,
    pub views: Vec<View>,
    #[serde(skip_serializing)]
    pub extras: BTreeMap<String, Value>,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct Notebook {
    pub id: String,
    pub path: String,
    pub title: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub description: String,
    pub created: String,
    pub sources: Vec<String>,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub query: String,
}

#[derive(Default, Deserialize)]
struct NotebookFrontmatter {
    #[serde(default)]
    notebook_id: String,
    #[serde(default)]
    description: String,
    #[serde(default)]
    sources: Vec<Value>,
    #[serde(default)]
    query: String,
}

#[derive(Default, Deserialize)]
struct BaseFrontmatter {
    #[serde(default)]
    title: String,
    #[serde(default)]
    created: String,
    #[serde(default)]
    base_id: String,
    #[serde(default)]
    description: String,
    #[serde(default)]
    properties: BTreeMap<String, PropertyConfig>,
    #[serde(default)]
    views: Vec<View>,
    #[serde(flatten)]
    extras: BTreeMap<String, Value>,
}

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct Coverage {
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub from: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub to: String,
}

#[derive(Clone, Debug, Default, Deserialize, Eq, PartialEq, Serialize)]
pub struct Provenance {
    pub imported_at: String,
    #[serde(default, skip_serializing_if = "String::is_empty")]
    pub source_name: String,
    pub source_sha256: String,
}

#[derive(Clone, Debug, Eq, PartialEq, Serialize)]
pub struct DatasetHandle {
    pub path: String,
    pub slug: String,
    pub title: String,
    pub created: String,
    pub source: String,
    #[serde(skip_serializing_if = "BTreeMap::is_empty")]
    pub schema: BTreeMap<String, PropertyConfig>,
    pub coverage: Coverage,
    pub provenance: Provenance,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub identity_field: String,
    #[serde(skip_serializing_if = "String::is_empty")]
    pub refresh_command: String,
    pub sensitivity: String,
    pub retention_rule: String,
}

#[derive(Default, Deserialize)]
struct DatasetFrontmatter {
    #[serde(default)]
    r#type: String,
    #[serde(default)]
    title: String,
    #[serde(default)]
    created: String,
    #[serde(default)]
    dataset_id: String,
    #[serde(default)]
    source: String,
    #[serde(default)]
    schema: BTreeMap<String, PropertyConfig>,
    #[serde(default)]
    coverage: Coverage,
    #[serde(default)]
    provenance: Provenance,
    #[serde(default)]
    identity_field: String,
    #[serde(default)]
    refresh_command: String,
    #[serde(default)]
    sensitivity: String,
    #[serde(default)]
    retention_rule: String,
}

#[derive(Debug, Error)]
pub enum TypedVaultError {
    #[error(transparent)]
    Vault(#[from] VaultError),
    #[error("{0}")]
    Contract(String),
    #[error("{context}: {detail}")]
    Yaml {
        context: &'static str,
        detail: String,
    },
}

/// Parses the read-only base-note contract.
///
/// # Errors
/// Returns a parser or base identity error.
pub fn parse_base(path: &str, input: &[u8]) -> Result<Base, TypedVaultError> {
    let document = parse_bytes(path, input)?;
    let note_type = value_string(&document, "type");
    let base_id = value_string(&document, "base_id");
    if note_type != "base" && base_id.is_empty() {
        return Err(TypedVaultError::Contract(format!(
            "{path} is not a base note (type={note_type:?})"
        )));
    }
    let frontmatter: BaseFrontmatter = decode_frontmatter(input, "parse base frontmatter")?;
    let id = if frontmatter.base_id.is_empty() {
        file_stem_markdown(path)
    } else {
        frontmatter.base_id
    };
    Ok(Base {
        id,
        path: path.to_owned(),
        title: if frontmatter.title.is_empty() {
            document.title
        } else {
            frontmatter.title
        },
        description: frontmatter.description,
        created: if frontmatter.created.is_empty() {
            document.created
        } else {
            frontmatter.created
        },
        tags: document.tags,
        properties: frontmatter.properties,
        views: frontmatter.views,
        extras: frontmatter.extras,
    })
}

/// Parses a read-only notebook note and preserves the Go source ordering rules.
///
/// # Errors
/// Returns a parser or notebook identity error.
pub fn parse_notebook(path: &str, input: &[u8]) -> Result<Notebook, TypedVaultError> {
    let document = parse_bytes(path, input)?;
    let note_type = value_string(&document, "type");
    if note_type != "notebook" {
        return Err(TypedVaultError::Contract(format!(
            "{path} is not a notebook note (type={note_type:?})"
        )));
    }
    let frontmatter: NotebookFrontmatter = decode_frontmatter(input, "parse notebook frontmatter")?;
    let mut sources: Vec<String> = frontmatter
        .sources
        .iter()
        .filter_map(Value::as_str)
        .filter(|source| !source.is_empty())
        .map(ToOwned::to_owned)
        .collect();
    sources.sort();
    Ok(Notebook {
        id: if frontmatter.notebook_id.is_empty() {
            file_stem_markdown(path)
        } else {
            frontmatter.notebook_id
        },
        path: path.to_owned(),
        title: document.title,
        description: frontmatter.description,
        created: document.created,
        sources,
        query: frontmatter.query,
    })
}

/// Parses and validates a contract-v6 dataset handle, including legacy policy defaults.
///
/// # Errors
/// Returns exact contract errors for missing or invalid persisted metadata.
pub fn parse_dataset_handle(path: &str, input: &[u8]) -> Result<DatasetHandle, TypedVaultError> {
    let document = parse_bytes(path, input)?;
    if document.document_type != "dataset" {
        return Err(TypedVaultError::Contract(format!(
            "{path} is not a dataset handle"
        )));
    }
    let mut frontmatter: DatasetFrontmatter = decode_frontmatter(input, "parse dataset handle")?;
    if frontmatter.r#type != "dataset" || frontmatter.source.is_empty() {
        return Err(TypedVaultError::Contract(format!(
            "dataset handle {path} is missing type or source"
        )));
    }
    if frontmatter.title.is_empty() || frontmatter.dataset_id.is_empty() {
        return Err(TypedVaultError::Contract(format!(
            "dataset handle {path} is missing title or dataset_id"
        )));
    }
    let has_sensitivity = document.frontmatter.contains_key("sensitivity");
    let has_retention = document.frontmatter.contains_key("retention_rule");
    match (has_sensitivity, has_retention) {
        (false, false) => {
            frontmatter.sensitivity = "restricted".to_owned();
            frontmatter.retention_rule = "default".to_owned();
        }
        (true, false) => {
            return Err(TypedVaultError::Contract(format!(
                "dataset handle {path}: dataset handle policy metadata is incomplete; missing retention_rule"
            )));
        }
        (false, true) => {
            return Err(TypedVaultError::Contract(format!(
                "dataset handle {path}: dataset handle policy metadata is incomplete; missing sensitivity"
            )));
        }
        (true, true) => {}
    }
    if !matches!(
        frontmatter.sensitivity.as_str(),
        "public" | "internal" | "confidential" | "restricted"
    ) {
        return Err(TypedVaultError::Contract(format!(
            "dataset handle {path}: invalid dataset sensitivity {:?} (valid: public, internal, confidential, restricted)",
            frontmatter.sensitivity
        )));
    }
    if frontmatter.retention_rule.chars().any(|character| {
        character.is_control()
            || get_general_category(character) == GeneralCategory::Format
            || matches!(character, '/' | '\\')
    }) {
        return Err(TypedVaultError::Contract(format!(
            "dataset handle {path}: invalid dataset retention_rule {:?}: control characters and path separators are not allowed",
            frontmatter.retention_rule
        )));
    }
    if frontmatter.retention_rule.trim().is_empty() {
        return Err(TypedVaultError::Contract(format!(
            "dataset handle {path}: dataset handle requires retention_rule"
        )));
    }
    Ok(DatasetHandle {
        path: path.to_owned(),
        slug: frontmatter.dataset_id,
        title: frontmatter.title,
        created: frontmatter.created,
        source: frontmatter.source,
        schema: frontmatter.schema,
        coverage: frontmatter.coverage,
        provenance: frontmatter.provenance,
        identity_field: frontmatter.identity_field,
        refresh_command: frontmatter.refresh_command,
        sensitivity: frontmatter.sensitivity,
        retention_rule: frontmatter.retention_rule,
    })
}

fn decode_frontmatter<T: DeserializeOwned + 'static>(
    input: &[u8],
    context: &'static str,
) -> Result<T, TypedVaultError> {
    let (frontmatter, _, found) = split_frontmatter(input);
    if !found || frontmatter.is_empty() {
        return noyalib::from_slice(b"{}\n").map_err(|error| TypedVaultError::Yaml {
            context,
            detail: error.to_string(),
        });
    }
    noyalib::from_slice(&frontmatter).map_err(|error| TypedVaultError::Yaml {
        context,
        detail: error.to_string(),
    })
}

fn value_string(document: &Document, key: &str) -> String {
    document
        .frontmatter
        .get(key)
        .and_then(Value::as_str)
        .unwrap_or_default()
        .to_owned()
}

fn file_stem_markdown(path: &str) -> String {
    let base = Path::new(path)
        .file_name()
        .and_then(|name| name.to_str())
        .unwrap_or(path);
    base.strip_suffix(".md").unwrap_or(base).to_owned()
}
