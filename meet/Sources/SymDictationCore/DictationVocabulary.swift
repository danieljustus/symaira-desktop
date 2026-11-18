import Foundation

/// The user's dictation vocabulary: hotwords plus `wrong -> right` corrections.
///
/// Both lists live as Markdown under `<vault>/.symdesk/dictation/` so that Mac and iPhone
/// share one source of truth and the user can edit them in any editor. Parsing is lenient
/// (unknown lines are ignored, malformed items become warnings) and serialization is
/// canonical, so `parse(serialize(x)) == x` holds for values produced by the parser.
public struct DictationVocabulary: Equatable, Sendable {
  /// Hotwords (names, jargon) in file order, unique case-insensitively.
  public var terms: [String]
  /// Corrections, global ones first, then scoped sections in order of first appearance.
  public var corrections: [DictationCorrection]

  public static let empty = DictationVocabulary(terms: [], corrections: [])

  public init(terms: [String] = [], corrections: [DictationCorrection] = []) {
    self.terms = terms
    self.corrections = corrections
  }

  /// Parses both files at once; warnings of the two files are concatenated (dictionary first).
  public static func parse(
    dictionaryMarkdown: String,
    correctionsMarkdown: String
  ) -> DictationParseResult<DictationVocabulary> {
    let dictionary = parseDictionary(dictionaryMarkdown)
    let corrections = parseCorrections(correctionsMarkdown)
    return DictationParseResult(
      value: DictationVocabulary(terms: dictionary.value, corrections: corrections.value),
      warnings: dictionary.warnings + corrections.warnings)
  }

  // MARK: dictionary.md

  /// Every list item is a term; headings, prose and blank lines are ignored.
  public static func parseDictionary(_ markdown: String) -> DictationParseResult<[String]> {
    var terms: [String] = []
    var seen = Set<String>()
    var warnings: [DictationParseWarning] = []
    for (index, line) in lines(of: markdown).enumerated() {
      guard let item = listItemText(line) else { continue }
      guard !item.isEmpty else {
        warnings.append(.init(line: index + 1, message: "empty list item"))
        continue
      }
      if seen.insert(item.lowercased()).inserted { terms.append(item) }
    }
    return DictationParseResult(value: terms, warnings: warnings)
  }

  public static func serializeDictionary(_ terms: [String]) -> String {
    var output = "# Dictionary\n"
    var seen = Set<String>()
    var body: [String] = []
    for term in terms {
      let clean = singleLine(term)
      if !clean.isEmpty, seen.insert(clean.lowercased()).inserted { body.append("- \(clean)") }
    }
    if !body.isEmpty { output += "\n" + body.joined(separator: "\n") + "\n" }
    return output
  }

  // MARK: corrections.md

  /// `- wrong -> right` items (`→` also accepted). Items before the first `## <scope>` heading
  /// are global. Items under an unusable heading are skipped with a warning each, so a broken
  /// heading can never silently turn app-specific rules into global ones.
  public static func parseCorrections(
    _ markdown: String
  ) -> DictationParseResult<[DictationCorrection]> {
    var parsed: [DictationCorrection] = []
    var warnings: [DictationParseWarning] = []
    var scope: DictationScope?
    var scopeIsBroken = false
    for (index, line) in lines(of: markdown).enumerated() {
      let number = index + 1
      if let heading = secondLevelHeading(line) {
        scope = DictationScope(heading: heading)
        scopeIsBroken = scope == nil
        if scopeIsBroken { warnings.append(.init(line: number, message: "empty section scope")) }
        continue
      }
      guard let item = listItemText(line) else { continue }
      if scopeIsBroken {
        warnings.append(.init(line: number, message: "skipped: section has no valid scope"))
        continue
      }
      switch parseCorrectionItem(item) {
      case .success(let pair):
        parsed.append(DictationCorrection(wrong: pair.0, right: pair.1, scope: scope))
      case .failure(let problem):
        warnings.append(.init(line: number, message: problem.message))
      }
    }
    return DictationParseResult(value: canonicalOrder(parsed), warnings: warnings)
  }

