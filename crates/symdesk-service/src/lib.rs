#![deny(unsafe_code)]

//! Go `internal/service`-level composition: reviewed recipe runs, sibling-tool
//! discovery consumers and AI provider orchestration. Everything here is
//! library code driven by explicit inputs so the CLI, MCP and HTTP facades
//! share one implementation.

pub mod gofmt;
pub mod recipes;
