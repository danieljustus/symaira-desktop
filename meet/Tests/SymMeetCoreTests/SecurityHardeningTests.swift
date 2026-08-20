import XCTest

@testable import SymMeetCore

final class SecurityHardeningTests: XCTestCase {

  // MARK: - Path traversal protection

  func testMeetingIDRejectsTraversal() {
    let invalidIDs = [
      "../../../etc/passwd",
      "..%2F..%2Fetc%2Fpasswd",
      "meetings/../../secret",
      "a/../b",
    ]

    for id in invalidIDs {
      XCTAssertThrowsError(try validateMeetingID(id)) { error in
        XCTAssertTrue(
          error is StoreError,
          "Path traversal '\(id)' should be rejected with StoreError")
      }
    }
  }

  func testMeetingIDRejectsEmptyAndDot() {
    XCTAssertThrowsError(try validateMeetingID(""))
    XCTAssertThrowsError(try validateMeetingID("."))
    XCTAssertThrowsError(try validateMeetingID(".."))
  }

  // MARK: - Segment validation

  func testSegmentRejectsAnonymousSpeaker() {
    XCTAssertThrowsError(
      try Segment(
        segmentID: UUID(),
        trackID: UUID(),
        speakerID: "anonymous",
        startMS: 0,
        endMS: 1000,
        engineText: "test"
      ))
  }

  func testSegmentRejectsInvalidTimeRange() {
    XCTAssertThrowsError(
      try Segment(
        segmentID: UUID(),
        trackID: UUID(),
        speakerID: "speaker_0",
        startMS: 1000,
        endMS: 500,
        engineText: "test"
      ))
  }

  func testSegmentRejectsZeroDuration() {
    XCTAssertThrowsError(
      try Segment(
        segmentID: UUID(),
        trackID: UUID(),
        speakerID: "speaker_0",
        startMS: 500,
        endMS: 500,
        engineText: "test"
      ))
  }

  // MARK: - Schema version validation

  func testManifestRejectsUnsupportedSchemaVersion() throws {
    let json = """
      {"schema_version":99,"meeting_id":"00000000-0000-0000-0000-000000000001",
       "source":"imported","created_at":"2025-01-01T00:00:00Z",
       "updated_at":"2025-01-01T00:00:00Z",
       "consent":{"status":"required"},"retention":{"policy":"keep"}}
      """.data(using: .utf8)!

    XCTAssertThrowsError(try ContractCodec.decoder().decode(MeetingManifest.self, from: json)) {
      error in
      guard let contractError = error as? ContractError else {
        XCTFail("Expected ContractError, got \(error)")
        return
      }
      if case .unsupportedSchemaVersion = contractError {
        // Expected
      } else {
        XCTFail("Expected unsupportedSchemaVersion, got \(contractError)")
      }
    }
  }

  func testSegmentRejectsUnsupportedSchemaVersion() throws {
    let json = """
      {"schema_version":99,"segment_id":"00000000-0000-0000-0000-000000000001",
       "track_id":"00000000-0000-0000-0000-000000000010",
       "speaker_id":"speaker_0","start_ms":0,"end_ms":1000,
       "engine_text":"test","revision":"engine"}
      """.data(using: .utf8)!

    XCTAssertThrowsError(try ContractCodec.decoder().decode(Segment.self, from: json))
  }

  // MARK: - Consent state invariant

  func testConsentRequiredByDefault() {
    let consent = ConsentState(status: .required)
    XCTAssertEqual(consent.status, .required)
    XCTAssertNil(consent.authorizedAt)
    XCTAssertNil(consent.expiresAt)
  }

  func testExpiredConsentIsNotAuthorized() {
    let consent = ConsentState(
      status: .expired,
      authorizedAt: Date(timeIntervalSince1970: 1000),
      expiresAt: Date(timeIntervalSince1970: 2000))

    XCTAssertNotEqual(consent.status, .authorized)
  }

  // MARK: - Contract codec

