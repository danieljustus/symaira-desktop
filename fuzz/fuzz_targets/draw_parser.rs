#![no_main]
#![deny(unsafe_code)]
use libfuzzer_sys::fuzz_target;
use symdesk_render::draw::{parse_json, parse_source};
fuzz_target!(|data: &[u8]| {
    let _ = parse_json(data);
    if let Ok(source) = std::str::from_utf8(data) {
        if let Ok(diagram) = parse_source(source) {
            // Traverse serialization too, including arbitrary custom JSON values.
            let _ = diagram.to_json();
        }
    }
});
