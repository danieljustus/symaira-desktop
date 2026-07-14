import CryptoKit
import Foundation
import Security

public enum ServerConnectionError: LocalizedError {
    case invalidURL
    case missingConfiguration
    case invalidResponse
    case server(status: Int, message: String)
    case keychain(OSStatus)

    public var errorDescription: String? {
        switch self {
        case .invalidURL:
            return "Enter a valid http:// or https:// SymDesk Server URL."
        case .missingConfiguration:
            return "No SymDesk Server connection is configured."
        case .invalidResponse:
            return "The server returned an invalid response."
        case .server(let status, let message):
            return "Server error \(status): \(message)"
        case .keychain(let status):
            return "The connection token could not be stored in Keychain (\(status))."
        }
    }
}

public struct ServerConnection: Equatable, Sendable {
    public let url: URL
    public let token: String

    public init(url: URL, token: String) {
        self.url = url
        self.token = token
    }
}

public enum ServerConnectionConfig {
    private static let urlKey = "symdesk.server.url.v1"
    private static let service = "com.symaira.desktop.server"
    private static let account = "self-hosted-token"

    public static var hasConnection: Bool {
        connection() != nil
    }

    public static func connection() -> ServerConnection? {
        guard let rawURL = UserDefaults.standard.string(forKey: urlKey),
              let url = normalizedURL(rawURL),
              let token = try? readToken(), !token.isEmpty else {
            return nil
        }
        return ServerConnection(url: url, token: token)
    }

    public static func save(url rawURL: String, token: String) throws {
        guard let url = normalizedURL(rawURL) else { throw ServerConnectionError.invalidURL }
        let trimmedToken = token.trimmingCharacters(in: .whitespacesAndNewlines)
        guard trimmedToken.count >= 32 else {
            throw ServerConnectionError.server(status: 0, message: "The token must contain at least 32 characters.")
        }
        try writeToken(trimmedToken)
        UserDefaults.standard.set(url.absoluteString, forKey: urlKey)
        VaultConfig.resetLocalVault()
    }

    public static func reset() {
        UserDefaults.standard.removeObject(forKey: urlKey)
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account
        ]
        SecItemDelete(query as CFDictionary)
    }

    public static func normalizedURL(_ value: String) -> URL? {
        let trimmed = value.trimmingCharacters(in: .whitespacesAndNewlines)
        guard var components = URLComponents(string: trimmed),
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
        guard status == errSecSuccess, let data = item as? Data,
              let token = String(data: data, encoding: .utf8) else {
            throw ServerConnectionError.keychain(status)
        }
        return token
    }

    private static func writeToken(_ token: String) throws {
        let data = Data(token.utf8)
        let query: [String: Any] = [
            kSecClass as String: kSecClassGenericPassword,
            kSecAttrService as String: service,
            kSecAttrAccount as String: account
        ]
        let attributes: [String: Any] = [kSecValueData as String: data]
        let updateStatus = SecItemUpdate(query as CFDictionary, attributes as CFDictionary)
        if updateStatus == errSecSuccess { return }
        guard updateStatus == errSecItemNotFound else { throw ServerConnectionError.keychain(updateStatus) }
        var add = query
        add[kSecValueData as String] = data
        let addStatus = SecItemAdd(add as CFDictionary, nil)
        guard addStatus == errSecSuccess else { throw ServerConnectionError.keychain(addStatus) }
    }
}

public final class RemoteDeskClient: @unchecked Sendable {
    private let connection: ServerConnection
    private let session: URLSession

    public init(connection: ServerConnection, session: URLSession = .shared) {
        self.connection = connection
        self.session = session
    }

    public func status() async throws -> DeskStatus {
        let data = try await request(path: "/api/v1/status")
        return try JSONDecoder().decode(DeskStatus.self, from: data)
    }

    public func command(arguments: [String], stdin: String = "") async throws -> Data {
        let payload = try JSONEncoder().encode(CommandRequest(arguments: arguments, stdin: stdin))
        return try await request(path: "/api/v1/command", method: "POST", body: payload, contentType: "application/json")
    }

    public func noteContent(path: String) async throws -> String {
        let data = try await request(path: "/api/v1/files", query: [URLQueryItem(name: "path", value: path)])
        return String(decoding: data, as: UTF8.self)
    }

    public func saveNote(path: String, content: String) async throws {
        _ = try await request(
            path: "/api/v1/files",
            query: [URLQueryItem(name: "path", value: path)],
            method: "PUT",
            body: Data(content.utf8),
            contentType: "text/markdown; charset=utf-8"
        )
    }

