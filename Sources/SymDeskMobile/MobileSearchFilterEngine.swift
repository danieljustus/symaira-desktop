import Foundation

/// Applies search plans and UI filter selections to the in-memory note
/// snapshot. The typed-operator path and the filter-chip path funnel
/// through the same predicates, so `tag:invoice` typed and the tag chip
/// selected always produce the same result set (shared contract §2).
///
/// Filter semantics mirror `internal/sidecar`'s `SearchPlan`:
///   - path   → case-insensitive substring (LIKE %v%)
///   - tag    → case-insensitive exact tag match
///   - type   → case-insensitive exact document_type match
///   - status → case-insensitive exact status match
/// Terms are AND-combined prefix matches over the normalized search blob;
/// phrases require the exact (normalized) substring; negation inverts a
/// single term/filter; regexes run over title + body.
enum MobileSearchFilterEngine {

    /// UI-only filters not representable in the operator grammar
    /// (correspondent, date range). `notesOnly`/`documentsOnly` are the
    /// existing library toggles.
    struct UIFilters: Equatable, Sendable {
        var tags: [String] = []
        var correspondents: [String] = []
        var documentTypes: [String] = []
        var dateRange: ClosedRange<String>? = nil  // ISO yyyy-MM-dd
        var notesOnly = false
        var documentsOnly = false

        static let none = UIFilters()
    }

    /// Distinct filter values derived from what the vault actually
    /// contains (not a hardcoded list).
    struct Facets: Equatable, Sendable {
        var tags: [String] = []
        var correspondents: [String] = []
        var documentTypes: [String] = []
        var statuses: [String] = []
    }

    // MARK: - Facets

    static func facets(of notes: [MobileNote]) -> Facets {
        var result = Facets()
        for note in notes {
            result.tags.append(contentsOf: note.tags)
            if !note.correspondent.isEmpty { result.correspondents.append(note.correspondent) }
            if !note.documentType.isEmpty { result.documentTypes.append(note.documentType) }
            if !note.status.isEmpty { result.statuses.append(note.status) }
        }
        result.tags = orderedUnique(result.tags)
        result.correspondents = orderedUnique(result.correspondents)
        result.documentTypes = orderedUnique(result.documentTypes)
        result.statuses = orderedUnique(result.statuses)
        return result
    }

    // MARK: - Matching

    /// Applies a parsed operator plan plus the UI filter selection.
    /// Everything is AND-combined.
    static func filter(
        _ notes: [MobileNote],
        plan: MobileSearchQueryParser.Plan,
        ui: UIFilters
    ) -> [MobileNote] {
        notes.filter { note in
            matches(note, plan: plan, ui: ui)
        }
    }

    static func matches(
        _ note: MobileNote,
        plan: MobileSearchQueryParser.Plan,
        ui: UIFilters
    ) -> Bool {
        for filter in plan.filters {
            if !matchesOperatorFilter(note, filter) { return false }
        }
        for term in plan.terms {
            if !matchesTerm(note, term) { return false }
        }
        for regex in plan.regexes {
            if !matchesRegex(note, regex) { return false }
        }
        for tag in ui.tags where !note.tags.contains(where: { $0.caseInsensitiveCompare(tag) == .orderedSame }) {
            return false
        }
        if !ui.correspondents.isEmpty,
           !ui.correspondents.contains(where: { $0.caseInsensitiveCompare(note.correspondent) == .orderedSame }) {
            return false
        }
        if !ui.documentTypes.isEmpty,
           !ui.documentTypes.contains(where: { $0.caseInsensitiveCompare(note.documentType) == .orderedSame }) {
            return false
        }
        if let range = ui.dateRange {
            let date = note.documentDate.isEmpty ? note.created : note.documentDate
            let day = String(date.prefix(10))
            guard day >= range.lowerBound, day <= range.upperBound else { return false }
        }
        if ui.notesOnly && note.isDocument { return false }
        if ui.documentsOnly && !note.isDocument { return false }
        return true
    }

    // MARK: - Predicates

