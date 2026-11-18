import XCTest

@testable import SymDictationCore

final class DictationPipelineTests: XCTestCase {
  private func pipeline(
    _ corrections: [DictationCorrection] = [],
    postProcessor: (any DictationPostProcessor)? = nil,
    timeout: TimeInterval = 5
  ) -> DictationPipeline {
    DictationPipeline(
      vocabulary: DictationVocabulary(corrections: corrections),
      postProcessor: postProcessor,
      postProcessingTimeout: timeout)
  }

  // MARK: corrections

  func testCorrectionTable() {
    let rules = [
      DictationCorrection(wrong: "git hub", right: "GitHub"),
      DictationCorrection(wrong: "grüße", right: "Grüße"),
      DictationCorrection(wrong: "straße", right: "Straße"),
      DictationCorrection(wrong: "kubernetes", right: "Kubernetes"),
      DictationCorrection(wrong: "mfg", right: "mit freundlichen Grüßen"),
    ]
    let cases: [(input: String, expected: String)] = [
      ("ich nutze git hub heute", "ich nutze GitHub heute"),
      ("Git Hub ist gut", "GitHub ist gut"),
      ("git   hub", "GitHub"),
      ("viele grüße", "viele Grüße"),
      ("die Straße ist lang", "die Straße ist lang"),
      ("die STRASSE", "die Straße"),  // ICU case folding maps ß and SS together
      ("Meine straße", "Meine Straße"),
      ("grüßen und grüße", "grüßen und Grüße"),  // `grüße` must not match inside `grüßen`
      ("mfg", "mit freundlichen Grüßen"),
      ("Mfg Daniel", "Mit freundlichen Grüßen Daniel"),
      ("digit hubert", "digit hubert"),
      ("kubernetes, kubernetes.", "Kubernetes, Kubernetes."),
    ]
    let sut = pipeline(rules)
    for item in cases {
      XCTAssertEqual(sut.correct(item.input).text, item.expected, item.input)
    }
  }

  func testAppliedCorrectionsCountsEveryReplacement() {
    let sut = pipeline([DictationCorrection(wrong: "foo", right: "bar")])
    XCTAssertEqual(sut.correct("foo foo Foo").appliedCorrections, 3)
    XCTAssertEqual(sut.correct("nothing").appliedCorrections, 0)
  }

  func testCapitalizationOfFirstLetterOnlyWhenSourceCapitalized() {
    let sut = pipeline([DictationCorrection(wrong: "hallo", right: "guten tag")])
    XCTAssertEqual(sut.correct("Hallo Welt").text, "Guten tag Welt")
    XCTAssertEqual(sut.correct("sag hallo").text, "sag guten tag")
  }

  func testScopedCorrectionsOnlyApplyInMatchingContext() {
    let rules = [
      DictationCorrection(wrong: "pr", right: "PR", scope: .host("github.com")),
      DictationCorrection(wrong: "hi", right: "Guten Tag", scope: .app("com.apple.mail")),
    ]
    let sut = pipeline(rules)
    XCTAssertEqual(sut.correct("pr hi").text, "pr hi")
    XCTAssertEqual(
      sut.correct("pr hi", context: DictationContext(appBundleID: "com.apple.mail")).text,
      "pr Guten Tag")
    XCTAssertEqual(
      sut.correct(
        "pr hi", context: DictationContext(appBundleID: "COM.APPLE.MAIL", host: "gist.github.com")
      ).text,
      "PR Guten Tag")
    XCTAssertEqual(
      sut.correct("pr", context: DictationContext(host: "notgithub.com")).text, "pr")
  }

  func testHostBeatsAppBeatsGlobal() {
    let rules = [
      DictationCorrection(wrong: "tool", right: "global"),
      DictationCorrection(wrong: "tool", right: "app", scope: .app("com.browser")),
      DictationCorrection(wrong: "tool", right: "host", scope: .host("example.com")),
      DictationCorrection(wrong: "other", right: "O"),
    ]
    let sut = pipeline(rules)
    let both = DictationContext(appBundleID: "com.browser", host: "example.com")
    XCTAssertEqual(sut.correct("tool other", context: both).text, "host O")
    XCTAssertEqual(
      sut.correct("tool", context: DictationContext(appBundleID: "com.browser")).text, "app")
    XCTAssertEqual(sut.correct("tool", context: DictationContext(appBundleID: "x")).text, "global")
  }

  func testRegexMetacharactersInRulesAreLiteral() {
    let sut = pipeline([DictationCorrection(wrong: "c++", right: "C++")])
    XCTAssertEqual(sut.correct("ich mag c++ sehr").text, "ich mag C++ sehr")
  }

  // MARK: fillers and whitespace

