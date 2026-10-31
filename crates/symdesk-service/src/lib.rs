#![deny(unsafe_code)]

//! Go `internal/service`-level composition: reviewed recipe runs, sibling-tool
//! adapters and AI provider orchestration. Everything here is library code
//! driven by explicit inputs so the CLI, MCP and HTTP facades share one
//! implementation.

pub mod ai;
pub mod gofmt;
pub(crate) mod gojson;
pub mod memory;
pub mod process;
pub mod recipes;
