//! Replays pinned CoreKit `vectorkit/turboquant` observations against the
//! local Rust codec (#1137). Regenerate with
//! `go run ./scripts/rust-port/cmd/turboquant-gen`.

use serde::Deserialize;
use symdesk_index::turboquant::{
    Codec, PackedCode, RandomRotation, SidecarMeta, decode_sidecar, hadamard_transform,
    unmarshal_metadata, unpack_sidecar_blob,
};

const FIXTURE: &str = include_str!("../../../testdata/port/retrieval/turboquant-codec.json");

#[derive(Deserialize)]
struct Fixture {
    schema_version: u32,
    oracle: Oracle,
    signs: Vec<SignCase>,
    hadamard: Vec<VectorCase>,
    rotations: Vec<RotateCase>,
    codecs: Vec<CodecCase>,
    sidecar_meta_json: Vec<MetaCase>,
    errors: Vec<ErrorCase>,
}

#[derive(Deserialize)]
struct Oracle {
    go_version: String,
    corekit_module: String,
    corekit_sum: String,
}

#[derive(Deserialize)]
struct SignCase {
    dim: usize,
    seed: i64,
    signs: String,
}

#[derive(Deserialize)]
struct VectorCase {
    input_f64_hex: String,
    output_f64_hex: String,
}

#[derive(Deserialize)]
struct RotateCase {
    dim: usize,
    seed: i64,
    input_f64_hex: String,
    forward_f64_hex: String,
    inverse_f64_hex: String,
}

#[derive(Deserialize)]
struct CodecCase {
    id: String,
    dim: i64,
    bit_width: i64,
    seed: i64,
    block_size: i64,
    vector_f32_hex: String,
    query_f32_hex: String,
    norm_f32_bits: u32,
    rot_dim: usize,
    packed_hex: String,
    min_f64_bits: u64,
    max_f64_bits: u64,
    decoded_grid: Vec<i64>,
    score_f32_bits: u32,
    score_cosine_f32_bits: u32,
    metadata_hex: String,
    metadata_roundtrip_packed_hex: String,
    sidecar_blob_hex: String,
    sidecar_meta_json: String,
    sidecar_decoded_grid: Vec<i64>,
    sidecar_score_f32_bits: u32,
    sidecar_score_cosine_f32_bits: u32,
}

#[derive(Deserialize)]
struct MetaCase {
    norm_f32_bits: u32,
    projection_seed: i64,
    quantizer_mode: String,
    json: String,
}

#[derive(Deserialize)]
struct ErrorCase {
    id: String,
    operation: String,
    #[serde(default)]
    dim: i64,
    #[serde(default)]
    bit_width: i64,
    #[serde(default)]
    seed: i64,
    #[serde(default)]
    input_hex: String,
    #[serde(default)]
    meta_json: String,
    error: String,
}

fn fixture() -> Fixture {
    let fixture: Fixture = serde_json::from_str(FIXTURE).expect("committed Go fixture must parse");
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.oracle.go_version, "go1.26.9");
    assert_eq!(
        fixture.oracle.corekit_module,
        "github.com/danieljustus/symaira-corekit/v0.18.3-0.20261012110002-27d2daf8ec1e"
    );
    assert_eq!(
        fixture.oracle.corekit_sum,
        "h1:lEwB4izhkSVb8fREvaPOiXuVHx7CF6fymsYGC8O1bsc="
    );
    fixture
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

fn f64s(hex_text: &str) -> Vec<f64> {
    bytes(hex_text)
        .as_chunks::<8>()
        .0
        .iter()
        .map(|c| f64::from_le_bytes(*c))
        .collect()
}

fn f32s(hex_text: &str) -> Vec<f32> {
    bytes(hex_text)
        .as_chunks::<4>()
        .0
        .iter()
        .map(|c| f32::from_le_bytes(*c))
        .collect()
}

fn bits64(values: &[f64]) -> Vec<u64> {
    values.iter().map(|v| v.to_bits()).collect()
}

fn grid(values: &[f32], code: &PackedCode) -> Vec<i64> {
    let mut scale = code.min.abs().max(code.max.abs());
    if scale == 0.0 {
        scale = 1.0;
    }
    values
        .iter()
        .map(|&v| (f64::from(v) / scale * 4096.0).round() as i64)
        .collect()
}

