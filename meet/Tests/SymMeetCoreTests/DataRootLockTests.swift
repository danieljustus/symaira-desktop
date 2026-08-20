import Foundation
import XCTest

@testable import SymMeetCore

final class DataRootLockTests: XCTestCase {
  private var root: URL!

  override func setUpWithError() throws {
    root = try makeTemporaryDirectory()
  }

  override func tearDownWithError() throws {
    try? FileManager.default.removeItem(at: root)
  }

  func testConcurrentLockAttemptsOnTheSameDataRootOnlyOneSucceeds() async throws {
    let first = DataRootLock(dataRoot: root)
    let second = DataRootLock(dataRoot: root)

    let handle = try await first.acquire()
    XCTAssertEqual(handle.owner.pid, getpid())

    do {
      _ = try await second.acquire()
      XCTFail("Expected the second lock attempt to fail while the first is held")
    } catch JobError.lockHeld(let ownerPID) {
      XCTAssertEqual(ownerPID, getpid())
    }
  }

  func testReleaseThenReacquireSucceeds() async throws {
    let lock = DataRootLock(dataRoot: root)
    let handle = try await lock.acquire()
    try await lock.release(handle)

    let reacquired = try await lock.acquire()
    XCTAssertEqual(reacquired.owner.pid, getpid())
  }

  func testReleasingAHandleThisInstanceDoesNotHoldThrows() async throws {
    let owner = DataRootLock(dataRoot: root)
    let bystander = DataRootLock(dataRoot: root)
    let handle = try await owner.acquire()

    await assertThrowsJobError(try await bystander.release(handle), .lockNotOwned)
    // The real owner can still release its own lock afterward.
    try await owner.release(handle)
  }

  func testReleasingATamperedHandleAfterOwnRealAcquireThrowsAndDoesNotDeleteTheLiveLock()
    async throws
  {
    let lock = DataRootLock(dataRoot: root)
    let handle = try await lock.acquire()
    let forged = LockHandle(
      lockURL: handle.lockURL,
      owner: LockOwner(
        pid: handle.owner.pid, processStartTime: handle.owner.processStartTime,
        bootTime: handle.owner.bootTime, sessionToken: UUID(), meetingID: nil,
        acquiredAt: handle.owner.acquiredAt))

    await assertThrowsJobError(try await lock.release(forged), .lockNotOwned)
    // The lock file must still be intact and owned by the real handle.
    let diagnosis = await lock.diagnose()
    guard case .liveOwner(let owner) = diagnosis else {
      return XCTFail("Expected the real lock to remain held, got \(diagnosis)")
    }
    XCTAssertEqual(owner.sessionToken, handle.owner.sessionToken)
  }

  func testStaleLockFromADeadPIDIsRecoveredAndBroken() async throws {
    let lock = DataRootLock(dataRoot: root)
    try writeRawOwner(
      LockOwner(
        pid: Self.almostCertainlyDeadPID, processStartTime: 1, bootTime: 1,
        sessionToken: UUID(), meetingID: nil, acquiredAt: Date()))

    let handle = try await lock.acquire()
    XCTAssertEqual(handle.owner.pid, getpid())
  }

  func testLockFromADifferentBootSessionIsTreatedAsStaleEvenWithALivePID() async throws {
    let lock = DataRootLock(dataRoot: root)
    // Same (live, real) PID as this test process, but a boot time that
    // cannot possibly be current -- simulating a lock left behind by a
    // process from a prior boot whose PID was later reused.
    try writeRawOwner(
      LockOwner(
        pid: getpid(), processStartTime: LockOwnership.processStartTime(for: getpid()) ?? 0,
        bootTime: 1, sessionToken: UUID(), meetingID: nil, acquiredAt: Date()))

    let handle = try await lock.acquire()
    XCTAssertEqual(handle.owner.pid, getpid())
  }

  func testLiveButReusedPIDWithMismatchedStartTimeIsTreatedAsStale() async throws {
    let lock = DataRootLock(dataRoot: root)
    let realBootTime = LockOwnership.hostBootTime() ?? 0
    // Alive PID (this test process), correct boot time, but a start time
    // that does not match this process's real start time -- simulating a
    // stale record whose PID number has since been reused by an unrelated
    // process. Verification must not accept the PID number alone.
    try writeRawOwner(
      LockOwner(
        pid: getpid(), processStartTime: 1, bootTime: realBootTime, sessionToken: UUID(),
        meetingID: nil, acquiredAt: Date()))

    let handle = try await lock.acquire()
    XCTAssertEqual(handle.owner.pid, getpid())
  }

