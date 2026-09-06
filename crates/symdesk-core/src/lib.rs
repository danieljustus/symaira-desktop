#![deny(unsafe_code)]

//! Language-neutral output contracts for the staged Symaira Desktop Rust port.

use serde::Serialize;

pub mod config;
pub mod document_format;
pub mod german;
pub mod query;
pub mod simhash;
pub mod textnorm;

#[derive(Debug, Serialize)]
struct VersionDocument<'a> {
    tool: &'a str,
    version: &'a str,
    schema_version: u8,
}

/// Renders the stable plain-text version response.
#[must_use]
pub fn render_version_text(tool: &str, version: &str) -> String {
    format!("{tool} {version}\n")
}

/// Renders the stable schema-v1 JSON version response.
///
/// # Errors
///
/// Returns an error only if serialization of the fixed document fails.
pub fn render_version_json(tool: &str, version: &str) -> Result<String, serde_json::Error> {
    let document = VersionDocument {
        tool,
        version,
        schema_version: 1,
    };
    let mut output = serde_json::to_string(&document)?;
    output.push('\n');
    Ok(output)
}

#[cfg(test)]
mod tests {
    use super::{render_version_json, render_version_text};

    #[test]
    fn text_contract_is_exact() {
        assert_eq!(render_version_text("symdesk", "1.2.3"), "symdesk 1.2.3\n");
    }

    #[test]
    fn json_contract_is_exact() {
        assert_eq!(
            render_version_json("symroom", "dev").expect("serialize fixed version document"),
            "{\"tool\":\"symroom\",\"version\":\"dev\",\"schema_version\":1}\n"
        );
    }
}
