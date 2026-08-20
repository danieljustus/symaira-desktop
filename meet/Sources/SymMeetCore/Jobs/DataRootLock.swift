import Darwin
import Foundation

/// The identity recorded by whichever process currently holds a
/// ``DataRootLock``. Liveness is verified from the PID plus the host boot
/// time and the process's own start time, never from file age alone, so a
/// PID that has been reused by an unrelated process is not mistaken for the
/// original owner.
public struct LockOwner: Codable, Equatable, Sendable {
  public let pid: Int32
  public let processStartTime: Int64
  public let bootTime: Int64
  public let sessionToken: UUID
  public let meetingID: UUID?
  public let acquiredAt: Date

  public init(
    pid: Int32,
    processStartTime: Int64,
    bootTime: Int64,
    sessionToken: UUID,
    meetingID: UUID?,
    acquiredAt: Date
  ) {
    self.pid = pid
    self.processStartTime = processStartTime
    self.bootTime = bootTime
    self.sessionToken = sessionToken
    self.meetingID = meetingID
    self.acquiredAt = acquiredAt
  }

  private enum CodingKeys: String, CodingKey {
    case pid
    case processStartTime = "process_start_time"
    case bootTime = "boot_time"
    case sessionToken = "session_token"
    case meetingID = "meeting_id"
    case acquiredAt = "acquired_at"
  }
}

/// A held lock. Only the ``DataRootLock`` instance that produced this handle
/// may release it.
public struct LockHandle: Equatable, Sendable {
  public let lockURL: URL
  public let owner: LockOwner
}

/// A read-only assessment of whatever lock file currently exists, without
/// attempting to claim or clear it.
public enum LockDiagnosis: Equatable, Sendable {
  case unlocked
  case liveOwner(LockOwner)
  case staleOwner(LockOwner)
  /// The lock file exists but cannot be decoded, so liveness cannot be
  /// verified. Callers must not treat this as stale automatically.
  case corrupt
}

/// Process-identity helpers used to stamp and verify ``LockOwner`` values.
/// Every verification defaults to "assume live" when it cannot positively
/// prove the owner is gone -- a lock is only ever judged stale from positive
/// evidence (a dead PID, a reused PID, or a machine reboot), never from
/// absence of evidence.
enum LockOwnership {
  static func currentOwner(meetingID: UUID?, sessionToken: UUID, acquiredAt: Date = Date())
    -> LockOwner
  {
    let pid = getpid()
    return LockOwner(
      pid: pid,
      processStartTime: processStartTime(for: pid) ?? 0,
      bootTime: hostBootTime() ?? 0,
      sessionToken: sessionToken,
      meetingID: meetingID,
      acquiredAt: acquiredAt)
  }

  /// Returns `true` unless there is positive evidence the recorded owner is
  /// gone: the PID is dead, the host has rebooted since the lock was
  /// written, or the PID is alive but belongs to a different process than
  /// the one that wrote the lock (detected via a mismatched start time).
  static func isLive(_ owner: LockOwner) -> Bool {
    guard isProcessAlive(owner.pid) else { return false }
    guard let currentBoot = hostBootTime() else { return true }
    guard currentBoot == owner.bootTime else { return false }
    guard let liveStart = processStartTime(for: owner.pid) else { return true }
    return liveStart == owner.processStartTime
  }

  static func isProcessAlive(_ pid: Int32) -> Bool {
    guard pid > 0 else { return false }
    if kill(pid, 0) == 0 { return true }
    return errno != ESRCH
  }

  static func processStartTime(for pid: Int32) -> Int64? {
    var info = proc_bsdinfo()
    let expected = Int32(MemoryLayout<proc_bsdinfo>.size)
    let written = proc_pidinfo(pid, PROC_PIDTBSDINFO, 0, &info, expected)
    guard written == expected else { return nil }
    return Int64(info.pbi_start_tvsec)
  }

  static func hostBootTime() -> Int64? {
    var mib: [Int32] = [CTL_KERN, KERN_BOOTTIME]
    var boottime = timeval()
    var size = MemoryLayout<timeval>.size
    let result = mib.withUnsafeMutableBufferPointer { pointer -> Int32 in
      sysctl(pointer.baseAddress, 2, &boottime, &size, nil, 0)
    }
    guard result == 0 else { return nil }
    return Int64(boottime.tv_sec)
  }
}

