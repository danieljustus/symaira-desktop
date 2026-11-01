#![no_main]

use libfuzzer_sys::fuzz_target;
use symdesk_protocol::mcp_frame::{check_output_frames, drive_stream};

fuzz_target!(|input: &[u8]| {
    let first = drive_stream(input);
    // The transport is a pure function of the byte stream.
    assert_eq!(first, drive_stream(input));
    // Nothing but well-formed JSON-RPC frames may reach stdout.
    check_output_frames(&first.0).expect("stdout carries only well-formed frames");
});
