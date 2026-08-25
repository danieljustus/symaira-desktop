import Foundation
import SymairaIngestContract

// DeskCore conforms to ClassificationRulesClient directly, the same pattern
// `MeetingsDataSource` already uses (see DeskCoreExportTests/MeetingModels):
// the Rules screen and mail-account editor now run through the already-
// resolved local/remote transport instead of locating a separate binary.
// symingest's rule store and mail config were absorbed into `symdesk rules`
// and `symdesk mail rules` (#609); this stops the app from looking for the
// retired standalone binary (#610). The JSON envelopes are unchanged, so
// decoding goes through the same `SymingestRulesContract` decoders the old
// client used.
extension DeskCore: ClassificationRulesClient {
    public func listRules() async throws -> [ClassificationRule] {
        let data = try await runChecked(arguments: rulesArgs("list"))
        return try SymingestRulesContract.decodeList(from: data).rules
    }

    public func addRule(pattern: String, kind: String, value: String) async throws -> ClassificationRule {
        let data = try await runChecked(arguments: rulesArgs("add", pattern, kind, value))
        return try SymingestRulesContract.decodeRule(from: data).rule
    }

    public func updateRule(id: Int64, pattern: String, kind: String, value: String) async throws -> ClassificationRule {
        let data = try await runChecked(arguments: rulesArgs("update", String(id), pattern, kind, value))
        return try SymingestRulesContract.decodeRule(from: data).rule
    }

    public func deleteRule(id: Int64) async throws {
        let data = try await runChecked(arguments: rulesArgs("delete", String(id)))
        _ = try SymingestRulesContract.decodeDelete(from: data)
    }

    public func testRules(text: String) async throws -> [ClassificationRuleMatch] {
        let data = try await runChecked(arguments: rulesArgs("test", text))
        return try SymingestRulesContract.decodeTest(from: data).matches
    }

    public func dryRunRule(pattern: String, kind: String, value: String) async throws -> RulesDryRunResponse {
        let data = try await runChecked(arguments: rulesArgs("dry-run", pattern, kind, value))
        return try SymingestRulesContract.decodeDryRun(from: data)
    }

    // Mail accounts are not vault-scoped, and the CLI resolves its own
    // config path (project `.symingest.toml`, else the global config) now
    // that the app stopped pinning an explicit `~/.config/symingest/...`
    // path (#610 point 7).
    public func listMailRules() async throws -> [MailAccount] {
        let data = try await runChecked(arguments: ["mail", "rules", "list", "--json"])
        return try SymingestRulesContract.decodeMail(from: data).accounts
    }

    public func createMailRule(_ account: MailAccount) async throws -> MailAccount {
        let data = try await runChecked(arguments: ["mail", "rules", "create", "--json"], stdin: encoded(account))
        let result = try SymingestRulesContract.decodeMail(from: data)
        return result.accounts.first ?? account
    }

    public func updateMailRule(id: String, account: MailAccount) async throws -> MailAccount {
        let data = try await runChecked(arguments: ["mail", "rules", "update", id, "--json"], stdin: encoded(account))
        let result = try SymingestRulesContract.decodeMail(from: data)
        return result.accounts.first ?? account
    }

    public func deleteMailRule(id: String) async throws {
        let data = try await runChecked(arguments: ["mail", "rules", "delete", id, "--json"])
        _ = try SymingestRulesContract.decodeMail(from: data)
    }

    // Positional arguments come after a `--` terminator: a rule pattern or a
    // block of text under test can legitimately begin with a dash (pasting a
    // bulleted line is the obvious case), and without the terminator cobra
    // reads it as a flag and fails with "unknown shorthand flag".
    private func rulesArgs(_ subcommand: String, _ positionals: String...) -> [String] {
        var args = ["rules", subcommand, "--json"] + vaultArgs
        if !positionals.isEmpty {
            args.append("--")
            args.append(contentsOf: positionals)
        }
        return args
    }

    private func encoded(_ account: MailAccount) throws -> String {
        String(data: try JSONEncoder().encode(account), encoding: .utf8) ?? ""
    }
}
