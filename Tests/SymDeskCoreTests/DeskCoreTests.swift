import XCTest
@testable import SymDeskCore

final class DeskCoreTests: XCTestCase {
    func testNoteDecoding() throws {
        let json = """
        {
            "path": "/tmp/note.md",
            "title": "My Note",
            "sha256": "abcdef",
            "modified_at": "2026-07-06T12:00:00Z",
            "indexed_at": "2026-07-06T12:00:01Z"
        }
        """.data(using: .utf8)!
        
        let note = try JSONDecoder().decode(Note.self, from: json)
        XCTAssertEqual(note.path, "/tmp/note.md")
        XCTAssertEqual(note.title, "My Note")
        XCTAssertEqual(note.sha256, "abcdef")
        XCTAssertEqual(note.modifiedAt, "2026-07-06T12:00:00Z")
    }

    func testNoteDecodingAcceptsCurrentListShape() throws {
        let json = """
        {
            "path": "notes/example.md",
            "title": "Example",
            "modified": "2026-07-13T12:00:00Z"
        }
        """.data(using: .utf8)!

        let note = try JSONDecoder().decode(Note.self, from: json)

        XCTAssertEqual(note.path, "notes/example.md")
        XCTAssertEqual(note.modifiedAt, "2026-07-13T12:00:00Z")
        XCTAssertEqual(note.sha256, "")
        XCTAssertEqual(note.indexedAt, "")
    }

    func testSearchResponseDecodingIncludesSyntaxHint() throws {
        let json = """
        {
            "results": [{
                "path": "finance/invoice.md",
                "title": "Invoice",
                "snippet": "Tax invoice",
                "score": 0
            }],
            "hint": "Search syntax was invalid, so this was searched as plain full text."
        }
        """.data(using: .utf8)!

        let response = try JSONDecoder().decode(SearchResponse.self, from: json)
        XCTAssertEqual(response.results.count, 1)
        XCTAssertEqual(response.results[0].title, "Invoice")
        XCTAssertNotNil(response.hint)
    }

    func testEventDecoding() throws {
        let json = """
        {
            "event": "file_changed",
            "path": "/tmp/note.md",
            "ts": 123456789
        }
        """.data(using: .utf8)!
        
        let ev = try JSONDecoder().decode(VaultEvent.self, from: json)
        XCTAssertEqual(ev.event, "file_changed")
        XCTAssertEqual(ev.path, "/tmp/note.md")
        XCTAssertEqual(ev.ts, 123456789)
    }

    func testDocumentItemDecoding() throws {
        let json = """
        {
            "path": "inbox/receipt-2026.md",
            "title": "Receipt 2026",
            "document_date": "2026-07-01",
            "person": "Daniel",
            "status": "open",
            "due_date": "2026-08-01",
            "confidence": 85,
            "correspondent": "Acme Corp",
            "document_type": "invoice",
            "asn": 42
        }
        """.data(using: .utf8)!

        let doc = try JSONDecoder().decode(DocumentItem.self, from: json)
        XCTAssertEqual(doc.path, "inbox/receipt-2026.md")
        XCTAssertEqual(doc.title, "Receipt 2026")
        XCTAssertEqual(doc.documentDate, "2026-07-01")
        XCTAssertEqual(doc.person, "Daniel")
        XCTAssertEqual(doc.status, "open")
        XCTAssertEqual(doc.dueDate, "2026-08-01")
        XCTAssertEqual(doc.confidence, 85)
        XCTAssertEqual(doc.correspondent, "Acme Corp")
        XCTAssertEqual(doc.documentType, "invoice")
        XCTAssertEqual(doc.asn, 42)
        XCTAssertEqual(doc.id, doc.path)
    }

