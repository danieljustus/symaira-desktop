//! Platform MIME lookup matching Go's `mime.TypeByExtension` Unix initialization.

#[cfg(any(test, target_os = "linux"))]
use std::collections::HashMap;

#[derive(Debug, Default)]
struct MimeTypes {
    exact: HashMap<String, String>,
    lower: HashMap<String, String>,
}

impl MimeTypes {
    fn insert(&mut self, extension: String, media_type: String) {
        self.lower
            .insert(extension.to_lowercase(), media_type.clone());
        self.exact.insert(extension, media_type);
    }

    fn get(&self, extension: &str) -> Option<&String> {
        self.exact
            .get(extension)
            .or_else(|| self.lower.get(&extension.to_lowercase()))
    }
}
#[cfg(target_os = "linux")]
use std::{fs, sync::OnceLock};

#[cfg(target_os = "linux")]
const GLOB_FILES: &[&str] = &["/usr/local/share/mime/globs2", "/usr/share/mime/globs2"];
#[cfg(target_os = "linux")]
const TYPE_FILES: &[&str] = &[
    "/etc/mime.types",
    "/etc/apache2/mime.types",
    "/etc/apache/mime.types",
    "/etc/httpd/conf/mime.types",
];

#[cfg(target_os = "linux")]
pub(super) fn type_by_extension(extension: &str) -> Option<String> {
    static TYPES: OnceLock<MimeTypes> = OnceLock::new();
    TYPES.get_or_init(load_system_types).get(extension).cloned()
}

#[cfg(target_os = "linux")]
fn load_system_types() -> MimeTypes {
    let glob = GLOB_FILES
        .iter()
        .find_map(|path| fs::read_to_string(path).ok());
    let type_files = TYPE_FILES
        .iter()
        .filter_map(|path| fs::read_to_string(path).ok())
        .collect::<Vec<_>>();
    load_types(
        glob.as_deref(),
        &type_files.iter().map(String::as_str).collect::<Vec<_>>(),
    )
}

#[cfg(any(test, target_os = "linux"))]
fn load_types(glob_contents: Option<&str>, type_contents: &[&str]) -> MimeTypes {
    let mut types = MimeTypes::default();
    for (extension, media_type) in builtin_types() {
        types.insert(extension, media_type);
    }
    if let Some(contents) = glob_contents {
        parse_globs(contents, &mut types, is_builtin_extension);
    } else {
        for contents in type_contents {
            parse_type_file(contents, &mut types);
        }
    }
    types
}

#[cfg(any(test, target_os = "linux"))]
fn parse_globs(contents: &str, types: &mut MimeTypes, is_builtin: fn(&str) -> bool) {
    for line in contents.lines() {
        let fields: Vec<_> = line.split(':').collect();
        if fields.len() < 3 || fields[0].is_empty() || fields[2].len() < 3 {
            continue;
        }
        let glob = fields[2];
        if fields[0].starts_with('#') || !glob.starts_with("*.") {
            continue;
        }
        // Go checks the extension after the required "*." prefix. Checking
        // the complete glob would reject every valid bare extension because
        // it necessarily contains the leading '*'.
        let extension = &glob[1..];
        if extension.contains(['?', '*', '[']) {
            continue;
        }
        if is_builtin(extension) || types.exact.contains_key(extension) {
            continue;
        }
        let Some(media_type) = normalize_media_type(fields[1]) else {
            continue;
        };
        types.insert(extension.to_owned(), media_type);
    }
}

#[cfg(any(test, target_os = "linux"))]
fn parse_type_file(contents: &str, types: &mut MimeTypes) {
    for line in contents.lines() {
        let mut fields = line.split_whitespace();
        let Some(media_type) = fields.next() else {
            continue;
        };
        if media_type.starts_with('#') {
            continue;
        }
        for extension in fields {
            if extension.starts_with('#') {
                break;
            }
            let Some(media_type) = normalize_media_type(media_type) else {
                continue;
            };
            types.insert(format!(".{extension}"), media_type);
        }
    }
}

