import Darwin
import XCTest
@testable import SymDeskCore
import SymairaCLIRunner
import SymairaToolKit

/// SWIFT-001 (docs/rust-port/contract-matrix.md): the macOS app's real
/// `DeskCore` public methods and `EventWatcher`, driven through
/// `LocalDeskTransport` against the Rust `symdesk` binary instead of the Go
/// one. Nothing here is mocked: every call spawns the binary, so argv
/// placement, JSON decoding, the NDJSON streams and the subprocess lifecycle
/// are the same code paths the shipped app uses.
///
/// Gated on `SYMDESK_CONSUMER_BINARY` (absolute path of the Rust binary). The
/// class skips without it; `make swift-consumer` fails when the variable is
/// missing or when the xcresult shows zero executed tests, so a skip can never
/// count as a pass.
@MainActor
final class RustBackendConsumerTests: XCTestCase {
    private var sandbox: URL!
    private var vault: URL!
    private var savedEnvironment: [String: String?] = [:]
    private var tool: DetectedTool!
    private var transport: LocalDeskTransport!

    private static let isolatedVariables = [
        "HOME", "CFFIXED_USER_HOME", "XDG_CONFIG_HOME", "XDG_DATA_HOME", "XDG_CACHE_HOME", "XDG_STATE_HOME",
        "SYMDESK_VAULT", "SYMDESK_SERVER_TOKEN", "SYMDESK_WORKER_TOKEN",
        "SYMDESK_AI_PROVIDER", "SYMDESK_OLLAMA_URL", "SYMDESK_LANG"
    ]

    override func setUp() async throws {
        let binary = ProcessInfo.processInfo.environment["SYMDESK_CONSUMER_BINARY"] ?? ""
        guard !binary.isEmpty else {
            throw XCTSkip("SYMDESK_CONSUMER_BINARY is not set; use `make swift-consumer`")
        }
        let binaryURL = URL(fileURLWithPath: binary)
        XCTAssertTrue(FileManager.default.isExecutableFile(atPath: binaryURL.path), "Rust binary missing: \(binary)")

        // Canonical (symlink-free) location: the temporary directory lives
        // under /var -> /private/var. A vault opened through that symlink makes
        // `relations inverse` return nothing (see docs/rust-port finding in the
        // SWIFT-001 matrix row), which is unrelated to what this class proves.
        // (`resolvingSymlinksInPath` maps /private/var back to /var, so use realpath.)
        let canonicalTemp = FileManager.default.temporaryDirectory.path.withCString { pointer -> String in
            guard let resolved = realpath(pointer, nil) else { return FileManager.default.temporaryDirectory.path }
            defer { free(resolved) }
            return String(cString: resolved)
        }
        sandbox = URL(fileURLWithPath: canonicalTemp, isDirectory: true)
            .appendingPathComponent("rust-consumer-\(UUID().uuidString)", isDirectory: true)
        let home = sandbox.appendingPathComponent("home", isDirectory: true)
        vault = sandbox.appendingPathComponent("vault", isDirectory: true)
        try FileManager.default.createDirectory(at: home, withIntermediateDirectories: true)
        try FileManager.default.createDirectory(at: vault.appendingPathComponent("projects"), withIntermediateDirectories: true)

        // CLIRunner and EventWatcher launch the binary with the inherited
        // process environment, so isolating the test process isolates every
        // child (index database, config, share/archive directories).
        for name in Self.isolatedVariables {
            savedEnvironment[name] = ProcessInfo.processInfo.environment[name]
            unsetenv(name)
        }
        setenv("HOME", home.path, 1)
        setenv("CFFIXED_USER_HOME", home.path, 1)
        setenv("XDG_CONFIG_HOME", home.appendingPathComponent(".config").path, 1)
        setenv("XDG_DATA_HOME", home.appendingPathComponent(".local/share").path, 1)
        setenv("XDG_CACHE_HOME", home.appendingPathComponent(".cache").path, 1)

        try write("alpha.md", """
        ---
        title: "Alpha Plan"
        created: "2026-01-02T03:04:05Z"
        tags: [planning]
        status: open
        ---

        # Alpha Plan

        Zebra migration strategy. Links to [[Beta Notes]].

        """)
        try write("projects/beta.md", """
        ---
        title: "Beta Notes"
        created: "2026-01-03T03:04:05Z"
        tags: []
        project: "[[Alpha Plan]]"
        ---

        Beta body mentions zebra too.

        """)

        let toolDescriptor = SymairaTool(
            id: "symdesk-rust", displayName: "SymDesk (Rust)",
            binaryName: binaryURL.lastPathComponent, homebrewFormula: "symdesk"
        )
        let detector = ToolDetector(locator: BinaryLocator(bundle: nil, userOverride: binaryURL))
        guard let detected = await detector.detect(toolDescriptor) else {
            XCTFail("Rust binary \(binary) was not detected as a SymDesk core")
            throw DeskCoreError.coreNotFound
        }
        tool = detected
        transport = LocalDeskTransport(tool: detected)
    }

