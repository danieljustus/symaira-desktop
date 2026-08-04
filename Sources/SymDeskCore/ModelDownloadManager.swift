import CryptoKit
import Foundation

public enum ModelInstallError: Error, Sendable, Equatable {
    case licenseNotAccepted
    case invalidDescriptor
    case insufficientSpace(availableBytes: Int64, requiredBytes: Int64)
    case transport(String)
    case checksumMismatch(expected: String, actual: String)
    case fileSystem(String)

    public var errorDescription: String? {
        switch self {
        case .licenseNotAccepted:
            return "The model license must be accepted before downloading."
        case .invalidDescriptor:
            return "The model descriptor is invalid."
        case .insufficientSpace(let available, let required):
            return "Not enough free disk space: \(ByteCountFormatter.string(fromByteCount: available, countStyle: .file)) available, \(ByteCountFormatter.string(fromByteCount: required, countStyle: .file)) required."
        case .transport(let message):
            return "Download failed: \(message)"
        case .checksumMismatch:
            return "Downloaded model failed its checksum verification and was discarded."
        case .fileSystem(let message):
            return "Model storage error: \(message)"
        }
    }
}

/// Per-model install state as observed by the UI.
public enum ModelInstallState: Equatable, Sendable {
    case notInstalled
    case downloading(progress: Double)
    case paused
    case installed
    case failed(message: String)

    public var isInstalled: Bool {
        if case .installed = self { return true }
        return false
    }

    public var isDownloading: Bool {
        if case .downloading = self { return true }
        return false
    }
}

/// Injectable free-space provider so tests never depend on the real volume.
public protocol ModelSpaceChecker: Sendable {
    func availableBytes(at url: URL) throws -> Int64
}

public struct DefaultModelSpaceChecker: ModelSpaceChecker {
    public init() {}

    public func availableBytes(at url: URL) throws -> Int64 {
        let values = try url.resourceValues(forKeys: [.volumeAvailableCapacityForImportantUsageKey])
        return values.volumeAvailableCapacityForImportantUsage ?? 0
    }
}

/// Injectable file-system facade so tests never touch the real model storage.
public protocol ModelFileSystem: Sendable {
    func fileExists(at url: URL) -> Bool
    func createDirectory(at url: URL, withIntermediateDirectories: Bool) throws
    func moveItem(at source: URL, to destination: URL) throws
    func removeItem(at url: URL) throws
    func subdirectories(at url: URL) -> [URL]
}

public struct DefaultModelFileSystem: ModelFileSystem {
    public init() {}

    public func fileExists(at url: URL) -> Bool {
        FileManager.default.fileExists(atPath: url.path)
    }

    public func createDirectory(at url: URL, withIntermediateDirectories: Bool) throws {
        try FileManager.default.createDirectory(at: url, withIntermediateDirectories: withIntermediateDirectories)
    }

    public func moveItem(at source: URL, to destination: URL) throws {
        try FileManager.default.moveItem(at: source, to: destination)
    }

    public func removeItem(at url: URL) throws {
        try FileManager.default.removeItem(at: url)
    }

    public func subdirectories(at url: URL) -> [URL] {
        (try? FileManager.default.contentsOfDirectory(at: url, includingPropertiesForKeys: [.isDirectoryKey]))
            .map { $0.filter { (try? $0.resourceValues(forKeys: [.isDirectoryKey]))?.isDirectory == true } }
            ?? []
    }
}

/// Persisted per-model install record. Written only after the artifact passed
/// checksum verification; the model directory's manifest is the source of
/// truth for "is this model installed".
struct ModelManifest: Codable, Equatable {
    let modelID: String
    let displayName: String
    let filename: String
    let revision: String
    let sha256: String
    let licenseName: String
    let installedAt: Date
}

/// Downloads models into an app-owned directory under
/// `~/Library/Application Support/` and verifies them before install.
///
/// Properties the implementation guarantees:
/// - **Never writes into the app bundle** — the storage root is
///   `Application Support/SymDesk/Models`, so the app stays signature-valid
///   after downloads and restarts.
/// - **Pinned revision + checksum** — the descriptor carries the immutable
///   URL and expected SHA-256; a mismatch aborts the install and deletes the
///   partial artifact.
/// - **Space check first** — the declared artifact size is compared against
///   the volume's available capacity before any byte is downloaded.
/// - **License gate** — `install(_:licenseAccepted:)` refuses to start unless
///   the user accepted the model's license (link shown in the UI).
/// - **Progress / cancel / resume** — download tasks report progress; cancel
///   keeps resume data (when the transport provides it) and moves the model
///   to `.paused`; resume continues or restarts.
/// - **Removal** — `remove` deletes the whole model directory.
///
/// Session lifecycle: a `URLSession` is created on demand for a download
/// batch and invalidated as soon as no tasks remain. The session is never
/// invalidated from `deinit` — doing so would deliver the invalidation
/// callback to an already-deallocating delegate and crash.
@MainActor
public final class ModelDownloadManager: NSObject, ObservableObject {
    @Published public private(set) var states: [String: ModelInstallState] = [:]