#[cfg(any(test, target_os = "linux"))]
fn normalize_media_type(input: &str) -> Option<String> {
    let (raw_base, _) = input.split_once(';').unwrap_or((input, ""));
    let base = raw_base.trim();
    if let Some((major, subtype)) = base.split_once('/') {
        if !is_token(major) || !is_token(subtype) || subtype.contains('/') {
            return None;
        }
    } else if !is_token(base) {
        return None;
    }
    let mut params: Vec<(String, String)> = Vec::new();
    let mut remainder = &input[raw_base.len()..];
    while !remainder.trim().is_empty() {
        remainder = remainder.trim_start();
        let after_semicolon = remainder.strip_prefix(';')?;
        remainder = after_semicolon.trim_start();
        if remainder.is_empty() {
            return None;
        }
        let end = remainder.find(|c: char| c == '=' || c.is_ascii_whitespace() || c == ';')?;
        let name = &remainder[..end];
        if !is_token(name) {
            return None;
        }
        remainder = remainder[end..].trim_start();
        remainder = remainder.strip_prefix('=')?.trim_start();
        let (value, after) = if let Some(value) = remainder.strip_prefix('"') {
            let mut out = String::new();
            let mut chars = value.char_indices();
            let mut end = None;
            while let Some((index, ch)) = chars.next() {
                if ch == '"' {
                    end = Some(index);
                    break;
                }
                if ch == '\\' {
                    let (_, escaped) = chars.next()?;
                    if is_tspecial(escaped) {
                        out.push(escaped);
                    } else {
                        out.push('\\');
                        out.push(escaped);
                    }
                } else if ch == '\r' || ch == '\n' {
                    return None;
                } else {
                    out.push(ch);
                }
            }
            let end = end?;
            (out, &value[end + 1..])
        } else {
            let end = remainder
                .find(|c: char| c.is_ascii_whitespace() || c == ';')
                .unwrap_or(remainder.len());
            let value = &remainder[..end];
            if value.is_empty() || !is_token(value) {
                return None;
            }
            (value.to_owned(), &remainder[end..])
        };
        let lname = name.to_ascii_lowercase();
        if let Some((_, previous)) = params.iter().find(|(n, _)| n == &lname) {
            if previous != &value {
                return None;
            }
        } else {
            params.push((lname, value));
        }
        remainder = after;
    }
    // Go preserves the original value unless text/* lacks a non-empty,
    // lower-case charset parameter and therefore invokes FormatMediaType.
    let Some((major, subtype)) = base.split_once('/') else {
        return Some(input.to_owned());
    };
    if !input.starts_with("text/")
        || params
            .iter()
            .any(|(name, value)| name == "charset" && !value.is_empty())
    {
        return Some(input.to_owned());
    }
    params.push(("charset".to_owned(), "utf-8".to_owned()));
    params.sort_by(|a, b| a.0.cmp(&b.0));
    let mut out = format!(
        "{}/{}",
        major.to_ascii_lowercase(),
        subtype.to_ascii_lowercase()
    );
    for (name, value) in params {
        out.push_str("; ");
        out.push_str(&name);
        out.push('=');
        if is_token(&value) {
            out.push_str(&value);
        } else {
            out.push('\"');
            out.push_str(&value.replace('\\', "\\\\").replace('\"', "\\\""));
            out.push('\"');
        }
    }
    Some(out)
}
#[cfg(any(test, target_os = "linux"))]
fn is_token(value: &str) -> bool {
    !value.is_empty()
        && value
            .bytes()
            .all(|byte| matches!(byte, 0x21..=0x7e) && !b"()<>@,;:\\\"/[]?= \t".contains(&byte))
}

#[cfg(any(test, target_os = "linux"))]
fn is_tspecial(value: char) -> bool {
    matches!(
        value,
        '(' | ')' | '<' | '>' | '@' | ',' | ';' | ':' | '\\' | '"' | '/' | '[' | ']' | '?' | '='
    )
}

#[cfg(any(test, target_os = "linux"))]
fn builtin_types() -> HashMap<String, String> {
    [
        (".ai", "application/postscript"),
        (".apk", "application/vnd.android.package-archive"),
        (".apng", "image/apng"),
        (".avif", "image/avif"),
        (".bin", "application/octet-stream"),
        (".bmp", "image/bmp"),
        (".com", "application/octet-stream"),
        (".css", "text/css; charset=utf-8"),
        (".csv", "text/csv; charset=utf-8"),
        (".doc", "application/msword"),
        (
            ".docx",
            "application/vnd.openxmlformats-officedocument.wordprocessingml.document",
        ),
        (".ehtml", "text/html; charset=utf-8"),
        (".eml", "message/rfc822"),
        (".eps", "application/postscript"),
        (".exe", "application/octet-stream"),
        (".flac", "audio/flac"),
        (".gif", "image/gif"),
        (".gz", "application/gzip"),
        (".htm", "text/html; charset=utf-8"),
        (".html", "text/html; charset=utf-8"),
        (".ico", "image/vnd.microsoft.icon"),
        (".ics", "text/calendar; charset=utf-8"),
        (".jfif", "image/jpeg"),
        (".jpeg", "image/jpeg"),
        (".jpg", "image/jpeg"),
        (".js", "text/javascript; charset=utf-8"),
        (".json", "application/json"),
        (".m4a", "audio/mp4"),
        (".mjs", "text/javascript; charset=utf-8"),
        (".mp3", "audio/mpeg"),
        (".mp4", "video/mp4"),
        (".oga", "audio/ogg"),
        (".ogg", "audio/ogg"),
        (".ogv", "video/ogg"),
        (".opus", "audio/ogg"),
        (".pdf", "application/pdf"),
        (".pjp", "image/jpeg"),
        (".pjpeg", "image/jpeg"),
        (".png", "image/png"),
        (".ppt", "application/vnd.ms-powerpoint"),
        (
            ".pptx",
            "application/vnd.openxmlformats-officedocument.presentationml.presentation",
        ),
        (".ps", "application/postscript"),
        (".rdf", "application/rdf+xml"),
        (".rtf", "application/rtf"),
        (".shtml", "text/html; charset=utf-8"),
        (".svg", "image/svg+xml"),
        (".text", "text/plain; charset=utf-8"),
        (".tif", "image/tiff"),
        (".tiff", "image/tiff"),
        (".txt", "text/plain; charset=utf-8"),
        (".vtt", "text/vtt; charset=utf-8"),
        (".wasm", "application/wasm"),
        (".wav", "audio/wav"),
        (".webm", "audio/webm"),
        (".webp", "image/webp"),
        (".xbl", "text/xml; charset=utf-8"),
        (".xbm", "image/x-xbitmap"),
        (".xht", "application/xhtml+xml"),
        (".xhtml", "application/xhtml+xml"),
        (".xls", "application/vnd.ms-excel"),
        (
            ".xlsx",
            "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet",
        ),
        (".xml", "text/xml; charset=utf-8"),
        (".xsl", "text/xml; charset=utf-8"),
        (".zip", "application/zip"),
    ]
    .into_iter()
    .map(|(extension, media_type)| (extension.to_owned(), media_type.to_owned()))
    .collect()
}

