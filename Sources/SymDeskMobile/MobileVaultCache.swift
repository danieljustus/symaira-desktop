import Foundation

/// Lightweight persisted snapshot cache for cache-first launch.
///
/// On a cold start the app shows the last successfully parsed snapshot
/// immediately, then refreshes in the background. Only the fields the
/// workspace list and note rows render are stored; full bodies are not, so
/// the cache stays small and the refresh re-parses nothing it can avoid.
/// The cache is cleared together with the search index on disconnect/reset.
struct MobileVaultCache: Codable {
    var notes: [CachedNote]
    var skippedFiles: Int
    var savedAt: Date

    struct CachedNote: Codable {
        var path: String
        var title: String
        var bodyPreview: String
        var tags: [String]
        var documentType: String
        var status: String
        var dueDate: String
        var modifiedAt: Date
    }

    static let filename = "snapshot-cache.json"

    static func defaultURL() -> URL {
        let base = (try? FileManager.default.url(
            for: .applicationSupportDirectory,
            in: .userDomainMask,
            appropriateFor: nil,
            create: true
        )) ?? FileManager.default.temporaryDirectory
        return base.appendingPathComponent("SymDeskMobile/\(filename)")
    }

    static func load(from url: URL) -> MobileVaultCache? {
        guard let data = try? Data(contentsOf: url) else { return nil }
        let decoder = JSONDecoder()
        decoder.dateDecodingStrategy = .iso8601
        return try? decoder.decode(MobileVaultCache.self, from: data)
    }

    func save(to url: URL) {
        let encoder = JSONEncoder()
        encoder.dateEncodingStrategy = .iso8601
        encoder.outputFormatting = [.sortedKeys]
        guard let data = try? encoder.encode(self) else { return }
        try? FileManager.default.createDirectory(
            at: url.deletingLastPathComponent(),
            withIntermediateDirectories: true
        )
        try? data.write(to: url, options: .atomic)
    }

    static func remove(at url: URL) {
        try? FileManager.default.removeItem(at: url)
    }
}
