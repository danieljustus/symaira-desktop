//! Resolve configured LLM keys without exposing secret references on failure.
//! The implementation (and its tests) live in `symdesk_service::secrets`.

pub use symdesk_service::secrets::resolve_key;
