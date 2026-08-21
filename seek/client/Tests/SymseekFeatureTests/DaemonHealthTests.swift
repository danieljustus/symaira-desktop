import Darwin
import Foundation
import XCTest
@testable import SymseekFeature

/// Minimal local HTTP/1.1 test server.  Serves a fixed status code and
/// body on 127.0.0.1:<ephemeral port> and records the Authorization
/// header of each request, so the daemon health check can be exercised
/// without a real symseek binary.
final class TestHTTPServer: @unchecked Sendable {
    private let listenFD: Int32
    let port: Int
    let statusCode: Int
    let body: String

    private let lock = NSLock()
    private var _authorizationHeader: String?
    private var _requestCount = 0

    /// Authorization header of the most recent request (e.g. "Bearer xyz").
    var authorizationHeader: String? {
        lock.lock()
        defer { lock.unlock() }
        return _authorizationHeader
    }

    var requestCount: Int {
        lock.lock()
        defer { lock.unlock() }
        return _requestCount
    }

    init?(statusCode: Int = 200, body: String = #"{"status":"ok"}"#) {
        self.statusCode = statusCode
        self.body = body

        let fd = socket(AF_INET, SOCK_STREAM, 0)
        guard fd >= 0 else { return nil }

        var addr = sockaddr_in()
        addr.sin_len = UInt8(MemoryLayout<sockaddr_in>.size)
        addr.sin_family = sa_family_t(AF_INET)
        addr.sin_port = 0 // ephemeral port
        addr.sin_addr = in_addr(s_addr: inet_addr("127.0.0.1"))
        var reuse: Int32 = 1
        setsockopt(fd, SOL_SOCKET, SO_REUSEADDR, &reuse, socklen_t(MemoryLayout<Int32>.size))

        let bound = withUnsafePointer(to: &addr) { ptr in
            ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) { sockPtr in
                bind(fd, sockPtr, socklen_t(MemoryLayout<sockaddr_in>.size))
            }
        }
        guard bound == 0, listen(fd, 8) == 0 else {
            close(fd)
            return nil
        }

        var boundAddr = sockaddr_in()
        var boundLen = socklen_t(MemoryLayout<sockaddr_in>.size)
        let nameResult = withUnsafeMutablePointer(to: &boundAddr) { ptr in
            ptr.withMemoryRebound(to: sockaddr.self, capacity: 1) { sockPtr in
                getsockname(fd, sockPtr, &boundLen)
            }
        }
        guard nameResult == 0 else {
            close(fd)
            return nil
        }

        self.listenFD = fd
        self.port = Int(UInt16(bigEndian: boundAddr.sin_port))
    }

    deinit {
        stop()
    }

    /// Starts the accept loop on a background thread. Call once.
    func start() {
        let thread = Thread { [weak self] in
            self?.serveLoop()
        }
        thread.name = "TestHTTPServer.accept"
        thread.start()
    }

    /// Closes the listening socket, unblocking the accept loop.
    func stop() {
        shutdown(listenFD, SHUT_RDWR)
        close(listenFD)
    }

    private func serveLoop() {
        while true {
            var clientAddr = sockaddr()
            var clientLen = socklen_t(MemoryLayout<sockaddr>.size)
            let client = accept(listenFD, &clientAddr, &clientLen)
            if client < 0 { break }
            handle(client)
        }
    }

    private func handle(_ client: Int32) {
        defer { close(client) }
        var requestData = Data()
        var buffer = [UInt8](repeating: 0, count: 4096)
        while !requestData.contains(Data("\r\n\r\n".utf8)) {
            let n = buffer.withUnsafeMutableBytes { bytes -> Int in
                read(client, bytes.baseAddress, bytes.count)
            }
            if n <= 0 { return }
            requestData.append(contentsOf: buffer[0..<n])
            if requestData.count > 64 * 1024 { return }
        }

        if let headerEnd = requestData.range(of: Data("\r\n\r\n".utf8)) {
            let headerText = String(data: requestData[..<headerEnd.lowerBound], encoding: .utf8) ?? ""
            lock.lock()
            _requestCount += 1
            for line in headerText.components(separatedBy: "\r\n") {
                if line.lowercased().hasPrefix("authorization:") {
                    _authorizationHeader = line.dropFirst("authorization:".count)
                        .trimmingCharacters(in: .whitespaces)
                    break
                }
            }
            lock.unlock()
        }

        let reason: String
        switch statusCode {
        case 200: reason = "OK"
        case 401: reason = "Unauthorized"
        case 500: reason = "Internal Server Error"
        default: reason = "OK"
        }
        let response = "HTTP/1.1 \(statusCode) \(reason)\r\n" +
            "Content-Length: \(body.utf8.count)\r\n" +
            "Connection: close\r\n\r\n" + body
        _ = Data(response.utf8).withUnsafeBytes { bytes in
            write(client, bytes.baseAddress, bytes.count)
        }
    }
}

