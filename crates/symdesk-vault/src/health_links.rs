#![deny(unsafe_code)]

use std::{collections::HashSet, path::Path};

#[derive(Clone, Debug, Default, Eq, PartialEq)]
pub struct LinkInventory {
    pub paths: Vec<String>,
    pub titles: Vec<String>,
    pub aliases: Vec<String>,
    pub attachments: Vec<String>,
}

#[derive(Debug)]
pub struct HealthLinkResolver {
    paths: HashSet<String>,
    titles: HashSet<String>,
    aliases: HashSet<String>,
    attachments: HashSet<String>,
}

impl HealthLinkResolver {
    #[must_use]
    pub fn new(inventory: &LinkInventory) -> Self {
        Self {
            paths: lower_set(&inventory.paths),
            titles: lower_set(&inventory.titles),
            aliases: lower_set(&inventory.aliases),
            attachments: lower_set(&inventory.attachments),
        }
    }

    #[must_use]
    pub fn exists(&self, target: &str) -> bool {
        let target_lower = target.to_lowercase();
        if self.paths.contains(&target_lower) {
            return true;
        }
        let target_without_extension = without_extension(&target_lower);
        if self.paths.contains(target_without_extension) || self.titles.contains(&target_lower) {
            return true;
        }
        if self.attachments.contains(&target_lower) {
            return true;
        }
        let target_base = slash_base(target).to_lowercase();
        if self.attachments.contains(&target_base) {
            return true;
        }
        if self.aliases.contains(&target_lower)
            || self.aliases.contains(target_without_extension)
            || self.aliases.contains(&target_base)
        {
            return true;
        }
        self.aliases.contains(without_extension(&target_base))
    }

    #[must_use]
    pub fn check(&self, raw_link: &str) -> Option<bool> {
        normalize_health_link_target(raw_link).map(|target| self.exists(&target))
    }
}

#[must_use]
pub fn normalize_health_link_target(link: &str) -> Option<String> {
    let mut target = link.trim();
    if let Some((before, _)) = target.split_once('|') {
        target = before;
    }
    if let Some(index) = target.find(['#', '^']) {
        target = &target[..index];
    }
    target = target.trim();
    if target.is_empty() || target.contains("://") || target.starts_with("mailto:") {
        return None;
    }
    if !target.contains('/') && Path::new(target).extension().is_none() {
        return None;
    }
    Some(clean_slash_path(target))
}

fn lower_set(values: &[String]) -> HashSet<String> {
    values.iter().map(|value| value.to_lowercase()).collect()
}

fn without_extension(value: &str) -> &str {
    let Some(extension) = Path::new(value)
        .extension()
        .and_then(|extension| extension.to_str())
    else {
        return value;
    };
    value
        .strip_suffix(&format!(".{extension}"))
        .unwrap_or(value)
}

fn slash_base(value: &str) -> &str {
    value.rsplit('/').next().unwrap_or(value)
}

fn clean_slash_path(value: &str) -> String {
    let normalized = value.replace('\\', "/");
    let absolute = normalized.starts_with('/');
    let mut cleaned: Vec<&str> = Vec::new();
    for component in normalized.split('/') {
        match component {
            "" | "." => {}
            ".." if cleaned.last().is_some_and(|last| *last != "..") => {
                cleaned.pop();
            }
            ".." if !absolute => cleaned.push(".."),
            ".." => {}
            part => cleaned.push(part),
        }
    }
    let mut result = cleaned.join("/");
    if absolute {
        result.insert(0, '/');
    }
    if result.is_empty() {
        result.push('.');
    }
    result.strip_prefix("./").unwrap_or(&result).to_owned()
}