  func testAcquireNeverAssumesStaleFromFileAgeAlone() async throws {
    let lock = DataRootLock(dataRoot: root)
    let handle = try await lock.acquire()
    // Back-date the lock file's modification time far into the past; a
    // correct implementation must still refuse to steal a live lock.
    try FileManager.default.setAttributes(
      [.modificationDate: Date(timeIntervalSinceNow: -1_000_000)], ofItemAtPath: handle.lockURL.path
    )

    let bystander = DataRootLock(dataRoot: root)
    await assertThrowsJobError(try await bystander.acquire(), .lockHeld(ownerPID: getpid()))
  }

  func testAcquireRefusesToClearAnUndecodableLockFile() async throws {
    let lock = DataRootLock(dataRoot: root)
    try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
    try Data("not valid json".utf8).write(to: root.appending(path: JobLayout.lockFile))

    await assertThrowsJobError(try await lock.acquire(), .operationFailed)
  }

  func testDiagnoseReportsUnlockedLiveStaleAndCorrupt() async throws {
    let lock = DataRootLock(dataRoot: root)

    var diagnosis = await lock.diagnose()
    XCTAssertEqual(diagnosis, .unlocked)

    let handle = try await lock.acquire()
    diagnosis = await lock.diagnose()
    guard case .liveOwner(let liveOwner) = diagnosis else {
      return XCTFail("Expected a live owner, got \(diagnosis)")
    }
    XCTAssertEqual(liveOwner.pid, getpid())
    try await lock.release(handle)

    try writeRawOwner(
      LockOwner(
        pid: Self.almostCertainlyDeadPID, processStartTime: 1, bootTime: 1, sessionToken: UUID(),
        meetingID: nil, acquiredAt: Date()))
    diagnosis = await lock.diagnose()
    guard case .staleOwner = diagnosis else {
      return XCTFail("Expected a stale owner, got \(diagnosis)")
    }

    try Data("garbage".utf8).write(to: root.appending(path: JobLayout.lockFile))
    diagnosis = await lock.diagnose()
    XCTAssertEqual(diagnosis, .corrupt)
  }

  func testCurrentOwnerReflectsTheRecordedMeetingID() async throws {
    let lock = DataRootLock(dataRoot: root)
    let meetingID = UUID()
    let handle = try await lock.acquire(meetingID: meetingID)
    let owner = try await lock.currentOwner()
    // `acquiredAt` round-trips through second-precision ISO 8601 JSON, so it
    // is compared separately with a tolerance instead of via `==`.
    XCTAssertEqual(owner?.pid, handle.owner.pid)
    XCTAssertEqual(owner?.sessionToken, handle.owner.sessionToken)
    XCTAssertEqual(owner?.meetingID, meetingID)
    let acquiredAtDelta = abs(
      (owner?.acquiredAt ?? .distantPast).timeIntervalSince(handle.owner.acquiredAt))
    XCTAssertLessThan(acquiredAtDelta, 1)
  }

  // MARK: Helpers

  /// A PID chosen from the high end of the 32-bit PID space, which macOS
  /// does not assign (its PID space tops out far below `Int32.max`), so
  /// `kill(pid, 0)` reliably reports it as dead without racing a real process.
  private static let almostCertainlyDeadPID: Int32 = 2_000_000_000

  private func writeRawOwner(_ owner: LockOwner) throws {
    try FileManager.default.createDirectory(at: root, withIntermediateDirectories: true)
    let data = try ContractCodec.encoder().encode(owner)
    try data.write(to: root.appending(path: JobLayout.lockFile))
  }
}

private func makeTemporaryDirectory() throws -> URL {
  let directory = FileManager.default.temporaryDirectory.appending(
    path: "symmeet-jobs-\(UUID().uuidString)")
  try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
  return directory
}

private func assertThrowsJobError<T>(
  _ expression: @autoclosure () async throws -> T,
  _ expected: JobError,
  file: StaticString = #filePath,
  line: UInt = #line
) async {
  do {
    _ = try await expression()
    XCTFail("Expected \(expected) to be thrown", file: file, line: line)
  } catch let error as JobError {
    XCTAssertEqual(error, expected, file: file, line: line)
  } catch {
    XCTFail("Expected JobError.\(expected), got \(error)", file: file, line: line)
  }
}
