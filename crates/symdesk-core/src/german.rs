#![deny(unsafe_code)]

//! Conservative German search normalization used by the FTS5 indexes.

use std::collections::HashSet;

#[must_use]
pub fn search_tokens(value: &str) -> Vec<String> {
    let mut output = Vec::new();
    let mut seen = HashSet::new();
    for field in value.to_lowercase().split_whitespace() {
        let token = trim_token(field);
        if token.is_empty() || is_stopword(&token) {
            continue;
        }
        let stem = stem(&token);
        if !stem.is_empty() && seen.insert(stem.clone()) {
            output.push(stem);
        }
    }
    output
}

#[must_use]
pub fn normalized_text(value: &str) -> String {
    search_tokens(value).join(" ")
}

#[must_use]
pub fn fts_query(value: &str) -> String {
    value
        .split_whitespace()
        .filter_map(term_expression)
        .collect::<Vec<_>>()
        .join(" AND ")
}

#[must_use]
pub fn trigram_query(value: &str) -> String {
    let tokens = search_tokens(value);
    if tokens.is_empty()
        || tokens
            .iter()
            .any(|token| token.chars().count() < 3 || !word_characters_only(token))
    {
        return "\"\"".to_owned();
    }
    tokens.join(" AND ")
}

#[must_use]
pub fn fts_term(value: &str, phrase: bool) -> String {
    if !phrase {
        return fts_query(value);
    }
    let tokens = search_tokens(value);
    if tokens.is_empty() {
        String::new()
    } else {
        format!("\"{}\"", tokens.join(" ").replace('"', "\"\""))
    }
}

#[must_use]
pub fn trigram_term(value: &str, phrase: bool) -> String {
    if !phrase {
        return trigram_query(value);
    }
    let mut parts = Vec::new();
    for field in value.to_lowercase().split_whitespace() {
        let token = trim_token(field);
        if token.is_empty() || token.chars().count() < 3 || !word_characters_only(&token) {
            return "\"\"".to_owned();
        }
        parts.push(fold_umlauts(&token));
    }
    if parts.is_empty() {
        "\"\"".to_owned()
    } else {
        format!("\"{}\"", parts.join(" "))
    }
}

fn term_expression(value: &str) -> Option<String> {
    let lowercase = value.to_lowercase();
    let token = trim_token(&lowercase);
    if token.is_empty() || is_stopword(&token) {
        return None;
    }
    let stem = stem(&token);
    if stem.is_empty() {
        None
    } else {
        Some(format!("\"{}\"*", stem.replace('"', "\"\"")))
    }
}

fn trim_token(value: &str) -> String {
    value
        .trim_matches(|character: char| !character.is_alphabetic() && !character.is_numeric())
        .to_owned()
}

fn fold_umlauts(value: &str) -> String {
    value
        .replace('ä', "a")
        .replace('ö', "o")
        .replace('ü', "u")
        .replace('ß', "ss")
}

fn stem(value: &str) -> String {
    let folded = fold_umlauts(&value.to_lowercase());
    for suffix in [
        "lichkeit", "igkeit", "heit", "keit", "isch", "lich", "ung", "end", "ern", "em", "er",
        "en", "es", "e", "s",
    ] {
        if folded.ends_with(suffix)
            && folded
                .chars()
                .count()
                .saturating_sub(suffix.chars().count())
                >= 4
        {
            return folded[..folded.len() - suffix.len()].to_owned();
        }
    }
    folded
}

fn word_characters_only(value: &str) -> bool {
    value
        .chars()
        .all(|character| character.is_alphabetic() || character.is_numeric())
}

fn is_stopword(value: &str) -> bool {
    matches!(
        value,
        "aber"
            | "alle"
            | "allem"
            | "allen"
            | "aller"
            | "alles"
            | "als"
            | "also"
            | "am"
            | "an"
            | "auch"
            | "auf"
            | "aus"
            | "bei"
            | "bin"
            | "bis"
            | "bist"
            | "da"
            | "dabei"
            | "dadurch"
            | "dafür"
            | "daher"
            | "damit"
            | "danach"
            | "dann"
            | "das"
            | "dass"
            | "davon"
            | "dazu"
            | "dem"
            | "den"
            | "denn"
            | "der"
            | "des"
            | "die"
            | "dies"
            | "diese"
            | "diesem"
            | "diesen"
            | "dieser"
            | "dieses"
            | "doch"
            | "dort"
            | "du"
            | "durch"
            | "ein"
            | "eine"
            | "einem"
            | "einen"
            | "einer"
            | "eines"
            | "einige"
            | "er"
            | "es"
            | "für"
            | "gegen"
            | "hat"
            | "haben"
            | "hier"
            | "ich"
            | "im"
            | "in"
            | "ist"
            | "ja"
            | "jede"
            | "jedem"
            | "jeden"
            | "jeder"
            | "jedes"
            | "kein"
            | "keine"
            | "mit"
            | "nach"
            | "nicht"
            | "noch"
            | "nur"
            | "oder"
            | "ohne"
            | "sehr"
            | "sie"
            | "sind"
            | "so"
            | "über"
            | "um"
            | "und"
            | "uns"
            | "unter"
            | "vom"
            | "von"
            | "vor"
            | "war"
            | "waren"
            | "was"
            | "weil"
            | "weiter"
            | "welche"
            | "wenn"
            | "wer"
            | "wie"
            | "wieder"
            | "will"
            | "wir"
            | "wird"
            | "wo"
            | "zu"
            | "zum"
            | "zur"
    )
}