#[cfg(any(test, target_os = "linux"))]
fn is_builtin_extension(extension: &str) -> bool {
    builtin_types().iter().any(|(known, _)| known == extension)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn injected_glob_loader_selects_first_entry_and_falls_back_to_type_files() {
        let glob = "50:application/old:*.md\n40:text/markdown:*.md\n50:application/json:*.json";
        let types = load_types(Some(glob), &[]);
        assert_eq!(types.get(".md").unwrap(), "application/old");
        assert_eq!(types.get(".json").unwrap(), "application/json");

        let types = load_types(None, &["text/markdown md\napplication/custom custom"]);
        assert_eq!(types.get(".md").unwrap(), "text/markdown; charset=utf-8");
        assert_eq!(types.get(".custom").unwrap(), "application/custom");
    }

    #[test]
    fn injected_globs_do_not_override_builtins_and_type_files_do() {
        let types = load_types(Some("50:text/other:*.json\n50:text/markdown:*.md"), &[]);
        assert_eq!(types.get(".json").unwrap(), "application/json");
        assert_eq!(types.get(".md").unwrap(), "text/markdown; charset=utf-8");

        let types = load_types(None, &["application/custom json"]);
        assert_eq!(types.get(".json").unwrap(), "application/custom");
    }

    #[test]
    fn lookup_prefers_exact_case_then_lowercase_fallback() {
        let mut types = MimeTypes::default();
        types.insert(".Md".to_owned(), "application/exact".to_owned());
        types.insert(".md".to_owned(), "text/lower".to_owned());
        assert_eq!(
            types.get(".Md").map(String::as_str),
            Some("application/exact")
        );
        assert_eq!(types.get(".MD").map(String::as_str), Some("text/lower"));
    }

    #[test]
    fn media_type_parameters_match_go_stdlib_oracle() {
        let cases = [
            ("text/custom", Some("text/custom; charset=utf-8")),
            (
                "text/custom; foo=bar",
                Some("text/custom; charset=utf-8; foo=bar"),
            ),
            ("text/custom; charset=", None),
            (
                "text/custom; Charset=US-ASCII",
                Some("text/custom; Charset=US-ASCII"),
            ),
            (
                "text/custom; foo=bar; foo=bar",
                Some("text/custom; charset=utf-8; foo=bar"),
            ),
            ("text/custom; foo=bar; foo=baz", None),
            (
                "text/custom; foo={}",
                Some("text/custom; charset=utf-8; foo={}"),
            ),
            ("text/custom; foo", None),
            ("text/custom; =bar", None),
            (
                r#"text/custom; foo="\\name""#,
                Some(r#"text/custom; charset=utf-8; foo="\\name""#),
            ),
            (
                r#"text/custom; foo="a;b""#,
                Some(r#"text/custom; charset=utf-8; foo="a;b""#),
            ),
            (
                "text/custom; title*=utf-8''caf%C3%A9",
                Some("text/custom; charset=utf-8; title*=utf-8''caf%C3%A9"),
            ),
            ("application/custom", Some("application/custom")),
            (
                "application/custom; foo=bar",
                Some("application/custom; foo=bar"),
            ),
            ("application/custom; charset=", None),
            ("foo", Some("foo")),
        ];
        for (input, expected) in cases {
            assert_eq!(
                normalize_media_type(input).as_deref(),
                expected,
                "input {input:?}"
            );
        }
    }

    #[test]
    fn malformed_and_pattern_globs_are_ignored() {
        let mut types = MimeTypes::default();
        for (extension, media_type) in builtin_types() {
            types.insert(extension, media_type);
        }
        parse_globs(
            "#comment\nnot:a:glob\n50:text/plain:*.[ch]",
            &mut types,
            |_| false,
        );
        assert!(!types.exact.contains_key("*.[ch]"));
    }
}
