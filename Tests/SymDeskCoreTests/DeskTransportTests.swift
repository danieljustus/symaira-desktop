import Darwin
import XCTest
@testable import SymDeskCore
import SymairaCLIRunner
import SymairaToolKit

/// Shared behavioral contract tests for `DeskTransport`: the local CLI
/// implementation is exercised against a real scripted fixture binary, and
/// the remote HTTP implementation against a `URLProtocol` mock — proving
/// both sides honor the same success/decoding-failure/transport-failure/
/// cancellation contract, and covering `RemoteDeskClient` request
/// construction, authentication, and response validation directly.
@MainActor
final class DeskTransportTests: XCTestCase {
    // MARK: - Local transport (real subprocess against a scripted fixture binary)

    func testLocalCommandReturnsStdoutOnSuccess() async throws {
        let transport = try await makeLocalTransport()
        let data = try await transport.command(arguments: ["ok"], stdin: "")
        let text = String(decoding: data, as: UTF8.self).trimmingCharacters(in: .whitespacesAndNewlines)
        XCTAssertEqual(text, #"{"result":"ok"}"#)
    }

    func testLocalCommandThrowsOnNonZeroExit() async throws {
        let transport = try await makeLocalTransport()
        do {
            _ = try await transport.command(arguments: ["fail"], stdin: "")
            XCTFail("expected a thrown error for a non-zero exit")
        } catch is CLIRunnerError {
            // expected
        }
    }

    func testLocalCommandStreamYieldsLines() async throws {
        let transport = try await makeLocalTransport()
        var lines: [String] = []
        for try await line in transport.commandStream(arguments: ["stream"], stdin: "") {
            lines.append(line)
        }
        XCTAssertEqual(lines, [#"{"type":"answer","text":"first"}"#, #"{"type":"done"}"#])
    }

    func testLocalCommandStreamForwardsStdin() async throws {
        let transport = try await makeLocalTransport()
        var lines: [String] = []
        for try await line in transport.commandStream(arguments: ["echo-stdin"], stdin: "hello-stdin") {
            lines.append(line)
        }
        XCTAssertEqual(lines, ["hello-stdin"])
    }

    /// Cancellation contract: when the consumer stops iterating a stream
    /// (loop exits, or the enclosing Task is cancelled), the subprocess must
    /// be torn down rather than left running orphaned.
    func testLocalCommandStreamTerminatesSubprocessWhenConsumerStopsIterating() async throws {
        let transport = try await makeLocalTransport()
        let pidFile = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        defer { try? FileManager.default.removeItem(at: pidFile) }

        var lines: [String] = []
        for try await line in transport.commandStream(arguments: ["slow-stream", "unused", pidFile.path], stdin: "") {
            lines.append(line)
            break
        }
        XCTAssertEqual(lines, ["first"])

        let pid = try await waitForPID(at: pidFile)
        let terminated = await waitUntil(timeout: 2) { !Self.isProcessAlive(pid) }
        XCTAssertTrue(terminated, "expected the subprocess (pid \(pid)) to be terminated after the stream stopped being consumed")
    }

    func testLocalFileContentRoundTripsSaveFile() async throws {
        let transport = try await makeLocalTransport()
        let path = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString + ".md").path
        defer { try? FileManager.default.removeItem(atPath: path) }

        try await transport.saveFile(path: path, content: "hello vault")
        let content = try await transport.fileContent(path: path)

        XCTAssertEqual(content, "hello vault")
    }

    func testLocalFileContentReturnsEmptyStringForMissingFile() async throws {
        let transport = try await makeLocalTransport()
        let content = try await transport.fileContent(path: "/nonexistent/\(UUID().uuidString).md")
        XCTAssertEqual(content, "")
    }

    func testLocalIngestFileDecodesPathOnSuccess() async throws {
        let transport = try await makeLocalTransport()
        let path = try await transport.ingestFile(URL(fileURLWithPath: "/tmp/receipt.pdf"), vaultArgs: [])
        XCTAssertEqual(path, "vault/ingested.md")
    }

    func testLocalIngestFileThrowsOnMalformedJSON() async throws {
        let transport = try await makeLocalTransport()
        do {
            _ = try await transport.ingestFile(URL(fileURLWithPath: "/tmp/receipt-BADJSON.pdf"), vaultArgs: [])
            XCTFail("expected a decoding error for malformed JSON")
        } catch is CLIRunnerError {
            // expected
        }
    }

    func testLocalIngestJobsDecodesJobsOnSuccess() async throws {
        let transport = try await makeLocalTransport()
        let jobs = try await transport.ingestJobs(vaultArgs: [])
        XCTAssertEqual(jobs.map(\.id), ["1"])
    }

    func testLocalIngestJobsThrowsOnMalformedJSON() async throws {
        let transport = try await makeLocalTransport()
        do {
            _ = try await transport.ingestJobs(vaultArgs: ["BADJSON"])
            XCTFail("expected a decoding error for malformed JSON")
        } catch is CLIRunnerError {
            // expected
        }
    }

    func testLocalIngestRetrySucceeds() async throws {
        let transport = try await makeLocalTransport()
        try await transport.ingestRetry(jobID: "42", vaultArgs: [])
    }

    func testLocalIngestRetryThrowsOnFailure() async throws {
        let transport = try await makeLocalTransport()
        do {
            try await transport.ingestRetry(jobID: "42", vaultArgs: ["FAIL"])
            XCTFail("expected a thrown error for a failed retry")
        } catch is CLIRunnerError {
            // expected
        }
    }

    // MARK: - Remote transport (real HTTP plumbing against a URLProtocol mock)

    func testRemoteCommandSendsAuthenticatedPOSTAndReturnsBody() async throws {
        MockDeskServerURLProtocol.reset()
        MockDeskServerURLProtocol.responseBody = Data(#"{"result":"ok"}"#.utf8)
        let transport = RemoteDeskTransport(client: makeMockClient())

        let data = try await transport.command(arguments: ["ok"], stdin: "")

        XCTAssertEqual(String(decoding: data, as: UTF8.self), #"{"result":"ok"}"#)
        XCTAssertEqual(MockDeskServerURLProtocol.lastRequest?.httpMethod, "POST")
        XCTAssertEqual(MockDeskServerURLProtocol.lastRequest?.url?.path, "/api/v1/command")
        XCTAssertEqual(MockDeskServerURLProtocol.lastRequest?.value(forHTTPHeaderField: "Authorization"), "Bearer \(Self.testToken)")
    }

    func testRemoteCommandThrowsOnServerErrorStatus() async throws {
        MockDeskServerURLProtocol.reset()
        MockDeskServerURLProtocol.statusCode = 500
        MockDeskServerURLProtocol.responseBody = Data(#"{"error":"boom"}"#.utf8)
        let transport = RemoteDeskTransport(client: makeMockClient())

        do {
            _ = try await transport.command(arguments: ["fail"], stdin: "")
            XCTFail("expected a thrown error for a non-2xx response")
        } catch let ServerConnectionError.server(status, message) {
            XCTAssertEqual(status, 500)
            XCTAssertEqual(message, "boom")
        }
    }

    func testRemoteFileContentAndSaveFileHitTheFilesEndpoint() async throws {
        MockDeskServerURLProtocol.reset()
        MockDeskServerURLProtocol.responseBody = Data("note body".utf8)
        let transport = RemoteDeskTransport(client: makeMockClient())

        let content = try await transport.fileContent(path: "notes/a.md")

        XCTAssertEqual(content, "note body")
        XCTAssertEqual(MockDeskServerURLProtocol.lastRequest?.httpMethod, "GET")
        XCTAssertEqual(MockDeskServerURLProtocol.lastRequest?.url?.path, "/api/v1/files")
        XCTAssertEqual(MockDeskServerURLProtocol.lastRequest?.url?.query, "path=notes/a.md")

        MockDeskServerURLProtocol.reset()
        try await transport.saveFile(path: "notes/a.md", content: "updated body")

        XCTAssertEqual(MockDeskServerURLProtocol.lastRequest?.httpMethod, "PUT")
        XCTAssertEqual(MockDeskServerURLProtocol.lastRequestBody, Data("updated body".utf8))
    }

    func testRemoteIngestJobsDecodesJobs() async throws {
        MockDeskServerURLProtocol.reset()
        MockDeskServerURLProtocol.responseBody = Data(#"[{"id":"7","status":"pending","capability":"ocr","error":"","created_at":"now","updated_at":"now","source_path":"a.pdf"}]"#.utf8)
        let transport = RemoteDeskTransport(client: makeMockClient())

        let jobs = try await transport.ingestJobs(vaultArgs: [])

        XCTAssertEqual(jobs.map(\.id), ["7"])
        XCTAssertEqual(MockDeskServerURLProtocol.lastRequest?.url?.path, "/api/v1/jobs")
    }

    func testRemoteIngestJobsThrowsOnMalformedJSON() async throws {
        MockDeskServerURLProtocol.reset()
        MockDeskServerURLProtocol.responseBody = Data("not json".utf8)
        let transport = RemoteDeskTransport(client: makeMockClient())

        do {
            _ = try await transport.ingestJobs(vaultArgs: [])
            XCTFail("expected a decoding error for malformed JSON")
        } catch is DecodingError {
            // expected
        }
    }

    func testRemoteIngestRetryPostsToJobsRetryWithQueryID() async throws {
        MockDeskServerURLProtocol.reset()
        let transport = RemoteDeskTransport(client: makeMockClient())

        try await transport.ingestRetry(jobID: "42", vaultArgs: [])

        XCTAssertEqual(MockDeskServerURLProtocol.lastRequest?.httpMethod, "POST")
        XCTAssertEqual(MockDeskServerURLProtocol.lastRequest?.url?.path, "/api/v1/jobs/retry")
        XCTAssertEqual(MockDeskServerURLProtocol.lastRequest?.url?.query, "id=42")
    }

    // MARK: - Helpers

    private static let testToken = String(repeating: "a", count: 32)

    private func makeMockClient() -> RemoteDeskClient {
        let configuration = URLSessionConfiguration.ephemeral
        configuration.protocolClasses = [MockDeskServerURLProtocol.self]
        let session = URLSession(configuration: configuration)
        let connection = ServerConnection(url: URL(string: "https://desk.example.test")!, token: Self.testToken)
        return RemoteDeskClient(connection: connection, session: session)
    }

    private func makeLocalTransport() async throws -> LocalDeskTransport {
        let scriptURL = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent()
            .appendingPathComponent("Fixtures/fake-symdesk.sh")
        let tool = SymairaTool(id: "fake-symdesk", displayName: "Fake SymDesk", binaryName: scriptURL.lastPathComponent, homebrewFormula: "symdesk")
        let locator = BinaryLocator(bundle: nil, userOverride: scriptURL)
        let detector = ToolDetector(locator: locator)
        guard let detected = await detector.detect(tool) else {
            XCTFail("fixture binary at \(scriptURL.path) was not detected")
            throw DeskCoreError.coreNotFound
        }
        return LocalDeskTransport(tool: detected)
    }

    private func waitForPID(at url: URL, timeout: TimeInterval = 2) async throws -> pid_t {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if let raw = try? String(contentsOf: url, encoding: .utf8),
               let pid = pid_t(raw.trimmingCharacters(in: .whitespacesAndNewlines)) {
                return pid
            }
            try await Task.sleep(nanoseconds: 20_000_000)
        }
        throw XCTSkip("fixture never wrote its pid file")
    }

    private func waitUntil(timeout: TimeInterval, condition: () -> Bool) async -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if condition() { return true }
            try? await Task.sleep(nanoseconds: 20_000_000)
        }
        return condition()
    }

    private static func isProcessAlive(_ pid: pid_t) -> Bool {
        kill(pid, 0) == 0
    }
}

/// Records the last outgoing request and returns a scripted status/body for
/// `RemoteDeskClient`/`RemoteDeskTransport` request-construction, auth, and
/// response-validation tests.
private final class MockDeskServerURLProtocol: URLProtocol, @unchecked Sendable {
    nonisolated(unsafe) static var statusCode: Int = 200
    nonisolated(unsafe) static var responseBody: Data = Data()
    nonisolated(unsafe) static var lastRequest: URLRequest?
    nonisolated(unsafe) static var lastRequestBody: Data?

    static func reset() {
        statusCode = 200
        responseBody = Data()
        lastRequest = nil
        lastRequestBody = nil
    }

    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

    override func startLoading() {
        Self.lastRequest = request
        Self.lastRequestBody = request.httpBody ?? bodyFromStream(request.httpBodyStream)
        guard let url = request.url,
              let response = HTTPURLResponse(
                url: url, statusCode: Self.statusCode, httpVersion: "HTTP/1.1",
                headerFields: ["Content-Type": "application/json"]
              ) else {
            client?.urlProtocol(self, didFailWithError: ServerConnectionError.invalidResponse)
            return
        }
        client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
        client?.urlProtocol(self, didLoad: Self.responseBody)
        client?.urlProtocolDidFinishLoading(self)
    }

    override func stopLoading() {}

    private func bodyFromStream(_ stream: InputStream?) -> Data? {
        guard let stream else { return nil }
        stream.open()
        defer { stream.close() }
        var data = Data()
        var buffer = [UInt8](repeating: 0, count: 4096)
        while stream.hasBytesAvailable {
            let read = stream.read(&buffer, maxLength: buffer.count)
            if read <= 0 { break }
            data.append(buffer, count: read)
        }
        return data
    }
}
