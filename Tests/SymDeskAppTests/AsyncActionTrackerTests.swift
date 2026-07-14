import XCTest
@testable import SymDesk

private struct DummyError: Error, LocalizedError {
    var errorDescription: String? { "dummy failure" }
}

@MainActor
final class AsyncActionTrackerTests: XCTestCase {
    // Mirrors a failed destructive action (e.g. deleting a view): the
    // failure must be visible and the id must remain usable afterward
    // instead of the caller advancing local state as if it succeeded.
    func testFailedDestructiveActionRecordsFailureAndClearsInFlight() async {
        let tracker = AsyncActionTracker<String>()

        let succeeded = await tracker.run("view-1") {
            throw DummyError()
        }

        XCTAssertFalse(succeeded)
        XCTAssertEqual(tracker.failureMessage(for: "view-1"), "dummy failure")
        XCTAssertFalse(tracker.isInFlight("view-1"))
    }

    func testSuccessfulActionClearsAnyPriorFailure() async {
        let tracker = AsyncActionTracker<String>()
        _ = await tracker.run("view-1") { throw DummyError() }
        XCTAssertNotNil(tracker.failureMessage(for: "view-1"))

        let succeeded = await tracker.run("view-1") {}

        XCTAssertTrue(succeeded)
        XCTAssertNil(tracker.failureMessage(for: "view-1"))
        XCTAssertFalse(tracker.isInFlight("view-1"))
    }

    // Mirrors a failed recoverable action (e.g. queue retry): a second
    // attempt for the same id after a failure must be allowed to run and
    // can succeed, proving the item stays retryable.
    func testRecoverableActionCanBeRetriedAfterFailure() async {
        let tracker = AsyncActionTracker<String>()
        var attempt = 0

        let first = await tracker.run("job-1") {
            attempt += 1
            throw DummyError()
        }
        XCTAssertFalse(first)
        XCTAssertEqual(tracker.failureMessage(for: "job-1"), "dummy failure")

        let second = await tracker.run("job-1") {
            attempt += 1
        }

        XCTAssertTrue(second)
        XCTAssertEqual(attempt, 2)
        XCTAssertNil(tracker.failureMessage(for: "job-1"))
    }

    func testConcurrentRunForSameIDIsDeduplicated() async {
        let tracker = AsyncActionTracker<String>()
        tracker.testMarkInFlight("job-1")

        let ran = await tracker.run("job-1") {
            XCTFail("operation should not run while already in flight")
        }

        XCTAssertFalse(ran)
    }

    func testDifferentIDsTrackIndependently() async {
        let tracker = AsyncActionTracker<String>()

        _ = await tracker.run("a") { throw DummyError() }
        _ = await tracker.run("b") {}

        XCTAssertNotNil(tracker.failureMessage(for: "a"))
        XCTAssertNil(tracker.failureMessage(for: "b"))
    }
}
