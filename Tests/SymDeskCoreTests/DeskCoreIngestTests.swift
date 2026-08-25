import XCTest
@testable import SymDeskCore
import SymairaCLIRunner
import SymairaIngestContract

/// Argument construction and envelope decoding for `DeskCore`'s
/// `ClassificationRulesClient`/`ReOCRClient` conformance (issue #610): the
/// Rules screen and Re-OCR button used to shell out to the retired
/// `symingest` binary; now `DeskCore` drives the already-resolved transport
/// directly, the same pattern `DeskCoreExportTests` uses.
@MainActor
final class DeskCoreIngestTests: XCTestCase {
    private var previousTransport: DeskTransport?
    private var previousVaultPath: String?
    private var transport: RecordingTransport!

    override func setUp() async throws {
        try await super.setUp()
        previousTransport = DeskCore.shared.transport
        previousVaultPath = DeskCore.shared.vaultPath
        transport = RecordingTransport()
        DeskCore.shared.transport = transport
        DeskCore.shared.vaultPath = "/tmp/vault"
    }

    override func tearDown() async throws {
        DeskCore.shared.transport = previousTransport
        DeskCore.shared.vaultPath = previousVaultPath
        transport = nil
        try await super.tearDown()
    }

    // MARK: - Rules

    func testListRulesSendsRulesListAndDecodesEnvelope() async throws {
        transport.response = Data(#"{"schema_version":1,"rules":[{"id":1,"pattern":"invoice","kind":"category","value":"Finance","created_at":"2026-01-01T00:00:00Z"}]}"#.utf8)

        let rules = try await DeskCore.shared.listRules()

        XCTAssertEqual(transport.lastArguments, ["rules", "list", "--json", "--vault", "/tmp/vault"])
        XCTAssertEqual(rules.map(\.pattern), ["invoice"])
    }

    func testAddRuleSendsPositionalArgsAndDecodesRule() async throws {
        transport.response = Data(#"{"schema_version":1,"rule":{"id":2,"pattern":"receipt","kind":"tag","value":"Review","created_at":null}}"#.utf8)

        let rule = try await DeskCore.shared.addRule(pattern: "receipt", kind: "tag", value: "Review")

        XCTAssertEqual(transport.lastArguments, ["rules", "add", "receipt", "tag", "Review", "--json", "--vault", "/tmp/vault"])
        XCTAssertEqual(rule.id, 2)
    }

    func testUpdateRuleSendsIDAndDecodesRule() async throws {
        transport.response = Data(#"{"schema_version":1,"rule":{"id":2,"pattern":"receipt","kind":"tag","value":"Archive","created_at":null}}"#.utf8)

        let rule = try await DeskCore.shared.updateRule(id: 2, pattern: "receipt", kind: "tag", value: "Archive")

        XCTAssertEqual(transport.lastArguments, ["rules", "update", "2", "receipt", "tag", "Archive", "--json", "--vault", "/tmp/vault"])
        XCTAssertEqual(rule.value, "Archive")
    }

    func testDeleteRuleSendsIDAndDecodesDeleteEnvelope() async throws {
        transport.response = Data(#"{"schema_version":1,"id":2,"deleted":true}"#.utf8)

        try await DeskCore.shared.deleteRule(id: 2)

        XCTAssertEqual(transport.lastArguments, ["rules", "delete", "2", "--json", "--vault", "/tmp/vault"])
    }

    func testTestRulesDecodesMatches() async throws {
        transport.response = Data(#"{"schema_version":1,"matches":[{"id":1,"pattern":"invoice","kind":"category","value":"Finance"}]}"#.utf8)

        let matches = try await DeskCore.shared.testRules(text: "invoice #123")

        XCTAssertEqual(transport.lastArguments, ["rules", "test", "invoice #123", "--json", "--vault", "/tmp/vault"])
        XCTAssertEqual(matches.map(\.id), [1])
    }

    func testDryRunRuleDecodesResponse() async throws {
        transport.response = Data(
            #"{"schema_version":1,"operation":"dry_run","proposed_rule":{"pattern":"invoice","kind":"category","value":"Finance"},"vault_path":"/tmp/vault","total_documents":2,"matched_documents":1,"skipped_documents":0,"matches":[{"document_id":1,"note_path":"a.md","title":"A","matched_rule_ids":[0]}],"skipped":[]}"#
                .utf8
        )

        let result = try await DeskCore.shared.dryRunRule(pattern: "invoice", kind: "category", value: "Finance")

        XCTAssertEqual(transport.lastArguments, ["rules", "dry-run", "invoice", "category", "Finance", "--json", "--vault", "/tmp/vault"])
        XCTAssertEqual(result.matchedDocuments, 1)
    }

    // MARK: - Mail rules

    // Mail accounts are not vault-scoped, so unlike the rules commands above
    // this must NOT carry `--vault` (#610 point 7).
    func testListMailRulesOmitsVaultFlag() async throws {
        transport.response = Data(#"{"schema_version":1,"operation":"list","config_path":"/tmp/config.toml","accounts":[],"reload_required":false,"warnings":[]}"#.utf8)

        let accounts = try await DeskCore.shared.listMailRules()

        XCTAssertEqual(transport.lastArguments, ["mail", "rules", "list", "--json"])
        XCTAssertTrue(accounts.isEmpty)
    }

    // A missing mail config is not an error on the new CLI surface — it
    // comes back as a normal, empty account list (#610 point 6).
    func testListMailRulesTreatsEmptyResultAsUnconfiguredNotAnError() async throws {
        transport.response = Data(#"{"schema_version":1,"operation":"list","config_path":"/tmp/config.toml","accounts":[],"reload_required":false,"warnings":[]}"#.utf8)

        let accounts = try await DeskCore.shared.listMailRules()

        XCTAssertTrue(accounts.isEmpty)
    }

    func testCreateMailRuleSendsAccountJSONOnStdin() async throws {
        transport.response = Data(
            #"{"schema_version":1,"operation":"create","config_path":"/tmp/config.toml","accounts":[{"id":"a@b:993/INBOX","host":"b","port":993,"username":"a","password_secret":"symvault://imap/a","folder":"INBOX","from":[],"subject":[],"has_attachment":false,"action":"mark_seen","move_to":"","archive_mail":false}],"reload_required":true,"warnings":[]}"#
                .utf8
        )
        let account = MailAccount(host: "b", port: 993, username: "a", passwordSecret: "symvault://imap/a")

        let created = try await DeskCore.shared.createMailRule(account)

        XCTAssertEqual(transport.lastArguments, ["mail", "rules", "create", "--json"])
        XCTAssertEqual(created.id, "a@b:993/INBOX")
        let sentAccount = try JSONDecoder().decode(MailAccount.self, from: Data(transport.lastStdin.utf8))
        XCTAssertEqual(sentAccount.host, "b")
        XCTAssertEqual(sentAccount.username, "a")
    }

    func testUpdateMailRuleSendsIDAndAccountJSON() async throws {
        transport.response = Data(
            #"{"schema_version":1,"operation":"update","config_path":"/tmp/config.toml","accounts":[{"id":"a@b:993/INBOX","host":"b","port":993,"username":"a","password_secret":"symvault://imap/a","folder":"INBOX","from":[],"subject":[],"has_attachment":false,"action":"mark_seen","move_to":"","archive_mail":false}],"reload_required":true,"warnings":[]}"#
                .utf8
        )
        let account = MailAccount(host: "b", port: 993, username: "a", passwordSecret: "symvault://imap/a")

        _ = try await DeskCore.shared.updateMailRule(id: "a@b:993/INBOX", account: account)

        XCTAssertEqual(transport.lastArguments, ["mail", "rules", "update", "a@b:993/INBOX", "--json"])
        let sentAccount = try JSONDecoder().decode(MailAccount.self, from: Data(transport.lastStdin.utf8))
        XCTAssertEqual(sentAccount.host, "b")
    }

    func testDeleteMailRuleSendsID() async throws {
        transport.response = Data(#"{"schema_version":1,"operation":"delete","config_path":"/tmp/config.toml","accounts":[],"reload_required":true,"warnings":[]}"#.utf8)

        try await DeskCore.shared.deleteMailRule(id: "a@b:993/INBOX")

        XCTAssertEqual(transport.lastArguments, ["mail", "rules", "delete", "a@b:993/INBOX", "--json"])
    }

    // MARK: - Re-OCR

    func testReprocessByArchivePathSendsPathAndDecodesCompletedEnvelope() async throws {
        transport.response = Data(#"{"schema_version":1,"document_id":5,"job_id":9,"status":"completed","output_path":"/vault/doc.md"}"#.utf8)

        let response = try await DeskCore.shared.reprocess(archivePath: "/vault/originals/doc.pdf")

        XCTAssertEqual(transport.lastArguments, ["ingest", "reocr", "/vault/originals/doc.pdf", "--json", "--vault", "/tmp/vault"])
        XCTAssertEqual(response.status, "completed")
    }

    func testReprocessByDocumentIDSendsFlag() async throws {
        transport.response = Data(#"{"schema_version":1,"document_id":5,"job_id":9,"status":"completed","output_path":"/vault/doc.md"}"#.utf8)

        _ = try await DeskCore.shared.reprocess(documentID: 5)

        XCTAssertEqual(transport.lastArguments, ["ingest", "reocr", "--document-id", "5", "--json", "--vault", "/tmp/vault"])
    }

    /// `ingest reocr` reports a failure through its own {status, error}
    /// envelope on stdout even when the process exits non-zero (issue #438's
    /// contract). `reprocess` must still surface that envelope rather than
    /// discarding stdout because of the exit code (issue #610) — this is
    /// what lets the Re-OCR button show a real error instead of vanishing.
    func testReprocessDecodesFailureEnvelopeEvenOnNonZeroExit() async throws {
        transport.response = Data(#"{"schema_version":1,"document_id":0,"job_id":0,"status":"failed","output_path":"","error":{"code":"no_archived_original","message":"no archived original for this document"}}"#.utf8)
        transport.exitCode = 1

        let response = try await DeskCore.shared.reprocess(archivePath: "/vault/originals/missing.pdf")

        XCTAssertEqual(response.status, "failed")
        XCTAssertEqual(response.error?.code, "no_archived_original")
    }

    func testReprocessThrowsWhenStdoutIsNotAValidEnvelope() async throws {
        transport.response = Data("not json".utf8)
        transport.exitCode = 1
        transport.stderr = Data("boom".utf8)

        do {
            _ = try await DeskCore.shared.reprocess(archivePath: "/vault/originals/doc.pdf")
            XCTFail("expected a thrown error when stdout isn't a valid envelope")
        } catch DeskCoreError.cliExecutionFailed(let exitCode, let stderr) {
            XCTAssertEqual(exitCode, 1)
            XCTAssertEqual(stderr, "boom")
        }
    }
}

/// Records what the core asked the transport to run — arguments and stdin —
/// and replays a canned response, optionally with a non-zero exit code.
private final class RecordingTransport: DeskTransport, @unchecked Sendable {
    var response = Data("{}".utf8)
    var exitCode: Int32 = 0
    var stderr = Data()
    private(set) var lastArguments: [String] = []
    private(set) var lastStdin: String = ""

    func command(arguments: [String], stdin: String) async throws -> Data {
        lastArguments = arguments
        lastStdin = stdin
        guard exitCode == 0 else {
            throw CLIRunnerError.executionFailed(code: exitCode, fullStderr: String(decoding: stderr, as: UTF8.self))
        }
        return response
    }

    func commandResult(arguments: [String]) async throws -> CLIResult {
        lastArguments = arguments
        return CLIResult(stdout: response, stderr: stderr, exitCode: exitCode)
    }

    func commandStream(arguments: [String], stdin: String) -> AsyncThrowingStream<String, Error> {
        lastArguments = arguments
        return AsyncThrowingStream { $0.finish() }
    }

    func fileContent(path: String) async throws -> String { "" }
    func saveFile(path: String, content: String) async throws {}
    func ingestFile(_ fileURL: URL, vaultArgs: [String]) async throws -> String { "" }
    func ingestJobs(vaultArgs: [String]) async throws -> [IngestJob] { [] }
    func ingestRetry(jobID: String, vaultArgs: [String]) async throws {}
}
