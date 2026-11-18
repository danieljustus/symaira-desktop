import XCTest

@testable import SymDictationCore

final class DictationVaultLoaderTests: XCTestCase {
  private var vault: URL!

  override func setUpWithError() throws {
    vault = FileManager.default.temporaryDirectory.appending(
      path: "dictation-vault-\(UUID().uuidString)")
    try FileManager.default.createDirectory(at: vault, withIntermediateDirectories: true)
  }

  override func tearDownWithError() throws {
    try? FileManager.default.removeItem(at: vault)
  }

  func testNoVaultYieldsEmptyVocabularyWithoutWarnings() {
    let result = DictationVaultLoader(vaultURL: nil).loadVocabulary()
    XCTAssertEqual(result.value, .empty)
    XCTAssertTrue(result.warnings.isEmpty)
  }

  func testVaultWithoutDictationDirectoryYieldsEmptyVocabulary() {
    let result = DictationVaultLoader(vaultURL: vault).loadVocabulary()
    XCTAssertEqual(result.value, .empty)
    XCTAssertTrue(result.warnings.isEmpty)
  }

  func testLoadsDictionaryAndCorrectionsFromVault() throws {
    let directory = vault.appending(path: ".symdesk/dictation", directoryHint: .isDirectory)
    try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
    try "- Symaira\n- Parakeet\n".write(
      to: directory.appending(path: "dictionary.md"), atomically: true, encoding: .utf8)
    try "- sim eira -> Symaira\n".write(
      to: directory.appending(path: "corrections.md"), atomically: true, encoding: .utf8)

    let vocabulary = DictationVaultLoader(vaultURL: vault).loadVocabulary().value
    XCTAssertEqual(vocabulary.terms, ["Symaira", "Parakeet"])
    XCTAssertEqual(vocabulary.corrections.map(\.right), ["Symaira"])
  }

  func testOnlyOneFilePresentStillWorks() throws {
    let directory = vault.appending(path: ".symdesk/dictation", directoryHint: .isDirectory)
    try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
    try "- Nur Begriff\n".write(
      to: directory.appending(path: "dictionary.md"), atomically: true, encoding: .utf8)
    let vocabulary = DictationVaultLoader(vaultURL: vault).loadVocabulary().value
    XCTAssertEqual(vocabulary.terms, ["Nur Begriff"])
    XCTAssertTrue(vocabulary.corrections.isEmpty)
  }

  func testEnvironmentVariableSelectsVault() {
    XCTAssertEqual(
      DictationVaultLoader(environment: ["SYMDESK_VAULT": vault.path]).vaultURL?.path, vault.path)
    XCTAssertNil(DictationVaultLoader(environment: [:]).vaultURL)
    XCTAssertNil(DictationVaultLoader(environment: ["SYMDESK_VAULT": ""]).vaultURL)
  }
}
