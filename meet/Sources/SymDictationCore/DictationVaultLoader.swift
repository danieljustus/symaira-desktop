import Foundation

/// Reads the shared dictation vocabulary from a document vault.
///
/// Files live at `<vault>/.symdesk/dictation/{dictionary,corrections}.md`. Everything about
/// this is optional: no vault, no directory or an unreadable file simply yields the empty
/// vocabulary. A missing vocabulary is never an error; dictation works without it.
///
/// `styles.md` is not read here because `SymDictationCore` has no parser for it yet.
public struct DictationVaultLoader: Sendable {
  public static let relativeDirectory = ".symdesk/dictation"

  public let vaultURL: URL?

  public init(vaultURL: URL?) {
    self.vaultURL = vaultURL
  }

  /// The vault configured through `SYMDESK_VAULT`, if any.
  public init(environment: [String: String]) {
    if let path = environment["SYMDESK_VAULT"], !path.isEmpty {
      self.vaultURL = URL(fileURLWithPath: (path as NSString).expandingTildeInPath)
    } else {
      self.vaultURL = nil
    }
  }

  public func loadVocabulary() -> DictationParseResult<DictationVocabulary> {
    guard let vaultURL else { return DictationParseResult(value: .empty) }
    let directory = vaultURL.appending(path: Self.relativeDirectory, directoryHint: .isDirectory)
    return DictationVocabulary.parse(
      dictionaryMarkdown: read("dictionary.md", in: directory),
      correctionsMarkdown: read("corrections.md", in: directory))
  }

  private func read(_ name: String, in directory: URL) -> String {
    (try? String(contentsOf: directory.appending(path: name), encoding: .utf8)) ?? ""
  }
}