    override func tearDown() async throws {
        for (name, value) in savedEnvironment {
            if let value { setenv(name, value, 1) } else { unsetenv(name) }
        }
        savedEnvironment = [:]
        if let sandbox { try? FileManager.default.removeItem(at: sandbox) }
    }

    // MARK: - Helpers

    private func write(_ relativePath: String, _ content: String) throws {
        let url = vault.appendingPathComponent(relativePath)
        try FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        try content.write(to: url, atomically: true, encoding: .utf8)
    }

    /// The production singleton wired to the Rust transport: the same
    /// `vaultArgs`/`runDecoding` paths the app uses once `initialize()` has
    /// detected a core.
    private func makeCore() -> DeskCore {
        let core = DeskCore.shared
        core.transport = transport
        core.vaultPath = vault.path
        return core
    }

    // MARK: - Version / schema handshake (DeskCore.initialize)

    func testVersionJSONAndSchemaHandshake() async throws {
        let detector = ToolDetector(locator: BinaryLocator(bundle: nil, userOverride: tool.location.url))
        try detector.requireSchemaVersion(1, of: tool)

        let result = try await transport.commandResult(arguments: ["version", "--json"])
        XCTAssertEqual(result.exitCode, 0)
        let status = try JSONDecoder().decode(DeskStatus.self, from: result.stdout)
        XCTAssertEqual(status.schemaVersion, 1)
        XCTAssertFalse(status.version.isEmpty)
        if let expected = ProcessInfo.processInfo.environment["SYMDESK_CONSUMER_EXPECT_VERSION"], !expected.isEmpty {
            XCTAssertEqual(status.version, expected)
        }
    }

    // MARK: - Read paths

    func testListSearchBacklinksGraphAndInverseRelations() async throws {
        let core = makeCore()
        // `relations inverse` and `views exec` read the derived sidecar index,
        // so (like Go) they return nothing on a vault that was never indexed.
        // The app indexes on open; mirror that with its own reindex call.
        _ = try await core.reindexVault(prune: false)

        let notes = try await core.listFiles()
        XCTAssertEqual(Set(notes.map(\.path)), ["alpha.md", "projects/beta.md"])
        let alpha = try XCTUnwrap(notes.first { $0.path == "alpha.md" })
        XCTAssertEqual(alpha.title, "Alpha Plan")
        XCTAssertEqual(alpha.type, "note")
        XCTAssertFalse(alpha.modifiedAt.isEmpty)

        let search = try await core.search(query: "zebra")
        XCTAssertEqual(Set(search.results.map(\.path)), ["alpha.md", "projects/beta.md"])
        XCTAssertEqual(search.results.first { $0.path == "alpha.md" }?.title, "Alpha Plan")
        // Once indexed, the hybrid path can also return other chunks of the same
        // notes (frontmatter), so only require that each note has a matching hit.
        for path in ["alpha.md", "projects/beta.md"] {
            XCTAssertTrue(
                search.results.contains { $0.path == path && $0.snippet.lowercased().contains("zebra") },
                "no zebra snippet for \(path): \(search.results)"
            )
        }

        let backlinksToBeta = try await core.backlinks(for: "projects/beta.md")
        XCTAssertEqual(backlinksToBeta, ["alpha.md"])
        let backlinksToAlpha = try await core.backlinks(for: "alpha.md")
        XCTAssertEqual(backlinksToAlpha, [])

        let graph = try await core.getGraph()
        XCTAssertEqual(Set(graph.nodes.map(\.id)), ["alpha.md", "projects/beta.md"])
        XCTAssertEqual(graph.nodes.first { $0.id == "projects/beta.md" }?.label, "Beta Notes")
        XCTAssertEqual(graph.edges.map(\.id), ["alpha.md->projects/beta.md"])

        let inverse = try await core.relationsInverse(path: "alpha.md")
        XCTAssertEqual(inverse.count, 1)
        XCTAssertEqual(inverse.first?.source, "projects/beta.md")
        XCTAssertEqual(inverse.first?.title, "Beta Notes")
        XCTAssertEqual(inverse.first?.property, "project")
    }