    /// App-owned storage root. Never the bundle, never the system cache.
    public let modelsDirectory: URL

    private let sessionConfiguration: URLSessionConfiguration
    private let spaceChecker: ModelSpaceChecker
    private let fileSystem: ModelFileSystem

    /// Active download session, created on demand and invalidated when the
    /// task table empties. All access happens on the main actor.
    private var session: URLSession?

    private var descriptors: [String: ModelDescriptor] = [:]
    private var tasks: [String: URLSessionDownloadTask] = [:]
    private var resumeData: [String: Data] = [:]
    private var acceptedLicenses: Set<String> = []

    /// The app-owned model storage root.
    public static func defaultModelsDirectory() -> URL {
        let base = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first
            ?? FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent("Library/Application Support")
        return base.appendingPathComponent("SymDesk/Models", isDirectory: true)
    }

    public init(
        modelsDirectory: URL? = nil,
        sessionConfiguration: URLSessionConfiguration = .default,
        spaceChecker: ModelSpaceChecker = DefaultModelSpaceChecker(),
        fileSystem: ModelFileSystem = DefaultModelFileSystem()
    ) {
        self.modelsDirectory = modelsDirectory ?? Self.defaultModelsDirectory()
        self.sessionConfiguration = sessionConfiguration
        self.spaceChecker = spaceChecker
        self.fileSystem = fileSystem
        super.init()
        refreshInstalledStates()
    }

    // MARK: - Queries

    public func state(for model: ModelDescriptor) -> ModelInstallState {
        states[model.id] ?? (isInstalled(model) ? .installed : .notInstalled)
    }

    public func isInstalled(_ model: ModelDescriptor) -> Bool {
        installedURL(for: model) != nil
    }

    /// The verified artifact location, or nil when the model is not installed.
    public func installedURL(for model: ModelDescriptor) -> URL? {
        let url = modelDirectory(model).appendingPathComponent(model.filename)
        guard fileSystem.fileExists(at: url) else { return nil }
        return url
    }

    public func modelDirectory(_ model: ModelDescriptor) -> URL {
        modelsDirectory.appendingPathComponent(model.id, isDirectory: true)
    }

    /// Re-scans the storage root so states reflect previously installed
    /// models (e.g. after a relaunch).
    public func refreshInstalledStates() {
        guard fileSystem.fileExists(at: modelsDirectory) else { return }
        for dir in fileSystem.subdirectories(at: modelsDirectory) {
            let manifestURL = dir.appendingPathComponent("manifest.json")
            guard let data = try? Data(contentsOf: manifestURL),
                  let manifest = try? JSONDecoder().decode(ModelManifest.self, from: data) else { continue }
            let artifact = dir.appendingPathComponent(manifest.filename)
            if fileSystem.fileExists(at: artifact) {
                states[manifest.modelID] = .installed
            }
        }
    }

    // MARK: - Lifecycle

    /// Starts (or resumes) the download for a model.
    ///
    /// - Parameter licenseAccepted: the user must have accepted the model
    ///   license (link surfaced in the UI) — otherwise `.licenseNotAccepted`
    ///   is thrown and nothing is downloaded.
    public func install(_ model: ModelDescriptor, licenseAccepted: Bool) throws {
        if isInstalled(model) {
            states[model.id] = .installed
            return
        }
        guard licenseAccepted || acceptedLicenses.contains(model.id) else {
            throw ModelInstallError.licenseNotAccepted
        }
        guard !model.id.isEmpty, model.filename != "", model.downloadURL.scheme != nil else {
            throw ModelInstallError.invalidDescriptor
        }

        // Pre-flight: free space must cover the declared artifact size.
        let available = try spaceChecker.availableBytes(at: modelsDirectory)
        guard available >= model.sizeBytes else {
            throw ModelInstallError.insufficientSpace(availableBytes: available, requiredBytes: model.sizeBytes)
        }

        acceptedLicenses.insert(model.id)
        descriptors[model.id] = model

        let activeSession = session ?? makeSession()
        if let data = resumeData[model.id] {
            let task = activeSession.downloadTask(withResumeData: data)
            tasks[model.id] = task
            states[model.id] = .downloading(progress: 0)
            task.resume()
            return
        }

        try fileSystem.createDirectory(at: modelDirectory(model), withIntermediateDirectories: true)
        let task = activeSession.downloadTask(with: model.downloadURL)
        tasks[model.id] = task
        states[model.id] = .downloading(progress: 0)
        task.resume()
    }

    /// Cancels an in-flight download. The model moves to `.paused`; resume
    /// data is kept when the transport provides it, otherwise `resume` starts
    /// the download from scratch.
    public func cancel(_ model: ModelDescriptor) {
        guard let task = tasks[model.id] else { return }
        tasks[model.id] = nil
        task.cancel { [weak self] data in
            Task { @MainActor in
                guard let self else { return }
                self.resumeData[model.id] = data
                self.states[model.id] = .paused
                self.invalidateSessionIfIdle()
            }
        }
    }

