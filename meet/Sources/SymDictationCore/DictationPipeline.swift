import Foundation

/// What a dictation produced, with enough detail for the UI and for diagnostics.
public struct DictationResult: Equatable, Sendable {
  /// Exactly what the speech engine returned.
  public var rawText: String
  /// After corrections, filler removal and whitespace normalization (no model involved).
  public var correctedText: String
  /// The text to insert: the post-processor's output, or `correctedText` if there is none or
  /// it failed.
  public var finalText: String
  /// Number of individual replacements made by corrections.
  public var appliedCorrections: Int
  /// True when a post-processor was configured but threw, timed out or returned nothing.
  public var postProcessingFailed: Bool

  public init(
    rawText: String,
    correctedText: String,
    finalText: String,
    appliedCorrections: Int,
    postProcessingFailed: Bool
  ) {
    self.rawText = rawText
    self.correctedText = correctedText
    self.finalText = finalText
    self.appliedCorrections = appliedCorrections
    self.postProcessingFailed = postProcessingFailed
  }
}

/// Turns a raw transcript into insertable text.
///
/// Order is fixed: (1) global corrections, (2) scoped corrections (host beats app), (3) filler
/// removal, (4) whitespace normalization, then the optional `DictationPostProcessor`.
/// Corrections run before filler removal so a correction can match the words as spoken, and
/// whitespace runs last because the earlier stages leave gaps.
public struct DictationPipeline: Sendable {
  public var vocabulary: DictationVocabulary
  public var postProcessor: (any DictationPostProcessor)?
  /// Seconds the post-processor may take before the corrected text is used instead.
  public var postProcessingTimeout: TimeInterval

  public init(
    vocabulary: DictationVocabulary = .empty,
    postProcessor: (any DictationPostProcessor)? = nil,
    postProcessingTimeout: TimeInterval = 5
  ) {
    self.vocabulary = vocabulary
    self.postProcessor = postProcessor
    self.postProcessingTimeout = postProcessingTimeout
  }

  /// Steps 1-4 only; synchronous and deterministic.
  public func correct(
    _ rawText: String,
    context: DictationContext = DictationContext()
  ) -> (text: String, appliedCorrections: Int) {
    let rules = DictationCorrectionEngine.effectiveRules(vocabulary.corrections, for: context)
    let corrected = DictationCorrectionEngine.apply(rules, to: rawText)
    let withoutFillers = DictationTextCleaner.removeFillers(corrected.text)
    return (DictationTextCleaner.normalizeWhitespace(withoutFillers), corrected.count)
  }

  /// Full pipeline including the optional post-processor. Never throws: on post-processing
  /// failure the corrected text is returned and `postProcessingFailed` is set.
  public func process(
    _ rawText: String,
    context: DictationContext = DictationContext()
  ) async -> DictationResult {
    let corrected = correct(rawText, context: context)
    var finalText = corrected.text
    var failed = false
    if let postProcessor, !corrected.text.isEmpty {
      if let polished = await DictationPostProcessing.run(
        postProcessor, on: corrected.text, context: context, timeout: postProcessingTimeout)
      {
        finalText = polished
      } else {
        failed = true
      }
    }
    return DictationResult(
      rawText: rawText,
      correctedText: corrected.text,
      finalText: finalText,
      appliedCorrections: corrected.appliedCorrections,
      postProcessingFailed: failed)
  }
}
