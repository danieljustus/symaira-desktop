use serde::Deserialize;
use symdesk_index::retrieval_quant_sidecar::{
    RETRIEVAL_QUANT_SIDECAR_HEADER_BYTES, RetrievalQuantSidecar,
};

const GO_WIRE_FIXTURE: &str =
    include_str!("../../../testdata/port/retrieval/quant-sidecar-wire.json");
const COREKIT_SUM: &str = "h1:pDtkMy0Pel1PWglNupMiLYpxo0WxUQ3CkjlR2LaUAZ4=";

#[derive(Debug, Deserialize)]
struct Fixture {
    schema_version: u32,
    oracle: Oracle,
    cases: Vec<WireCase>,
}

#[derive(Debug, Deserialize)]
struct Oracle {
    go_version: String,
    corekit_module: String,
    corekit_sum: String,
    scope: String,
}

#[derive(Clone, Debug, Deserialize)]
struct WireCase {
    id: String,
    operation: String,
    blob_hex: String,
    blob_len: usize,
    #[serde(default)]
    min_f32_bits: Option<u32>,
    #[serde(default)]
    max_f32_bits: Option<u32>,
    #[serde(default)]
    min_f64_bits: Option<u64>,
    #[serde(default)]
    max_f64_bits: Option<u64>,
    #[serde(default)]
    packed_hex: String,
    #[serde(default)]
    packed_len: usize,
    #[serde(default)]
    codec: Option<CodecInput>,
    #[serde(default)]
    error: Option<String>,
}

#[derive(Clone, Debug, Deserialize)]
struct CodecInput {
    dimension: usize,
    bit_width: i32,
    seed: i64,
    block_size: usize,
    vector_f32_bits: Vec<u32>,
}

fn fixture() -> Fixture {
    serde_json::from_str(GO_WIRE_FIXTURE).expect("committed Go wire fixture must parse")
}

fn decode_hex(value: &str) -> Result<Vec<u8>, String> {
    if !value.len().is_multiple_of(2) {
        return Err(format!("hex value has odd length: {}", value.len()));
    }
    value
        .as_bytes()
        .as_chunks::<2>()
        .0
        .iter()
        .map(|pair| {
            let high = hex_nibble(pair[0])?;
            let low = hex_nibble(pair[1])?;
            Ok((high << 4) | low)
        })
        .collect()
}

fn hex_nibble(value: u8) -> Result<u8, String> {
    match value {
        b'0'..=b'9' => Ok(value - b'0'),
        b'a'..=b'f' => Ok(value - b'a' + 10),
        b'A'..=b'F' => Ok(value - b'A' + 10),
        _ => Err(format!("invalid hex digit: {value}")),
    }
}

fn check_case(case: &WireCase) -> Result<(), String> {
    let blob = decode_hex(&case.blob_hex)?;
    if blob.len() != case.blob_len {
        return Err(format!(
            "{}: blob length {}, fixture says {}",
            case.id,
            blob.len(),
            case.blob_len
        ));
    }

    let decoded = RetrievalQuantSidecar::read_blob(&blob);
    if let Some(expected_error) = &case.error {
        return match decoded {
            Err(error) if error.to_string() == *expected_error => Ok(()),
            Err(error) => Err(format!(
                "{}: error {:?}, Go oracle says {:?}",
                case.id,
                error.to_string(),
                expected_error
            )),
            Ok(_) => Err(format!(
                "{}: Rust accepted a Go-rejected short blob",
                case.id
            )),
        };
    }

    let decoded = decoded.map_err(|error| format!("{}: unexpected error: {error}", case.id))?;
    let min_bits = case
        .min_f32_bits
        .ok_or_else(|| format!("{}: missing Go min f32 bits", case.id))?;
    let max_bits = case
        .max_f32_bits
        .ok_or_else(|| format!("{}: missing Go max f32 bits", case.id))?;
    if decoded.min.to_bits() != min_bits {
        return Err(format!(
            "{}: min bits {:08x}, Go oracle says {min_bits:08x}",
            case.id,
            decoded.min.to_bits()
        ));
    }
    if decoded.max.to_bits() != max_bits {
        return Err(format!(
            "{}: max bits {:08x}, Go oracle says {max_bits:08x}",
            case.id,
            decoded.max.to_bits()
        ));
    }
    if let Some(expected) = case.min_f64_bits
        && f64::from(decoded.min).to_bits() != expected
    {
        return Err(format!("{}: promoted min f64 bits differ from Go", case.id));
    }
    if let Some(expected) = case.max_f64_bits
        && f64::from(decoded.max).to_bits() != expected
    {
        return Err(format!("{}: promoted max f64 bits differ from Go", case.id));
    }

    let expected_packed = decode_hex(&case.packed_hex)?;
    if decoded.packed != expected_packed {
        return Err(format!("{}: opaque packed payload differs", case.id));
    }
    if decoded.packed.len() != case.packed_len {
        return Err(format!(
            "{}: packed length {}, Go oracle says {}",
            case.id,
            decoded.packed.len(),
            case.packed_len
        ));
    }
    if decoded.to_blob() != blob {
        return Err(format!("{}: read/write bytes differ", case.id));
    }
    Ok(())
}

