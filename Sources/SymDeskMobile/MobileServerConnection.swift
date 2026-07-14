import CryptoKit
import Foundation
import Security

enum MobileServerError: LocalizedError {
    case invalidURL
    case invalidResponse
    case server(Int, String)
    case keychain(OSStatus)

    var errorDescription: String? {
        switch self {
        case .invalidURL: return "Enter a valid http:// or https:// SymDesk Server URL."
        case .invalidResponse: return "The server returned an invalid response."
        case .server(let status, let message): return "Server error \(status): \(message)"
        case .keychain(let status): return "The access token could not be stored in Keychain (\(status))."
        }
    }
}

struct MobileServerConnection: Sendable {
    let url: URL
    let token: String
}

enum MobileServerConfig {
    private static let urlKey = "symdesk.mobile.server-url.v1"
    private static let service = "com.symaira.desktop.ios.server"
    private static let account = "self-hosted-token"

    static func connection() -> MobileServerConnection? {
        guard let rawURL = UserDefaults.standard.string(forKey: urlKey),
              let url = normalizedURL(rawURL), let token = try? readToken(), !token.isEmpty else { return nil }
        return MobileServerConnection(url: url, token: token)
    }

    static func save(url rawURL: String, token: String) throws -> MobileServerConnection {
        guard let url = normalizedURL(rawURL) else { throw MobileServerError.invalidURL }
        let token = token.trimmingCharacters(in: .whitespacesAndNewlines)
        guard token.count >= 32 else { throw MobileServerError.server(0, "The token must contain at least 32 characters.") }
        try writeToken(token)
        UserDefaults.standard.set(url.absoluteString, forKey: urlKey)
        return MobileServerConnection(url: url, token: token)
    }

    static func reset() {
        UserDefaults.standard.removeObject(forKey: urlKey)
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account
        ]
        SecItemDelete(query as CFDictionary)
    }

    static func normalizedURL(_ raw: String) -> URL? {
        guard var components = URLComponents(string: raw.trimmingCharacters(in: .whitespacesAndNewlines)),
              components.scheme == "http" || components.scheme == "https",
              components.host != nil,
              components.user == nil,
              components.password == nil,
              components.query == nil,
              components.fragment == nil else { return nil }
        components.path = components.path.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        guard components.path.isEmpty else { return nil }
        return components.url
    }

    private static func readToken() throws -> String {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account,
            kSecReturnData as String: true,
            kSecMatchLimit as String: kSecMatchLimitOne
        ]
        var item: CFTypeRef?
        let status = SecItemCopyMatching(query as CFDictionary, &item)
        guard status == errSecSuccess, let data = item as? Data, let token = String(data: data, encoding: .utf8) else {
            throw MobileServerError.keychain(status)
        }
        return token
    }

    private static func writeToken(_ token: String) throws {
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account
        ]
        let attributes: [String: Any] = [kSecValueData as String: Data(token.utf8)]
        let update = SecItemUpdate(query as CFDictionary, attributes as CFDictionary)
        if update == errSecSuccess { return }
        guard update == errSecItemNotFound else { throw MobileServerError.keychain(update) }
        var add = query
        add[kSecValueData as String] = Data(token.utf8)
        let status = SecItemAdd(add as CFDictionary, nil)
        guard status == errSecSuccess else { throw MobileServerError.keychain(status) }
    }
}

final class MobileRemoteClient: @unchecked Sendable {
    private let connection: MobileServerConnection
    private let session: URLSession

    init(connection: MobileServerConnection, session: URLSession = .shared) {
        self.connection = connection
        self.session = session
    }

    func status() async throws {
        _ = try await request(path: "/api/v1/status")
    }

