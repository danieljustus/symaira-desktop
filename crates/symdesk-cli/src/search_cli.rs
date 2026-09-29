use std::{
    collections::{BTreeMap, HashSet},
    path::{Path, PathBuf},
    time::Duration,
};

use symdesk_index::{
    RetrievalDb, RetrievalEmbeddingConfig, RetrievalHybridSearchResult, SearchHit,
    go_simple_lowercase, index_location_for_vault, local_hash_embedding,
    retrieval_embedding_config,
};
use symdesk_protocol::{embed_local_ollama, local_ollama_embeddings_endpoint};

const SEARCH_LIMIT: i64 = 5;
const DEFAULT_QUERY_DIMENSION: usize = 768;

#[derive(Clone, Debug, PartialEq, serde::Serialize)]
pub struct CliSearchHit {
    pub path: String,
    pub title: String,
    pub snippet: String,
    pub score: f64,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub anchor: Option<CliSearchAnchor>,
    #[serde(skip_serializing_if = "Vec::is_empty")]
    pub metadata_matches: Vec<String>,
    #[serde(skip_serializing_if = "Option::is_none")]
    pub source_type: Option<&'static str>,
    #[serde(skip_serializing_if = "is_false")]
    pub read_only: bool,
}

#[derive(Clone, Debug, Eq, PartialEq, serde::Serialize)]
pub struct CliSearchAnchor {
    pub kind: String,
    pub value: String,
}

fn is_false(value: &bool) -> bool {
    !value
}

/// Returns the Go search hint for malformed query syntax.
#[must_use]
pub fn syntax_fallback_hint(query: &str) -> Option<&'static str> {
    symdesk_core::query::parse(query)
        .err()
        .map(|_| "Search syntax was invalid, so this was searched as plain full text.")
}

/// Searches through the retrieval index for an eligible plain-text query.
/// `None` means Go would use its sidecar-only fallback for this query or index.
pub fn hybrid_search(
    vault: &Path,
    query: &str,
    sources: &[symdesk_index::SearchSource],
    sidecar: &symdesk_index::Sidecar,
) -> Result<Option<Vec<CliSearchHit>>, String> {
    let plan = symdesk_core::query::parse(query);
    if query.trim().is_empty() || !matches!(plan, Ok(ref parsed) if !parsed.requires_sidecar()) {
        return Ok(None);
    }

    let environment = std::env::vars().collect::<BTreeMap<_, _>>();
    let cwd =
        std::env::current_dir().map_err(|error| format!("read current directory: {error}"))?;
    let config = retrieval_embedding_config(&environment, &cwd)
        .map_err(|error| format!("load retrieval configuration: {error}"))?;
    if config.expand_query || config.rerank_query {
        return Err(
            "hybrid search does not support configured query expansion or reranking yet; disable expand_query and rerank_query".to_owned(),
        );
    }
    if config.vector_backend != "sqlite" || config.vector_quantization != "off" {
        return Err(format!(
            "hybrid search does not support configured vector backend {:?} with quantization {:?}; use sqlite with vector_quantization=off",
            config.vector_backend, config.vector_quantization
        ));
    }
    if local_ollama_embeddings_endpoint(&config.ollama_url).is_none() {
        return Err(format!(
            "hybrid search does not support configured embedding endpoint {:?}; configure an HTTP Ollama endpoint on loopback",
            config.ollama_url
        ));
    }

    let temp_root = std::env::temp_dir();
    let index_path =
        index_location_for_vault(&vault.to_string_lossy(), &environment, &cwd, &temp_root)
            .map_err(|error| format!("resolve retrieval index: {error}"))?;
    let index = RetrievalDb::open_at(index_path)
        .map_err(|error| format!("open retrieval index: {error}"))?;

    // Go checks mixed spaces before it asks the embedding backend. A mixed
    // index takes the existing lexical fallback path without a provider call.
    let spaces = match index.detect_mixed_embedding_spaces() {
        Ok(spaces) if spaces.len() <= 1 => spaces,
        Ok(_) | Err(_) => return Ok(None),
    };

    let query_vector = query_embedding(query, &config)?;
    let query_model = if query_vector.model == "local-hash" {
        "local-hash"
    } else {
        &config.model
    };
    let index_has_ollama = spaces.iter().any(|entry| {
        entry
            .space
            .split_once('/')
            .is_some_and(|(_, model)| model != "local-hash")
    });
    let index_has_fallback = spaces.iter().any(|entry| {
        entry
            .space
            .split_once('/')
            .is_some_and(|(_, model)| model == "local-hash")
    });
    if query_model == "local-hash" && index_has_ollama && !index_has_fallback {
        eprintln!(
            "warning: query embedding fell back to local hash while the index uses an Ollama model; semantic scores may be unreliable"
        );
    }

    let mut roots = vec![
        vault
            .canonicalize()
            .map_err(|error| format!("resolve vault root: {error}"))?,
    ];
    for source in sources {
        let path = PathBuf::from(&source.path)
            .canonicalize()
            .map_err(|error| format!("resolve registered source {:?}: {error}", source.path))?;
        if !roots.contains(&path) {
            roots.push(path);
        }
    }

    let scoped = !sources.is_empty();
    let mut results = Vec::<(CliSearchHit, String)>::new();
    let mut by_path = HashSet::new();
    for root in &roots {
        let prefix = if scoped {
            let mut prefix = root.to_string_lossy().into_owned();
            if !prefix.ends_with(std::path::MAIN_SEPARATOR) {
                prefix.push(std::path::MAIN_SEPARATOR);
            }
            prefix
        } else {
            String::new()
        };
        let response = match index.search_hybrid_with_path(
            query,
            &query_vector.vector,
            query_model,
            &prefix,
            SEARCH_LIMIT,
        ) {
            Ok(response) => response,
            Err(_) => return Ok(None),
        };
        for warning in response.warnings {
            eprintln!("{warning}");
        }
        for result in response.results {
            let Some(projected) = project_hit(vault, &roots, query, result, sidecar) else {
                continue;
            };
            if scoped && !by_path.insert(projected.1.clone()) {
                continue;
            }
            results.push(projected);
        }
    }
    results.sort_by(|left, right| {
        right
            .0
            .score
            .partial_cmp(&left.0.score)
            .unwrap_or(std::cmp::Ordering::Equal)
    });
    results.truncate(SEARCH_LIMIT as usize);
    if results.is_empty() {
        return Ok(None);
    }
    Ok(Some(results.into_iter().map(|(hit, _)| hit).collect()))
}

