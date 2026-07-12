import XCTest
import Foundation
@testable import SymDeskCore
import SymairaIngestContract

@MainActor
final class ClassificationRulesViewModelTests: XCTestCase {
    func testSaveUpdateDeleteAndExistingDocumentDryRun() async throws {
        let client = FakeClassificationRulesClient()
        let viewModel = ClassificationRulesViewModel(client: client)

        let saveResult = await viewModel.saveRule(id: nil, pattern: "  invoice ", kind: " category ", value: " Finance ")
        XCTAssertTrue(saveResult)
        let ruleID = try XCTUnwrap(viewModel.rules.first?.id)
        let updateResult = await viewModel.saveRule(id: ruleID, pattern: "receipt", kind: "tag", value: "Finance")
        XCTAssertTrue(updateResult)

        viewModel.dryRunPattern = "receipt"
        viewModel.dryRunKind = "tag"
        viewModel.dryRunValue = "Review"
        let dryRunResult = await viewModel.runDryRun()
        XCTAssertTrue(dryRunResult)
        XCTAssertEqual(viewModel.dryRunResult?.matchedDocuments, 1)

        let deleteResult = await viewModel.deleteRule(id: ruleID)
        XCTAssertTrue(deleteResult)
        XCTAssertTrue(viewModel.rules.isEmpty)
    }

    func testMailRoundTripAndMissingInputsFailBeforeClientCalls() async {
        let client = FakeClassificationRulesClient()
        let viewModel = ClassificationRulesViewModel(client: client)
        let emptyPatternSave = await viewModel.saveRule(id: nil, pattern: " ", kind: "category", value: "Finance")
        XCTAssertFalse(emptyPatternSave)
        let emptyDryRun = await viewModel.runDryRun()
        XCTAssertFalse(emptyDryRun)
        let emptyTest = await viewModel.test()
        XCTAssertFalse(emptyTest)
        let calls = await client.addCallCount()
        XCTAssertEqual(calls, 0)

        let account = MailAccount(host: "imap.example.com", port: 993, username: "daniel", passwordSecret: "symvault://imap/daniel")
        let mailSave = await viewModel.saveMail(id: nil, account: account)
        XCTAssertTrue(mailSave)
        XCTAssertEqual(viewModel.mailAccounts.count, 1)
    }
}

private actor FakeClassificationRulesClient: ClassificationRulesClient {
    private var storedRules: [ClassificationRule] = []
    private var storedMail: [MailAccount] = []
    private var nextID: Int64 = 1
    private var _addCallCount: Int = 0

    func addCallCount() -> Int { _addCallCount }

    func listRules() async throws -> [ClassificationRule] { storedRules }

    func addRule(pattern: String, kind: String, value: String) async throws -> ClassificationRule {
        _addCallCount += 1
        let rule = ClassificationRule(id: nextID, pattern: pattern, kind: kind, value: value, createdAt: "2026-07-12T10:00:00Z")
        nextID += 1
        storedRules.append(rule)
        return rule
    }

    func updateRule(id: Int64, pattern: String, kind: String, value: String) async throws -> ClassificationRule {
        let rule = ClassificationRule(id: id, pattern: pattern, kind: kind, value: value, createdAt: "2026-07-12T10:00:00Z")
        guard let index = storedRules.firstIndex(where: { $0.id == id }) else { throw SymingestRulesError.commandFailed("missing fake rule") }
        storedRules[index] = rule
        return rule
    }

    func deleteRule(id: Int64) async throws { storedRules.removeAll { $0.id == id } }

    func testRules(text: String) async throws -> [ClassificationRuleMatch] {
        storedRules.filter { text.localizedCaseInsensitiveContains($0.pattern) }.map { ClassificationRuleMatch(id: $0.id, pattern: $0.pattern, kind: $0.kind, value: $0.value) }
    }

    func dryRunRule(pattern: String, kind: String, value: String) async throws -> RulesDryRunResponse {
        let matches = storedRules.filter { pattern.localizedCaseInsensitiveContains($0.pattern) || $0.pattern.localizedCaseInsensitiveContains(pattern) }
        return RulesDryRunResponse(
            schemaVersion: 1,
            operation: "dry_run",
            proposedRule: ProposedClassificationRule(pattern: pattern, kind: kind, value: value),
            vaultPath: "/vault",
            totalDocuments: matches.isEmpty ? 0 : 1,
            matchedDocuments: matches.isEmpty ? 0 : 1,
            skippedDocuments: 0,
            matches: matches.isEmpty ? [] : [RulesDryRunMatch(documentID: 1, notePath: "/vault/receipt.md", title: "Receipt", matchedRuleIDs: matches.map(\.id))],
            skipped: []
        )
    }

    func listMailRules() async throws -> [MailAccount] { storedMail }

    func createMailRule(_ account: MailAccount) async throws -> MailAccount {
        let created = MailAccount(
            id: account.stableID,
            host: account.host,
            port: account.port,
            username: account.username,
            passwordSecret: account.passwordSecret,
            folder: account.folder,
            from: account.from,
            subject: account.subject,
            hasAttachment: account.hasAttachment,
            action: account.action,
            moveTo: account.moveTo,
            archiveMail: account.archiveMail
        )
        storedMail.append(created)
        return created
    }

    func updateMailRule(id: String, account: MailAccount) async throws -> MailAccount {
        let updated = MailAccount(
            id: id,
            host: account.host,
            port: account.port,
            username: account.username,
            passwordSecret: account.passwordSecret,
            folder: account.folder,
            from: account.from,
            subject: account.subject,
            hasAttachment: account.hasAttachment,
            action: account.action,
            moveTo: account.moveTo,
            archiveMail: account.archiveMail
        )
        guard let index = storedMail.firstIndex(where: { $0.stableID == id }) else { throw SymingestRulesError.commandFailed("missing fake mail account") }
        storedMail[index] = updated
        return updated
    }

    func deleteMailRule(id: String) async throws { storedMail.removeAll { $0.stableID == id } }
}
