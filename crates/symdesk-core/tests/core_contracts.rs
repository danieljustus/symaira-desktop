#![deny(unsafe_code)]

use regex as _;
use serde::Deserialize;
use symdesk_core::{document_format, german, simhash, textnorm};
use time as _;
use toml as _;

#[derive(Deserialize)]
struct Fixture<T> {
    schema_version: u8,
    cases: T,
}

#[derive(Deserialize)]
struct SimhashCases {
    minimum_body_length: usize,
    short_body_similarity_cap: u32,
    fingerprints: Vec<Fingerprint>,
    pairs: Vec<Pair>,
    parse: Vec<ParseCase>,
}

#[derive(Deserialize)]
struct Fingerprint {
    text: String,
    hash: u64,
    hex: String,
    content_length: usize,
}

#[derive(Deserialize)]
struct Pair {
    left: String,
    right: String,
    hamming_distance: u32,
    similarity: u32,
    content_similarity: u32,
}

#[derive(Deserialize)]
struct ParseCase {
    input: String,
    #[serde(default)]
    value: u64,
    error: Option<String>,
}

#[test]
fn simhash_matches_go_fixtures() {
    let fixture: Fixture<SimhashCases> =
        serde_json::from_str(include_str!("../../../testdata/port/core/simhash.json"))
            .expect("decode simhash fixture");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(
        fixture.cases.minimum_body_length,
        simhash::MINIMUM_BODY_LENGTH
    );
    assert_eq!(
        fixture.cases.short_body_similarity_cap,
        simhash::SHORT_BODY_SIMILARITY_CAP
    );
    for case in fixture.cases.fingerprints {
        assert_eq!(simhash::compute(&case.text), case.hash);
        assert_eq!(simhash::compute_hex(&case.text), case.hex);
        assert_eq!(simhash::content_length(&case.text), case.content_length);
    }
    for case in fixture.cases.pairs {
        let left = simhash::compute(&case.left);
        let right = simhash::compute(&case.right);
        assert_eq!(
            simhash::hamming_distance(left, right),
            case.hamming_distance
        );
        assert_eq!(simhash::similarity(left, right), case.similarity);
        assert_eq!(
            simhash::similarity_for_content(left, right, &case.left, &case.right),
            case.content_similarity
        );
    }
    for case in fixture.cases.parse {
        match (simhash::parse_hex(&case.input), case.error) {
            (Ok(value), None) => assert_eq!(value, case.value),
            (Err(error), Some(expected)) => assert_eq!(error, expected),
            (result, expected) => panic!("parse mismatch: result={result:?} error={expected:?}"),
        }
    }
}

#[derive(Deserialize)]
struct TextNormCases {
    dehyphenate: Vec<TextCase>,
    language: Vec<LanguageCase>,
}

#[derive(Deserialize)]
struct TextCase {
    input: String,
    output: String,
}

#[derive(Deserialize)]
struct LanguageCase {
    input: String,
    language: String,
}

#[test]
fn text_normalization_matches_go_fixtures() {
    let fixture: Fixture<TextNormCases> =
        serde_json::from_str(include_str!("../../../testdata/port/core/textnorm.json"))
            .expect("decode textnorm fixture");
    for case in fixture.cases.dehyphenate {
        assert_eq!(textnorm::dehyphenate(&case.input), case.output);
    }
    for case in fixture.cases.language {
        assert_eq!(textnorm::detect_language(&case.input), case.language);
    }
}

#[derive(Deserialize)]
struct GermanCase {
    input: String,
    tokens: Vec<String>,
    normalized: String,
    fts_query: String,
    trigram_query: String,
    fts_term: String,
    trigram_term: String,
    phrase_fts_term: String,
    phrase_trigram_term: String,
}

#[test]
fn german_search_normalization_matches_go_fixtures() {
    let fixture: Fixture<Vec<GermanCase>> = serde_json::from_str(include_str!(
        "../../../testdata/port/core/german-search.json"
    ))
    .expect("decode German fixture");
    for case in fixture.cases {
        assert_eq!(german::search_tokens(&case.input), case.tokens);
        assert_eq!(german::normalized_text(&case.input), case.normalized);
        assert_eq!(german::fts_query(&case.input), case.fts_query);
        assert_eq!(german::trigram_query(&case.input), case.trigram_query);
        assert_eq!(german::fts_term(&case.input, false), case.fts_term);
        assert_eq!(german::trigram_term(&case.input, false), case.trigram_term);
        assert_eq!(german::fts_term(&case.input, true), case.phrase_fts_term);
        assert_eq!(
            german::trigram_term(&case.input, true),
            case.phrase_trigram_term
        );
    }
}

#[derive(Deserialize)]
struct FormatCases {
    registry: Vec<FormatSpec>,
    supported_extensions: Vec<String>,
    lookups: Vec<FormatLookup>,
    unknown_error: String,
    drm_error: String,
}

#[derive(Deserialize)]
struct FormatSpec {
    extension: String,
    kind: String,
    name: String,
    supported: bool,
    reason: Option<String>,
    error: Option<String>,
}

#[derive(Deserialize)]
struct FormatLookup {
    input: String,
    normalized: String,
    kind: Option<String>,
    recognized: bool,
    supported: bool,
}

#[test]
fn document_format_registry_matches_go_fixtures() {
    let fixture: Fixture<FormatCases> = serde_json::from_str(include_str!(
        "../../../testdata/port/core/document-formats.json"
    ))
    .expect("decode document-format fixture");
    let mut actual = Vec::new();
    for format in document_format::SUPPORTED_FORMATS {
        actual.push((format.extension, format.kind, format.name, true, None, None));
    }
    for item in document_format::UNSUPPORTED_FORMATS {
        actual.push((
            item.format.extension,
            item.format.kind,
            item.format.name,
            false,
            Some(item.reason.to_owned()),
            Some(document_format::unsupported_format_error(item.format.kind)),
        ));
    }
    assert_eq!(actual.len(), fixture.cases.registry.len());
    for (actual, expected) in actual.into_iter().zip(fixture.cases.registry) {
        assert_eq!(actual.0, expected.extension);
        assert_eq!(actual.1, expected.kind);
        assert_eq!(actual.2, expected.name);
        assert_eq!(actual.3, expected.supported);
        assert_eq!(actual.4, expected.reason);
        assert_eq!(actual.5, expected.error);
    }
    assert_eq!(
        document_format::supported_extensions(),
        fixture.cases.supported_extensions
    );
    for case in fixture.cases.lookups {
        assert_eq!(
            document_format::normalize_extension(&case.input),
            case.normalized
        );
        assert_eq!(
            document_format::kind_for_extension(&case.input),
            case.kind.as_deref()
        );
        assert_eq!(
            document_format::kind_for_extension(&case.input).is_some(),
            case.recognized
        );
        assert_eq!(document_format::is_supported(&case.input), case.supported);
    }
    assert_eq!(
        document_format::unsupported_format_error("application/x-unknown"),
        fixture.cases.unknown_error
    );
    assert_eq!(
        document_format::DRM_PROTECTED_ERROR,
        fixture.cases.drm_error
    );
}