    public func jobs() async throws -> Data {
        try await request(path: "/api/v1/jobs")
    }

    public func retryJob(id: String) async throws {
        _ = try await request(path: "/api/v1/jobs/retry", query: [URLQueryItem(name: "id", value: id)], method: "POST")
    }

    public func ingest(fileURL: URL) async throws -> String {
        let boundary = "SymDesk-\(UUID().uuidString)"
        let temporary = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString + ".multipart")
        FileManager.default.createFile(atPath: temporary.path, contents: nil)
        defer { try? FileManager.default.removeItem(at: temporary) }
        let handle = try FileHandle(forWritingTo: temporary)
        defer { try? handle.close() }
        try handle.write(contentsOf: Data("--\(boundary)\r\nContent-Disposition: form-data; name=\"file\"; filename=\"\(escapedFilename(fileURL.lastPathComponent))\"\r\nContent-Type: application/octet-stream\r\n\r\n".utf8))
        let input = try FileHandle(forReadingFrom: fileURL)
        defer { try? input.close() }
        while let chunk = try input.read(upToCount: 1_048_576), !chunk.isEmpty {
            try handle.write(contentsOf: chunk)
        }
        try handle.write(contentsOf: Data("\r\n--\(boundary)--\r\n".utf8))
        try handle.synchronize()

        var request = try makeRequest(path: "/api/v1/ingest", method: "POST")
        request.setValue("multipart/form-data; boundary=\(boundary)", forHTTPHeaderField: "Content-Type")
        let (data, response) = try await session.upload(for: request, fromFile: temporary)
        try validate(response: response, data: data)
        let result = try JSONDecoder().decode(RemoteJob.self, from: data)
        return result.id
    }

    public func cachedFile(path: String) async throws -> URL {
        let cacheRoot = FileManager.default.urls(for: .cachesDirectory, in: .userDomainMask)[0]
            .appendingPathComponent("SymDeskRemote", isDirectory: true)
        try FileManager.default.createDirectory(at: cacheRoot, withIntermediateDirectories: true)
        let digest = SHA256.hash(data: Data(path.utf8)).map { String(format: "%02x", $0) }.joined()
        let output = cacheRoot.appendingPathComponent(digest + "-" + URL(fileURLWithPath: path).lastPathComponent)
        let data = try await request(path: "/api/v1/files", query: [URLQueryItem(name: "path", value: path)])
        try data.write(to: output, options: .atomic)
        return output
    }

    private struct CommandRequest: Codable {
        let arguments: [String]
        let stdin: String
    }

    private struct RemoteJob: Codable {
        let id: String
    }

    private func request(
        path: String,
        query: [URLQueryItem] = [],
        method: String = "GET",
        body: Data? = nil,
        contentType: String? = nil
    ) async throws -> Data {
        var request = try makeRequest(path: path, query: query, method: method)
        request.httpBody = body
        if let contentType { request.setValue(contentType, forHTTPHeaderField: "Content-Type") }
        let (data, response) = try await session.data(for: request)
        try validate(response: response, data: data)
        return data
    }

    private func makeRequest(path: String, query: [URLQueryItem] = [], method: String) throws -> URLRequest {
        guard var components = URLComponents(url: connection.url.appendingPathComponent(path), resolvingAgainstBaseURL: false) else {
            throw ServerConnectionError.invalidURL
        }
        components.queryItems = query.isEmpty ? nil : query
        guard let url = components.url else { throw ServerConnectionError.invalidURL }
        var request = URLRequest(url: url)
        request.httpMethod = method
        request.timeoutInterval = 300
        request.setValue("Bearer \(connection.token)", forHTTPHeaderField: "Authorization")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        return request
    }

    private func validate(response: URLResponse, data: Data) throws {
        guard let response = response as? HTTPURLResponse else { throw ServerConnectionError.invalidResponse }
        guard (200..<300).contains(response.statusCode) else {
            let payload = try? JSONDecoder().decode(ErrorPayload.self, from: data)
            throw ServerConnectionError.server(status: response.statusCode, message: payload?.error ?? "Unknown error")
        }
    }

    private struct ErrorPayload: Codable { let error: String }

    private func escapedFilename(_ value: String) -> String {
        value.replacingOccurrences(of: "\\", with: "_").replacingOccurrences(of: "\"", with: "_")
    }
}
