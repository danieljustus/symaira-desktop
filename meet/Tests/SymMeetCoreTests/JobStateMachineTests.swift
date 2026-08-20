import Foundation
import XCTest

@testable import SymMeetCore

final class JobStateMachineTests: XCTestCase {
  /// The exhaustive set of transitions the issue's state diagram allows:
  ///
  ///   queued -> preparing -> transcribing -> exporting -> succeeded
  ///          -> cancelled                (no in-flight write to wait for)
  ///                       -> failed
  ///                       -> cancelling -> cancelled
  ///   failed | cancelled | interrupted -> queued        (explicit retry/resume only)
  ///   preparing | transcribing | exporting | cancelling -> interrupted
  ///                                            (recovery only, never automatic progress)
  private static let expectedAllowed: Set<JobStateMachine.Transition> = [
    .init(.queued, .preparing),
    .init(.queued, .cancelled),

    .init(.preparing, .transcribing),
    .init(.preparing, .failed),
    .init(.preparing, .cancelling),
    .init(.preparing, .interrupted),

    .init(.transcribing, .exporting),
    .init(.transcribing, .failed),
    .init(.transcribing, .cancelling),
    .init(.transcribing, .interrupted),

    .init(.exporting, .succeeded),
    .init(.exporting, .failed),
    .init(.exporting, .cancelling),
    .init(.exporting, .interrupted),

    .init(.cancelling, .cancelled),
    .init(.cancelling, .interrupted),

    .init(.failed, .queued),
    .init(.cancelled, .queued),
    .init(.interrupted, .queued),
  ]

  func testTableDrivenTransitionsCoverEveryAllowedAndForbiddenPair() {
    for from in JobStatus.allCases {
      for to in JobStatus.allCases {
        let transition = JobStateMachine.Transition(from, to)
        let expected = Self.expectedAllowed.contains(transition)
        XCTAssertEqual(
          JobStateMachine.isAllowed(from: from, to: to), expected,
          "expected isAllowed(\(from.rawValue) -> \(to.rawValue)) == \(expected)")
      }
    }
  }

  func testValidateThrowsForEveryForbiddenTransition() {
    for from in JobStatus.allCases {
      for to in JobStatus.allCases {
        let transition = JobStateMachine.Transition(from, to)
        if Self.expectedAllowed.contains(transition) {
          XCTAssertNoThrow(try JobStateMachine.validate(from: from, to: to))
        } else {
          XCTAssertThrowsError(try JobStateMachine.validate(from: from, to: to)) { error in
            XCTAssertEqual(error as? JobError, .invalidTransition(from: from, to: to))
          }
        }
      }
    }
  }

  func testNoStatusHasASelfTransition() {
    for status in JobStatus.allCases {
      XCTAssertFalse(
        JobStateMachine.isAllowed(from: status, to: status),
        "\(status.rawValue) must not transition to itself")
    }
  }

  func testSucceededIsTerminalWithNoOutgoingTransitions() {
    for to in JobStatus.allCases {
      XCTAssertFalse(JobStateMachine.isAllowed(from: .succeeded, to: to))
    }
  }

  func testRetryIsOnlyReachableFromFailedCancelledOrInterrupted() {
    let sources: Set<JobStatus> = [.failed, .cancelled, .interrupted]
    for from in JobStatus.allCases {
      let allowed = JobStateMachine.isAllowed(from: from, to: .queued)
      XCTAssertEqual(allowed, sources.contains(from), "unexpected retry reachability from \(from)")
    }
  }

  func testActiveAndTerminalClassificationMatchesStateDiagram() {
    XCTAssertTrue(JobStatus.preparing.isActive)
    XCTAssertTrue(JobStatus.transcribing.isActive)
    XCTAssertTrue(JobStatus.exporting.isActive)
    XCTAssertTrue(JobStatus.cancelling.isActive)
    XCTAssertFalse(JobStatus.queued.isActive)
    XCTAssertFalse(JobStatus.succeeded.isActive)
    XCTAssertFalse(JobStatus.failed.isActive)
    XCTAssertFalse(JobStatus.cancelled.isActive)
    XCTAssertFalse(JobStatus.interrupted.isActive)

    XCTAssertTrue(JobStatus.succeeded.isTerminal)
    XCTAssertTrue(JobStatus.failed.isTerminal)
    XCTAssertTrue(JobStatus.cancelled.isTerminal)
    XCTAssertTrue(JobStatus.interrupted.isTerminal)
    XCTAssertFalse(JobStatus.queued.isTerminal)
    XCTAssertFalse(JobStatus.preparing.isTerminal)
  }

  func testJobStatusJSONIsSnakeCaseCompatible() throws {
    // JobStatus is already a single lowercase token per case, but this
    // guards against an accidental camelCase raw value creeping in later.
    for status in JobStatus.allCases {
      XCTAssertEqual(status.rawValue, status.rawValue.lowercased())
    }
  }
}