  func testFillerRemovalTable() {
    let cases: [(input: String, expected: String)] = [
      ("ich äh glaube", "ich glaube"),
      ("Ich, ähm, glaube das", "Ich, glaube das"),
      ("Äh, das stimmt", "Das stimmt"),
      ("Das ist, öhm, richtig.", "Das ist, richtig."),
      ("hm das weiß ich nicht", "das weiß ich nicht"),
      ("Hmm, okay", "Okay"),
      ("so uh yeah", "so yeah"),
      ("Um, well", "Well"),
      ("ich glaube ähm.", "ich glaube."),
      ("ÄH ÄHM", ""),
      // words that merely contain a filler
      ("Humor ist wichtig", "Humor ist wichtig"),
      ("Hmmel und Hmmm", "Hmmel und Hmmm"),
      ("umgehen Ähre Uhr", "umgehen Ähre Uhr"),
      ("Uh-oh", "Uh-oh"),
      // "um" is a German preposition
      ("um drei Uhr", "um drei Uhr"),
      ("Um zu gewinnen", "Um zu gewinnen"),
    ]
    let sut = pipeline()
    for item in cases {
      XCTAssertEqual(sut.correct(item.input).text, item.expected, item.input)
    }
  }

  func testWhitespaceNormalizationTable() {
    let cases: [(input: String, expected: String)] = [
      ("  a   b  ", "a b"),
      ("hallo , welt .", "hallo, welt."),
      ("was ?  wirklich !", "was? wirklich!"),
      ("liste : a ; b", "liste: a; b"),
      ("zeile eins  \n  zeile zwei", "zeile eins\nzeile zwei"),
      ("tab\t\tgetrennt", "tab getrennt"),
    ]
    let sut = pipeline()
    for item in cases {
      XCTAssertEqual(sut.correct(item.input).text, item.expected, item.input)
    }
  }

  func testCorrectionsRunBeforeFillerRemoval() {
    let sut = pipeline([DictationCorrection(wrong: "äh pi", right: "API")])
    XCTAssertEqual(sut.correct("die äh pi ist da").text, "die API ist da")
  }

  // MARK: post-processing

  private struct Fixed: DictationPostProcessor {
    let output: String
    func process(_ text: String, context: DictationContext) async throws -> String { output }
  }
  private struct Throwing: DictationPostProcessor {
    struct Boom: Error {}
    func process(_ text: String, context: DictationContext) async throws -> String { throw Boom() }
  }
  private struct Slow: DictationPostProcessor {
    func process(_ text: String, context: DictationContext) async throws -> String {
      try await Task.sleep(nanoseconds: 30_000_000_000)
      return "too late"
    }
  }
  private struct Echo: DictationPostProcessor {
    func process(_ text: String, context: DictationContext) async throws -> String {
      "[\(text)|\(context.appBundleID ?? "-")]"
    }
  }

  func testNoPostProcessorFinalEqualsCorrected() async {
    let result = await pipeline([DictationCorrection(wrong: "x", right: "y")]).process("äh x")
    XCTAssertEqual(result.rawText, "äh x")
    XCTAssertEqual(result.correctedText, "y")
    XCTAssertEqual(result.finalText, "y")
    XCTAssertEqual(result.appliedCorrections, 1)
    XCTAssertFalse(result.postProcessingFailed)
  }

  func testPostProcessorOutputIsUsedAndSeesCorrectedText() async {
    let result = await pipeline(postProcessor: Echo()).process(
      "äh hallo", context: DictationContext(appBundleID: "com.x"))
    XCTAssertEqual(result.finalText, "[hallo|com.x]")
    XCTAssertEqual(result.correctedText, "hallo")
    XCTAssertFalse(result.postProcessingFailed)
  }

  func testPostProcessorFailureKeepsCorrectedText() async {
    let result = await pipeline(postProcessor: Throwing()).process("ähm hallo welt")
    XCTAssertEqual(result.finalText, "hallo welt")
    XCTAssertEqual(result.correctedText, "hallo welt")
    XCTAssertTrue(result.postProcessingFailed)
  }

  func testPostProcessorTimeoutKeepsCorrectedText() async {
    let started = Date()
    let result = await pipeline(postProcessor: Slow(), timeout: 0.05).process("hallo")
    XCTAssertEqual(result.finalText, "hallo")
    XCTAssertTrue(result.postProcessingFailed)
    XCTAssertLessThan(Date().timeIntervalSince(started), 5)
  }

  func testEmptyPostProcessorOutputCountsAsFailure() async {
    let result = await pipeline(postProcessor: Fixed(output: "  \n")).process("hallo")
    XCTAssertEqual(result.finalText, "hallo")
    XCTAssertTrue(result.postProcessingFailed)
  }

  func testEmptyTranscriptSkipsPostProcessor() async {
    let result = await pipeline(postProcessor: Throwing()).process("äh")
    XCTAssertEqual(result.finalText, "")
    XCTAssertFalse(result.postProcessingFailed)
  }
}
