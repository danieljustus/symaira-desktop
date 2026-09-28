#![deny(unsafe_code)]

//! SymRoom core: the Go `internal/room/{identity,event}` model ported for the
//! Go→Rust migration (contract row ROOM-001).
//!
//! The crate is driven by Go-generated vectors
//! (`testdata/port/room/identity-events.json`, written by
//! `internal/room/room/port_identity_event_contract_test.go`): member ids,
//! canonical signed bytes, signatures, the JSON line format and the identity
//! file round trip are compared byte for byte against the Go implementation.
//!
//! Identity resolution follows the Go environment, optional `symvault`,
//! macOS Keychain, and file fallback chain. Provider fallbacks are tested
//! with isolated fake executables.

pub mod approval;
pub mod artifact;
pub mod desk_watch;
pub mod event;
pub mod identity;
pub mod index;
pub mod journal;
pub mod log;
pub mod members;
pub mod room_init;
pub mod runs;
