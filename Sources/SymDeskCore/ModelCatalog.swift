import Foundation

/// A single downloadable model artifact as referenced by the app.
///
/// Models are referenced by an internal name (`id`) everywhere in the UI and
/// in persisted state; the concrete download URL, pinned revision and checksum
/// live only in the catalog. A descriptor describes exactly **one artifact
/// file** — the download manager verifies and installs a single file per model.
/// Models that ship as a directory of files (e.g. MLX safetensors shards)
/// need a packaging step (single archive) or a follow-up extension of the
/// descriptor before they can be added to the catalog.
public struct ModelDescriptor: Codable, Equatable, Identifiable, Sendable {
    /// Internal, stable name used in the UI, on disk and in persisted state.
    /// Never a foreign URL — see `downloadURL`.
    public let id: String

    /// Human-readable model name shown in the UI.
    public let displayName: String

    /// File name of the artifact inside the model directory.
    public let filename: String

    /// Pinned, immutable download location for the artifact. The revision must
    /// be fixed at catalog build time — "latest" is never allowed.
    public let downloadURL: URL

    /// The pinned model revision this descriptor refers to (human-readable,
    /// recorded in the install manifest for provenance).
    public let pinnedRevision: String

    /// Expected SHA-256 of the artifact, hex encoded, lowercase.
    public let expectedSHA256: String

    /// Expected artifact size in bytes, used for the pre-download free-space
    /// check and shown to the user before download.
    public let sizeBytes: Int64

    /// License name (e.g. "Apache-2.0") shown together with `licenseURL`.
    public let licenseName: String

    /// License page shown to the user before the download is allowed to start.
    public let licenseURL: URL

    public init(
        id: String,
        displayName: String,
        filename: String,
        downloadURL: URL,
        pinnedRevision: String,
        expectedSHA256: String,
        sizeBytes: Int64,
        licenseName: String,
        licenseURL: URL
    ) {
        self.id = id
        self.displayName = displayName
        self.filename = filename
        self.downloadURL = downloadURL
        self.pinnedRevision = pinnedRevision
        self.expectedSHA256 = expectedSHA256
        self.sizeBytes = sizeBytes
        self.licenseName = licenseName
        self.licenseURL = licenseURL
    }
}

/// The registry of models the app can download.
///
/// Deliberately empty right now: selecting concrete models is out of scope for
/// the download-infrastructure issue (#348) and belongs to the model-selection
/// issues (#347 on-device embeddings, #350 on-device OCR). The download
/// mechanism, its app-owned storage location, checksum verification, space
/// check, license gate, cancel/resume and removal are fully implemented and
/// tested against synthetic descriptors in `ModelDownloadManagerTests`.
///
/// Adding a model is a catalog-only change:
/// ```
/// ModelCatalog.all = [
///     ModelDescriptor(
///         id: "qwen3-embedding-0.6b-4bit",
///         displayName: "Qwen3 Embedding 0.6B (4-bit)",
///         filename: "model.zip",
///         downloadURL: URL(string: "https://…/resolve/<pinned-revision>/model.zip")!,
///         pinnedRevision: "<sha>",
///         expectedSHA256: "<sha256 of the artifact>",
///         sizeBytes: 320_000_000,
///         licenseName: "Apache-2.0",
///         licenseURL: URL(string: "https://huggingface.co/…/blob/<revision>/LICENSE")!
///     ),
/// ]
/// ```
public enum ModelCatalog {
    public static let all: [ModelDescriptor] = []
}