pub fn lexical_hits(
    hits: Vec<SearchHit>,
    sources: &[symdesk_index::SearchSource],
) -> Vec<CliSearchHit> {
    hits.into_iter()
        .map(|hit| {
            let external = sources
                .iter()
                .any(|source| Path::new(&hit.path).starts_with(Path::new(&source.path)));
            CliSearchHit {
                path: hit.path,
                title: hit.title,
                snippet: hit.snippet,
                score: 0.0,
                anchor: None,
                metadata_matches: Vec::new(),
                source_type: external.then_some("external"),
                read_only: external,
            }
        })
        .collect()
}

struct QueryVector {
    vector: Vec<f32>,
    model: String,
}

fn query_embedding(query: &str, config: &RetrievalEmbeddingConfig) -> Result<QueryVector, String> {
    let runtime = tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
        .map_err(|error| format!("create query embedding runtime: {error}"))?;
    let inputs = [query.to_owned()];
    let dimensions = config.embedding_dim;
    let embedded = runtime.block_on(embed_local_ollama(
        &config.ollama_url,
        &config.model,
        &inputs,
        dimensions,
        Duration::from_secs(config.timeout_seconds),
    ));
    if let Ok(mut vectors) = embedded
        && vectors.len() == 1
    {
        let vector = vectors.remove(0);
        if dimensions.is_none_or(|expected| vector.len() == expected) {
            return Ok(QueryVector {
                vector,
                model: config.model.clone(),
            });
        }
        if let Some(expected) = dimensions {
            eprintln!(
                "engine: embedding dimension mismatch: expected {expected}, got {}; falling back to local hash vector",
                vector.len()
            );
        }
    }
    let dimension = dimensions.unwrap_or(DEFAULT_QUERY_DIMENSION);
    let vector = local_hash_embedding(query, dimension)
        .map_err(|error| format!("create local query embedding: {error}"))?;
    Ok(QueryVector {
        vector,
        model: "local-hash".to_owned(),
    })
}