    func testIndexStatusAndReindex() async throws {
        let core = makeCore()

        let before = try await core.retrievalStatus()
        XCTAssertEqual(before.vaultDocumentCount, 2)
        XCTAssertFalse(before.embeddingModel.isEmpty)
        XCTAssertNotNil(before.indexLocation)

        let reindex = try await core.reindexVault(prune: false)
        XCTAssertEqual(reindex.status, "ok")
        XCTAssertEqual(reindex.indexed + reindex.skipped, 2)

        let pruned = try await core.reindexVault(prune: true)
        XCTAssertEqual(pruned.status, "ok")
        XCTAssertNotNil(pruned.pruned)

        let retry = try await core.retryFailedIndex()
        XCTAssertEqual(retry.status, "ok")
        XCTAssertEqual(retry.failed, 0)
    }

    // MARK: - Write paths

    func testNoteNewPropsEditAndDailyNote() async throws {
        let core = makeCore()

        let created = try await core.noteNew(title: "Gamma Note")
        XCTAssertEqual(created, "Gamma_Note.md")
        let createdText = try String(contentsOf: vault.appendingPathComponent(created), encoding: .utf8)
        XCTAssertTrue(createdText.hasPrefix("---\ntitle: \"Gamma Note\"\ncreated: \""), createdText)
        XCTAssertTrue(createdText.contains("\ntags: []\n---"), createdText)

        try await core.noteEditProperty(path: created, key: "status", value: "done")
        let edited = try String(contentsOf: vault.appendingPathComponent(created), encoding: .utf8)
        XCTAssertTrue(edited.contains("\nstatus: \"done\"\n"), edited)

        let daily = try await core.noteDaily()
        XCTAssertTrue(daily.range(of: #"^\d{4}-\d{2}-\d{2}\.md$"#, options: .regularExpression) != nil, daily)
        XCTAssertTrue(FileManager.default.fileExists(atPath: vault.appendingPathComponent(daily).path))
        let dailyAgain = try await core.noteDaily()
        XCTAssertEqual(dailyAgain, daily, "note daily is idempotent")

        let notes = try await core.listFiles()
        XCTAssertTrue(notes.contains { $0.path == created && $0.title == "Gamma Note" })
        XCTAssertTrue(notes.contains { $0.path == daily })
    }

    func testViewsListSaveGetDeleteExecAndExport() async throws {
        let core = makeCore()
        _ = try await core.reindexVault(prune: false) // `views exec` queries the sidecar index
        let initial = try await core.viewsList()
        XCTAssertEqual(initial, [])

        let view = DbView(
            id: "open-items",
            name: "Open items",
            filters: [DbFilter(key: "status", operatorString: "eq", value: "open")],
            sorts: [DbSort(key: "title", ascending: true)],
            columns: ["title", "status"]
        )
        try await core.viewsSave(view)

        let listed = try await core.viewsList()
        XCTAssertEqual(listed.map(\.id), ["open-items"])
        let fetched = try await core.viewsGet(id: "open-items")
        XCTAssertEqual(fetched, view)

        let rows = try await core.viewsExec(id: "open-items")
        let decodedRows = try JSONDecoder().decode([[String: String]].self, from: rows)
        XCTAssertEqual(decodedRows.count, 1)
        XCTAssertEqual(decodedRows.first?["title"], "Alpha Plan")
        XCTAssertEqual(decodedRows.first?["status"], "open")

        let csv = String(decoding: try await core.viewsExportCSV(id: "open-items"), as: UTF8.self)
        XCTAssertEqual(csv.replacingOccurrences(of: "\r\n", with: "\n"), "title,status\nAlpha Plan,open\n")

        try await core.viewsDelete(id: "open-items")
        let afterDelete = try await core.viewsList()
        XCTAssertEqual(afterDelete, [])
    }

    // MARK: - Meetings / doctor / ingest queue

    func testMeetingListDoctorAndIngestJobs() async throws {
        let core = makeCore()

        let meetings = try await core.meetingsList()
        XCTAssertEqual(meetings, [])
        let report = try await core.meetingsListReport()
        XCTAssertEqual(report.meetings, [])
        XCTAssertEqual(report.failures.count, 0)

        let doctor = try await core.getDoctorReport()
        XCTAssertEqual(doctor.overall, "ok")
        XCTAssertEqual(doctor.vault?.status, "ok")
        XCTAssertEqual(doctor.vault?.path, vault.path)
        XCTAssertEqual(doctor.sidecar?.status, "ok")
        XCTAssertEqual(doctor.contract?.status, "ok")

        let jobs = try await core.ingestJobs()
        XCTAssertEqual(jobs.count, 0)
        let page = try await core.ingestJobPage(limit: 5, offset: 0)
        XCTAssertEqual(page.jobs.count, 0)
        XCTAssertEqual(page.total, 0)
        XCTAssertEqual(page.limit, 5)
        XCTAssertEqual(page.offset, 0)
    }

    func testFailingCommandSurfacesAsError() async throws {
        let core = makeCore()
        do {
            try await core.resolveConflict(path: "alpha.md", action: "not-an-action")
            XCTFail("an invalid conflict action must exit non-zero and throw")
        } catch is CLIRunnerError {
            // expected: argv reached the Rust binary and its exit status propagated
        }
    }

    // MARK: - NDJSON streams

    func testAskStreamsNoProviderFallbackEvents() async throws {
        let core = makeCore()
        var events: [AIEvent] = []
        for try await event in core.ask(query: "What is zebra?") {
            events.append(event)
        }
        XCTAssertEqual(events.last?.type, .done)
        let answer = events.filter { $0.type == .answer }.compactMap(\.text).joined()
        XCTAssertTrue(answer.contains("AI feature not configured"), answer)
        let toolEvents = events.filter { $0.type == .tool }
        XCTAssertTrue(toolEvents.contains { $0.toolName == "search" && $0.status == "running" })
        XCTAssertTrue(toolEvents.contains { $0.toolName == "search" && $0.status == "done" })
    }

    func testAskAgentStreamsFallbackEvents() async throws {
        let core = makeCore()
        var events: [AIEvent] = []
        for try await event in core.ask(query: "zebra", agent: true) {
            events.append(event)
        }
        XCTAssertEqual(events.last?.type, .done)
        XCTAssertTrue(events.contains { $0.type == .answer && ($0.text ?? "").contains("AI feature not configured") })
    }

    func testTransformStreamsChunksFromStdin() async throws {
        let core = makeCore()
        var chunks: [String] = []
        for try await chunk in core.transform(text: "some text to summarize", intent: "summarize") {
            chunks.append(chunk)
        }
        XCTAssertFalse(chunks.isEmpty)
        XCTAssertTrue(chunks.joined().contains("AI feature not configured"), chunks.joined())
    }

    // MARK: - events --json + subprocess lifecycle

    func testEventWatcherReceivesVaultEventAndStopTerminatesSubprocess() async throws {
        let watcher = EventWatcher.shared
        watcher.reset()
        // The vault path is unique per test run, so it identifies the child
        // in the process table without needing access to `Process`.
        let marker = vault.path
        XCTAssertEqual(Self.processIDs(matching: marker), [], "no stale events process for this vault")

        watcher.start(tool: tool, vaultPath: vault.path)
        XCTAssertTrue(watcher.isWatching)

        let started = await waitUntil(timeout: 20) { !Self.processIDs(matching: marker).isEmpty }
        XCTAssertTrue(started, "EventWatcher never launched `symdesk events --json`")
        let pids = Self.processIDs(matching: marker)
        XCTAssertEqual(pids.count, 1)
        let pid = try XCTUnwrap(pids.first)

        // The debounce (500 ms) and the filesystem watcher start-up are slow
        // on a loaded host, and the watcher may first report the seed notes
        // written during setUp, so keep touching a note until an event for
        // that note arrives.
        let target = vault.appendingPathComponent("watched.md")
        func watchedEvent() -> VaultEvent? {
            watcher.allEvents.last { URL(fileURLWithPath: $0.path).lastPathComponent == "watched.md" }
        }
        var counter = 0
        let received = await waitUntil(timeout: 60) {
            counter += 1
            try? "event body \(counter)\n".write(to: target, atomically: true, encoding: .utf8)
            return watchedEvent() != nil
        }
        XCTAssertTrue(received, "no vault event for watched.md decoded from the events stream; got \(watcher.allEvents)")
        let event = try XCTUnwrap(watchedEvent())
        XCTAssertTrue(["file_added", "file_changed", "index_updated"].contains(event.event), event.event)
        XCTAssertEqual(URL(fileURLWithPath: event.path).resolvingSymlinksInPath().lastPathComponent, "watched.md")
        XCTAssertTrue(event.path.hasSuffix("/watched.md"), event.path)
        XCTAssertGreaterThan(event.ts, 1_700_000_000, "RFC 3339 ts must decode to epoch seconds")
        XCTAssertEqual(watcher.latestEvent, watcher.allEvents.last)

        watcher.stop()
        XCTAssertFalse(watcher.isWatching)
        let gone = await waitUntil(timeout: 10) { !Self.isProcessAlive(pid) }
        XCTAssertTrue(gone, "stop() must terminate the events subprocess (pid \(pid))")
        XCTAssertEqual(Self.processIDs(matching: marker), [])
        watcher.reset()
    }

    func testCommandStreamCancellationTerminatesEventsSubprocess() async throws {
        let marker = vault.path
        var started = false
        let task = Task { @MainActor in
            for try await _ in self.transport.commandStream(arguments: ["events", "--json", "--vault", marker], stdin: "") {
                break
            }
        }
        started = await waitUntil(timeout: 20) { !Self.processIDs(matching: marker).isEmpty }
        XCTAssertTrue(started)
        let pid = try XCTUnwrap(Self.processIDs(matching: marker).first)
        task.cancel()
        let gone = await waitUntil(timeout: 10) { !Self.isProcessAlive(pid) }
        XCTAssertTrue(gone, "cancelling the stream consumer must tear down the subprocess (pid \(pid))")
    }

    // MARK: - Process table helpers

    private func waitUntil(timeout: TimeInterval, condition: () -> Bool) async -> Bool {
        let deadline = Date().addingTimeInterval(timeout)
        while Date() < deadline {
            if condition() { return true }
            try? await Task.sleep(nanoseconds: 250_000_000)
        }
        return condition()
    }

    private static func isProcessAlive(_ pid: pid_t) -> Bool {
        // A terminated child that was not yet reaped is a zombie; treat it as
        // gone because the process no longer runs.
        guard kill(pid, 0) == 0 else { return false }
        let ps = Process()
        ps.executableURL = URL(fileURLWithPath: "/bin/ps")
        ps.arguments = ["-o", "stat=", "-p", String(pid)]
        let pipe = Pipe()
        ps.standardOutput = pipe
        try? ps.run()
        ps.waitUntilExit()
        let state = String(decoding: pipe.fileHandleForReading.readDataToEndOfFile(), as: UTF8.self)
            .trimmingCharacters(in: .whitespacesAndNewlines)
        return !state.isEmpty && !state.hasPrefix("Z")
    }

    /// PIDs of live processes whose command line contains `fragment` and
    /// `events --json` (the watcher's argv).
    private static func processIDs(matching fragment: String) -> [pid_t] {
        let ps = Process()
        ps.executableURL = URL(fileURLWithPath: "/bin/ps")
        ps.arguments = ["-axo", "pid=,stat=,command="]
        let pipe = Pipe()
        ps.standardOutput = pipe
        try? ps.run()
        let output = String(decoding: pipe.fileHandleForReading.readDataToEndOfFile(), as: UTF8.self)
        ps.waitUntilExit()
        return output.split(separator: "\n").compactMap { line -> pid_t? in
            let parts = line.split(separator: " ", maxSplits: 2, omittingEmptySubsequences: true)
            guard parts.count == 3, !parts[1].hasPrefix("Z"),
                  parts[2].contains("events --json"), parts[2].contains(fragment) else { return nil }
            return pid_t(parts[0])
        }
    }
}
