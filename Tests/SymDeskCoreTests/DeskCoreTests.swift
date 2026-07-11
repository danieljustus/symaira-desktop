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
            "vault": {"symseek": "ok", "symmemory": "ok"},
            "sidecar": {"symingest": "available"},
            "tools": {"symseek": "ok", "symmemory": "not found", "symingest": "ok"}
        }
        """.data(using: .utf8)!

        let report = try JSONDecoder().decode(DoctorReport.self, from: json)
        XCTAssertEqual(report.overall, "ok")
        XCTAssertTrue(report.tools.isAvailable("symseek"))
        XCTAssertFalse(report.tools.isAvailable("symmemory"))
        XCTAssertTrue(report.tools.isAvailable("symingest"))
    }

    func testDoctorReportDecodingWithMissingFields() throws {
        let json = """
        {
            "overall": "degraded"
        }
        """.data(using: .utf8)!

        let report = try JSONDecoder().decode(DoctorReport.self, from: json)
        XCTAssertEqual(report.overall, "degraded")
        XCTAssertFalse(report.tools.isAvailable("symseek"))
        XCTAssertFalse(report.vault.isAvailable("symmemory"))
    }

    func testDoctorReportToolAvailabilityVariants() {
        let available = DoctorReport.ToolAvailability(symseek: "ok", symmemory: nil, symingest: nil, symfetch: nil, symvault: nil)
        XCTAssertTrue(available.isAvailable("symseek"))

        let found = DoctorReport.ToolAvailability(symseek: "found", symmemory: nil, symingest: nil, symfetch: nil, symvault: nil)
        XCTAssertTrue(found.isAvailable("symseek"))

        let status = DoctorReport.ToolAvailability(symseek: "Available", symmemory: nil, symingest: nil, symfetch: nil, symvault: nil)
        XCTAssertTrue(status.isAvailable("symseek"))

        let missing = DoctorReport.ToolAvailability(symseek: "not found", symmemory: nil, symingest: nil, symfetch: nil, symvault: nil)
        XCTAssertFalse(missing.isAvailable("symseek"))

        let unknown = DoctorReport.ToolAvailability(symseek: nil, symmemory: nil, symingest: nil, symfetch: nil, symvault: nil)
        XCTAssertFalse(unknown.isAvailable("symseek"))
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
}