/// URLProtocol that fails every request, to simulate an arbitrary
/// network error deterministically.
private final class FailingURLProtocol: URLProtocol {
    override class func canInit(with request: URLRequest) -> Bool { true }
    override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }
    override func startLoading() {
        client?.urlProtocol(self, didFailWithError: URLError(.cannotConnectToHost))
    }
    override func stopLoading() {}
}

/// Issue #307: "App reports 'Daemon Active' while no daemon is running,
/// and never starts one".  Only an authenticated HTTP 200 from the
/// daemon may count as running; a 401 or any network error must not, and
/// the CLI status fallback is gone entirely.
final class DaemonHealthTests: XCTestCase {

    // No listener on the port → connection refused (network error) must
    // NOT be running, so start() falls through to launching `serve`.
    func testNoListenerIsNotRunning() async throws {
        // Bind a listener to obtain a free port, then release it so
        // nothing answers there anymore.
        let server = try XCTUnwrap(TestHTTPServer())
        server.start()
        let freePort = server.port
        server.stop()

        let ok = await DaemonHealth.check(port: freePort, token: "test-token")
        XCTAssertFalse(ok, "connection refused must not count as running")
    }

    // Authenticated HTTP 200 → running, and the bearer token must be
    // sent on the status check (auth integration from #318).
    func testAuthenticated200IsRunningAndSendsToken() async throws {
        let server = try XCTUnwrap(TestHTTPServer(statusCode: 200, body: #"{"documents":0}"#))
        server.start()
        defer { server.stop() }

        let ok = await DaemonHealth.check(port: server.port, token: "sekrit-307")
        XCTAssertTrue(ok, "authenticated HTTP 200 must count as running")
        XCTAssertEqual(server.authorizationHeader, "Bearer sekrit-307",
                       "status check must send Authorization: Bearer <token>")
    }

    // 401 (bad/missing token) → NOT running; falls through to launch.
    func testUnauthorized401IsNotRunningButStillSendsToken() async throws {
        let server = try XCTUnwrap(TestHTTPServer(statusCode: 401, body: "unauthorized"))
        server.start()
        defer { server.stop() }

        let ok = await DaemonHealth.check(port: server.port, token: "wrong-token")
        XCTAssertFalse(ok, "HTTP 401 must not count as running")
        XCTAssertEqual(server.authorizationHeader, "Bearer wrong-token",
                       "the (wrong) token must still be sent on the status check")
    }

    // HTTP 200 with an empty body is not a daemon status response.
    func test200WithEmptyBodyIsNotRunning() async throws {
        let server = try XCTUnwrap(TestHTTPServer(statusCode: 200, body: ""))
        server.start()
        defer { server.stop() }

        let ok = await DaemonHealth.check(port: server.port, token: "t")
        XCTAssertFalse(ok, "empty 200 body must not count as running")
    }

    // Any network error (simulated) → NOT running.
    func testNetworkErrorIsNotRunning() async throws {
        let config = URLSessionConfiguration.ephemeral
        config.protocolClasses = [FailingURLProtocol.self]
        let session = URLSession(configuration: config)

        let ok = await DaemonHealth.check(port: 12345, token: "t", session: session)
        XCTAssertFalse(ok, "network errors must never set .running")
    }
}
