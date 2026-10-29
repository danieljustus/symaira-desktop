//! Portable relative path fields in the docs-read fixture, not arbitrary text.
pub fn normalize(text: &str) -> String {
    text.replace(r"Path:nested\beta.md ", "Path:nested/beta.md ")
        .replace(r#""path":"nested\\beta.md""#, r#""path":"nested/beta.md""#)
}
