import Foundation
import SymairaIngestContract

public enum RulesErrorKind {
    case validation
    case availability
}

@MainActor
public final class ClassificationRulesViewModel: ObservableObject {
    @Published public var rules: [ClassificationRule] = []
    @Published public var isLoading = false
    @Published public var isPerformingOperation = false
    @Published public var lastError: String?
    @Published public var lastErrorKind: RulesErrorKind = .availability
    @Published public var lastActionMessage: String?

    @Published public var testText: String = ""
    @Published public var matches: [ClassificationRuleMatch] = []

    @Published public var dryRunPattern: String = ""
    @Published public var dryRunKind: String = ""
    @Published public var dryRunValue: String = ""
    @Published public var dryRunResult: RulesDryRunResponse?

    @Published public var mailAccounts: [MailAccount] = []
    @Published public var mailError: String?

    private let client: any ClassificationRulesClient
    private let configPath: String?

    // `DeskCore.shared` conforms to `ClassificationRulesClient` (see
    // DeskCoreRules.swift) and already tracks the active vault, so this no
    // longer needs a `vaultPath` override or a pinned config path — the
    // `symdesk` CLI resolves its own (#610 point 7).
    public convenience init(configPath: String? = nil) {
        self.init(client: DeskCore.shared, configPath: configPath)
    }

    public init(client: any ClassificationRulesClient, configPath: String? = nil) {
        self.client = client
        self.configPath = configPath
    }

    @discardableResult
    public func load() async -> Bool {
        isLoading = true
        lastError = nil
        do {
            rules = try await client.listRules()
            isLoading = false
            return true
        } catch {
            lastError = error.localizedDescription
            lastErrorKind = .availability
            isLoading = false
            return false
        }
    }

    // A missing mail config is not a special case any more: `mail rules
    // list` returns an empty account list rather than an error (mail
    // ingestion is optional), so there is nothing left to sniff for in the
    // error text here (#610 point 6).
    @discardableResult
    public func loadMail() async -> Bool {
        mailError = nil
        do {
            mailAccounts = try await client.listMailRules()
            return true
        } catch {
            var cleaned = error.localizedDescription
            if let range = cleaned.range(of: #"CLI execution failed with exit code \d+:\s*"#, options: .regularExpression) {
                cleaned.removeSubrange(range)
            }
            mailError = cleaned
            return false
        }
    }

    private func normalizedValues(pattern: String, kind: String, value: String) -> (pattern: String, kind: String, value: String)? {
        let p = pattern.trimmingCharacters(in: .whitespacesAndNewlines)
        let k = kind.trimmingCharacters(in: .whitespacesAndNewlines)
        let v = value.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !p.isEmpty, !k.isEmpty, !v.isEmpty else { return nil }
        return (p, k, v)
    }

    @discardableResult
    public func saveRule(id: Int64?, pattern: String, kind: String, value: String) async -> Bool {
        guard let values = normalizedValues(pattern: pattern, kind: kind, value: value) else {
            lastError = "Pattern, kind, and value are required."
            lastErrorKind = .validation
            return false
        }
        isPerformingOperation = true
        lastError = nil
        lastActionMessage = nil
        do {
            if let id {
                _ = try await client.updateRule(id: id, pattern: values.pattern, kind: values.kind, value: values.value)
                lastActionMessage = "Rule updated."
            } else {
                _ = try await client.addRule(pattern: values.pattern, kind: values.kind, value: values.value)
                lastActionMessage = "Rule added."
            }
            _ = await load()
            isPerformingOperation = false
            return true
        } catch {
            lastError = error.localizedDescription
            lastErrorKind = .availability
            isPerformingOperation = false
            return false
        }
    }

    @discardableResult
    public func deleteRule(id: Int64) async -> Bool {
        isPerformingOperation = true
        lastError = nil
        do {
            try await client.deleteRule(id: id)
            _ = await load()
            isPerformingOperation = false
            return true
        } catch {
            lastError = error.localizedDescription
            lastErrorKind = .availability
            isPerformingOperation = false
            return false
        }
    }

    @discardableResult
    public func test() async -> Bool {
        let text = testText.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else {
            lastError = "Enter sample text to test classification rules."
            lastErrorKind = .validation
            return false
        }
        isPerformingOperation = true
        lastError = nil
        do {
            matches = try await client.testRules(text: text)
            isPerformingOperation = false
            return true
        } catch {
            lastError = error.localizedDescription
            lastErrorKind = .availability
            isPerformingOperation = false
            return false
        }
    }

    @discardableResult
    public func runDryRun() async -> Bool {
        guard let values = normalizedValues(pattern: dryRunPattern, kind: dryRunKind, value: dryRunValue) else {
            lastError = "Pattern, kind, and value are required for the existing-document dry-run."
            lastErrorKind = .validation
            return false
        }
        isPerformingOperation = true
        lastError = nil
        dryRunResult = nil
        do {
            dryRunResult = try await client.dryRunRule(pattern: values.pattern, kind: values.kind, value: values.value)
            isPerformingOperation = false
            return true
        } catch {
            lastError = error.localizedDescription
            lastErrorKind = .availability
            isPerformingOperation = false
            return false
        }
    }

    @discardableResult
    public func saveMail(id: String?, account: MailAccount) async -> Bool {
        isPerformingOperation = true
        lastError = nil
        mailError = nil
        lastActionMessage = nil
        do {
            if let id, !id.isEmpty {
                _ = try await client.updateMailRule(id: id, account: account)
                lastActionMessage = "Mail account updated. Restart the watcher to apply changes."
            } else {
                _ = try await client.createMailRule(account)
                lastActionMessage = "Mail account created. Restart the watcher to apply changes."
            }
            _ = await loadMail()
            isPerformingOperation = false
            return true
        } catch {
            lastError = error.localizedDescription
            lastErrorKind = .availability
            isPerformingOperation = false
            return false
        }
    }

    @discardableResult
    public func deleteMail(id: String) async -> Bool {
        guard !id.isEmpty else {
            lastError = "Cannot delete account with empty identifier."
            lastErrorKind = .validation
            return false
        }
        isPerformingOperation = true
        lastError = nil
        do {
            try await client.deleteMailRule(id: id)
            _ = await loadMail()
            isPerformingOperation = false
            return true
        } catch {
            lastError = error.localizedDescription
            lastErrorKind = .availability
            isPerformingOperation = false
            return false
        }
    }
}
