import Foundation

/// A tag with its associated file count, used by the tag browser UI.
struct TagEntry: Identifiable, Equatable {
    var id: String { name }
    let name: String
    let count: Int
}

/// Scans Markdown files in the vault to extract tags from YAML frontmatter.
enum TagParser {

    /// Parse a comma-separated tags value (the format used by frontmatter `tags:`).
    static func parseTagsValue(_ value: String) -> [String] {
        // Handle YAML list format: ["tag1", "tag2"] or [tag1, tag2]
        let trimmed = value.trimmingCharacters(in: .whitespaces)
        if trimmed.hasPrefix("[") && trimmed.hasSuffix("]") {
            let inner = trimmed.dropFirst().dropLast()
            return inner
                .split(separator: ",")
                .map { $0.trimmingCharacters(in: .whitespacesAndNewlines).trimmingCharacters(in: CharacterSet(charactersIn: "\"'")) }
                .filter { !$0.isEmpty }
        }
        // Handle comma-separated format: tag1, tag2, tag3
        return trimmed
            .split(separator: ",")
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty }
    }

    /// Extract tags from a raw Markdown string by scanning YAML frontmatter
    /// for the `tags:` key.
    static func extractTags(from markdown: String) -> [String] {
        let lines = markdown.components(separatedBy: .newlines)
        guard lines.count >= 2, lines[0].trimmingCharacters(in: .whitespaces) == "---" else {
            return []
        }

        var inTags = false
        var tagsLine = ""
        var tagsAccumulator: [String] = []

        for line in lines.dropFirst() {
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            if trimmed == "---" { break }

            if inTags {
                // YAML list continuation: lines starting with "  - " or "  -"
                if trimmed.hasPrefix("- ") || trimmed == "-" || trimmed.hasPrefix("-") {
                    let tag = trimmed.dropFirst().trimmingCharacters(in: .whitespaces)
                    if !tag.isEmpty {
                        tagsAccumulator.append(tag)
                    }
                } else {
                    // Multi-line value continuation or end of tags
                    if !trimmed.isEmpty && !trimmed.hasPrefix("#") {
                        // Could be continuation of the value
                        tagsLine += " \(trimmed)"
                    }
                    inTags = false
                }
                continue
            }

            if trimmed.hasPrefix("tags:") || trimmed.hasPrefix("tags:") {
                let remainder = String(trimmed.dropFirst(5)).trimmingCharacters(in: .whitespaces)
                if remainder.isEmpty || remainder == "|" || remainder == ">" {
                    // Multi-line tags (YAML block scalar)
                    inTags = true
                    tagsAccumulator = []
                } else {
                    tagsLine = remainder
                    break
                }
            }
        }

        if inTags {
            // Multi-line YAML list
            return tagsAccumulator.map { $0.trimmingCharacters(in: CharacterSet(charactersIn: "\"'")) }
        }

        guard !tagsLine.isEmpty else { return [] }
        return parseTagsValue(tagsLine)
    }

    /// Aggregate tags from a list of markdown-strings keyed by path.
    /// Returns tags sorted by count descending, then alphabetically.
    static func aggregate(_ entries: [String: String]) -> [TagEntry] {
        var counts: [String: Int] = [:]
        for (_, markdown) in entries {
            let tags = extractTags(from: markdown)
            for tag in tags {
                let normalized = tag.lowercased().trimmingCharacters(in: .whitespaces)
                guard !normalized.isEmpty else { continue }
                counts[normalized, default: 0] += 1
            }
        }
        return counts
            .map { TagEntry(name: $0.key, count: $0.value) }
            .sorted { $0.count == $1.count ? $0.name < $1.name : $0.count > $1.count }
    }
}