    func testDocumentItemDecodingWithEmptyFields() throws {
        let json = """
        {
            "path": "notes/plain.md",
            "title": "Plain Note"
        }
        """.data(using: .utf8)!

        let doc = try JSONDecoder().decode(DocumentItem.self, from: json)
        XCTAssertEqual(doc.path, "notes/plain.md")
        XCTAssertEqual(doc.title, "Plain Note")
        XCTAssertEqual(doc.documentDate, "")
        XCTAssertEqual(doc.status, "")
        XCTAssertEqual(doc.confidence, 0)
        XCTAssertEqual(doc.asn, 0)
    }

    func testDocumentStatusEnum() {
        XCTAssertEqual(DocumentStatus.open.rawValue, "open")
        XCTAssertEqual(DocumentStatus.needsReview.rawValue, "needs_review")
        XCTAssertEqual(DocumentStatus.allCases.count, 6)
    }

    func testDocumentPreviewResolverMakesRelativeNotePathAbsolute() throws {
        let vault = URL(fileURLWithPath: NSTemporaryDirectory())
            .appendingPathComponent(UUID().uuidString, isDirectory: true)
        let resolved = DocumentPreviewResolver.noteURL(
            documentPath: "documents/invoice.md",
            vaultPath: vault.path
        )

        XCTAssertEqual(
            resolved?.path,
            vault.appendingPathComponent("documents/invoice.md").standardizedFileURL.path
        )
    }

    func testDocumentPreviewResolverPrefersArchivedOriginal() throws {
        let vault = URL(fileURLWithPath: NSTemporaryDirectory())
            .appendingPathComponent(UUID().uuidString, isDirectory: true)
        let archive = vault.appendingPathComponent("archive/invoice.pdf")
        let source = vault.appendingPathComponent("incoming/invoice.pdf")
        try FileManager.default.createDirectory(at: archive.deletingLastPathComponent(), withIntermediateDirectories: true)
        try FileManager.default.createDirectory(at: source.deletingLastPathComponent(), withIntermediateDirectories: true)
        try Data("archive".utf8).write(to: archive)
        try Data("source".utf8).write(to: source)
        defer { try? FileManager.default.removeItem(at: vault) }

        let resolved = DocumentPreviewResolver.sourceURL(
            documentPath: "documents/invoice.md",
            properties: [
                "archive_path": "archive/invoice.pdf",
                "source_path": source.path,
            ],
            vaultPath: vault.path
        )

        XCTAssertEqual(resolved?.path, archive.standardizedFileURL.path)
    }

    func testDocumentPreviewResolverFallsBackToSourceWhenArchiveIsMissing() throws {
        let vault = URL(fileURLWithPath: NSTemporaryDirectory())
            .appendingPathComponent(UUID().uuidString, isDirectory: true)
        let source = vault.appendingPathComponent("source.pdf")
        try FileManager.default.createDirectory(at: vault, withIntermediateDirectories: true)
        try Data("source".utf8).write(to: source)
        defer { try? FileManager.default.removeItem(at: vault) }

        let resolved = DocumentPreviewResolver.sourceURL(
            documentPath: "invoice.md",
            properties: [
                "archive_path": "missing.pdf",
                "source_path": "source.pdf",
            ],
            vaultPath: vault.path
        )

        XCTAssertEqual(resolved?.path, source.standardizedFileURL.path)
    }

    func testDocumentPropertiesDecodesMixedFrontmatterValues() throws {
        let data = #"{"archive_path":"/archive/invoice.pdf","confidence":91,"note_visible":true,"tags":["finance","paid"]}"#
            .data(using: .utf8)!

        let properties = try DocumentProperties.decode(data)

        XCTAssertEqual(properties["archive_path"], "/archive/invoice.pdf")
        XCTAssertEqual(properties["confidence"], "91")
        XCTAssertEqual(properties["note_visible"], "true")
        XCTAssertEqual(properties["tags"], "finance, paid")
    }

    func testDocumentStatusLabelsAndImages() {
        for status in DocumentStatus.allCases {
            XCTAssertFalse(status.label.isEmpty, "\(status.rawValue) should have a label")
            XCTAssertFalse(status.systemImage.isEmpty, "\(status.rawValue) should have a systemImage")
        }
    }

