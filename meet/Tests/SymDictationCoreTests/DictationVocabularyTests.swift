import XCTest

@testable import SymDictationCore

final class DictationVocabularyTests: XCTestCase {
  func testDictionaryParsesItemsAndIgnoresOtherLines() {
    let markdown = """
      # Dictionary

      Some prose that is not a term.
      - Symaira
      * Kubernetes
      + Übergrößenträger
      ---
      - symaira
      ## Heading

      """
    let result = DictationVocabulary.parseDictionary(markdown)
    XCTAssertEqual(result.value, ["Symaira", "Kubernetes", "Übergrößenträger"])
    XCTAssertTrue(result.warnings.isEmpty)
  }

  func testDictionaryEmptyItemWarnsWithLineNumber() {
    let result = DictationVocabulary.parseDictionary("# T\n- a\n-\n- b\n")
    XCTAssertEqual(result.value, ["a", "b"])
    XCTAssertEqual(result.warnings, [DictationParseWarning(line: 3, message: "empty list item")])
  }

  func testDictionaryRoundTrip() {
    let terms = ["Zebra", "Äpfel", "GitHub", "e-mail"]
    let text = DictationVocabulary.serializeDictionary(terms)
    XCTAssertEqual(DictationVocabulary.parseDictionary(text).value, terms)
    XCTAssertEqual(
      DictationVocabulary.serializeDictionary(DictationVocabulary.parseDictionary(text).value), text
    )
  }

  func testCorrectionsGlobalAndScopedSections() {
    let markdown = """
      # Corrections

      - git hub -> GitHub
      - mfg → Mit freundlichen Grüßen

      ## com.apple.mail

      - hallo -> Guten Tag

      ## host:github.com

      - pr -> PR
      """
    let result = DictationVocabulary.parseCorrections(markdown)
    XCTAssertTrue(result.warnings.isEmpty)
    XCTAssertEqual(
      result.value,
      [
        DictationCorrection(wrong: "git hub", right: "GitHub"),
        DictationCorrection(wrong: "mfg", right: "Mit freundlichen Grüßen"),
        DictationCorrection(wrong: "hallo", right: "Guten Tag", scope: .app("com.apple.mail")),
        DictationCorrection(wrong: "pr", right: "PR", scope: .host("github.com")),
      ])
  }

  func testCorrectionsRoundTripIsStable() {
    let markdown = """
      - a -> b
      ## host:x.org
      - c → d
      ## com.x.app
      - e -> f
      ## host:x.org
      - g -> h
      """
    let parsed = DictationVocabulary.parseCorrections(markdown).value
    let serialized = DictationVocabulary.serializeCorrections(parsed)
    let reparsed = DictationVocabulary.parseCorrections(serialized)
    XCTAssertEqual(reparsed.value, parsed)
    XCTAssertTrue(reparsed.warnings.isEmpty)
    XCTAssertEqual(DictationVocabulary.serializeCorrections(reparsed.value), serialized)
  }

  func testMalformedCorrectionLinesBecomeWarnings() {
    let markdown = """
      # Corrections
      - good -> fine
      - no arrow here
      - -> missing wrong
      - missing right ->
      ## 
      - orphan -> skipped
      ## host:
      - also -> skipped
      ## com.ok
      - ok -> OK
      """
    let result = DictationVocabulary.parseCorrections(markdown)
    XCTAssertEqual(
      result.value,
      [
        DictationCorrection(wrong: "good", right: "fine"),
        DictationCorrection(wrong: "ok", right: "OK", scope: .app("com.ok")),
      ])
    XCTAssertEqual(result.warnings.map(\.line), [3, 4, 5, 6, 7, 8, 9])
  }

  func testBrokenSectionNeverFallsBackToGlobal() {
    let result = DictationVocabulary.parseCorrections("## host:\n- a -> b\n")
    XCTAssertTrue(result.value.isEmpty)
    XCTAssertEqual(result.warnings.count, 2)
  }

  func testCombinedParseConcatenatesWarnings() {
    let result = DictationVocabulary.parse(
      dictionaryMarkdown: "-\n", correctionsMarkdown: "- x\n")
    XCTAssertEqual(result.warnings.count, 2)
    XCTAssertEqual(result.value, .empty)
  }

  func testCRLFLineEndings() {
    let result = DictationVocabulary.parseCorrections("- a -> b\r\n- c -> d\r\n")
    XCTAssertEqual(result.value.map(\.wrong), ["a", "c"])
  }
}