    private static func matchesOperatorFilter(_ note: MobileNote, _ filter: MobileSearchQueryParser.Filter) -> Bool {
        let matched: Bool
        switch filter.field {
        case .path:
            matched = anyFilterValue(filter.value) { note.path.lowercased().contains($0.lowercased()) }
        case .filename:
            matched = anyFilterValue(filter.value) { note.filename.lowercased().contains($0.lowercased()) }
        case .filetype:
            let fileType = URL(fileURLWithPath: note.filename).pathExtension.lowercased()
            matched = anyFilterValue(filter.value) { fileType == $0.trimmingCharacters(in: CharacterSet(charactersIn: ".")).lowercased() }
        case .created:
            matched = matchesDateFilter(note.created, expression: filter.value, reference: Date())
        case .modified:
            matched = matchesDateFilter(note.modifiedAt, expression: filter.value, reference: Date())
        case .tag:
            matched = anyFilterValue(filter.value) { wanted in note.tags.contains { $0.caseInsensitiveCompare(wanted) == .orderedSame } }
        case .type:
            matched = anyFilterValue(filter.value) { note.documentType.caseInsensitiveCompare($0) == .orderedSame }
        case .status:
            matched = anyFilterValue(filter.value) { note.status.caseInsensitiveCompare($0) == .orderedSame }
        }
        return filter.negated ? !matched : matched
    }

    /// Bare terms are prefix matches over the normalized search blob
    /// (the FTS `term*` behaviour); phrases are exact normalized matches.
    private static func matchesTerm(_ note: MobileNote, _ term: MobileSearchQueryParser.Term) -> Bool {
        let normalized = MobileVaultParser.normalizedSearchQuery(term.value)
        guard !normalized.isEmpty else { return false }
        let matched: Bool
        if term.phrase {
            matched = note.searchText.contains(normalized)
        } else {
            matched = note.searchText
                .split(whereSeparator: \.isWhitespace)
                .contains { $0.hasPrefix(normalized) }
        }
        return term.negated ? !matched : matched
    }

    private static func matchesRegex(_ note: MobileNote, _ regex: MobileSearchQueryParser.Regex) -> Bool {
        guard let expression = try? NSRegularExpression(pattern: regex.pattern) else { return false }
        let content = note.title + "\n" + note.body
        let range = NSRange(content.startIndex..<content.endIndex, in: content)
        let matched = expression.firstMatch(in: content, range: range) != nil
        return regex.negated ? !matched : matched
    }

    // MARK: - Helpers

    private static func anyFilterValue(_ value: String, matches: (String) -> Bool) -> Bool {
        value.split(separator: ",").contains { part in
            let trimmed = part.trimmingCharacters(in: .whitespaces)
            return !trimmed.isEmpty && matches(trimmed)
        }
    }

    private static func matchesDateFilter(_ raw: String, expression: String, reference: Date) -> Bool {
        guard let date = parseDate(raw) else { return false }
        return expression.split(separator: ",").contains { part in
            guard let range = parseDateRange(String(part), reference: reference) else { return false }
            return range.contains(date)
        }
    }

    private static func matchesDateFilter(_ date: Date, expression: String, reference: Date) -> Bool {
        expression.split(separator: ",").contains { part in
            guard let range = parseDateRange(String(part), reference: reference) else { return false }
            return range.contains(date)
        }
    }

    private static func parseDate(_ raw: String) -> Date? {
        let iso = ISO8601DateFormatter()
        if let date = iso.date(from: raw) { return date }
        let formatter = DateFormatter()
        formatter.calendar = Calendar(identifier: .gregorian)
        formatter.locale = Locale(identifier: "en_US_POSIX")
        formatter.timeZone = TimeZone(secondsFromGMT: 0)
        formatter.dateFormat = "yyyy-MM-dd"
        return formatter.date(from: String(raw.prefix(10)))
    }

    private static func parseDateRange(_ raw: String, reference: Date) -> ClosedRange<Date>? {
        let value = raw.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        let calendar = Calendar(identifier: .gregorian)
        if value.hasPrefix("last ") {
            let component: Calendar.Component
            switch value {
            case "last day": component = .day
            case "last week": component = .weekOfYear
            case "last month": component = .month
            case "last year": component = .year
            default: return nil
            }
            guard let from = calendar.date(byAdding: component, value: -1, to: reference) else { return nil }
            return from...reference
        }
        let parts = value.components(separatedBy: "..")
        guard parts.count <= 2, let from = parseDate(parts[0]) else { return nil }
        let to = parts.count == 2 ? parseDate(parts[1]) : from
        guard let end = to else { return nil }
        let endOfDay = calendar.date(byAdding: .day, value: 1, to: calendar.startOfDay(for: end))?.addingTimeInterval(-0.001) ?? end
        return calendar.startOfDay(for: from)...endOfDay
    }

    private static func orderedUnique(_ values: [String]) -> [String] {
        var seen: Set<String> = []
        return values.filter { seen.insert($0.lowercased()).inserted }
    }
}