    func testDocFilterPresetDefaults() {
        let presets = DocFilterPreset.defaults
        XCTAssertGreaterThanOrEqual(presets.count, 7)
        XCTAssertEqual(presets[0].status, nil)
        XCTAssertEqual(presets[1].status, .open)
    }

    func testSimilarDocDecoding() throws {
        let json = """
        {
            "path": "other/receipt.md",
            "title": "Other Receipt",
            "similarity": 92
        }
        """.data(using: .utf8)!

        let doc = try JSONDecoder().decode(SimilarDoc.self, from: json)
        XCTAssertEqual(doc.similarity, 92)
        XCTAssertEqual(doc.id, doc.path)
    }

    func testReviewDocDecoding() throws {
        let json = """
        {
            "path": "inbox/scan.md",
            "title": "Scan",
            "confidence": 30,
            "reasons": ["confidence 30 < 70", "missing document_type"]
        }
        """.data(using: .utf8)!

        let doc = try JSONDecoder().decode(ReviewDoc.self, from: json)
        XCTAssertEqual(doc.confidence, 30)
        XCTAssertEqual(doc.reasons.count, 2)
        XCTAssertEqual(doc.reasons[0], "confidence 30 < 70")
    }

    // MARK: - VaultConfig Tests

    func testVaultConfigResetClearsState() {
        VaultConfig.setVault(url: URL(fileURLWithPath: "/tmp/test-vault"))
        XCTAssertTrue(VaultConfig.hasConfiguredVault)
        XCTAssertFalse(VaultConfig.isDemoMode)

        VaultConfig.reset()
        XCTAssertFalse(VaultConfig.hasConfiguredVault)
        XCTAssertNil(VaultConfig.vaultPath())
    }

    func testVaultConfigSetDemoMode() {
        VaultConfig.setDemoVault(url: URL(fileURLWithPath: "/tmp/demo-vault"))
        XCTAssertTrue(VaultConfig.hasConfiguredVault)
        XCTAssertTrue(VaultConfig.isDemoMode)
        XCTAssertEqual(VaultConfig.vaultPath(), "/tmp/demo-vault")
    }

    func testVaultConfigSetRealVault() {
        VaultConfig.setVault(url: URL(fileURLWithPath: "/tmp/real-vault"))
        XCTAssertTrue(VaultConfig.hasConfiguredVault)
        XCTAssertFalse(VaultConfig.isDemoMode)
        XCTAssertEqual(VaultConfig.vaultPath(), "/tmp/real-vault")
    }

    // MARK: - DoctorReport Tests

    func testDoctorReportDecoding() throws {
        let json = """
        {
            "overall": "ok",
            "vault": {"status": "ok"},
            "sidecar": {"status": "available"},
            "tools": {"symvault": "ok", "symmemory": "not found", "symbrowse": "available"}
        }
        """.data(using: .utf8)!

        let report = try JSONDecoder().decode(DoctorReport.self, from: json)
        XCTAssertEqual(report.overall, "ok")
        XCTAssertTrue(report.tools.isAvailable("symvault"))
        XCTAssertFalse(report.tools.isAvailable("symmemory"))
        XCTAssertTrue(report.tools.isAvailable("symbrowse"))
    }

    func testDoctorReportDecodingWithMissingFields() throws {
        let json = """
        {
            "overall": "degraded"
        }
        """.data(using: .utf8)!

        let report = try JSONDecoder().decode(DoctorReport.self, from: json)
        XCTAssertEqual(report.overall, "degraded")
        XCTAssertFalse(report.tools.isAvailable("symvault"))
        XCTAssertNil(report.vault)
        XCTAssertFalse(report.tools.isAvailable("symmemory"))
    }

