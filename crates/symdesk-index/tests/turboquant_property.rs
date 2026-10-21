//! Replays the pinned CoreKit property corpus (#1137). Encoded bytes and
//! min/max must match exactly; decoded components and scores must stay within
//! the Go-recorded tolerances. The Go sidecar read-back gate lives in
//! `scripts/rust-port/cmd/turboquant-gen/rust_sidecar_test.go`.

use serde::Deserialize;
use symdesk_index::turboquant::{Codec, PackedCode, sidecar_corpus::render_sidecar_corpus};

const PROPERTY: &str = include_str!("../../../testdata/port/retrieval/turboquant-property.json");
const RUST_SIDECARS: &str =
    include_str!("../../../testdata/port/retrieval/turboquant-rust-sidecars.json");

#[derive(Deserialize)]
struct Property {
    schema_version: u32,
    oracle: Oracle,
    cases: Vec<Case>,
}

#[derive(Deserialize)]
struct Oracle {
    go_version: String,
    corekit_module: String,
    corekit_sum: String,
}

#[derive(Deserialize)]
struct Case {
    id: String,
    dim: i64,
    bit_width: i64,
    seed: i64,
    vector_f32_hex: String,
    query_f32_hex: String,
    rot_dim: usize,
    packed_hex: String,
    min_f64_bits: u64,
    max_f64_bits: u64,
    decoded_f32_hex: String,
    decode_abs_tolerance: f64,
    score_f32_bits: u32,
    score_abs_tolerance: f64,
}

fn property() -> Property {
    let property: Property =
        serde_json::from_str(PROPERTY).expect("committed property corpus must parse");
    assert_eq!(property.schema_version, 1);
    assert_eq!(property.oracle.go_version, "go1.26.10");
    assert_eq!(
        property.oracle.corekit_module,
        "github.com/danieljustus/symaira-corekit/v0.18.3-0.20261012110002-27d2daf8ec1e"
    );
    assert_eq!(
        property.oracle.corekit_sum,
        "h1:lEwB4izhkSVb8fREvaPOiXuVHx7CF6fymsYGC8O1bsc="
    );
    assert!(
        property.cases.len() >= 200,
        "property corpus shrank to {}",
        property.cases.len()
    );
    property
}

fn bytes(hex: &str) -> Vec<u8> {
    assert!(hex.len().is_multiple_of(2), "odd hex length");
    (0..hex.len())
        .step_by(2)
        .map(|i| u8::from_str_radix(&hex[i..i + 2], 16).expect("hex"))
        .collect()
}

fn hex(data: &[u8]) -> String {
    data.iter().map(|b| format!("{b:02x}")).collect()
}

fn f32s(hex_text: &str) -> Vec<f32> {
    bytes(hex_text)
        .as_chunks::<4>()
        .0
        .iter()
        .map(|c| f32::from_le_bytes(*c))
        .collect()
}

#[test]
fn encode_is_byte_identical_to_go() {
    for case in property().cases {
        let codec = Codec::new(case.dim, case.bit_width, case.seed, 0).expect(&case.id);
        assert_eq!(codec.rot_dim(), case.rot_dim, "{}", case.id);
        let code = codec.encode(&f32s(&case.vector_f32_hex)).expect(&case.id);
        assert_eq!(hex(&code.bytes), case.packed_hex, "{} packed", case.id);
        assert_eq!(code.min.to_bits(), case.min_f64_bits, "{} min", case.id);
        assert_eq!(code.max.to_bits(), case.max_f64_bits, "{} max", case.id);
    }
}

#[test]
fn decode_and_score_stay_within_go_bounds() {
    let mut worst_decode = 0.0_f64;
    let mut worst_score = 0.0_f64;
    for case in property().cases {
        let codec = Codec::new(case.dim, case.bit_width, case.seed, 0).expect(&case.id);
        let packed = PackedCode {
            bytes: bytes(&case.packed_hex),
            min: f64::from_bits(case.min_f64_bits),
            max: f64::from_bits(case.max_f64_bits),
        };
        let decoded = codec.decode(&packed).expect(&case.id);
        let expected = f32s(&case.decoded_f32_hex);
        assert_eq!(decoded.len(), expected.len(), "{} decoded length", case.id);
        for (j, (got, want)) in decoded.iter().zip(&expected).enumerate() {
            let diff = (f64::from(*got) - f64::from(*want)).abs();
            worst_decode =
                worst_decode.max(diff / case.decode_abs_tolerance.max(f64::MIN_POSITIVE));
            assert!(
                diff <= case.decode_abs_tolerance,
                "{} decoded[{j}] off by {diff} > {}",
                case.id,
                case.decode_abs_tolerance
            );
        }
        let score = f64::from(codec.score(&f32s(&case.query_f32_hex), &packed));
        let want = f64::from(f32::from_bits(case.score_f32_bits));
        let diff = (score - want).abs();
        worst_score = worst_score.max(diff / case.score_abs_tolerance.max(f64::MIN_POSITIVE));
        assert!(
            diff <= case.score_abs_tolerance,
            "{} score off by {diff} > {}",
            case.id,
            case.score_abs_tolerance
        );
    }
    // Ratios are reported for review; anything above 1.0 already failed above.
    eprintln!(
        "property worst decode/tolerance ratio {worst_decode:e}, score/tolerance ratio {worst_score:e}"
    );
}

#[test]
fn rust_written_sidecars_match_committed_corpus() {
    let rendered = render_sidecar_corpus(PROPERTY).expect("sidecar corpus renders");
    assert_eq!(
        rendered, RUST_SIDECARS,
        "regenerate with turboquant-sidecar-writer"
    );
}