/// An advisory, filesystem-based lock so at most one process at a time
/// mutates job state under a data root. The lock is scoped to the whole
/// data root, not to an individual job: `symmeet` runs one active mutating
/// process per data root by design (see the Jobs subsystem overview).
public actor DataRootLock {
  private static let maxStaleRecoveryAttempts = 5

  public let layout: JobLayout
  private let sessionToken = UUID()
  private var handle: LockHandle?

  public init(dataRoot: URL) {
    layout = JobLayout(dataRoot: dataRoot)
  }

  /// Claims the lock, breaking it first only if the existing owner is
  /// verifiably gone. Throws ``JobError/lockHeld(ownerPID:)`` if a live
  /// owner holds it, or ``JobError/operationFailed`` if the lock file
  /// exists but cannot be decoded (unverifiable, so it is never cleared
  /// automatically).
  @discardableResult
  public func acquire(meetingID: UUID? = nil) throws -> LockHandle {
    try prepareDataRoot()
    let owner = LockOwnership.currentOwner(meetingID: meetingID, sessionToken: sessionToken)

    for _ in 0..<Self.maxStaleRecoveryAttempts {
      switch try createExclusive(owner: owner) {
      case .created:
        let newHandle = LockHandle(lockURL: layout.lockURL, owner: owner)
        handle = newHandle
        return newHandle
      case .alreadyExists:
        let existing: LockOwner?
        do {
          existing = try Self.readOwner(at: layout.lockURL)
        } catch {
          throw JobError.operationFailed
        }
        guard let existing else { continue }
        guard !LockOwnership.isLive(existing) else {
          throw JobError.lockHeld(ownerPID: existing.pid)
        }
        try? FileManager.default.removeItem(at: layout.lockURL)
      }
    }
    throw JobError.operationFailed
  }

  /// Releases a lock previously returned by ``acquire(meetingID:)`` on this
  /// same instance. Never removes a lock file this instance did not create.
  public func release(_ releasedHandle: LockHandle) throws {
    guard let current = handle,
      current.lockURL == releasedHandle.lockURL,
      current.owner.sessionToken == releasedHandle.owner.sessionToken
    else {
      throw JobError.lockNotOwned
    }

    if let onDisk = try? Self.readOwner(at: releasedHandle.lockURL),
      onDisk.sessionToken != releasedHandle.owner.sessionToken
    {
      handle = nil
      throw JobError.lockNotOwned
    }

    try? FileManager.default.removeItem(at: releasedHandle.lockURL)
    handle = nil
  }

  /// Reads the current lock owner, if any, without attempting to claim or
  /// clear anything.
  public func currentOwner() throws -> LockOwner? {
    try Self.readOwner(at: layout.lockURL)
  }

  /// Assesses the current lock file without mutating it.
  public func diagnose() -> LockDiagnosis {
    do {
      guard let owner = try Self.readOwner(at: layout.lockURL) else { return .unlocked }
      return LockOwnership.isLive(owner) ? .liveOwner(owner) : .staleOwner(owner)
    } catch {
      return .corrupt
    }
  }

  private func prepareDataRoot() throws {
    do {
      try FileManager.default.createDirectory(
        at: layout.dataRoot, withIntermediateDirectories: true)
      try FileManager.default.setAttributes(
        [.posixPermissions: 0o700], ofItemAtPath: layout.dataRoot.path)
    } catch {
      throw JobError.operationFailed
    }
  }

  private enum CreationOutcome {
    case created
    case alreadyExists
  }

  private func createExclusive(owner: LockOwner) throws -> CreationOutcome {
    let data = try ContractCodec.encoder().encode(owner)
    let descriptor = open(layout.lockURL.path, O_CREAT | O_EXCL | O_WRONLY, 0o644)
    guard descriptor >= 0 else {
      if errno == EEXIST { return .alreadyExists }
      throw JobError.operationFailed
    }
    defer { close(descriptor) }

    let written = data.withUnsafeBytes { buffer -> Int in
      guard let base = buffer.baseAddress else { return 0 }
      return Darwin.write(descriptor, base, buffer.count)
    }
    guard written == data.count, fsync(descriptor) == 0 else {
      throw JobError.operationFailed
    }
    return .created
  }

  private static func readOwner(at url: URL) throws -> LockOwner? {
    guard FileManager.default.fileExists(atPath: url.path) else { return nil }
    let data = try Data(contentsOf: url)
    return try ContractCodec.decoder().decode(LockOwner.self, from: data)
  }
}