    func testDoctorReportToolAvailabilityVariants() {
        let available = DoctorReport.ToolAvailability(symmemory: "ok")
        XCTAssertTrue(available.isAvailable("symmemory"))

        let found = DoctorReport.ToolAvailability(symmemory: "found")
        XCTAssertTrue(found.isAvailable("symmemory"))

        let status = DoctorReport.ToolAvailability(symmemory: "Available")
        XCTAssertTrue(status.isAvailable("symmemory"))

        let missing = DoctorReport.ToolAvailability(symmemory: "not found")
        XCTAssertFalse(missing.isAvailable("symmemory"))

        let unknown = DoctorReport.ToolAvailability(symmemory: nil)
        XCTAssertFalse(unknown.isAvailable("symmemory"))
        XCTAssertFalse(unknown.isAvailable("unknown-tool"))
    }

    // MARK: - AIEvent Tests

    func testAnswerEventDecoding() throws {
        let json = """
        {"type":"answer","text":"Hello world"}
        """.data(using: .utf8)!

        let event = try JSONDecoder().decode(AIEvent.self, from: json)
        XCTAssertEqual(event.type, .answer)
        XCTAssertEqual(event.text, "Hello world")
        XCTAssertNil(event.path)
        XCTAssertNil(event.toolName)
    }

    func testCitationEventDecoding() throws {
        let json = """
        {"type":"citation","path":"notes/test.md","title":"Test Note","snippet":"Some snippet","score":0.85}
        """.data(using: .utf8)!

        let event = try JSONDecoder().decode(AIEvent.self, from: json)
        XCTAssertEqual(event.type, .citation)
        XCTAssertEqual(event.path, "notes/test.md")
        XCTAssertEqual(event.title, "Test Note")
        XCTAssertEqual(event.snippet, "Some snippet")
        XCTAssertEqual(event.score!, 0.85, accuracy: 0.001)
        XCTAssertNil(event.text)
    }

    func testToolEventDecoding() throws {
        let json = """
        {"type":"tool","tool_name":"search","status":"done"}
        """.data(using: .utf8)!

        let event = try JSONDecoder().decode(AIEvent.self, from: json)
        XCTAssertEqual(event.type, .tool)
        XCTAssertEqual(event.toolName, "search")
        XCTAssertEqual(event.status, "done")
        XCTAssertNil(event.text)
    }

    func testDoneEventDecoding() throws {
        let json = """
        {"type":"done"}
        """.data(using: .utf8)!

        let event = try JSONDecoder().decode(AIEvent.self, from: json)
        XCTAssertEqual(event.type, .done)
        XCTAssertNil(event.text)
        XCTAssertNil(event.path)
    }

    // MARK: - Agentic loop events (issue #317)

    func testAgentToolCallEventDecoding() throws {
        let json = """
        {"type":"tool","tool_name":"desk_search","iteration":1,"tool_inputs":{"query":"alpha"}}
        """.data(using: .utf8)!

        let event = try JSONDecoder().decode(AIEvent.self, from: json)
        XCTAssertEqual(event.type, .tool)
        XCTAssertEqual(event.toolName, "desk_search")
        XCTAssertEqual(event.iteration, 1)
        XCTAssertEqual(event.toolInputs, "{\"query\":\"alpha\"}")
        XCTAssertNil(event.toolOutput)
    }

    func testAgentToolResultEventDecoding() throws {
        let json = """
        {"type":"tool","tool_name":"desk_search","iteration":2,"tool_output":"{\\"results\\":[]}","tool_output_truncated":true}
        """.data(using: .utf8)!

        let event = try JSONDecoder().decode(AIEvent.self, from: json)
        XCTAssertEqual(event.iteration, 2)
        XCTAssertEqual(event.toolOutput, "{\"results\":[]}")
        XCTAssertEqual(event.toolOutputTruncated, true)
        XCTAssertNil(event.toolInputs)
    }

    func testAgentTerminalEventDecoding() throws {
        let json = """
        {"type":"done","token_usage":1347,"context_window":8000}
        """.data(using: .utf8)!

        let event = try JSONDecoder().decode(AIEvent.self, from: json)
        XCTAssertEqual(event.type, .done)
        XCTAssertEqual(event.tokenUsage, 1347)
        XCTAssertEqual(event.contextWindow, 8000)
    }

