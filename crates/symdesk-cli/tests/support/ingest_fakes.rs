//! Loopback fakes of the RUST-013 ingest-bodies replay (the Rust twins of the
//! fakes in `cmd/symdesk/ingest_bodies_*_port_test.go`).
use std::{any::Any, collections::BTreeMap};

/// Starts the named fake, registers the placeholders it provides and returns
/// a guard that keeps it alive for the case.
pub fn start(name: &str, _placeholders: &mut BTreeMap<String, String>) -> Box<dyn Any> {
    panic!("unknown ingest fake server {name:?}")
}
