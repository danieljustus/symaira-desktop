//! Exact persisted TurboQuant sidecar blob framing only.
//!
//! The Go wire shape is two little-endian `f32` bit patterns followed by
//! opaque packed bytes. This module does not decode the packed code or assign
//! semantics to its contents.

use thiserror::Error;

/// Number of bytes in the persisted little-endian min/max header.
pub const RETRIEVAL_QUANT_SIDECAR_HEADER_BYTES: usize = 8;

/// A TurboQuant sidecar header and its uninterpreted packed payload.
#[derive(Clone, Debug, PartialEq)]
pub struct RetrievalQuantSidecar {
    /// Min value stored as the first little-endian `f32` in the blob.
    pub min: f32,
    /// Max value stored as the second little-endian `f32` in the blob.
    pub max: f32,
    /// Packed code bytes copied verbatim from the blob; their layout is opaque.
    pub packed: Vec<u8>,
}

/// Error returned when a persisted sidecar blob is shorter than its header.
#[derive(Clone, Copy, Debug, Eq, Error, PartialEq)]
pub enum RetrievalQuantSidecarError {
    /// The Go unpacker rejects every blob shorter than eight bytes.
    #[error("turboquant: code bytes too short: blob {actual} bytes, need >= 8")]
    HeaderTooShort { actual: usize },
}

impl RetrievalQuantSidecar {
    /// Reads the exact Go min/max header and copies the remaining bytes.
    ///
    /// Exactly eight bytes is valid and represents an empty packed payload.
    /// No constraints are imposed on min/max ordering, finiteness, or payload
    /// length because the Go `UnpackSidecarBlob` helper imposes none.
    pub fn read_blob(blob: &[u8]) -> Result<Self, RetrievalQuantSidecarError> {
        if blob.len() < RETRIEVAL_QUANT_SIDECAR_HEADER_BYTES {
            return Err(RetrievalQuantSidecarError::HeaderTooShort { actual: blob.len() });
        }

        let min = f32::from_bits(u32::from_le_bytes([blob[0], blob[1], blob[2], blob[3]]));
        let max = f32::from_bits(u32::from_le_bytes([blob[4], blob[5], blob[6], blob[7]]));

        Ok(Self {
            min,
            max,
            packed: blob[RETRIEVAL_QUANT_SIDECAR_HEADER_BYTES..].to_vec(),
        })
    }

    /// Writes min/max as their exact little-endian `f32` bits, then appends
    /// the opaque packed payload unchanged.
    #[must_use]
    pub fn to_blob(&self) -> Vec<u8> {
        let mut blob = Vec::with_capacity(RETRIEVAL_QUANT_SIDECAR_HEADER_BYTES + self.packed.len());
        blob.extend_from_slice(&self.min.to_bits().to_le_bytes());
        blob.extend_from_slice(&self.max.to_bits().to_le_bytes());
        blob.extend_from_slice(&self.packed);
        blob
    }
}
