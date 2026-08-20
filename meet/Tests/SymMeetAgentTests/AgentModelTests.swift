import Testing
import Foundation
import AVFoundation
import SymMeetCore
@testable import SymMeetCapture
@testable import SymMeetAgent

struct FakeMicrophoneProvider: MicrophoneAuthorizationProvider {
  var status: AVAuthorizationStatus
  var requestResult: Bool

  func authorizationStatus() -> AVAuthorizationStatus { status }
  func requestAccess() async -> Bool { requestResult }
}

actor FakeScreenRecordingProvider: ScreenRecordingAuthorizationProvider {
  let authorized: Bool
  private(set) var requestCalled = false

  init(authorized: Bool) {
    self.authorized = authorized
  }

  nonisolated func isAuthorized() -> Bool {
    authorized
  }

  func requestAuthorization() async {
    requestCalled = true
  }
}

@Suite("AgentModelTests")
@MainActor
struct AgentModelTests {

  @Test("Initializes in permissionRequired when unauthorized")
  func testInitUnauthorized() async {
    let service = CapturePermissionService(
      microphoneProvider: FakeMicrophoneProvider(status: .denied, requestResult: false),
      screenRecordingProvider: FakeScreenRecordingProvider(authorized: false)
    )
    let model = AgentModel(permissionService: service)
    await model.checkPermissions()
    #expect(model.state == .permissionRequired)
  }

  @Test("Initializes in idle when fully authorized")
  func testInitAuthorized() async {
    let service = CapturePermissionService(
      microphoneProvider: FakeMicrophoneProvider(status: .authorized, requestResult: true),
      screenRecordingProvider: FakeScreenRecordingProvider(authorized: true)
    )
    let model = AgentModel(permissionService: service)
    await model.checkPermissions()
    #expect(model.state == .idle)
  }

  @Test("Start flow transition checks")
  func testStartFlow() async {
    let service = CapturePermissionService(
      microphoneProvider: FakeMicrophoneProvider(status: .authorized, requestResult: true),
      screenRecordingProvider: FakeScreenRecordingProvider(authorized: true)
    )
    let model = AgentModel(permissionService: service)
    #expect(model.state == .idle)

    await model.initiateRecording(purpose: "Test Meeting")
    #expect(model.state == .consentConfirmation)

    model.cancelConsent()
    #expect(model.state == .idle)
  }
}
