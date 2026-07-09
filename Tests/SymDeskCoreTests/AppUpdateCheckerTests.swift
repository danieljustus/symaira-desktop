import XCTest
@testable import SymDeskCore
import SymairaUpdateCheck

private struct StubHTTPClient: UpdateHTTPClient {
    let body: String
    let status: Int

    func data(for request: URLRequest) async throws -> (Data, URLResponse) {
        let response = HTTPURLResponse(
            url: request.url!,
            statusCode: status,
            httpVersion: nil,
            headerFields: nil
        )!
        return (Data(body.utf8), response)
    }
}

private final class InMemorySkippedVersionStore: SkippedVersionStore, @unchecked Sendable {
    var tag: String?

    func skippedTag() -> String? { tag }
    func setSkippedTag(_ tag: String?) { self.tag = tag }
}

@MainActor
final class AppUpdateCheckerTests: XCTestCase {
    private var cacheDir: URL!

    override func setUpWithError() throws {
        cacheDir = FileManager.default.temporaryDirectory
            .appendingPathComponent("symdesk-updatecheck-\(UUID().uuidString)")
    }

    override func tearDownWithError() throws {
        try? FileManager.default.removeItem(at: cacheDir)
    }

    private func makeChecker(
        latestTag: String,
        status: Int = 200,
        currentVersion: String = "v1.0.0",
        store: SkippedVersionStore = InMemorySkippedVersionStore()
    ) -> AppUpdateChecker {
        let body = "{\"tag_name\":\"\(latestTag)\",\"html_url\":\"https://github.com/danieljustus/symaira-desktop/releases/tag/\(latestTag)\"}"
        let updateChecker = UpdateChecker(
            owner: "danieljustus",
            repo: "symaira-desktop",
            client: StubHTTPClient(body: body, status: status),
            cacheDirectory: cacheDir
        )
        return AppUpdateChecker(checker: updateChecker, store: store, currentVersion: { currentVersion })
    }

    func testReportsAvailableUpdate() async {
        let checker = makeChecker(latestTag: "v1.1.0")
        await checker.checkForUpdate()
        guard case .available(let release) = checker.status else {
            return XCTFail("expected .available, got \(checker.status)")
        }
        XCTAssertEqual(release.tagName, "v1.1.0")
    }

    func testUpToDateReportsUpToDate() async {
        let checker = makeChecker(latestTag: "v1.0.0")
        await checker.checkForUpdate()
        XCTAssertEqual(checker.status, .upToDate)
    }

    func testHTTPErrorReportsError() async {
        let checker = makeChecker(latestTag: "v1.1.0", status: 500)
        await checker.checkForUpdate()
        guard case .error = checker.status else {
            return XCTFail("expected .error, got \(checker.status)")
        }
    }

    func testSkippedVersionIsNotReprompted() async {
        let store = InMemorySkippedVersionStore()
        let checker = makeChecker(latestTag: "v1.1.0", store: store)
        await checker.checkForUpdate()
        guard case .available(let release) = checker.status else {
            return XCTFail("expected .available before skip")
        }

        checker.skip(release)
        XCTAssertEqual(store.skippedTag(), "v1.1.0")

        await checker.checkForUpdate()
        guard case .skipped(let skippedRelease) = checker.status else {
            return XCTFail("expected .skipped after skip, got \(checker.status)")
        }
        XCTAssertEqual(skippedRelease.tagName, "v1.1.0")
    }

    func testForceCheckBypassesSkipGate() async {
        let store = InMemorySkippedVersionStore()
        store.setSkippedTag("v1.1.0")
        let checker = makeChecker(latestTag: "v1.1.0", store: store)

        await checker.checkForUpdate(force: true)
        guard case .available(let release) = checker.status else {
            return XCTFail("expected .available with force=true, got \(checker.status)")
        }
        XCTAssertEqual(release.tagName, "v1.1.0")
    }

    func testNewSkippedTagSupersedesOldOne() async {
        let store = InMemorySkippedVersionStore()
        store.setSkippedTag("v1.0.5")
        let checker = makeChecker(latestTag: "v1.1.0", store: store)

        await checker.checkForUpdate()
        guard case .available(let release) = checker.status else {
            return XCTFail("expected .available since skipped tag doesn't match latest, got \(checker.status)")
        }
        XCTAssertEqual(release.tagName, "v1.1.0")
    }
}
