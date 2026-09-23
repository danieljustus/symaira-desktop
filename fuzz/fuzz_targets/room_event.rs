#![no_main]

use libfuzzer_sys::fuzz_target;
use symroom_core::event::Event;

fuzz_target!(|input: &[u8]| {
    if let Ok(event) = Event::unmarshal_json_line(input) {
        let line = event.marshal_json_line().expect("parsed event marshals");
        let replayed = Event::unmarshal_json_line(&line).expect("marshalled event parses");
        assert_eq!(
            replayed
                .marshal_json_line()
                .expect("replayed event marshals"),
            line
        );
    }
});
