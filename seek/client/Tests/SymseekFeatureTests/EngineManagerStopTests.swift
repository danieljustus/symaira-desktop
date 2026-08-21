import Foundation
import XCTest
@testable import SymseekFeature

/// Issue #306: "Pressing Stop Server on the Dashboard deadlocks the app and
/// it must be force-quit".  The Dashboard Stop button used to call
/// EngineManager.stop() synchronously on the main actor, which reached
/// DaemonSupervisor.stopInternal() and blocked the main thread on a mutex
/// that was never released.
///
/// The fix routes the supervisor stop path through a dedicated background
/// queue (EngineManager.stopQueue) and awaits it, so the main actor is never
/// blocked.  These tests verify stop() returns promptly and leaves the
/// manager in a sane state even when there is no daemon process — they need
/// no real symseek binary.
@MainActor
final class EngineManagerStopTests: XCTestCase {

    /// A fresh EngineManager has no daemon process.  stop() must still
    /// complete promptly — if the supervisor stop path ever ran on the main
    /// actor and blocked on a lock, this test would hang (XCTest would
    /// time out) instead of returning.
    func testStopOnNonRunningManagerReturnsWithoutBlocking() async {
        let manager = EngineManager()
        XCTAssertFalse(manager.isRunning, "fresh manager must not be running")

        let start = Date()
        await manager.stop()
        let elapsed = Date().timeIntervalSince(start)

        XCTAssertLessThan(elapsed, 2.0,
                          "stop() must return promptly, never block on a supervisor lock")

        if case .failed(let message) = manager.state {
            XCTFail("stop() on a non-running engine must not fail it: \(message)")
        }
    }

    /// stop() must be idempotent: repeated calls are serialized on the stop
    /// queue and must never wedge or flip the engine into .failed.
    func testRepeatedStopIsIdempotentAndNeverFailsEngine() async {
        let manager = EngineManager()

        await manager.stop()
        await manager.stop()
        await manager.stop()

        if case .failed(let message) = manager.state {
            XCTFail("repeated stop() must not fail the engine: \(message)")
        }
    }
}
