import CryptoKit
import Foundation
import XCTest
@testable import SymDeskCore

// MARK: - Mock transport

/// Serves canned responses for every request; records call count so tests can
/// prove whether a download actually started.
final class MockURLProtocol: URLProtocol {
    nonisolated(unsafe) static var requestHandler: ((URLRequest) throws -> (HTTPURLResponse, Data))?
    nonisolated(unsafe) static var requestCount = 0
    /// When > 0 the payload is delivered in two halves with a pause in
    /// between, so a test can cancel the task mid-flight.
    nonisolated(unsafe) static var midFlightDelay: TimeInterval = 0

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        guard let handler = MockURLProtocol.requestHandler else {
            client?.urlProtocol(self, didFailWithError: URLError(.badURL))
            return
        }
        do {
            let (response, data) = try handler(request)
            MockURLProtocol.requestCount += 1
            client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
            if MockURLProtocol.midFlightDelay > 0, data.count > 1 {
                let midpoint = data.count / 2
                client?.urlProtocol(self, didLoad: Data(data.prefix(midpoint)))
                Thread.sleep(forTimeInterval: MockURLProtocol.midFlightDelay)
                client?.urlProtocol(self, didLoad: Data(data.dropFirst(midpoint)))
            } else {
                client?.urlProtocol(self, didLoad: data)
            }
            client?.urlProtocolDidFinishLoading(self)
        } catch {
            client?.urlProtocol(self, didFailWithError: error)
        }
    }

    override func stopLoading() {}
}

// MARK: - Stubs

struct StubSpaceChecker: ModelSpaceChecker {
    var available: Int64
    func availableBytes(at url: URL) throws -> Int64 { available }
}

// MARK: - Tests

@MainActor
final class ModelDownloadManagerTests: XCTestCase {
    private let payload = Data("hello model payload".utf8)

    private var modelsDirectory: URL!

    override func setUp() {
        super.setUp()
        modelsDirectory = FileManager.default.temporaryDirectory
            .appendingPathComponent("symdesk-model-tests-\(UUID().uuidString)", isDirectory: true)
        MockURLProtocol.requestHandler = { [payload] _ in
            (HTTPURLResponse(url: URL(string: "https://example.invalid/model.bin")!,
                             statusCode: 200, httpVersion: nil, headerFields: nil)!, payload)
        }
        MockURLProtocol.requestCount = 0
        MockURLProtocol.midFlightDelay = 0
    }

    override func tearDown() {
        try? FileManager.default.removeItem(at: modelsDirectory)
        MockURLProtocol.requestHandler = nil
        super.tearDown()
    }

    private func sessionConfiguration() -> URLSessionConfiguration {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [MockURLProtocol.self]
        return config
    }

    private func makeManager(spaceAvailable: Int64 = 1 << 30) -> ModelDownloadManager {
        ModelDownloadManager(
            modelsDirectory: modelsDirectory,
            sessionConfiguration: sessionConfiguration(),
            spaceChecker: StubSpaceChecker(available: spaceAvailable)
        )
    }

    private func makeModel(sha: String? = nil, sizeBytes: Int64 = 128) -> ModelDescriptor {
        ModelDescriptor(
            id: "test-model",
            displayName: "Test Model",
            filename: "model.bin",
            downloadURL: URL(string: "https://example.invalid/models/test-model/model.bin")!,
            pinnedRevision: "rev-123",
            expectedSHA256: sha ?? Self.sha256Hex(of: payload),
            sizeBytes: sizeBytes,
            licenseName: "Apache-2.0",
            licenseURL: URL(string: "https://example.invalid/models/test-model/LICENSE")!
        )
    }

    private static func sha256Hex(of data: Data) -> String {
        SHA256.hash(data: data).map { String(format: "%02x", $0) }.joined()
    }

    private func waitUntil(
        _ condition: @escaping @MainActor () -> Bool,
        timeout: TimeInterval = 8
    ) async {
        let deadline = Date().addingTimeInterval(timeout)
        while !(await condition()) && Date() < deadline {
            try? await Task.sleep(nanoseconds: 50_000_000)
        }
    }

    // MARK: Storage location

    func testDefaultModelsDirectoryIsOutsideAppBundle() {
        let dir = ModelDownloadManager.defaultModelsDirectory()
        XCTAssertFalse(dir.path.hasPrefix(Bundle.main.bundleURL.path),
                       "Model storage must never live inside the app bundle")
        XCTAssertEqual(dir.lastPathComponent, "Models")
        XCTAssertTrue(dir.path.contains("Application Support"))
    }

    // MARK: Gates

    func testInstallRequiresLicenseAcceptance() async throws {
        let manager = makeManager()
        let model = makeModel()
        XCTAssertThrowsError(try manager.install(model, licenseAccepted: false)) { error in
            XCTAssertEqual(error as? ModelInstallError, .licenseNotAccepted)
        }
        XCTAssertEqual(manager.state(for: model), .notInstalled)
        try? await Task.sleep(nanoseconds: 100_000_000)
        XCTAssertEqual(MockURLProtocol.requestCount, 0, "no download may start without license acceptance")
    }

