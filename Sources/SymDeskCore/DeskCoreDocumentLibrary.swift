import Foundation

extension DeskCore {
    public func docsList(status: String? = nil, type: String? = nil, fileType: String? = nil, person: String? = nil, asn: Int? = nil) async throws -> [DocumentItem] {
        var args = ["docs", "list", "--json"] + vaultArgs
        if let s = status, !s.isEmpty { args += ["--status", s] }
        if let t = type, !t.isEmpty { args += ["--type", t] }
        if let ft = fileType, !ft.isEmpty { args += ["--file-type", ft] }
        if let p = person, !p.isEmpty { args += ["--person", p] }
        if let asn, asn > 0 { args += ["--asn", "\(asn)"] }
		return try await runDecoding([DocumentItem].self, arguments: args)
    }

    public func docSetStatus(path: String, status: String) async throws {
		_ = try await runChecked(arguments: ["doc", "status", path, status] + vaultArgs)
    }

    public func docSetDue(path: String, date: String) async throws {
		_ = try await runChecked(arguments: ["doc", "due", path, date] + vaultArgs)
    }
}
