import Foundation

/// Rule-based cleanup that runs after corrections: filler words and whitespace.
///
/// This is the always-on, fully local "polish" stage; anything smarter (a language model)
/// is an opt-in `DictationPostProcessor` that runs after it.
enum DictationTextCleaner {
  private static let wordEdge = "[\\p{L}\\p{N}_'’\\-]"
  private static let fillerRegex = try? NSRegularExpression(
    pattern: "(?<!\(wordEdge))(äh|ähm|öhm|hmm|hm|uh|um)(?!\(wordEdge))(,?)[ \\t]*",
    options: [.caseInsensitive])

  /// Removes standalone German/English hesitation tokens (`äh`, `ähm`, `öhm`, `hm`, `hmm`,
  /// `uh`, `um`) together with one trailing comma. Words that merely contain them
  /// (`Humor`, `Hmmel`, `umgehen`) are untouched.
  ///
  /// `um` is also an everyday German preposition (`um drei Uhr`, `um zu`), so it only counts
  /// as a filler when the speaker set it off with a comma or it ends a sentence. Dropping it
  /// everywhere would corrupt ordinary German; the cost is that an unpunctuated English
  /// "um I think" survives.
  ///
  /// When a removed capitalized filler opened a sentence, the next word is capitalized so that
  /// `Äh, das stimmt` becomes `Das stimmt`.
  static func removeFillers(_ text: String) -> String {
    guard let regex = fillerRegex else { return text }
    let source = text as NSString
    var output = ""
    var cursor = 0
    var capitalizeNext = false
    for match in regex.matches(in: text, range: NSRange(location: 0, length: source.length)) {
      let token = source.substring(with: match.range(at: 1)).lowercased()
      let hasComma = match.range(at: 2).length > 0
      let end = match.range.location + match.range.length
      if token == "um" && !hasComma && !endsSentence(source, from: end) { continue }
      var kept = source.substring(
        with: NSRange(location: cursor, length: match.range.location - cursor))
      if capitalizeNext {
        kept = capitalizingFirstLetter(kept)
        capitalizeNext = false
      }
      output += kept
      cursor = end
      // Only a capitalized filler signals that the engine saw a sentence start.
      let wasCapitalized = source.substring(with: match.range(at: 1)).first?.isUppercase == true
      capitalizeNext = wasCapitalized && startsSentence(output)
    }
    var tail = source.substring(from: cursor)
    if capitalizeNext { tail = capitalizingFirstLetter(tail) }
    return output + tail
  }

  /// Collapses runs of spaces/tabs, removes spaces before `, . ! ? : ;` and around line
  /// breaks, and trims the ends. Line breaks themselves are preserved.
  static func normalizeWhitespace(_ text: String) -> String {
    var result = text
    let steps: [(String, String)] = [
      ("[ \\t]+", " "),
      ("[ \\t]*(\\r\\n|\\n|\\r)[ \\t]*", "$1"),
      (" +([,.!?:;])", "$1"),
    ]
    for (pattern, template) in steps {
      guard let regex = try? NSRegularExpression(pattern: pattern) else { continue }
      result = regex.stringByReplacingMatches(
        in: result, range: NSRange(location: 0, length: (result as NSString).length),
        withTemplate: template)
    }
    return result.trimmingCharacters(in: .whitespacesAndNewlines)
  }

  // MARK: helpers

  /// True when only whitespace and end-of-text, or sentence punctuation, follows `index`.
  private static func endsSentence(_ text: NSString, from index: Int) -> Bool {
    guard index < text.length else { return true }
    let next = text.substring(with: NSRange(location: index, length: 1))
    return ".!?;:".contains(next)
  }

  /// True when `text` is empty/whitespace only or ends in sentence punctuation + spaces.
  private static func startsSentence(_ text: String) -> Bool {
    guard let last = text.last(where: { !$0.isWhitespace }) else { return true }
    return ".!?".contains(last)
  }

  private static func capitalizingFirstLetter(_ text: String) -> String {
    guard let index = text.firstIndex(where: { !$0.isWhitespace }) else { return text }
    let character = text[index]
    guard character.isLetter, character.isLowercase else { return text }
    return String(text[..<index]) + character.uppercased()
      + String(text[text.index(after: index)...])
  }
}