#[test]
fn pinned_go_fixture_replays_all_wire_cases() {
    let fixture = fixture();
    assert_eq!(fixture.schema_version, 1);
    assert_eq!(fixture.oracle.go_version, "go1.26.6");
    assert_eq!(
        fixture.oracle.corekit_module,
        "github.com/danieljustus/symaira-corekit/v0.17.0"
    );
    assert_eq!(fixture.oracle.corekit_sum, COREKIT_SUM);
    assert!(fixture.oracle.scope.contains("NaN/infinity interpretation"));
    assert_eq!(fixture.cases.len(), 16, "all captured cases must execute");

    let mut encoded = 0;
    let mut too_short = 0;
    let mut successful_unpack = 0;
    for case in &fixture.cases {
        match case.operation.as_str() {
            "encode_sidecar_then_unpack" => {
                encoded += 1;
                let codec = case.codec.as_ref().expect("encoded case has codec input");
                assert_eq!(codec.dimension, codec.vector_f32_bits.len());
                assert!(
                    codec
                        .vector_f32_bits
                        .iter()
                        .all(|bits| f32::from_bits(*bits).is_finite())
                );
                assert!(matches!(codec.bit_width, 2 | 3 | 4 | 25));
                let _ = (codec.seed, codec.block_size);
            }
            "unpack_sidecar_blob" => {
                if case.error.is_some() {
                    too_short += 1;
                } else {
                    successful_unpack += 1;
                }
            }
            other => panic!("unknown fixture operation {other:?}"),
        }
        check_case(case).unwrap_or_else(|error| panic!("{error}"));
    }
    assert_eq!(encoded, 4);
    assert_eq!(too_short, 8);
    assert_eq!(successful_unpack, 4);
}

#[test]
fn every_truncated_header_matches_the_go_error() {
    let cases = fixture()
        .cases
        .into_iter()
        .filter(|case| case.id.starts_with("unpack_short_"))
        .collect::<Vec<_>>();
    assert_eq!(cases.len(), RETRIEVAL_QUANT_SIDECAR_HEADER_BYTES);
    for (length, case) in cases.iter().enumerate() {
        assert_eq!(case.blob_len, length);
        assert!(case.error.as_deref().is_some_and(|message| {
            message.starts_with("turboquant: code bytes too short: blob ")
                && message.ends_with(" bytes, need >= 8")
        }));
        check_case(case).unwrap_or_else(|error| panic!("{error}"));
    }
}

#[test]
fn exactly_eight_header_bytes_allow_an_empty_payload() {
    let case = fixture()
        .cases
        .into_iter()
        .find(|case| case.id == "unpack_empty_payload")
        .expect("empty-payload case is required");
    assert_eq!(case.blob_len, RETRIEVAL_QUANT_SIDECAR_HEADER_BYTES);
    assert_eq!(case.packed_len, 0);
    assert_eq!(case.packed_hex, "");
    check_case(&case).unwrap_or_else(|error| panic!("{error}"));
}

#[test]
fn signed_zero_header_bits_survive_read_and_write() {
    let cases = fixture()
        .cases
        .into_iter()
        .filter(|case| case.id.contains("negative_zero"))
        .collect::<Vec<_>>();
    assert_eq!(cases.len(), 2);
    for case in &cases {
        assert!(case.min_f32_bits == Some(0x8000_0000) || case.max_f32_bits == Some(0x8000_0000));
        check_case(case).unwrap_or_else(|error| panic!("{error}"));
    }
}

#[test]
fn packed_payload_is_copied_and_retains_exact_length_and_bytes() {
    let case = fixture()
        .cases
        .into_iter()
        .find(|case| case.id == "unpack_opaque_payload")
        .expect("opaque-payload case is required");
    let mut source = decode_hex(&case.blob_hex).expect("fixture hex parses");
    let original = source.clone();
    let decoded = RetrievalQuantSidecar::read_blob(&source).expect("valid header parses");
    assert_eq!(decoded.packed.len(), case.packed_len);
    assert_eq!(
        decoded.packed,
        decode_hex(&case.packed_hex).expect("payload hex parses")
    );

    source[RETRIEVAL_QUANT_SIDECAR_HEADER_BYTES] ^= 0xff;
    assert_ne!(source, original);
    assert_eq!(
        decoded.to_blob(),
        original,
        "read payload must own its bytes"
    );
    assert_eq!(decoded.to_blob().len(), case.blob_len);
}

#[test]
fn fixture_mutation_negative_control_is_rejected_by_the_same_comparator() {
    let mut case = fixture()
        .cases
        .into_iter()
        .find(|case| case.id == "unpack_opaque_payload")
        .expect("opaque-payload case is required");
    check_case(&case).expect("the unmodified Go observation must pass");

    case.min_f32_bits = Some(case.min_f32_bits.expect("captured min bits") ^ 1);
    assert_eq!(
        check_case(&case).unwrap_err(),
        format!("{}: min bits c0200000, Go oracle says c0200001", case.id)
    );
}