    /// Fetches the remote snapshot, sending `previousETag` as `If-None-Match`
    /// so an unchanged vault costs a small `304` instead of a full
    /// download-and-reparse. `previousETag` should be `nil` on the first
    /// call or after switching servers.
    func snapshot(ifNoneMatch previousETag: String?) async throws -> MobileSnapshotResult {
        guard var components = URLComponents(url: connection.url.appendingPathComponent("/api/v1/snapshot"), resolvingAgainstBaseURL: false) else {
            throw MobileServerError.invalidURL
        }
        components.queryItems = nil
        guard let url = components.url else { throw MobileServerError.invalidURL }
        var request = URLRequest(url: url)
        request.setValue("Bearer \(connection.token)", forHTTPHeaderField: "Authorization")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        if let previousETag {
            request.setValue(previousETag, forHTTPHeaderField: "If-None-Match")
        }
        request.timeoutInterval = 120
        let (data, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse else { throw MobileServerError.invalidResponse }
        if http.statusCode == 304 {
            return .unchanged
        }
        guard (200..<300).contains(http.statusCode) else {
            let error = try? JSONDecoder().decode(ErrorResponse.self, from: data)
            throw MobileServerError.server(http.statusCode, error?.error ?? "Unknown error")
        }
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601
        let decoded = try decoder.decode(SnapshotResponse.self, from: data)
        let root = URL(fileURLWithPath: "/remote-vault", isDirectory: true)
        var notes: [MobileNote] = []
        var skipped = 0
        notes.reserveCapacity(decoded.notes.count)
        for record in decoded.notes {
            do {
                let fileURL = root.appendingPathComponent(record.path)
                notes.append(try MobileVaultParser.parse(data: Data(record.content.utf8), fileURL: fileURL, root: root, modifiedAt: record.modifiedAt))
            } catch {
                skipped += 1
            }
        }
        notes.sort { $0.modifiedAt != $1.modifiedAt ? $0.modifiedAt > $1.modifiedAt : $0.title < $1.title }
        let etag = http.value(forHTTPHeaderField: "ETag")
        return .updated(MobileVaultSnapshot(notes: notes, skippedFiles: skipped), etag: etag)
    }

    func cachedAttachment(for note: MobileNote) async throws -> URL? {
        for reference in note.attachmentReferences {
            let cleaned = MobileVaultParser.cleanedReference(reference)
            guard !cleaned.isEmpty, !cleaned.hasPrefix("/"), !cleaned.contains("://") else { continue }
            let noteDirectory = (note.path as NSString).deletingLastPathComponent
            let candidates = orderedUnique([
                (noteDirectory as NSString).appendingPathComponent(cleaned),
                cleaned
            ].map { URL(fileURLWithPath: $0).standardized.path.trimmingCharacters(in: CharacterSet(charactersIn: "/")) })
            for path in candidates where !path.hasPrefix("..") {
                do { return try await cachedFile(path: path) } catch { continue }
            }
        }
        return nil
    }

    private func cachedFile(path: String) async throws -> URL {
        let cache = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("SymDeskRemote", isDirectory: true)
        try FileManager.default.createDirectory(at: cache, withIntermediateDirectories: true)
        let digest = SHA256.hash(data: Data(path.utf8)).map { String(format: "%02x", $0) }.joined()
        let output = cache.appendingPathComponent(digest + "-" + URL(fileURLWithPath: path).lastPathComponent)
        let data = try await request(path: "/api/v1/files", query: [URLQueryItem(name: "path", value: path)])
        try data.write(to: output, options: .atomic)
        return output
    }

    private func request(path: String, query: [URLQueryItem] = []) async throws -> Data {
        guard var components = URLComponents(url: connection.url.appendingPathComponent(path), resolvingAgainstBaseURL: false) else {
            throw MobileServerError.invalidURL
        }
        components.queryItems = query.isEmpty ? nil : query
        guard let url = components.url else { throw MobileServerError.invalidURL }
        var request = URLRequest(url: url)
        request.setValue("Bearer \(connection.token)", forHTTPHeaderField: "Authorization")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        request.timeoutInterval = 120
        let (data, response) = try await session.data(for: request)
        guard let http = response as? HTTPURLResponse else { throw MobileServerError.invalidResponse }
        guard (200..<300).contains(http.statusCode) else {
            let error = try? JSONDecoder().decode(ErrorResponse.self, from: data)
            throw MobileServerError.server(http.statusCode, error?.error ?? "Unknown error")
        }
        return data
    }

    private func orderedUnique(_ values: [String]) -> [String] {
        var seen: Set<String> = []
        return values.filter { seen.insert($0).inserted }
    }

    private struct SnapshotResponse: Decodable {
        struct Record: Decodable {
            let path: String
            let content: String
            let modifiedAt: Date

            enum CodingKeys: String, CodingKey { case path, content, modifiedAt = "modified_at" }
        }
        let notes: [Record]
    }

    private struct ErrorResponse: Decodable { let error: String }
}

/// Result of a conditional `GET /api/v1/snapshot`: either the vault has not
/// changed since the ETag the caller sent (`.unchanged`), or a fresh
/// snapshot arrived along with its new ETag for the next conditional call.
enum MobileSnapshotResult: Sendable {
    case unchanged
    case updated(MobileVaultSnapshot, etag: String?)
}
