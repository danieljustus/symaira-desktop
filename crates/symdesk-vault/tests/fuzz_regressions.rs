#![deny(unsafe_code)]

use noyalib as _;
use serde as _;
use serde_json as _;
use thiserror as _;
use unicode_general_category as _;

#[test]
fn invalid_utf8_near_tag_scanner_is_panic_free() {
    let document = symdesk_vault::parse_bytes("fuzz/input.md", &[10, 10, 138])
        .expect("invalid UTF-8 body is lossily decoded like Go string handling");
    assert_eq!(document.size, 3);
    assert!(document.tags.is_empty());
}