    func testAIEventTypeRawValues() {
        XCTAssertEqual(AIEventType.answer.rawValue, "answer")
        XCTAssertEqual(AIEventType.citation.rawValue, "citation")
        XCTAssertEqual(AIEventType.tool.rawValue, "tool")
        XCTAssertEqual(AIEventType.done.rawValue, "done")
    }

    // MARK: - NotificationScheduler Tests

    func testUpcomingDueNotificationsFiltersByLeadTime() throws {
        let calendar = Calendar.current
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withFullDate]

        // Due in 3 days → with 1-day lead time, fire date is in 2 days (future)
        let dueSoon = formatter.string(from: calendar.date(byAdding: .day, value: 3, to: Date())!)
        // Due tomorrow → with 1-day lead time, fire date is today (included)
        let dueTomorrow = formatter.string(from: calendar.date(byAdding: .day, value: 1, to: Date())!)
        // Due yesterday → fire date was 2 days ago (past, should be skipped)
        let duePast = formatter.string(from: calendar.date(byAdding: .day, value: -1, to: Date())!)
        // Empty due date → should be skipped
        let noDue = ""

        let docs = [
            DocumentItem(
                path: "a.md", title: "Doc A", documentDate: "", person: "",
                status: "open", dueDate: dueSoon, confidence: 80,
                correspondent: "", documentType: "invoice"
            ),
            DocumentItem(
                path: "b.md", title: "Doc B", documentDate: "", person: "",
                status: "open", dueDate: dueTomorrow, confidence: 80,
                correspondent: "", documentType: "invoice"
            ),
            DocumentItem(
                path: "c.md", title: "Doc C", documentDate: "", person: "",
                status: "open", dueDate: noDue, confidence: 80,
                correspondent: "", documentType: "invoice"
            ),
        ]

        let scheduler = NotificationScheduler(leadTimeDays: 1)
        let notifications = scheduler.upcomingDueNotifications(from: docs)

