import AVFoundation
import Foundation
import Testing

@testable import SymMeetCapture

// MARK: - Fakes

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

// MARK: - Tests

@Suite("CapturePermissionService")
struct CapturePermissionServiceTests {

  @Test("currentStatus returns authorized when both are granted")
  func currentStatusAllGranted() async {
    let service = CapturePermissionService(
      microphoneProvider: FakeMicrophoneProvider(status: .authorized, requestResult: true),
      screenRecordingProvider: FakeScreenRecordingProvider(authorized: true)
    )
    let snapshot = await service.currentStatus()
    #expect(snapshot.microphone.status == .authorized)
    #expect(snapshot.screenRecording.status == .authorized)
    #expect(snapshot.allAuthorized)
  }

  @Test("currentStatus returns denied for microphone when denied")
  func currentStatusMicrophoneDenied() async {
    let service = CapturePermissionService(
      microphoneProvider: FakeMicrophoneProvider(status: .denied, requestResult: false),
      screenRecordingProvider: FakeScreenRecordingProvider(authorized: true)
    )
    let snapshot = await service.currentStatus()
    #expect(snapshot.microphone.status == .denied)
    #expect(!snapshot.allAuthorized)
  }

  @Test("currentStatus returns denied for screen recording when not authorized")
  func currentStatusScreenRecordingDenied() async {
    let service = CapturePermissionService(
      microphoneProvider: FakeMicrophoneProvider(status: .authorized, requestResult: true),
      screenRecordingProvider: FakeScreenRecordingProvider(authorized: false)
    )
    let snapshot = await service.currentStatus()
    #expect(snapshot.screenRecording.status == .denied)
    #expect(!snapshot.allAuthorized)
  }

  @Test("currentStatus returns notDetermined for microphone when undecided")
  func currentStatusMicrophoneUndetermined() async {
    let service = CapturePermissionService(
      microphoneProvider: FakeMicrophoneProvider(status: .notDetermined, requestResult: false),
      screenRecordingProvider: FakeScreenRecordingProvider(authorized: false)
    )
    let snapshot = await service.currentStatus()
    #expect(snapshot.microphone.status == .notDetermined)
  }

  @Test("currentStatus returns restricted for microphone when restricted")
  func currentStatusMicrophoneRestricted() async {
    let service = CapturePermissionService(
      microphoneProvider: FakeMicrophoneProvider(status: .restricted, requestResult: false),
      screenRecordingProvider: FakeScreenRecordingProvider(authorized: false)
    )
    let snapshot = await service.currentStatus()
    #expect(snapshot.microphone.status == .restricted)
  }

  @Test("requestMicrophoneAccess returns current status when already authorized")
  func requestMicrophoneAlreadyAuthorized() async {
    let service = CapturePermissionService(
      microphoneProvider: FakeMicrophoneProvider(status: .authorized, requestResult: true),
      screenRecordingProvider: FakeScreenRecordingProvider(authorized: true)
    )
    let result = await service.requestMicrophoneAccess()
    #expect(result == .authorized)
  }

  @Test("requestMicrophoneAccess returns current status when denied without re-prompting")
  func requestMicrophoneAlreadyDenied() async {
    let service = CapturePermissionService(
      microphoneProvider: FakeMicrophoneProvider(status: .denied, requestResult: false),
      screenRecordingProvider: FakeScreenRecordingProvider(authorized: false)
    )
    let result = await service.requestMicrophoneAccess()
    #expect(result == .denied)
  }

  @Test("denied capability provides a System Settings URL")
  func deniedCapabilityHasSettingsURL() {
    let capability = CaptureCapability(name: "Microphone", status: .denied)
    #expect(capability.settingsURL != nil)
  }

  @Test("authorized capability has no System Settings URL")
  func authorizedCapabilityHasNoSettingsURL() {
    let capability = CaptureCapability(name: "Microphone", status: .authorized)
    #expect(capability.settingsURL == nil)
  }
}
