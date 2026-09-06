#![deny(unsafe_code)]

use std::{
    collections::{HashMap, HashSet},
    path::Path,
};

#[derive(Clone, Debug, Eq, PartialEq)]
pub struct ResolveDocument {
    pub path: String,
    pub title: String,
    pub aliases: Vec<String>,
    pub links: Vec<String>,
}

#[derive(Clone, Debug, Eq, Ord, PartialEq, PartialOrd)]
pub struct ResolvedNode {
    pub id: String,
    pub label: String,
}

#[derive(Clone, Debug, Eq, Ord, PartialEq, PartialOrd)]
pub struct ResolvedEdge {
    pub source: String,
    pub target: String,
}

#[derive(Debug)]
pub struct Resolver {
    nodes: HashSet<String>,
    path: HashMap<String, String>,
    base: HashMap<String, String>,
    title: HashMap<String, String>,
    alias: HashMap<String, String>,
}

impl Resolver {
    #[must_use]
    pub fn new(documents: &[ResolveDocument]) -> Self {
        let mut sorted = documents.to_vec();
        sorted.sort_by(|left, right| left.path.cmp(&right.path));
        let mut resolver = Self {
            nodes: HashSet::new(),
            path: HashMap::new(),
            base: HashMap::new(),
            title: HashMap::new(),
            alias: HashMap::new(),
        };
        for document in sorted {
            resolver.nodes.insert(document.path.clone());
            insert_forms(&mut resolver.path, &document.path, &document.path);
            let base = Path::new(&document.path)
                .file_name()
                .and_then(|value| value.to_str())
                .unwrap_or(&document.path);
            insert_forms(&mut resolver.base, base, &document.path);
            let title = document.title.trim();
            if !title.is_empty() {
                insert_forms(&mut resolver.title, title, &document.path);
            }
            for alias in document.aliases {
                let alias = alias.trim();
                if !alias.is_empty() {
                    insert_forms(&mut resolver.alias, alias, &document.path);
                }
            }
        }
        resolver
    }

    #[must_use]
    pub fn resolve(&self, target: &str) -> String {
        let trimmed = target.trim();
        let lower = trimmed.to_lowercase();
        let lower_without_extension = without_extension(trimmed).to_lowercase();
        for lookup in [&self.path, &self.base, &self.title, &self.alias] {
            if let Some(node) = lookup.get(&lower) {
                return node.clone();
            }
            if let Some(node) = lookup.get(&lower_without_extension) {
                return node.clone();
            }
        }
        if self.nodes.contains(target) {
            return target.to_owned();
        }
        let markdown = format!("{target}.md");
        if self.nodes.contains(&markdown) {
            return markdown;
        }
        target.to_owned()
    }
}

#[must_use]
pub fn resolve_graph(documents: &[ResolveDocument]) -> (Vec<ResolvedNode>, Vec<ResolvedEdge>) {
    let resolver = Resolver::new(documents);
    let mut nodes: Vec<_> = documents
        .iter()
        .map(|document| ResolvedNode {
            id: document.path.clone(),
            label: document.title.clone(),
        })
        .collect();
    nodes.sort();
    nodes.dedup_by(|left, right| left.id == right.id);
    let mut edges = Vec::new();
    for document in documents {
        for link in &document.links {
            edges.push(ResolvedEdge {
                source: document.path.clone(),
                target: resolver.resolve(link),
            });
        }
    }
    edges.sort();
    edges.dedup();
    (nodes, edges)
}

fn insert_forms(map: &mut HashMap<String, String>, value: &str, path: &str) {
    map.entry(value.to_lowercase())
        .or_insert_with(|| path.to_owned());
    map.entry(without_extension(value).to_lowercase())
        .or_insert_with(|| path.to_owned());
}

fn without_extension(value: &str) -> &str {
    let extension = Path::new(value)
        .extension()
        .and_then(|extension| extension.to_str());
    if let Some(extension) = extension {
        value
            .strip_suffix(&format!(".{extension}"))
            .unwrap_or(value)
    } else {
        value
    }
}
