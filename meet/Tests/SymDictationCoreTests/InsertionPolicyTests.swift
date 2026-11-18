import XCTest

@testable import SymDictationCore

final class InsertionPolicyTests: XCTestCase {
  private let ok = InsertionConditions(secureInputActive: false, accessibilityTrusted: true)

  func testNativeAppPastes() {
    let target = InsertionTarget(bundleID: "com.apple.TextEdit", focus: .textInput)
    XCTAssertEqual(InsertionPolicy().plan(for: target, conditions: ok), .insert(.paste))
  }

  func testUnknownBundleAndUnknownFocusStillPastes() {
    XCTAssertEqual(
      InsertionPolicy().plan(for: InsertionTarget(), conditions: ok), .insert(.paste))
  }

  func testChromiumFamilyTypes() {
    for bundleID in [
      "com.google.Chrome", "org.chromium.Chromium", "com.brave.Browser",
      "com.microsoft.edgemac", "company.thebrowser.Browser",
    ] {
      let target = InsertionTarget(bundleID: bundleID, focus: .textInput)
      XCTAssertEqual(
        InsertionPolicy().plan(for: target, conditions: ok), .insert(.type), bundleID)
    }
  }

  func testSafariPastes() {
    let target = InsertionTarget(bundleID: "com.apple.Safari", focus: .textInput)
    XCTAssertEqual(InsertionPolicy().plan(for: target, conditions: ok), .insert(.paste))
  }

  func testElectronAppTypesRegardlessOfBundleID() {
    let target = InsertionTarget(bundleID: "com.example.chatapp", isElectronApp: true)
    XCTAssertEqual(InsertionPolicy().plan(for: target, conditions: ok), .insert(.type))
  }

  func testAlwaysTypeOptionAndExtraBundleIDs() {
    let target = InsertionTarget(bundleID: "com.apple.TextEdit")
    XCTAssertEqual(
      InsertionPolicy(alwaysType: true).plan(for: target, conditions: ok), .insert(.type))
    XCTAssertEqual(
      InsertionPolicy(extraTypingBundleIDs: ["com.apple.TextEdit"])
        .plan(for: target, conditions: ok),
      .insert(.type))
  }

  func testChromiumWithUnknownFocusIsNotMistakenForNoTextField() {
    let target = InsertionTarget(bundleID: "com.google.Chrome", focus: .unknown)
    XCTAssertEqual(InsertionPolicy().plan(for: target, conditions: ok), .insert(.type))
  }

  func testSecureInputAlwaysWinsAndNeverInserts() {
    let secure = InsertionConditions(secureInputActive: true, accessibilityTrusted: true)
    for target in [
      InsertionTarget(bundleID: "com.apple.TextEdit", focus: .textInput),
      InsertionTarget(bundleID: "com.google.Chrome", focus: .textInput),
      InsertionTarget(focus: .notTextInput),
    ] {
      XCTAssertEqual(
        InsertionPolicy(alwaysType: true).plan(for: target, conditions: secure),
        .card(.secureInput))
    }
  }

  func testSecureInputOutranksMissingAccessibility() {
    let both = InsertionConditions(secureInputActive: true, accessibilityTrusted: false)
    XCTAssertEqual(
      InsertionPolicy().plan(for: InsertionTarget(), conditions: both), .card(.secureInput))
  }

  func testMissingAccessibilityKeepsTextInCard() {
    let untrusted = InsertionConditions(secureInputActive: false, accessibilityTrusted: false)
    XCTAssertEqual(
      InsertionPolicy().plan(
        for: InsertionTarget(bundleID: "com.apple.TextEdit", focus: .textInput),
        conditions: untrusted),
      .card(.accessibilityMissing))
  }

  func testNoFocusedTextFieldKeepsTextInCard() {
    let target = InsertionTarget(bundleID: "com.apple.finder", focus: .notTextInput)
    XCTAssertEqual(InsertionPolicy().plan(for: target, conditions: ok), .card(.noTextField))
  }
}