    func testInstallChecksSpaceBeforeDownload() async throws {
        let manager = makeManager(spaceAvailable: 64) // less than sizeBytes 128
        let model = makeModel()
        XCTAssertThrowsError(try manager.install(model, licenseAccepted: true)) { error in
            guard case .insufficientSpace(let available, let required) = error as? ModelInstallError else {
                return XCTFail("expected insufficientSpace, got \(error)")
            }
            XCTAssertEqual(available, 64)
            XCTAssertEqual(required, 128)
        }
        XCTAssertEqual(manager.state(for: model), .notInstalled)
        try? await Task.sleep(nanoseconds: 100_000_000)
        XCTAssertEqual(MockURLProtocol.requestCount, 0, "no download may start without enough space")
    }

    // MARK: Happy path

    func testInstallDownloadsVerifiesAndInstalls() async throws {
        let manager = makeManager()
        let model = makeModel()

        try manager.install(model, licenseAccepted: true)
        await waitUntil { manager.state(for: model) == .installed }

        XCTAssertEqual(MockURLProtocol.requestCount, 1)
        XCTAssertTrue(manager.isInstalled(model))

        let installed = try XCTUnwrap(manager.installedURL(for: model))
        XCTAssertEqual(installed.lastPathComponent, "model.bin")
        XCTAssertEqual(try Data(contentsOf: installed), payload)

        // Manifest persisted with pinned revision + checksum for provenance.
        let manifestURL = manager.modelDirectory(model).appendingPathComponent("manifest.json")
        let manifest = try JSONDecoder().decode(ModelManifest.self, from: Data(contentsOf: manifestURL))
        XCTAssertEqual(manifest.modelID, model.id)
        XCTAssertEqual(manifest.revision, "rev-123")
        XCTAssertEqual(manifest.sha256, model.expectedSHA256)
        XCTAssertEqual(manifest.filename, "model.bin")
    }

    func testInstallIdempotentWhenAlreadyInstalled() async throws {
        let manager = makeManager()
        let model = makeModel()
        try manager.install(model, licenseAccepted: true)
        await waitUntil { manager.state(for: model) == .installed }

        try manager.install(model, licenseAccepted: false) // already installed → no license needed
        XCTAssertEqual(manager.state(for: model), .installed)
        XCTAssertEqual(MockURLProtocol.requestCount, 1, "no second download")
    }

    // MARK: Checksum

    func testChecksumMismatchAbortsAndDeletesPartial() async throws {
        let manager = makeManager()
        let model = makeModel(sha: String(repeating: "0", count: 64)) // wrong checksum

        try manager.install(model, licenseAccepted: true)
        await waitUntil {
            if case .failed = manager.state(for: model) { return true }
            return false
        }

        guard case .failed(let message) = manager.state(for: model) else {
            return XCTFail("expected failed state")
        }
        XCTAssertTrue(message.contains("checksum"), "failure message should name the checksum: \(message)")
        XCTAssertFalse(manager.isInstalled(model))
        XCTAssertNil(manager.installedURL(for: model))

        // No manifest, no leftover artifact.
        let manifestURL = manager.modelDirectory(model).appendingPathComponent("manifest.json")
        XCTAssertFalse(FileManager.default.fileExists(atPath: manifestURL.path))
        let leftovers = try FileManager.default.contentsOfDirectory(atPath: manager.modelDirectory(model).path)
        XCTAssertTrue(leftovers.isEmpty, "partial download must be cleaned up, found \(leftovers)")
    }

    // MARK: Cancel / resume

    func testCancelPausesAndResumeCompletes() async throws {
        MockURLProtocol.midFlightDelay = 0.4
        let manager = makeManager()
        let model = makeModel()

        try manager.install(model, licenseAccepted: true)
        // Wait until the download started, then cancel mid-flight.
        await waitUntil { manager.state(for: model).isDownloading }
        try? await Task.sleep(nanoseconds: 150_000_000)
        manager.cancel(model)

        await waitUntil { manager.state(for: model) == .paused }
        XCTAssertEqual(manager.state(for: model), .paused)

        // Resume: license already accepted, download completes.
        MockURLProtocol.midFlightDelay = 0
        try manager.install(model, licenseAccepted: false)
        await waitUntil { manager.state(for: model) == .installed }
        XCTAssertTrue(manager.isInstalled(model))
        XCTAssertEqual(MockURLProtocol.requestCount, 2, "resume must re-fetch the artifact")
    }

    // MARK: Removal

    func testRemoveDeletesModelDirectoryAndManifest() async throws {
        let manager = makeManager()
        let model = makeModel()
        try manager.install(model, licenseAccepted: true)
        await waitUntil { manager.state(for: model) == .installed }

        try manager.remove(model)

        XCTAssertEqual(manager.state(for: model), .notInstalled)
        XCTAssertFalse(manager.isInstalled(model))
        XCTAssertFalse(FileManager.default.fileExists(atPath: manager.modelDirectory(model).path))
    }

    // MARK: Persistence across manager instances

    func testInstalledStateSurvivesManagerRecreation() async throws {
        let managerA = makeManager()
        let model = makeModel()
        try managerA.install(model, licenseAccepted: true)
        await waitUntil { managerA.state(for: model) == .installed }

        // A fresh manager (as after relaunch) must see the installed model
        // purely from the on-disk manifest.
        let managerB = makeManager()
        XCTAssertTrue(managerB.isInstalled(model))
        XCTAssertEqual(managerB.state(for: model), .installed)
    }
}
