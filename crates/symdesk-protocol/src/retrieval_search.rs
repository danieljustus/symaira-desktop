use std::{
    collections::{BTreeMap, HashSet},
    path::{Path, PathBuf},
    time::Duration,
};

use symdesk_index::{
    RetrievalDb, RetrievalEmbeddingConfig, RetrievalHybridSearchResult, go_simple_lowercase,
    local_hash_embedding, open_retrieval_for_vault, retrieval_embedding_config,
};

use crate::{embed_local_ollama, expand_local_ollama_query, local_ollama_embeddings_endpoint};

const DEFAULT_QUERY_DIMENSION: usize = 768;

/// Runs the shared Go-compatible hybrid retrieval selection used by CLI and HTTP Ask.
///
/// `None` means the query or index requires the caller's existing lexical fallback.
pub fn hybrid_search_results(
    vault: &Path,
    query: &str,
    sources: &[symdesk_index::SearchSource],
    per_root_limit: i64,
) -> Result<Option<Vec<RetrievalHybridSearchResult>>, String> {
    if !hybrid_query_is_eligible(query) {
        return Ok(None);
    }
    let environment = symdesk_core::config::environment_snapshot()?;
    let cwd =
        std::env::current_dir().map_err(|error| format!("read current directory: {error}"))?;
    let index = open_retrieval_for_vault(
        &vault.to_string_lossy(),
        &environment,
        &cwd,
        &std::env::temp_dir(),
    )
    .map_err(|error| format!("open retrieval index: {error}"))?;
    hybrid_search_results_with_index(
        vault,
        query,
        sources,
        per_root_limit,
        &index,
        &environment,
        &cwd,
    )
}

pub(crate) fn hybrid_search_results_if_populated(
    vault: &Path,
    query: &str,
    sources: &[symdesk_index::SearchSource],
    per_root_limit: i64,
) -> Result<Option<Vec<RetrievalHybridSearchResult>>, String> {
    if !hybrid_query_is_eligible(query) {
        return Ok(None);
    }
    let environment = symdesk_core::config::environment_snapshot()?;
    let cwd =
        std::env::current_dir().map_err(|error| format!("read current directory: {error}"))?;
    let index = open_retrieval_for_vault(
        &vault.to_string_lossy(),
        &environment,
        &cwd,
        &std::env::temp_dir(),
    )
    .map_err(|error| format!("open retrieval index: {error}"))?;
    if index
        .count_chunks()
        .map_err(|error| format!("count retrieval chunks: {error}"))?
        == 0
    {
        return Ok(None);
    }
    hybrid_search_results_with_index(
        vault,
        query,
        sources,
        per_root_limit,
        &index,
        &environment,
        &cwd,
    )
}

pub(crate) fn hybrid_search_results_with_index(
    vault: &Path,
    query: &str,
    sources: &[symdesk_index::SearchSource],
    per_root_limit: i64,
    index: &RetrievalDb,
    environment: &BTreeMap<String, String>,
    cwd: &Path,
) -> Result<Option<Vec<RetrievalHybridSearchResult>>, String> {
    if !hybrid_query_is_eligible(query) {
        return Ok(None);
    }
    let config = retrieval_embedding_config(environment, cwd)
        .map_err(|error| format!("load retrieval configuration: {error}"))?;
    // The Go Client used by the real CLI/MCP search path currently ignores
    // rerank_query, vector_backend, and vector_quantization. Preserve that runtime behavior
    // until a production caller activates those settings.
    if local_ollama_embeddings_endpoint(&config.ollama_url).is_none() {
        return Err(format!(
            "hybrid search does not support configured embedding endpoint {:?}; configure an HTTP Ollama endpoint on loopback",
            config.ollama_url
        ));
    }

    // Go checks mixed spaces before it asks the embedding backend. A mixed
    // index takes the existing lexical fallback path without a provider call.
    let spaces = match index.detect_mixed_embedding_spaces() {
        Ok(spaces) if spaces.len() <= 1 => spaces,
        Ok(_) | Err(_) => return Ok(None),
    };

    let mut query_vector = query_embedding(query, &config)?;
    if config.expand_query {
        apply_query_expansion(query, &config, &mut query_vector);
    }
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
        // Go's source search tolerates a source that disappeared after it was
        // registered. The stored index uses its absolute path, so retain a
        // normalized lexical path when canonicalization is no longer possible.
        let source_path = PathBuf::from(&source.path);
        let path = source_path
            .canonicalize()
            .unwrap_or_else(|_| absolute_clean_path(&source_path, cwd));
        if !roots.contains(&path) {
            roots.push(path);
        }
    }

    let scoped = !sources.is_empty();
    let mut results = Vec::new();
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
            per_root_limit,
        ) {
            Ok(response) => response,
            Err(_) => return Ok(None),
        };
        for warning in response.warnings {
            eprintln!("{warning}");
        }
        for result in response.results {
            if !scoped || by_path.insert(result.chunk.document_path.clone()) {
                results.push(result);
            }
        }
    }
    results.sort_by(|left, right| {
        right
            .rrf_score
            .partial_cmp(&left.rrf_score)
            .unwrap_or(std::cmp::Ordering::Equal)
    });
    Ok(Some(results))
}

