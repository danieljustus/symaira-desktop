import Foundation

/// Faithful Swift port of the `internal/searchquery` operator grammar used
/// by the desktop CLI, MCP and server. The mobile query field parses the
/// *same* language so a typed `tag:invoice` behaves identically to the
/// filter chips — one semantics, two entry points (shared contract §2 of
/// the wave plan).
///
/// Supported syntax (AND semantics, all entries combined):
///   - `path:substring`  case-insensitive path substring (LIKE %v%)
///   - `filename:substring`  case-insensitive base-name substring (no parent directories)
///   - `filetype:pdf,epub`  case-insensitive extension alternatives
///   - `created:2026-08-01..2026-08-31`  inclusive creation date/range
///   - `modified:last day|week|month|year`  relative modification window
///   - `tag:name`        case-insensitive exact tag match
///   - `type:invoice`    case-insensitive exact document_type match
///   - `status:open`     case-insensitive exact status match
///   - `"a phrase"`      quoted exact phrase
///   - `-term`           negation (applies to the next term/filter)
///   - `/regex/`         regular expression over title + body
///   - bare words        prefix full-text terms
enum MobileSearchQueryParser {

    enum Field: String, CaseIterable {
        case path
        case filename
        case filetype
        case created
        case modified
        case tag
        case type
        case status
    }

    struct Filter: Equatable, Sendable {
        let field: Field
        let value: String
        let negated: Bool
    }

    struct Term: Equatable, Sendable {
        let value: String
        let phrase: Bool
        let negated: Bool
    }

    struct Regex: Equatable, Sendable {
        let pattern: String
        let negated: Bool
    }

    struct Plan: Equatable, Sendable {
        var filters: [Filter] = []
        var terms: [Term] = []
        var regexes: [Regex] = []
    }

    enum ParseError: Error, Equatable {
        case negationWithoutTerm
        case emptyPhrase
        case unterminatedPhrase
        case emptyRegex
        case unterminatedRegex
        case invalidRegex(String)
        case operatorRequiresValue(String)
        case invalidDate(String)
        case unknownOperator(String)
        case expectedTerm
    }

    /// Parses a query. A syntax error is intentionally returned to the
    /// caller, which degrades the whole query to safe plain text — the
    /// same policy as `searchquery.Parse`.
    static func parse(_ input: String) throws -> Plan {
        var plan = Plan()
        let characters = Array(input)
        var i = 0

        func skipSpace() {
            while i < characters.count, characters[i].isWhitespace {
                i += 1
            }
        }

        while true {
            skipSpace()
            if i >= characters.count { return plan }

            var negated = false
            if characters[i] == "-" {
                negated = true
                i += 1
                if i >= characters.count || characters[i].isWhitespace {
                    throw ParseError.negationWithoutTerm
                }
            }

            switch characters[i] {
            case "\"":
                let (value, next) = try quoted(characters, start: i)
                plan.terms.append(Term(value: value, phrase: true, negated: negated))
                i = next
            case "/":
                let (pattern, next) = try regexLiteral(characters, start: i)
                // Validate the pattern now, mirroring the Go port (RE2 →
                // NSRegularExpression semantics; both are bounded).
                do {
                    _ = try NSRegularExpression(pattern: pattern)
                } catch {
                    throw ParseError.invalidRegex(pattern)
                }
                plan.regexes.append(Regex(pattern: pattern, negated: negated))
                i = next
            default:
                let value = bare(characters, start: i)
                if value.isEmpty {
                    throw ParseError.expectedTerm
                }
                var consumed = value.count
                do {
                    if let (field, parsedValue) = try filter(value) {
                        var filterValue = parsedValue
                        if isDateField(field), filterValue.lowercased() == "last" {
                            var unitStart = i + consumed
                            while unitStart < characters.count, characters[unitStart].isWhitespace { unitStart += 1 }
                            let unitStartValue = unitStart
                            while unitStart < characters.count, !characters[unitStart].isWhitespace { unitStart += 1 }
                            let unit = String(characters[unitStartValue..<unitStart]).lowercased()
                            if isDateUnit(unit) {
                                filterValue += " " + unit
                                consumed = unitStart - i
                            }
                        }
                        if isDateField(field) { try validateDateValue(filterValue) }
                        plan.filters.append(Filter(field: field, value: filterValue, negated: negated))
                    } else {
                        plan.terms.append(Term(value: value, phrase: false, negated: negated))
                    }
                } catch let error as ParseError {
                    throw error
                }
                i += consumed
            }

            if i < characters.count, !characters[i].isWhitespace {
                throw ParseError.expectedTerm
            }
        }
    }