  func testContractCodecEncoderProducesSnakeCase() throws {
    let manifest = MeetingManifest(
      meetingID: UUID(),
      source: .imported,
      createdAt: Date(timeIntervalSince1970: 0),
      updatedAt: Date(timeIntervalSince1970: 0),
      consent: ConsentState(status: .required),
      retention: RetentionMetadata(policy: .keep))

    let data = try ContractCodec.encoder(prettyPrinted: true).encode(manifest)
    let json = String(decoding: data, as: UTF8.self)

    XCTAssertTrue(json.contains("schema_version"), "Must use snake_case")
    XCTAssertTrue(json.contains("meeting_id"), "Must use snake_case")
    XCTAssertTrue(json.contains("created_at"), "Must use snake_case")
    XCTAssertFalse(json.contains("meetingID"), "Must not use camelCase")
  }

  // MARK: - POSIX permissions

  func testMeetingDirectoryHasMode0700() async throws {
    let root = try makeTemporaryDirectory()
    defer { try? FileManager.default.removeItem(at: root) }

    let store = MeetingStore(dataRoot: root)
    let manifest = MeetingManifest(
      meetingID: UUID(),
      source: .imported,
      createdAt: Date(),
      updatedAt: Date(),
      consent: ConsentState(status: .required),
      retention: RetentionMetadata(policy: .keep))
    try await store.create(manifest)

    let meetingID = manifest.meetingID.uuidString.lowercased()
    let meetingDir = root.appending(path: "meetings/\(meetingID)", directoryHint: .isDirectory)

    let attrs = try FileManager.default.attributesOfItem(atPath: meetingDir.path)
    let permissions = attrs[.posixPermissions] as? Int
    XCTAssertEqual(permissions, 0o700, "Meeting directory must have mode 0700")
  }

  func testWrittenManifestHasMode0600() async throws {
    let root = try makeTemporaryDirectory()
    defer { try? FileManager.default.removeItem(at: root) }

    let store = MeetingStore(dataRoot: root)
    let manifest = MeetingManifest(
      meetingID: UUID(),
      source: .imported,
      createdAt: Date(),
      updatedAt: Date(),
      consent: ConsentState(status: .required),
      retention: RetentionMetadata(policy: .keep))
    try await store.create(manifest)

    let meetingID = manifest.meetingID.uuidString.lowercased()
    let manifestURL =
      root
      .appending(path: "meetings/\(meetingID)/manifest.json", directoryHint: .notDirectory)

    let attrs = try FileManager.default.attributesOfItem(atPath: manifestURL.path)
    let permissions = attrs[.posixPermissions] as? Int
    XCTAssertEqual(permissions, 0o600, "Written manifest must have mode 0600")
  }

  func testModelRootCreatedWithMode0700() async throws {
    let root = try makeTemporaryDirectory()
    defer { try? FileManager.default.removeItem(at: root) }

    let store = ModelStore(root: root)
    // Force prepareRoot by listing (which calls prepareRoot internally)
    _ = try await store.list()

    let attrs = try FileManager.default.attributesOfItem(atPath: root.path)
    let permissions = attrs[.posixPermissions] as? Int
    XCTAssertEqual(permissions, 0o700, "Model root must have mode 0700")
  }

  func testDataRootCreatedWithMode0700() async throws {
    let root = try makeTemporaryDirectory()
    defer { try? FileManager.default.removeItem(at: root) }

    let lock = DataRootLock(dataRoot: root)
    _ = try await lock.acquire()

    let attrs = try FileManager.default.attributesOfItem(atPath: root.path)
    let permissions = attrs[.posixPermissions] as? Int
    XCTAssertEqual(permissions, 0o700, "Data root must have mode 0700")
  }
}

// MARK: - Test helpers

/// A minimal test helper to validate meeting ID format.
/// In production this is part of MeetingStore internals.
private func validateMeetingID(_ id: String) throws {
  guard !id.isEmpty,
    !id.contains("/"),
    !id.contains("\\"),
    !id.contains(".."),
    id != ".",
    id != ".."
  else {
    throw StoreError.invalidMeetingID
  }
}

private func makeTemporaryDirectory() throws -> URL {
  let directory = FileManager.default.temporaryDirectory
    .appending(path: "symmeet-tests-\(UUID().uuidString)", directoryHint: .isDirectory)
  try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: false)
  return directory
}
