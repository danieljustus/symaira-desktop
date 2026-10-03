#![deny(unsafe_code)]

use std::{collections::BTreeMap, fs, io, path::Path};

use percent_encoding::percent_decode_str;
use pulldown_cmark::{Event, LinkType, Parser, Tag};

use crate::{go_lowercase, secure_path, walk_all};

/// Reads CommonMark link/image destinations, excluding code and raw HTML.
#[must_use]
pub fn extract_markdown_links(body: &str) -> Vec<String> {
    Parser::new(body)
        .filter_map(|event| match event {
            Event::Start(Tag::Link {
                link_type: LinkType::Email,
                dest_url,
                ..
            }) => Some(format!("mailto:{dest_url}")),
            Event::Start(Tag::Link { dest_url, .. } | Tag::Image { dest_url, .. }) => {
                Some(dest_url.into_string())
            }
            _ => None,
        })
        .collect()
}

/// Ignores URI schemes, remote network URLs and same-document fragments.
/// Percent escapes are decoded after removing fragments, so `%23` remains a
/// literal filename character. Malformed escapes remain unresolved local links.
#[must_use]
pub fn markdown_link_target(destination: &str) -> Option<String> {
    if destination.starts_with("//") {
        return None;
    }
    if let Some((scheme, _)) = destination.split_once(':')
        && scheme
            .as_bytes()
            .first()
            .is_some_and(u8::is_ascii_alphabetic)
        && scheme
            .bytes()
            .all(|c| c.is_ascii_alphanumeric() || b"+.-".contains(&c))
    {
        return None;
    }
    let destination = destination.split(['#', '?']).next().unwrap_or_default();
    if destination.is_empty() {
        return None;
    }
    let bytes = destination.as_bytes();
    for (index, byte) in bytes.iter().enumerate() {
        if *byte == b'%'
            && !bytes
                .get(index + 1..index + 3)
                .is_some_and(|pair| pair.iter().all(u8::is_ascii_hexdigit))
        {
            return Some(destination.to_owned());
        }
    }
    Some(
        percent_decode_str(destination)
            .decode_utf8()
            .map_or_else(|_| destination.to_owned(), |decoded| decoded.into_owned()),
    )
}

/// Vault inventory respects existing walk ignore rules. Each candidate is
/// confined again when checked, including file symlinks and root aliases.
#[derive(Debug)]
pub struct MarkdownLinkResolver<'a> {
    root: &'a Path,
    files: BTreeMap<String, Vec<String>>,
}

impl<'a> MarkdownLinkResolver<'a> {
    /// # Errors
    /// Propagates vault inventory errors.
    pub fn new(root: &'a Path) -> io::Result<Self> {
        let mut files: BTreeMap<String, Vec<String>> = BTreeMap::new();
        for entry in walk_all(root)? {
            let file = entry
                .path
                .to_string_lossy()
                .replace(std::path::MAIN_SEPARATOR, "/");
            let full = go_lowercase(&file);
            let base = go_lowercase(file.rsplit('/').next().unwrap_or(&file));
            files.entry(full.clone()).or_default().push(file.clone());
            if full != base {
                files.entry(base).or_default().push(file);
            }
        }
        Ok(Self { root, files })
    }

    #[must_use]
    pub fn check(&self, destination: &str) -> Option<bool> {
        markdown_link_target(destination).map(|target| self.exists(&target))
    }

    #[must_use]
    pub fn exists(&self, target: &str) -> bool {
        if target.contains(['\\', '\0']) || target.starts_with('/') {
            return false;
        }
        let mut parts = Vec::new();
        for part in target.split('/') {
            match part {
                "" | "." => {}
                ".." => {
                    if parts.pop().is_none() {
                        return false;
                    }
                }
                value => parts.push(value),
            }
        }
        if parts.is_empty() {
            return false;
        }
        let cleaned = parts.join("/");
        self.files
            .get(&go_lowercase(&cleaned))
            .is_some_and(|candidates| {
                candidates.iter().any(|candidate| {
                    secure_path(self.root, candidate).is_ok_and(|path| {
                        fs::metadata(path).is_ok_and(|metadata| metadata.is_file())
                    })
                })
            })
    }
}
