#![deny(unsafe_code)]

// MCP-001 at the framing layer: the pure transport port is replayed against
// the Go-recorded transcripts. Only cases whose Go output consists of `ping`
// results and parse/shape errors are eligible, because `drive_stream` answers
// every well-formed request with the ping result; the full-server replay of
// every case is `symdesk-cli/tests/mcp_protocol.rs`.

#[path = "support/mcp_cases.rs"]
mod mcp_cases;

use symdesk_protocol::mcp_frame::{check_output_frames, drive_stream};

fn eligible(case: &mcp_cases::Case) -> bool {
    !case.stdout.contains("serverInfo")
        && !case.stdout.contains("-32601")
        && !case.stdout.contains("-32602")
        && !case.stdout.contains("isError")
        && !case.stdout.contains("\"tools\"")
}

#[test]
fn eligible_go_transcripts_match_byte_for_byte() {
    let fixture = mcp_cases::load_fixture();
    let mut executed = 0;
    let mut failures = Vec::new();
    for case in fixture.cases.iter().filter(|case| eligible(case)) {
        let (stdout, fatal) = drive_stream(&mcp_cases::input(case));
        executed += 1;
        let stdout = String::from_utf8(stdout).expect("UTF-8 responses");
        let stderr = fatal.map(|text| format!("{text}\n")).unwrap_or_default();
        let wants_failure = case.exit_code != 0;
        if stdout != case.stdout || stderr != case.stderr || wants_failure != !stderr.is_empty() {
            failures.push(format!(
                "{}: stdout {:?} stderr {:?}, want {:?} / {:?}",
                case.id,
                &stdout[..stdout.len().min(200)],
                stderr,
                &case.stdout[..case.stdout.len().min(200)],
                case.stderr
            ));
        }
    }
    assert!(executed >= 150, "only {executed} eligible cases ran");
    assert!(
        failures.is_empty(),
        "{} of {executed} cases differ:\n{}",
        failures.len(),
        failures.join("\n")
    );
}

#[test]
fn every_recorded_input_yields_only_well_formed_frames() {
    let fixture = mcp_cases::load_fixture();
    let mut checked = 0;
    for case in &fixture.cases {
        let (stdout, _) = drive_stream(&mcp_cases::input(case));
        if let Err(error) = check_output_frames(&stdout) {
            panic!("{}: {error}", case.id);
        }
        checked += 1;
    }
    assert_eq!(checked, fixture.cases.len());
}

// Seeded, deterministic mutation of the recorded inputs: the transport must
// never panic, must be deterministic, and must never write anything but
// well-formed frames.
#[test]
fn mutated_streams_never_panic_or_pollute_stdout() {
    let fixture = mcp_cases::load_fixture();
    let inputs: Vec<Vec<u8>> = fixture
        .cases
        .iter()
        .filter(|case| case.generate.is_none())
        .map(mcp_cases::input)
        .collect();
    let mut state = 0x9e37_79b9_7f4a_7c15_u64;
    let mut next = move || {
        state ^= state << 13;
        state ^= state >> 7;
        state ^= state << 17;
        state
    };
    let mut streams = 0;
    for _ in 0..3000 {
        let mut data = inputs[(next() % inputs.len() as u64) as usize].clone();
        if next() % 3 == 0 {
            data.extend_from_slice(&inputs[(next() % inputs.len() as u64) as usize]);
        }
        for _ in 0..(next() % 4) {
            if data.is_empty() {
                break;
            }
            let at = (next() % data.len() as u64) as usize;
            match next() % 4 {
                0 => data[at] = (next() % 256) as u8,
                1 => {
                    data.remove(at);
                }
                2 => data.insert(at, (next() % 256) as u8),
                _ => data.truncate(at),
            }
        }
        let first = drive_stream(&data);
        assert_eq!(first, drive_stream(&data), "non-deterministic transport");
        check_output_frames(&first.0).expect("only well-formed frames reach stdout");
        streams += 1;
    }
    assert_eq!(streams, 3000);
}
