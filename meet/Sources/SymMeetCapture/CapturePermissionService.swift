@preconcurrency import AVFoundation
import Foundation

/// Protocol for testing: replaces live AVFoundation calls.
public protocol MicrophoneAuthorizationProvider: Sendable {
  func authorizationStatus() -> AVAuthorizationStatus
  func requestAccess() async -> Bool
}

/// Protocol for testing: replaces live ScreenCaptureKit calls.
public protocol ScreenRecordingAuthorizationProvider: Sendable {
  func isAuthorized() -> Bool
  func requestAuthorization() async
}

// MARK: - Live implementations

public struct LiveMicrophoneAuthorizationProvider: MicrophoneAuthorizationProvider, Sendable {
  public init() {}

  public func authorizationStatus() -> AVAuthorizationStatus {
    AVCaptureDevice.authorizationStatus(for: .audio)
  }

  public func requestAccess() async -> Bool {
    await AVCaptureDevice.requestAccess(for: .audio)
  }
}

public struct LiveScreenRecordingAuthorizationProvider: ScreenRecordingAuthorizationProvider,
  Sendable
{
  public init() {}

  public func isAuthorized() -> Bool {
    // CGPreflightScreenCaptureAccess() is the approved preflight without a prompt.
    CGPreflightScreenCaptureAccess()
  }

  /// Requests Screen Recording permission. This shows the system prompt on first call.
  public func requestAuthorization() async {
    // CGRequestScreenCaptureAccess() shows the macOS permission dialog.
    // We call it on a detached task because it may briefly block the calling thread.
    await withCheckedContinuation { continuation in
      Task.detached {
        CGRequestScreenCaptureAccess()
        continuation.resume()
      }
    }
  }
}

// MARK: - Service

/// Checks and requests macOS capture permissions without starting any capture session.
public actor CapturePermissionService {
  private let microphoneProvider: any MicrophoneAuthorizationProvider
  private let screenRecordingProvider: any ScreenRecordingAuthorizationProvider

  public init(
    microphoneProvider: any MicrophoneAuthorizationProvider = LiveMicrophoneAuthorizationProvider(),
    screenRecordingProvider: any ScreenRecordingAuthorizationProvider =
      LiveScreenRecordingAuthorizationProvider()
  ) {
    self.microphoneProvider = microphoneProvider
    self.screenRecordingProvider = screenRecordingProvider
  }

  /// Returns the current authorization snapshot without requesting permissions.
  public func currentStatus() -> CaptureCapabilitySnapshot {
    let micStatus = microphoneAuthorizationStatus()
    let scrStatus: CaptureAuthorizationStatus =
      screenRecordingProvider.isAuthorized() ? .authorized : .denied

    return CaptureCapabilitySnapshot(
      microphone: CaptureCapability(name: "Microphone", status: micStatus),
      screenRecording: CaptureCapability(name: "Screen Recording", status: scrStatus)
    )
  }

  /// Requests microphone access. Safe to call from any context; returns the new status.
  public func requestMicrophoneAccess() async -> CaptureAuthorizationStatus {
    let current = microphoneAuthorizationStatus()
    guard current == .notDetermined else { return current }
    _ = await microphoneProvider.requestAccess()
    return microphoneAuthorizationStatus()
  }

  /// Requests Screen Recording access by presenting the macOS permission dialog.
  /// This must only be called in response to an explicit user action.
  public func requestScreenRecordingAccess() async {
    await screenRecordingProvider.requestAuthorization()
  }

  // MARK: Private

  private func microphoneAuthorizationStatus() -> CaptureAuthorizationStatus {
    switch microphoneProvider.authorizationStatus() {
    case .authorized: .authorized
    case .denied: .denied
    case .restricted: .restricted
    case .notDetermined: .notDetermined
    @unknown default: .notDetermined
    }
  }
}
