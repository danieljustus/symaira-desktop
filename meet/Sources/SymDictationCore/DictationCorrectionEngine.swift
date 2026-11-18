import Foundation

/// Applies `wrong -> right` corrections to a transcript.
///
/// Matching works on whole words or phrases: a correction for `git hub` must not fire inside
/// `digit hubert`, and `art` must not rewrite `Start`. Boundaries are Unicode-aware (umlauts
/// and `ß` are letters), which a plain `\b` in a non-Unicode pattern gets wrong for German.
enum DictationCorrectionEngine {
  /// Rules in the order they are applied: global, then app-scoped, then host-scoped.
  ///
  /// A host is more specific than an app (one browser app shows many sites), so when several
  /// scopes define the same `wrong` text the most specific one wins and the less specific
  /// duplicates are dropped instead of being applied first and then fighting over the text.
  static func effectiveRules(
    _ corrections: [DictationCorrection],
    for context: DictationContext
  ) -> [DictationCorrection] {
    func key(_ item: DictationCorrection) -> String {
      item.wrong.split(whereSeparator: \.isWhitespace).joined(separator: " ").lowercased()
    }
    var global: [DictationCorrection] = []
    var app: [DictationCorrection] = []
    var host: [DictationCorrection] = []
    for item in corrections {
      switch item.scope {
      case nil: global.append(item)
      case .app?: if item.scope?.matches(context) == true { app.append(item) }
      case .host?: if item.scope?.matches(context) == true { host.append(item) }
      }
    }
    let hostKeys = Set(host.map(key))
    let appKeys = Set(app.map(key)).union(hostKeys)
    return global.filter { !appKeys.contains(key($0)) }
      + app.filter { !hostKeys.contains(key($0)) }
      + host
  }

  /// Applies `rules` in order. Returns the new text and the number of replacements made.
  static func apply(_ rules: [DictationCorrection], to text: String) -> (text: String, count: Int) {
    var current = text
    var total = 0
    for rule in rules {
      let result = replace(rule, in: current)
      current = result.text
      total += result.count
    }
    return (current, total)
  }

  private static func replace(
    _ rule: DictationCorrection,
    in text: String
  ) -> (text: String, count: Int) {
    guard let regex = makeRegex(for: rule.wrong), !text.isEmpty else { return (text, 0) }
    let source = text as NSString
    let matches = regex.matches(in: text, range: NSRange(location: 0, length: source.length))
    guard !matches.isEmpty else { return (text, 0) }
    var output = ""
    var cursor = 0
    for match in matches {
      output += source.substring(
        with: NSRange(location: cursor, length: match.range.location - cursor))
      output += adaptCapitalization(of: rule.right, toMatch: source.substring(with: match.range))
      cursor = match.range.location + match.range.length
    }
    output += source.substring(from: cursor)
    return (output, matches.count)
  }

  /// Sentence-initial words are capitalized by the recognizer; keep that when the replacement
  /// is written in lowercase in the settings file.
  private static func adaptCapitalization(of replacement: String, toMatch matched: String) -> String
  {
    guard let first = matched.first, first.isUppercase,
      let target = replacement.first, target.isLowercase
    else { return replacement }
    return target.uppercased() + replacement.dropFirst()
  }

  private static func makeRegex(for wrong: String) -> NSRegularExpression? {
    let words = wrong.split(whereSeparator: \.isWhitespace).map(String.init)
    guard let firstWord = words.first, let lastWord = words.last else { return nil }
    let wordCharacter = "[\\p{L}\\p{N}_]"
    // Only demand a boundary on sides that end in a word character; a term such as `c++`
    // has no boundary after `+`.
    let before = isWordEdge(firstWord.first) ? "(?<!\(wordCharacter))" : ""
    let after = isWordEdge(lastWord.last) ? "(?!\(wordCharacter))" : ""
    let body = words.map(NSRegularExpression.escapedPattern(for:)).joined(separator: "\\s+")
    return try? NSRegularExpression(pattern: before + body + after, options: [.caseInsensitive])
  }

  private static func isWordEdge(_ character: Character?) -> Bool {
    guard let character else { return false }
    return character.isLetter || character.isNumber || character == "_"
  }
}
