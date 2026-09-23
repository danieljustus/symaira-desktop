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
//! ponytail: identity resolution covers the environment and file chains only.
//! The `symvault` shell-out and the macOS Keychain chain of the Go `Load` stay
//! with the CLI port, because they are process and platform behaviour rather
//! than a signed-bytes contract; add them when ROOMCLI-002 lands and drive them
//! with a live differential instead of vectors.

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
