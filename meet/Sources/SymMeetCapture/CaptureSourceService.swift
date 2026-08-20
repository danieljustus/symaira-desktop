@preconcurrency import AVFoundation
import Foundation
@preconcurrency import ScreenCaptureKit

// MARK: - Provider protocols (injectable for testing)

/// A snapshot returned by a SCShareableContent provider.
public struct ShareableContentSnapshot: Sendable {
  public let displays: [(id: String, name: String)]
  public let applications: [(id: String, bundleID: String, name: String, isOnScreen: Bool)]

  public init(
    displays: [(id: String, name: String)],
    applications: [(id: String, bundleID: String, name: String, isOnScreen: Bool)]
  ) {
    self.displays = displays
    self.applications = applications
  }
}

/// Protocol for testing: replaces live SCShareableContent calls.
public protocol ShareableContentProvider: Sendable {
  func shareableContent(
    excludingDesktopWindows: Bool,
    onScreenWindowsOnly: Bool
  ) async throws -> ShareableContentSnapshot
}

public struct LiveShareableContentProvider: ShareableContentProvider, Sendable {
  public init() {}

  public func shareableContent(
    excludingDesktopWindows: Bool,
    onScreenWindowsOnly: Bool
  ) async throws -> ShareableContentSnapshot {
    let content = try await SCShareableContent.excludingDesktopWindows(
      excludingDesktopWindows,
      onScreenWindowsOnly: onScreenWindowsOnly
    )

    let displays = content.displays.map {
      (id: "display-\($0.displayID)", name: "Display \($0.displayID)")
    }

    let currentBundle = Bundle.main.bundleIdentifier ?? ""
    let applications = content.applications
      .filter { $0.bundleIdentifier != currentBundle }
      .map { app in
        (
          id: "app-\(app.processID)",
          bundleID: app.bundleIdentifier,
          name: app.applicationName,
          isOnScreen: content.windows.contains { $0.owningApplication?.processID == app.processID }
        )
      }

    return ShareableContentSnapshot(displays: displays, applications: applications)
  }
}

/// Protocol for testing: replaces live AVCaptureDevice enumeration.
public protocol MicrophoneDeviceProvider: Sendable {
  func availableMicrophones() -> [(id: String, name: String)]
}

public struct LiveMicrophoneDeviceProvider: MicrophoneDeviceProvider, Sendable {
  public init() {}

  public func availableMicrophones() -> [(id: String, name: String)] {
    let session = AVCaptureDevice.DiscoverySession(
      deviceTypes: [.microphone, .external],
      mediaType: .audio,
      position: .unspecified
    )
    return session.devices.map { (id: $0.uniqueID, name: $0.localizedName) }
  }
}

// MARK: - Service

/// Discovers available audio sources without starting any capture session.
public actor CaptureSourceService {
  private let contentProvider: any ShareableContentProvider
  private let microphoneDeviceProvider: any MicrophoneDeviceProvider

  public init(
    contentProvider: any ShareableContentProvider = LiveShareableContentProvider(),
    microphoneDeviceProvider: any MicrophoneDeviceProvider = LiveMicrophoneDeviceProvider()
  ) {
    self.contentProvider = contentProvider
    self.microphoneDeviceProvider = microphoneDeviceProvider
  }

  /// Enumerates all audio sources. Requires Screen Recording permission for system sources.
  public func availableSources() async throws -> CaptureSourceList {
    let snapshot = try await contentProvider.shareableContent(
      excludingDesktopWindows: true,
      onScreenWindowsOnly: false
    )

    let displaySources = snapshot.displays.map { d in
      CaptureSource(
        id: d.id,
        kind: .display,
        displayName: d.name,
        bundleID: nil,
        isActive: true,
        supportsSystemAudio: true
      )
    }

    let appSources = snapshot.applications.map { app in
      CaptureSource(
        id: app.id,
        kind: .application,
        displayName: app.name,
        bundleID: app.bundleID,
        isActive: app.isOnScreen,
        supportsSystemAudio: true
      )
    }

    let micSources = microphoneDeviceProvider.availableMicrophones().map { mic in
      CaptureSource(
        id: mic.id,
        kind: .microphone,
        displayName: mic.name,
        bundleID: nil,
        isActive: true,
        supportsSystemAudio: false
      )
    }

    return CaptureSourceList(
      displays: displaySources,
      applications: appSources,
      microphones: micSources
    )
  }

  /// Finds a specific running application by bundle ID.
  public func applicationSource(bundleID: String) async throws -> CaptureSource {
    let list = try await availableSources()
    guard let source = list.applications.first(where: { $0.bundleID == bundleID }) else {
      throw CaptureError.sourceNotFound(bundleID: bundleID)
    }
    return source
  }
}