#[test]
fn rotation_signs_match_go_math_rand() {
    let cases = fixture().signs;
    assert!(cases.len() >= 18);
    for case in cases {
        let rotation = RandomRotation::new(case.dim, case.seed);
        let mut basis = vec![0.0; rotation.padded()];
        basis[0] = 1.0;
        let mut out = vec![0.0; rotation.padded()];
        rotation.inverse(&basis, &mut out);
        let signs: String = out
            .iter()
            .map(|&v| if v > 0.0 { '+' } else { '-' })
            .collect();
        assert_eq!(signs, case.signs, "dim={} seed={}", case.dim, case.seed);
    }
}

#[test]
fn hadamard_and_rotations_are_bit_exact() {
    let fixture = fixture();
    assert!(!fixture.hadamard.is_empty() && !fixture.rotations.is_empty());
    for case in fixture.hadamard {
        let mut values = f64s(&case.input_f64_hex);
        hadamard_transform(&mut values);
        assert_eq!(
            bits64(&values),
            bits64(&f64s(&case.output_f64_hex)),
            "{}",
            case.input_f64_hex
        );
    }
    for case in fixture.rotations {
        let rotation = RandomRotation::new(case.dim, case.seed);
        let mut forward = vec![0.0; rotation.padded()];
        rotation.apply(&f64s(&case.input_f64_hex), &mut forward);
        assert_eq!(
            bits64(&forward),
            bits64(&f64s(&case.forward_f64_hex)),
            "forward dim={}",
            case.dim
        );
        let mut inverse = vec![0.0; rotation.padded()];
        rotation.inverse(&forward, &mut inverse);
        assert_eq!(
            bits64(&inverse),
            bits64(&f64s(&case.inverse_f64_hex)),
            "inverse dim={}",
            case.dim
        );
    }
}

#[test]
fn codec_matches_go_encode_decode_score_and_sidecars() {
    let cases = fixture().codecs;
    assert_eq!(cases.len(), 60);
    for case in cases {
        let id = &case.id;
        let codec = Codec::new(case.dim, case.bit_width, case.seed, case.block_size).expect(id);
        assert_eq!(codec.rot_dim(), case.rot_dim, "{id}");
        let vector = f32s(&case.vector_f32_hex);
        let query = f32s(&case.query_f32_hex);

        // Byte-identical encode and metadata.
        let code = codec.encode(&vector).expect(id);
        assert_eq!(hex(&code.bytes), case.packed_hex, "{id} packed");
        assert_eq!(code.min.to_bits(), case.min_f64_bits, "{id} min");
        assert_eq!(code.max.to_bits(), case.max_f64_bits, "{id} max");
        assert_eq!(
            hex(&codec.marshal_metadata()),
            case.metadata_hex,
            "{id} metadata"
        );
        let from_meta =
            Codec::from_metadata(&unmarshal_metadata(&bytes(&case.metadata_hex)).expect(id))
                .expect(id);
        assert_eq!(
            hex(&from_meta.encode(&vector).expect(id).bytes),
            case.metadata_roundtrip_packed_hex,
            "{id}"
        );

        assert_eq!(
            grid(&codec.decode(&code).expect(id), &code),
            case.decoded_grid,
            "{id} decode"
        );
        assert_eq!(
            codec.score(&query, &code).to_bits(),
            case.score_f32_bits,
            "{id} score"
        );
        assert_eq!(
            codec.score_cosine(&query, &code).to_bits(),
            case.score_cosine_f32_bits,
            "{id} cosine"
        );

        // Rust-written sidecar bytes and JSON are identical to Go's, so the
        // Go fallback reads them exactly as it reads its own.
        let norm = f32::from_bits(case.norm_f32_bits);
        let (blob, meta) = codec.encode_sidecar(&vector, norm).expect(id);
        assert_eq!(hex(&blob), case.sidecar_blob_hex, "{id} sidecar blob");
        assert_eq!(
            meta.to_json().expect(id),
            case.sidecar_meta_json,
            "{id} sidecar json"
        );

        // Go-written sidecars decode and score in Rust like the Go DB path.
        let go_blob = bytes(&case.sidecar_blob_hex);
        let go_meta: SidecarMeta = serde_json::from_str(&case.sidecar_meta_json).expect(id);
        assert_eq!(go_meta, meta, "{id} parsed meta");
        codec.validate_sidecar_meta(&go_meta).expect(id);
        let unpacked = unpack_sidecar_blob(&go_blob).expect(id);
        assert_eq!(
            grid(&decode_sidecar(&go_blob, &go_meta).expect(id), &code),
            case.sidecar_decoded_grid,
            "{id}"
        );
        assert_eq!(
            codec.score(&query, &unpacked).to_bits(),
            case.sidecar_score_f32_bits,
            "{id} sidecar score"
        );
        assert_eq!(
            codec.score_cosine(&query, &unpacked).to_bits(),
            case.sidecar_score_cosine_f32_bits,
            "{id} sidecar cosine"
        );
    }
}

