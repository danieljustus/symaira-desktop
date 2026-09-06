#![no_main]

use libfuzzer_sys::fuzz_target;

fuzz_target!(|input: &[u8]| {
    if let Ok(document) = symdesk_vault::parse_bytes("fuzz/input.md", input) {
        assert_eq!(document.size, i64::try_from(input.len()).unwrap_or(i64::MAX));
        assert_eq!(document.sha256.len(), 64);
        assert!(document.sha256.bytes().all(|byte| byte.is_ascii_hexdigit()));
    }
});