fn project_hit(
    vault: &Path,
    roots: &[PathBuf],
    query: &str,
    result: RetrievalHybridSearchResult,
    sidecar: &symdesk_index::Sidecar,
) -> Option<(CliSearchHit, String)> {
    let raw_path = PathBuf::from(&result.chunk.document_path);
    let path = resolve_result_path(vault, &raw_path)?;
    if !path.is_file() || !roots.iter().any(|root| path.starts_with(root)) {
        return None;
    }
    let source = roots.iter().skip(1).any(|root| path.starts_with(root));
    let display_path = if source {
        path.to_string_lossy().into_owned()
    } else {
        path.strip_prefix(vault)
            .ok()?
            .to_string_lossy()
            .into_owned()
    };
    let title = sidecar
        .get_title(&raw_path.to_string_lossy())
        .or_else(|_| sidecar.get_title(&path.to_string_lossy()))
        .unwrap_or_else(|_| {
            raw_path
                .file_stem()
                .and_then(|value| value.to_str())
                .unwrap_or_default()
                .to_owned()
        });
    let snippet =
        symdesk_vault::strip_search_metadata(&go_search_snippet(&result.chunk.content, &[query]));
    let hit = CliSearchHit {
        path: display_path,
        title,
        snippet,
        score: f64::from(result.rrf_score),
        anchor: None,
        metadata_matches: result.metadata_matches,
        source_type: source.then_some("external"),
        read_only: source,
    };
    Some((hit, path.to_string_lossy().into_owned()))
}

fn resolve_result_path(vault: &Path, raw_path: &Path) -> Option<PathBuf> {
    if raw_path.is_absolute() {
        return raw_path.canonicalize().ok();
    }
    raw_path
        .canonicalize()
        .or_else(|_| vault.join(raw_path).canonicalize())
        .ok()
}

fn go_search_snippet(content: &str, query_terms: &[&str]) -> String {
    const BOUND: usize = 300;
    let characters = content.chars().collect::<Vec<_>>();
    if characters.len() <= BOUND {
        return content.to_owned();
    }
    let mut terms = Vec::new();
    let mut seen = HashSet::new();
    for raw in query_terms {
        for word in raw.split_whitespace() {
            if !word.is_empty() && seen.insert(word.to_owned()) {
                terms.push(word.to_owned());
            }
        }
    }
    if terms.is_empty() {
        return characters[..BOUND].iter().collect();
    }
    let lower_content = go_simple_lowercase(content);
    let best = terms
        .iter()
        .filter_map(|term| lower_content.find(&go_simple_lowercase(term)))
        .min();
    let Some(best_byte) = best else {
        return characters[..BOUND].iter().collect();
    };
    // Go finds the byte offset in lowercased text, then counts runes in the
    // original byte prefix. This preserves its width-change behavior for
    // Unicode simple-case mappings such as U+0130 and U+212A.
    let rune_index = go_rune_count_prefix(content, best_byte);
    let mut start = rune_index.saturating_sub(BOUND / 2);
    let end = (start + BOUND).min(characters.len());
    if end == characters.len() {
        start = end.saturating_sub(BOUND);
    }
    characters[start..end].iter().collect()
}

fn go_rune_count_prefix(value: &str, byte_end: usize) -> usize {
    let byte_end = byte_end.min(value.len());
    let mut count = 0;
    for (offset, character) in value.char_indices() {
        if offset >= byte_end {
            break;
        }
        let rune_end = offset + character.len_utf8();
        if rune_end <= byte_end {
            count += 1;
        } else {
            // Go decodes each invalid UTF-8 byte in the sliced prefix as its
            // own RuneError, so a cut multibyte sequence contributes one per
            // included byte rather than one for the original scalar.
            count += byte_end - offset;
            break;
        }
    }
    count
}

#[cfg(test)]
mod tests {
    use super::{go_rune_count_prefix, go_search_snippet};

    #[test]
    fn snippet_uses_go_lowercase_byte_offset_and_original_text_rune_count() {
        for (prefix, expected_start) in [("İ", 150), ("K", 149)] {
            let content = format!("{prefix}{}needle{}", "x".repeat(300), "y".repeat(300));
            let expected = content
                .chars()
                .skip(expected_start)
                .take(300)
                .collect::<String>();
            assert_eq!(
                go_search_snippet(&content, &["needle"]),
                expected,
                "{prefix:?}"
            );
        }
    }

    #[test]
    fn rune_count_matches_go_invalid_byte_decoding_at_partial_utf8_prefixes() {
        assert_eq!(go_rune_count_prefix("İx", 1), 1);
        assert_eq!(go_rune_count_prefix("İx", 2), 1);
        assert_eq!(go_rune_count_prefix("Kx", 2), 2);
    }
}