    /// Deletes an installed model (or aborts and deletes a partial download)
    /// including its manifest. The storage directory is removed entirely.
    public func remove(_ model: ModelDescriptor) throws {
        if let task = tasks[model.id] {
            tasks[model.id] = nil
            task.cancel() // no completion handler: removal owns the state
        }
        resumeData[model.id] = nil
        try fileSystem.removeItem(at: modelDirectory(model))
        states[model.id] = .notInstalled
        invalidateSessionIfIdle()
    }

    // MARK: - Install pipeline

    private func makeSession() -> URLSession {
        let configuration = sessionConfiguration
        configuration.httpMaximumConnectionsPerHost = 1
        let newSession = URLSession(configuration: configuration, delegate: self, delegateQueue: .main)
        session = newSession
        return newSession
    }

    /// Invalidates the batch session once no download tasks remain. Only
    /// called on the main actor while `self` is alive, so the invalidation
    /// callback (delivered on the main queue) can never hit a deallocated
    /// delegate.
    private func invalidateSessionIfIdle() {
        guard tasks.isEmpty else { return }
        session?.invalidateAndCancel()
        session = nil
    }

    private func verifyAndInstall(_ model: ModelDescriptor, downloadedFile: URL) throws {
        let actual = try sha256(of: downloadedFile)
        guard actual == model.expectedSHA256 else {
            try? fileSystem.removeItem(at: downloadedFile)
            throw ModelInstallError.checksumMismatch(expected: model.expectedSHA256, actual: actual)
        }

        let modelDir = modelDirectory(model)
        try fileSystem.createDirectory(at: modelDir, withIntermediateDirectories: true)
        let destination = modelDir.appendingPathComponent(model.filename)
        try fileSystem.moveItem(at: downloadedFile, to: destination)

        let manifest = ModelManifest(
            modelID: model.id,
            displayName: model.displayName,
            filename: model.filename,
            revision: model.pinnedRevision,
            sha256: model.expectedSHA256,
            licenseName: model.licenseName,
            installedAt: Date()
        )
        let manifestURL = modelDir.appendingPathComponent("manifest.json")
        let data = try JSONEncoder().encode(manifest)
        try data.write(to: manifestURL, options: .atomic)
    }

    private func sha256(of url: URL) throws -> String {
        let handle = try FileHandle(forReadingFrom: url)
        defer { try? handle.close() }
        var hasher = SHA256()
        while let chunk = try handle.read(upToCount: 1 << 20) {
            hasher.update(data: chunk)
        }
        return hasher.finalize().map { String(format: "%02x", $0) }.joined()
    }
}

// MARK: - URLSessionDownloadDelegate
//
// The session is created with `delegateQueue: .main` (see `makeSession`), so
// every delegate callback executes on the main thread. The witnesses are
// explicitly `nonisolated` to satisfy the nonisolated protocol under strict
// concurrency, and `MainActor.assumeIsolated` is the sanctioned way to touch
// main-actor state from a context that is guaranteed (by the delegate queue)
// to be the main thread.

extension ModelDownloadManager: URLSessionDownloadDelegate {
    nonisolated public func urlSession(
        _ session: URLSession,
        downloadTask: URLSessionDownloadTask,
        didWriteData bytesWritten: Int64,
        totalBytesWritten: Int64,
        totalBytesExpectedToWrite: Int64
    ) {
        MainActor.assumeIsolated {
            guard let modelID = tasks.first(where: { $0.value === downloadTask })?.key else { return }
            let progress: Double = totalBytesExpectedToWrite > 0
                ? min(1.0, Double(totalBytesWritten) / Double(totalBytesExpectedToWrite))
                : 0
            states[modelID] = .downloading(progress: progress)
        }
    }

    nonisolated public func urlSession(
        _ session: URLSession,
        downloadTask: URLSessionDownloadTask,
        didFinishDownloadingTo location: URL
    ) {
        MainActor.assumeIsolated {
            guard let modelID = tasks.first(where: { $0.value === downloadTask })?.key,
                  let model = descriptors[modelID] else { return }
            tasks[modelID] = nil
            do {
                try verifyAndInstall(model, downloadedFile: location)
                states[modelID] = .installed
            } catch let error as ModelInstallError {
                states[modelID] = .failed(message: error.errorDescription ?? String(describing: error))
            } catch {
                states[modelID] = .failed(message: error.localizedDescription)
            }
            invalidateSessionIfIdle()
        }
    }

    nonisolated public func urlSession(
        _ session: URLSession,
        task: URLSessionTask,
        didCompleteWithError error: (any Error)?
    ) {
        MainActor.assumeIsolated {
            guard let downloadTask = task as? URLSessionDownloadTask,
                  let error,
                  let modelID = tasks.first(where: { $0.value === downloadTask })?.key else { return }
            // A deliberate cancel is handled by cancel(_:)'s completion handler.
            guard (error as NSError).code != NSURLErrorCancelled else { return }
            tasks[modelID] = nil
            states[modelID] = .failed(message: error.localizedDescription)
            invalidateSessionIfIdle()
        }
    }
}
