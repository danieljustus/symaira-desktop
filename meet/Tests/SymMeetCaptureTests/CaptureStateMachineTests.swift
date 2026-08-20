import CoreMedia
import Testing

@testable import SymMeetCapture

@Suite("CaptureStateMachine")
struct CaptureStateMachineTests {

  @Test("initial state is idle")
  func initialStateIsIdle() {
    let machine = CaptureStateMachine()
    #expect(machine.state == .idle)
  }

  @Test("idle → authorizing succeeds")
  func idleToAuthorizing() {
    var machine = CaptureStateMachine()
    let didTransition = machine.transition(to: .authorizing)
    #expect(didTransition)
    #expect(machine.state == .authorizing)
  }

  @Test("authorizing → starting succeeds")
  func authorizingToStarting() {
    var machine = CaptureStateMachine(initial: .authorizing)
    let didTransition = machine.transition(to: .starting)
    #expect(didTransition)
    #expect(machine.state == .starting)
  }

  @Test("starting → recording succeeds")
  func startingToRecording() {
    var machine = CaptureStateMachine(initial: .starting)
    let didTransition = machine.transition(to: .recording)
    #expect(didTransition)
    #expect(machine.state == .recording)
  }

  @Test("recording → pausing → paused succeeds")
  func recordingToPaused() {
    var machine = CaptureStateMachine(initial: .recording)
    let didPause = machine.transition(to: .pausing)
    #expect(didPause)
    let didFinishPause = machine.transition(to: .paused)
    #expect(didFinishPause)
    #expect(machine.state == .paused)
  }

  @Test("paused → starting (resume) succeeds")
  func pausedToStarting() {
    var machine = CaptureStateMachine(initial: .paused)
    let didTransition = machine.transition(to: .starting)
    #expect(didTransition)
  }

  @Test("recording → stopping → finished")
  func recordingToFinished() {
    var machine = CaptureStateMachine(initial: .recording)
    let didStop = machine.transition(to: .stopping)
    #expect(didStop)
    let didFinish = machine.transition(to: .finished)
    #expect(didFinish)
    #expect(machine.state == .finished)
  }

  @Test("paused → stopping → finished")
  func pausedToFinished() {
    var machine = CaptureStateMachine(initial: .paused)
    let didStop = machine.transition(to: .stopping)
    #expect(didStop)
    let didFinish = machine.transition(to: .finished)
    #expect(didFinish)
  }

  @Test("recording → failed (reason)")
  func recordingToFailed() {
    var machine = CaptureStateMachine(initial: .recording)
    let didFail = machine.transition(to: .failed(reason: "permission revoked"))
    #expect(didFail)
    if case .failed = machine.state {
    } else {
      Issue.record("Expected failed state")
    }
  }

  @Test("recording → interrupted (reason)")
  func recordingToInterrupted() {
    var machine = CaptureStateMachine(initial: .recording)
    let didInterrupt = machine.transition(to: .interrupted(reason: "sleep"))
    #expect(didInterrupt)
  }

  @Test("re-entrant start from recording is rejected")
  func reentrantStartIsRejected() {
    var machine = CaptureStateMachine(initial: .recording)
    let didTransition = machine.transition(to: .authorizing)
    #expect(!didTransition)
    #expect(machine.state == .recording)
  }

  @Test("idle → recording directly is rejected")
  func idleToRecordingIsRejected() {
    var machine = CaptureStateMachine()
    let didTransition = machine.transition(to: .recording)
    #expect(!didTransition)
    #expect(machine.state == .idle)
  }

  @Test("finished → authorizing is rejected")
  func finishedToAuthorizingIsRejected() {
    var machine = CaptureStateMachine(initial: .finished)
    let didTransition = machine.transition(to: .authorizing)
    #expect(!didTransition)
    #expect(machine.state == .finished)
  }

  @Test("failed → failed from idle is rejected")
  func idleToFailedIsRejected() {
    var machine = CaptureStateMachine(initial: .idle)
    let didTransition = machine.transition(to: .failed(reason: "test"))
    #expect(!didTransition)
  }

  @Test("CaptureState description is human readable")
  func stateDescriptions() {
    #expect(CaptureState.idle.description == "idle")
    #expect(CaptureState.recording.description == "recording")
    #expect(CaptureState.failed(reason: "x").description == "failed(x)")
    #expect(CaptureState.interrupted(reason: "sleep").description == "interrupted(sleep)")
  }
}

@Suite("ClockSynchronizer")
struct ClockSynchronizerTests {

  @Test("first sample anchors the origin at zero")
  func firstSampleAnchorsAtZero() async {
    let clock = ClockSynchronizer()
    let t0 = CMTime(seconds: 100, preferredTimescale: 48000)
    let result = await clock.sessionTime(for: t0)
    #expect(result == .zero)
  }

  @Test("subsequent sample is relative to origin")
  func subsequentSampleIsRelative() async {
    let clock = ClockSynchronizer()
    let t0 = CMTime(seconds: 100, preferredTimescale: 48000)
    let t1 = CMTime(seconds: 101, preferredTimescale: 48000)
    _ = await clock.sessionTime(for: t0)
    let result = await clock.sessionTime(for: t1)
    let expected = CMTime(seconds: 1, preferredTimescale: 48000)
    #expect(result == expected)
  }

  @Test("pause duration is subtracted from session time")
  func pauseDurationIsSubtracted() async {
    let clock = ClockSynchronizer()
    let t0 = CMTime(seconds: 100, preferredTimescale: 48000)
    let t1 = CMTime(seconds: 110, preferredTimescale: 48000)
    _ = await clock.sessionTime(for: t0)
    await clock.recordPause(duration: CMTime(seconds: 5, preferredTimescale: 48000))
    let result = await clock.sessionTime(for: t1)
    let expected = CMTime(seconds: 5, preferredTimescale: 48000)
    #expect(result == expected)
  }

  @Test("sample before origin returns nil")
  func sampleBeforeOriginReturnsNil() async {
    let clock = ClockSynchronizer()
    let t0 = CMTime(seconds: 100, preferredTimescale: 48000)
    _ = await clock.sessionTime(for: t0)
    // A sample 5s before origin with a 6s pause should result in negative time
    await clock.recordPause(duration: CMTime(seconds: 6, preferredTimescale: 48000))
    let tEarly = CMTime(seconds: 101, preferredTimescale: 48000)
    let result = await clock.sessionTime(for: tEarly)
    #expect(result == nil)
  }

  @Test("reset clears state")
  func resetClearsState() async {
    let clock = ClockSynchronizer()
    let t0 = CMTime(seconds: 100, preferredTimescale: 48000)
    _ = await clock.sessionTime(for: t0)
    await clock.reset()
    let t1 = CMTime(seconds: 200, preferredTimescale: 48000)
    let result = await clock.sessionTime(for: t1)
    #expect(result == .zero)  // re-anchors
  }
}
