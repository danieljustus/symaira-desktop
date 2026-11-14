use std::path::Path;

// Replace the disposable root only. Never repair actual output separators.
pub fn normalize(text: &str, root: &Path) -> String {
    let root = root.to_string_lossy();
    let escaped = root.replace('\\', "\\\\");
    let mut text = text.to_owned();
    for spelling in [escaped.replace('\\', "\\\\"), escaped, root.into_owned()] {
        text = text.replace(&spelling, "$ROOT");
    }
    text
}