#[test]
fn sidecar_meta_json_matches_go_marshal() {
    let cases = fixture().sidecar_meta_json;
    assert!(cases.len() >= 20);
    for case in cases {
        let meta = SidecarMeta {
            codec_version: 1,
            dimension: 768,
            bit_width: 4,
            quantizer_mode: case.quantizer_mode,
            projection_seed: case.projection_seed,
            norm: f32::from_bits(case.norm_f32_bits),
        };
        assert_eq!(
            meta.to_json().unwrap(),
            case.json,
            "norm bits {:#x}",
            case.norm_f32_bits
        );
        let parsed: SidecarMeta = serde_json::from_str(&case.json).unwrap();
        assert_eq!(parsed.to_json().unwrap(), case.json);
    }
    assert!(
        SidecarMeta {
            norm: f32::NAN,
            ..SidecarMeta::default()
        }
        .to_json()
        .is_err()
    );
}

#[test]
fn errors_match_go_strings() {
    let cases = fixture().errors;
    assert!(cases.len() >= 20);
    for case in cases {
        let meta = || serde_json::from_str::<SidecarMeta>(&case.meta_json).unwrap();
        let actual = match case.operation.as_str() {
            "new_codec" => Codec::new(case.dim, case.bit_width, case.seed, 0)
                .err()
                .map(|e| e.to_string()),
            "encode" => Codec::new(case.dim, case.bit_width, case.seed, 0)
                .unwrap()
                .encode(&f32s(&case.input_hex))
                .err()
                .map(|e| e.to_string()),
            "decode" => Codec::new(case.dim, case.bit_width, case.seed, 0)
                .unwrap()
                .decode(&PackedCode {
                    bytes: bytes(&case.input_hex),
                    min: 0.0,
                    max: 0.0,
                })
                .err()
                .map(|e| e.to_string()),
            "unmarshal_metadata" => unmarshal_metadata(&bytes(&case.input_hex))
                .err()
                .map(|e| e.to_string()),
            "codec_from_metadata" => {
                Codec::from_metadata(&unmarshal_metadata(&bytes(&case.input_hex)).unwrap())
                    .err()
                    .map(|e| e.to_string())
            }
            "codec_from_sidecar_meta" => Codec::from_sidecar_meta(&meta())
                .err()
                .map(|e| e.to_string()),
            "validate_sidecar_meta" => Codec::new(case.dim, case.bit_width, case.seed, 0)
                .unwrap()
                .validate_sidecar_meta(&meta())
                .err()
                .map(|e| e.to_string()),
            "decode_sidecar" => decode_sidecar(&bytes(&case.input_hex), &meta())
                .err()
                .map(|e| e.to_string()),
            other => panic!("unknown operation {other}"),
        };
        assert_eq!(actual.unwrap_or_default(), case.error, "{}", case.id);
    }
}

#[test]
fn short_codes_score_zero_instead_of_panicking() {
    let codec = Codec::new(5, 3, 9, 0).unwrap();
    let short = PackedCode {
        bytes: vec![1],
        min: -1.0,
        max: 1.0,
    };
    assert_eq!(codec.score(&[1.0; 5], &short), 0.0);
    assert_eq!(codec.score_cosine(&[1.0; 5], &short), 0.0);
}
