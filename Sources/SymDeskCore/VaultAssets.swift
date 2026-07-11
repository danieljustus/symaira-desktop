import Foundation

/// Stores pasted or dropped images inside the vault's assets folder and
/// returns the relative Markdown link target. The folder name defaults to
/// `assets` and is configurable via UserDefaults (`symdesk.assetsFolder`).
public struct VaultAssets {

    public static let defaultFolderName = "assets"
    public static let folderDefaultsKey = "symdesk.assetsFolder"

    /// The configured assets folder name (relative to the vault root).
    /// Rejects absolute paths and traversal so the folder stays inside the vault.
    public static func folderName(defaults: UserDefaults = .standard) -> String {
        guard let raw = defaults.string(forKey: folderDefaultsKey)?
            .trimmingCharacters(in: .whitespaces), !raw.isEmpty else {
            return defaultFolderName
        }
        let cleaned = raw.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        guard !cleaned.isEmpty, !cleaned.contains(".."), !raw.hasPrefix("/") else {
            return defaultFolderName
        }
        return cleaned
    }

    /// Builds a collision-safe file name: `base.ext`, then `base-2.ext`, ...
    /// `exists` reports whether a candidate name is already taken.
    public static func collisionSafeName(base: String, ext: String, exists: (String) -> Bool) -> String {
        let sanitizedBase = sanitize(base)
        var candidate = "\(sanitizedBase).\(ext)"
        var counter = 2
        while exists(candidate) {
            candidate = "\(sanitizedBase)-\(counter).\(ext)"
            counter += 1
        }
        return candidate
    }

    /// Replaces path separators and control characters in a file-name stem.
    static func sanitize(_ name: String) -> String {
        let invalid = CharacterSet(charactersIn: "/\\:").union(.controlCharacters).union(.newlines)
        let cleaned = name.components(separatedBy: invalid).joined(separator: "-")
            .trimmingCharacters(in: .whitespaces)
        return cleaned.isEmpty ? "pasted-image" : cleaned
    }

    /// Writes `data` into `<vaultRoot>/<assetsFolder>/` under a collision-safe
    /// name and returns the vault-relative path (for the Markdown link).
    @discardableResult
    public static func store(
        imageData data: Data,
        preferredName: String? = nil,
        fileExtension ext: String,
        vaultRoot: String,
        defaults: UserDefaults = .standard,
        now: Date = Date()
    ) throws -> String {
        let folder = folderName(defaults: defaults)
        let dirURL = URL(fileURLWithPath: vaultRoot).appendingPathComponent(folder, isDirectory: true)
        try FileManager.default.createDirectory(at: dirURL, withIntermediateDirectories: true)

        let base: String
        if let preferredName, !preferredName.isEmpty {
            base = (preferredName as NSString).deletingPathExtension
        } else {
            let fmt = DateFormatter()
            fmt.dateFormat = "yyyy-MM-dd-HHmmss"
            fmt.locale = Locale(identifier: "en_US_POSIX")
            base = "pasted-\(fmt.string(from: now))"
        }

        let name = collisionSafeName(base: base, ext: ext) { candidate in
            FileManager.default.fileExists(atPath: dirURL.appendingPathComponent(candidate).path)
        }
        let fileURL = dirURL.appendingPathComponent(name)
        try data.write(to: fileURL, options: .atomic)
        return "\(folder)/\(name)"
    }

    /// The Markdown snippet to insert for a stored asset.
    public static func markdownLink(for relativePath: String) -> String {
        // Percent-encode spaces so the link works in standard Markdown.
        let encoded = relativePath.replacingOccurrences(of: " ", with: "%20")
        let name = (relativePath as NSString).lastPathComponent
        return "![\(name)](\(encoded))"
    }
}
