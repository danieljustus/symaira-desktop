use symdesk_ingest::extract;
#[test]
fn ole_malformed_exponent_is_bounded() {
    for exponent in [26, 63, 64, 128, 255] {
        let mut data = vec![0; 512];
        data[..8].copy_from_slice(b"\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1");
        data[30] = exponent;
        assert_eq!(
            extract::detect_bytes("source.txt", &data),
            Ok(String::new())
        );
    }
}
#[test]
fn text_invalid_utf8_is_explicit_string_representation_divergence() {
    // Go strings can retain arbitrary bytes. This Rust API represents JSON-facing
    // Unicode text; raw invalid source bytes must be retained separately by caller.
    assert_eq!(
        extract::extract_bytes(b"\xff", "text/plain", 1)
            .unwrap()
            .text,
        "\u{fffd}"
    );
}
#[test]
fn deterministic_untrusted_prefix_smoke() {
    for size in 0..128 {
        for seed in 0..16u8 {
            let bytes = (0..size)
                .map(|i| (i as u8).wrapping_mul(37).wrapping_add(seed))
                .collect::<Vec<_>>();
            let _ = extract::detect_bytes("source.docx", &bytes);
            let _ = extract::extract_bytes(&bytes, "application/rtf", 256);
        }
    }
}