        // Should include Doc A (due in 3 days → fire in 2 days) and Doc B (due tomorrow → fire today)
        // Doc C (empty due date) should be skipped
        XCTAssertFalse(notifications.isEmpty, "Should have at least one notification")
        let paths = notifications.map(\.documentPath)
        XCTAssertTrue(paths.contains("a.md"), "Doc A should be included")
        XCTAssertTrue(paths.contains("b.md"), "Doc B (due tomorrow) should be included")
        XCTAssertFalse(paths.contains("c.md"), "Doc C (empty due date) should be skipped")
    }

    func testUpcomingDueNotificationsSkipsPastFireDates() throws {
        let calendar = Calendar.current
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withFullDate]

        // Due yesterday → with 1-day lead time, fire date was 2 days ago (past)
        let dueYesterday = formatter.string(from: calendar.date(byAdding: .day, value: -1, to: Date())!)

        let docs = [
            DocumentItem(
                path: "past.md", title: "Past Due", documentDate: "", person: "",
                status: "open", dueDate: dueYesterday, confidence: 80,
                correspondent: "", documentType: "invoice"
            ),
        ]

        let scheduler = NotificationScheduler(leadTimeDays: 1)
        let notifications = scheduler.upcomingDueNotifications(from: docs)

        XCTAssertTrue(notifications.isEmpty, "Past fire dates should be skipped")
    }

    func testUpcomingDueNotificationsSortedByFireDate() throws {
        let calendar = Calendar.current
        let formatter = ISO8601DateFormatter()
        formatter.formatOptions = [.withFullDate]

        let dueIn3 = formatter.string(from: calendar.date(byAdding: .day, value: 3, to: Date())!)
        let dueIn10 = formatter.string(from: calendar.date(byAdding: .day, value: 10, to: Date())!)

        let docs = [
            DocumentItem(
                path: "far.md", title: "Far", documentDate: "", person: "",
                status: "open", dueDate: dueIn10, confidence: 80,
                correspondent: "", documentType: "invoice"
            ),
            DocumentItem(
                path: "near.md", title: "Near", documentDate: "", person: "",
                status: "open", dueDate: dueIn3, confidence: 80,
                correspondent: "", documentType: "invoice"
            ),
        ]

        let scheduler = NotificationScheduler(leadTimeDays: 1)
        let notifications = scheduler.upcomingDueNotifications(from: docs)

        XCTAssertEqual(notifications.count, 2)
        XCTAssertEqual(notifications[0].documentPath, "near.md", "Near should fire first")
        XCTAssertEqual(notifications[1].documentPath, "far.md", "Far should fire second")
    }

    func testReviewQueueNotificationReturnsNilForZero() {
        let scheduler = NotificationScheduler()
        XCTAssertNil(scheduler.reviewQueueNotification(count: 0))
    }

    func testReviewQueueNotificationReturnsContentForNonZero() {
        let scheduler = NotificationScheduler()

        let one = scheduler.reviewQueueNotification(count: 1)
        XCTAssertNotNil(one)
        XCTAssertEqual(one?.title, "1 document needs review")

        let three = scheduler.reviewQueueNotification(count: 3)
        XCTAssertNotNil(three)
        XCTAssertEqual(three?.title, "3 documents need review")
        XCTAssertTrue(three?.body.contains("3 documents") ?? false)
    }

    func testNotificationSchedulerDefaultLeadTime() {
        let scheduler = NotificationScheduler()
        XCTAssertEqual(scheduler.leadTimeDays, 1)
    }

    func testNotificationSchedulerCustomLeadTime() {
        let scheduler = NotificationScheduler(leadTimeDays: 3)
        XCTAssertEqual(scheduler.leadTimeDays, 3)
    }

	func testServerURLNormalizationRequiresBareHTTPURL() {
		XCTAssertEqual(ServerConnectionConfig.normalizedURL(" https://desk.example.test/ ")?.absoluteString, "https://desk.example.test")
		XCTAssertEqual(ServerConnectionConfig.normalizedURL("http://192.168.1.4:8787")?.port, 8787)
		XCTAssertNil(ServerConnectionConfig.normalizedURL("desk.example.test"))
		XCTAssertNil(ServerConnectionConfig.normalizedURL("https://desk.example.test/api"))
		XCTAssertNil(ServerConnectionConfig.normalizedURL("https://user:secret@desk.example.test"))
		XCTAssertNil(ServerConnectionConfig.normalizedURL("https://desk.example.test?token=secret"))
	}

	func testIngestJobDecodesLocalNumericAndRemoteStringIDs() throws {
		let local = Data(#"{"id":42,"document_id":1,"kind":"ocr","status":"failed","attempts":2,"created_at":"now","updated_at":"now","source_path":"a.pdf"}"#.utf8)
		let remote = Data(#"{"id":"abcdef","status":"pending","capability":"ocr","error":"","created_at":"now","updated_at":"now","source_path":"archive/a.pdf"}"#.utf8)
		XCTAssertEqual(try JSONDecoder().decode(IngestJob.self, from: local).id, "42")
		let remoteJob = try JSONDecoder().decode(IngestJob.self, from: remote)
		XCTAssertEqual(remoteJob.id, "abcdef")
		XCTAssertEqual(remoteJob.kind, "ocr")
	}

	/// Regression test: a Go CLI command that (mistakenly) returns the JSON
	/// literal `null` for an array-typed result (e.g. `meeting list` on a
	/// vault with zero meeting notes) must decode as an empty array instead
	/// of throwing the raw "Cannot get unkeyed decoding container" error
	/// that crashed the Meetings tab on every fresh vault.
	func testDecodeTolerantOfNullArrayTreatsTopLevelNullAsEmptyArray() throws {
		let data = Data("null".utf8)
		let summaries = try DeskCore.decodeTolerantOfNullArray([MeetingNoteSummary].self, from: data)
		XCTAssertEqual(summaries, [])
	}

	func testDecodeTolerantOfNullArrayStillDecodesNonEmptyArrays() throws {
		let data = Data(#"[{"path":"meetings/m1.md","title":"Standup","meeting_id":"m1","started_at":"2026-07-06T12:00:00Z","duration_ms":60000,"language":"en","review_state":"draft"}]"#.utf8)
		let summaries = try DeskCore.decodeTolerantOfNullArray([MeetingNoteSummary].self, from: data)
		XCTAssertEqual(summaries.count, 1)
		XCTAssertEqual(summaries[0].meetingID, "m1")
	}

	func testDecodeTolerantOfNullArrayStillThrowsForNonArrayNull() throws {
		let data = Data("null".utf8)
		XCTAssertThrowsError(try DeskCore.decodeTolerantOfNullArray(SearchResponse.self, from: data))
	}

	func testCommandStreamYieldsLinesAsTheyArrive() async throws {
		MockStreamingURLProtocol.statusCode = 200
		MockStreamingURLProtocol.responseLines = [
			#"{"type":"answer","text":"first"}"#,
			#"{"type":"done"}"#,
		]
		let client = makeMockStreamingClient()

		var lines: [String] = []
		for try await line in client.commandStream(arguments: ["ask", "hi", "--json"]) {
			lines.append(line)
		}
		XCTAssertEqual(lines, MockStreamingURLProtocol.responseLines)
	}

	func testCommandStreamThrowsOnServerErrorStatus() async throws {
		MockStreamingURLProtocol.statusCode = 500
		MockStreamingURLProtocol.responseLines = []
		MockStreamingURLProtocol.errorBody = #"{"error":"boom"}"#
		let client = makeMockStreamingClient()

		do {
			for try await _ in client.commandStream(arguments: ["ask", "hi", "--json"]) {}
			XCTFail("expected commandStream to throw for a non-2xx response")
		} catch let ServerConnectionError.server(status, message) {
			XCTAssertEqual(status, 500)
			XCTAssertEqual(message, "boom")
		}
	}

	private func makeMockStreamingClient() -> RemoteDeskClient {
		let configuration = URLSessionConfiguration.ephemeral
		configuration.protocolClasses = [MockStreamingURLProtocol.self]
		let session = URLSession(configuration: configuration)
		let connection = ServerConnection(url: URL(string: "https://desk.example.test")!, token: String(repeating: "a", count: 32))
		return RemoteDeskClient(connection: connection, session: session)
	}
}

/// Simulates a streaming NDJSON response (or an error status) for
/// `RemoteDeskClient.commandStream` tests without a real HTTP server.
private final class MockStreamingURLProtocol: URLProtocol, @unchecked Sendable {
	nonisolated(unsafe) static var responseLines: [String] = []
	nonisolated(unsafe) static var statusCode: Int = 200
	nonisolated(unsafe) static var errorBody: String = ""

	override class func canInit(with request: URLRequest) -> Bool { true }
	override class func canonicalRequest(for request: URLRequest) -> URLRequest { request }

	override func startLoading() {
		guard let url = request.url,
			  let response = HTTPURLResponse(
				url: url, statusCode: Self.statusCode, httpVersion: "HTTP/1.1",
				headerFields: ["Content-Type": "application/x-ndjson"]
			  ) else {
			client?.urlProtocol(self, didFailWithError: ServerConnectionError.invalidResponse)
			return
		}
		client?.urlProtocol(self, didReceive: response, cacheStoragePolicy: .notAllowed)
		if Self.statusCode >= 300 {
			client?.urlProtocol(self, didLoad: Data(Self.errorBody.utf8))
		} else {
			for line in Self.responseLines {
				client?.urlProtocol(self, didLoad: Data((line + "\n").utf8))
			}
		}
		client?.urlProtocolDidFinishLoading(self)
	}

	override func stopLoading() {}
}