    // MARK: - Lexing

    private static func bare(_ characters: [Character], start: Int) -> String {
        var i = start
        while i < characters.count, !characters[i].isWhitespace {
            i += 1
        }
        return String(characters[start..<i])
    }

    private static func quoted(_ characters: [Character], start: Int) throws -> (String, Int) {
        var i = start + 1 // opening quote
        var value = ""
        var escaped = false
        while i < characters.count {
            let ch = characters[i]
            i += 1
            if escaped {
                value.append(ch)
                escaped = false
                continue
            }
            if ch == "\\" {
                escaped = true
                continue
            }
            if ch == "\"" {
                if value.isEmpty {
                    throw ParseError.emptyPhrase
                }
                return (value, i)
            }
            value.append(ch)
        }
        throw ParseError.unterminatedPhrase
    }

    private static func regexLiteral(_ characters: [Character], start: Int) throws -> (String, Int) {
        var i = start + 1 // opening slash
        var pattern = ""
        var escaped = false
        while i < characters.count {
            let ch = characters[i]
            i += 1
            if escaped {
                pattern.append("\\")
                pattern.append(ch)
                escaped = false
                continue
            }
            if ch == "\\" {
                escaped = true
                continue
            }
            if ch == "/" {
                if pattern.isEmpty {
                    throw ParseError.emptyRegex
                }
                return (pattern, i)
            }
            pattern.append(ch)
        }
        throw ParseError.unterminatedRegex
    }

    /// Recognises `field:value` operators. Unknown `word:` tokens with an
    /// identifier-like field name are errors (same as the Go port); other
    /// colons fall through as plain text.
    private static func filter(_ token: String) throws -> (Field, String)? {
        guard let colon = token.firstIndex(of: ":") else { return nil }
        let fieldName = String(token[..<colon])
        let value = String(token[token.index(after: colon)...])
        if value.isEmpty {
            throw ParseError.operatorRequiresValue(fieldName + ":")
        }
        guard let field = Field(rawValue: fieldName.lowercased()) else {
            // Unknown operator only when the field name looks like an
            // identifier (`foo:`); otherwise it is plain text.
            if isIdentifier(fieldName) {
                throw ParseError.unknownOperator(fieldName + ":")
            }
            return nil
        }
        return (field, value)
    }

    private static func isDateField(_ field: Field) -> Bool {
        field == .created || field == .modified
    }

    private static func isDateUnit(_ value: String) -> Bool {
        ["day", "week", "month", "year"].contains(value.lowercased())
    }

    private static func validateDateValue(_ value: String) throws {
        let expressions = value.split(separator: ",", omittingEmptySubsequences: false)
        guard !expressions.isEmpty else { throw ParseError.invalidDate(value) }
        for expression in expressions {
            let expression = String(expression).trimmingCharacters(in: .whitespaces)
            let lower = expression.lowercased()
            if ["last day", "last week", "last month", "last year"].contains(lower) { continue }
            let parts = expression.components(separatedBy: "..")
            guard parts.count <= 2, parts.allSatisfy({ !$0.trimmingCharacters(in: .whitespaces).isEmpty }) else {
                throw ParseError.invalidDate(value)
            }
            for part in parts {
                let trimmed = part.trimmingCharacters(in: .whitespaces)
                if ISO8601DateFormatter().date(from: trimmed) == nil {
                    let dateFormatter = DateFormatter()
                    dateFormatter.calendar = Calendar(identifier: .gregorian)
                    dateFormatter.locale = Locale(identifier: "en_US_POSIX")
                    dateFormatter.timeZone = TimeZone(secondsFromGMT: 0)
                    dateFormatter.dateFormat = "yyyy-MM-dd"
                    guard dateFormatter.date(from: trimmed) != nil else { throw ParseError.invalidDate(value) }
                }
            }
        }
    }

    /// `[[:alpha:]][[:alnum:]_-]*` — the Go port's operator-name shape.
    private static func isIdentifier(_ value: String) -> Bool {
        guard let first = value.first, first.isLetter else { return false }
        return value.dropFirst().allSatisfy { $0.isLetter || $0.isNumber || $0 == "_" || $0 == "-" }
    }
}