fn hybrid_query_is_eligible(query: &str) -> bool {
    let plan = symdesk_core::query::parse(query);
    !query.trim().is_empty() && matches!(plan, Ok(ref parsed) if !parsed.requires_sidecar())
}

fn absolute_clean_path(path: &Path, cwd: &Path) -> PathBuf {
    let absolute = if path.is_absolute() {
        path.to_path_buf()
    } else {
        cwd.join(path)
    };
    let mut cleaned = PathBuf::new();
    for component in absolute.components() {
        match component {
            std::path::Component::CurDir => {}
            std::path::Component::ParentDir => {
                cleaned.pop();
            }
            component => cleaned.push(component.as_os_str()),
        }
    }
    cleaned
}

/// Builds the query-centered snippet used by Go retrieval search results.
#[must_use]
pub fn go_search_snippet(content: &str, query_terms: &[&str]) -> String {
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
            count += byte_end - offset;
            break;
        }
    }
    count
}

struct QueryVector {
    vector: Vec<f32>,
    model: String,
    dimension_known: bool,
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
                dimension_known: true,
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
        dimension_known: dimensions.is_some(),
    })
}

fn apply_query_expansion(
    query: &str,
    config: &RetrievalEmbeddingConfig,
    original: &mut QueryVector,
) {
    let runtime = match tokio::runtime::Builder::new_current_thread()
        .enable_all()
        .build()
    {
        Ok(runtime) => runtime,
        Err(error) => {
            eprintln!(
                "engine: HyDE expansion failed (create runtime: {error}), using original query vector"
            );
            return;
        }
    };
    let passage = runtime.block_on(expand_local_ollama_query(
        &config.ollama_url,
        &config.expand_model,
        query,
        Duration::from_secs(config.expand_timeout_seconds),
    ));
    let passage = match passage {
        Ok(passage) => trim_hypothetical_passage(&passage),
        Err(error) => {
            eprintln!(
                "engine: HyDE expansion failed (hyde expansion failed: {error}), using original query vector"
            );
            return;
        }
    };
    if passage.is_empty() {
        eprintln!(
            "engine: HyDE expansion failed (hyde expansion failed: hyde expansion returned empty passage), using original query vector"
        );
        return;
    }

    let expansion_vector = if passage == query {
        // The Go generator caches by exact text, so an identical hypothetical
        // passage reuses the already generated original-query vector.
        original.vector.clone()
    } else {
        let embedded = runtime.block_on(embed_local_ollama(
            &config.ollama_url,
            &config.model,
            std::slice::from_ref(&passage),
            config.embedding_dim,
            Duration::from_secs(config.timeout_seconds),
        ));
        match embedded {
            Ok(mut vectors)
                if vectors.len() == 1
                    && (!original.dimension_known || vectors[0].len() == original.vector.len()) =>
            {
                vectors.remove(0)
            }
            Ok(vectors) => {
                eprintln!(
                    "engine: HyDE embedding dimension mismatch: expected {}, got {}; using local hash vector",
                    original.vector.len(),
                    vectors.first().map_or(0, Vec::len)
                );
                match local_hash_embedding(&passage, original.vector.len()) {
                    Ok(vector) => vector,
                    Err(error) => {
                        eprintln!(
                            "engine: HyDE embedding fallback failed ({error}), using original query vector"
                        );
                        return;
                    }
                }
            }
            Err(error) => {
                eprintln!("engine: HyDE embedding failed ({error}), using local hash vector");
                match local_hash_embedding(&passage, original.vector.len()) {
                    Ok(vector) => vector,
                    Err(error) => {
                        eprintln!(
                            "engine: HyDE embedding fallback failed ({error}), using original query vector"
                        );
                        return;
                    }
                }
            }
        }
    };
    if expansion_vector.len() == original.vector.len() {
        for (query_value, passage_value) in original.vector.iter_mut().zip(expansion_vector) {
            *query_value = (*query_value + passage_value) / 2.0;
        }
    }
}

fn trim_hypothetical_passage(text: &str) -> String {
    let trimmed = if text.chars().count() <= 512 {
        text.to_owned()
    } else {
        let prefix = text.chars().take(512).collect::<String>();
        prefix
            .rfind(' ')
            .map_or(prefix.clone(), |boundary| prefix[..boundary].to_owned())
    };
    trimmed.trim().to_owned()
}

#[cfg(test)]
mod tests {
    use super::{go_rune_count_prefix, go_search_snippet, trim_hypothetical_passage};

    #[test]
    fn snippet_matches_go_lowercase_byte_offset_and_original_text_rune_count() {
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

    #[test]
    fn hyde_passage_trim_matches_go_rune_limit_and_space_boundary() {
        let with_boundary = format!("{} tail", "x".repeat(511));
        assert_eq!(trim_hypothetical_passage(&with_boundary), "x".repeat(511));
        let no_boundary = "é".repeat(513);
        assert_eq!(trim_hypothetical_passage(&no_boundary), "é".repeat(512));
        assert_eq!(
            trim_hypothetical_passage("  short passage  "),
            "short passage"
        );
    }
}