  /// Writes global items first, then one `##` section per scope. Words must not themselves
  /// contain `->`, `→` or line breaks (line breaks are flattened to spaces).
  public static func serializeCorrections(_ corrections: [DictationCorrection]) -> String {
    var output = "# Corrections\n"
    var sections: [(scope: DictationScope?, lines: [String])] = []
    for item in canonicalOrder(corrections) {
      let wrong = singleLine(item.wrong)
      let right = singleLine(item.right)
      guard !wrong.isEmpty, !right.isEmpty else { continue }
      let text = "- \(wrong) -> \(right)"
      if let last = sections.indices.last, sections[last].scope == item.scope {
        sections[last].lines.append(text)
      } else {
        sections.append((item.scope, [text]))
      }
    }
    for section in sections {
      output += "\n"
      if let scope = section.scope { output += "## \(scope.headingText)\n\n" }
      output += section.lines.joined(separator: "\n") + "\n"
    }
    return output
  }

  // MARK: helpers

  private struct ItemProblem: Error { let message: String }

  private static func parseCorrectionItem(_ item: String) -> Result<(String, String), ItemProblem> {
    var cut: Range<String.Index>?
    for arrow in ["->", "→"] {
      if let found = item.range(of: arrow), cut == nil || found.lowerBound < cut!.lowerBound {
        cut = found
      }
    }
    guard let cut else {
      return .failure(ItemProblem(message: "missing '->' between wrong and right"))
    }
    let wrong = item[..<cut.lowerBound].trimmingCharacters(in: .whitespaces)
    let right = item[cut.upperBound...].trimmingCharacters(in: .whitespaces)
    if wrong.isEmpty { return .failure(ItemProblem(message: "empty text before '->'")) }
    if right.isEmpty { return .failure(ItemProblem(message: "empty replacement after '->'")) }
    return .success((wrong, right))
  }

  /// Stable partition: global first, then each scope in order of first appearance. Makes the
  /// parsed value canonical so a serialize/parse cycle is exactly the identity.
  private static func canonicalOrder(_ items: [DictationCorrection]) -> [DictationCorrection] {
    var order: [DictationScope?] = [nil]
    for item in items where !order.contains(where: { $0 == item.scope }) {
      order.append(item.scope)
    }
    return order.flatMap { scope in items.filter { $0.scope == scope } }
  }

  private static func lines(of markdown: String) -> [Substring] {
    markdown.split(omittingEmptySubsequences: false, whereSeparator: \.isNewline)
  }

  private static func singleLine(_ text: String) -> String {
    text.split(whereSeparator: \.isNewline).joined(separator: " ")
      .trimmingCharacters(in: .whitespaces)
  }

  /// Text after a `-`, `*` or `+` bullet; `nil` when the line is not a list item.
  private static func listItemText(_ line: Substring) -> String? {
    let trimmed = line.drop(while: { $0 == " " || $0 == "\t" })
    guard let marker = trimmed.first, "-*+".contains(marker) else { return nil }
    let rest = trimmed.dropFirst()
    guard rest.isEmpty || rest.first == " " || rest.first == "\t" else { return nil }
    return rest.trimmingCharacters(in: .whitespaces)
  }

  /// Text of a level-2 heading (`## x`), `nil` for any other line. `#` and `###` are ignored.
  private static func secondLevelHeading(_ line: Substring) -> String? {
    let trimmed = line.drop(while: { $0 == " " })
    guard trimmed.hasPrefix("##"), !trimmed.hasPrefix("###") else { return nil }
    let rest = trimmed.dropFirst(2)
    guard rest.isEmpty || rest.first == " " || rest.first == "\t" else { return nil }
    return rest.trimmingCharacters(in: .whitespaces)
  }
}
