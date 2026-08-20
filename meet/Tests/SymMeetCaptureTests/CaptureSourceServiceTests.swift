import Testing

@testable import SymMeetCapture

// MARK: - Fakes

struct FakeShareableContentProvider: ShareableContentProvider {
  var displays: [(id: String, name: String)]
  var applications: [(id: String, bundleID: String, name: String, isOnScreen: Bool)]
  var shouldThrow: Bool = false

  func shareableContent(
    excludingDesktopWindows: Bool,
    onScreenWindowsOnly: Bool
  ) async throws -> ShareableContentSnapshot {
    if shouldThrow { throw CaptureError.screenRecordingDenied }
    return ShareableContentSnapshot(displays: displays, applications: applications)
  }
}

struct FakeMicrophoneDeviceProvider: MicrophoneDeviceProvider {
  var devices: [(id: String, name: String)]
  func availableMicrophones() -> [(id: String, name: String)] { devices }
}

// MARK: - Tests

@Suite("CaptureSourceService")
struct CaptureSourceServiceTests {

  @Test("availableSources returns displays, apps, and microphones")
  func availableSourcesReturnsAll() async throws {
    let service = CaptureSourceService(
      contentProvider: FakeShareableContentProvider(
        displays: [(id: "display-1", name: "Display 1")],
        applications: [
          (id: "app-100", bundleID: "com.example.app", name: "ExampleApp", isOnScreen: true)
        ]
      ),
      microphoneDeviceProvider: FakeMicrophoneDeviceProvider(
        devices: [(id: "mic-1", name: "Built-in Microphone")]
      )
    )

    let list = try await service.availableSources()

    #expect(list.displays.count == 1)
    #expect(list.displays[0].id == "display-1")
    #expect(list.displays[0].kind == .display)
    #expect(list.displays[0].supportsSystemAudio)

    #expect(list.applications.count == 1)
    #expect(list.applications[0].bundleID == "com.example.app")
    #expect(list.applications[0].kind == .application)
    #expect(list.applications[0].isActive)

    #expect(list.microphones.count == 1)
    #expect(list.microphones[0].kind == .microphone)
    #expect(!list.microphones[0].supportsSystemAudio)
  }

  @Test("availableSources returns empty list when no sources exist")
  func availableSourcesEmpty() async throws {
    let service = CaptureSourceService(
      contentProvider: FakeShareableContentProvider(displays: [], applications: []),
      microphoneDeviceProvider: FakeMicrophoneDeviceProvider(devices: [])
    )
    let list = try await service.availableSources()
    #expect(list.all.isEmpty)
  }

  @Test("availableSources propagates errors from content provider")
  func availableSourcesThrowsOnPermissionError() async {
    let service = CaptureSourceService(
      contentProvider: FakeShareableContentProvider(
        displays: [],
        applications: [],
        shouldThrow: true
      ),
      microphoneDeviceProvider: FakeMicrophoneDeviceProvider(devices: [])
    )
    await #expect(throws: CaptureError.screenRecordingDenied) {
      _ = try await service.availableSources()
    }
  }

  @Test("applicationSource finds source by bundle ID")
  func applicationSourceFound() async throws {
    let service = CaptureSourceService(
      contentProvider: FakeShareableContentProvider(
        displays: [],
        applications: [
          (id: "app-200", bundleID: "com.zoom.us", name: "Zoom", isOnScreen: true)
        ]
      ),
      microphoneDeviceProvider: FakeMicrophoneDeviceProvider(devices: [])
    )
    let source = try await service.applicationSource(bundleID: "com.zoom.us")
    #expect(source.bundleID == "com.zoom.us")
    #expect(source.displayName == "Zoom")
  }

  @Test("applicationSource throws sourceNotFound when bundle ID does not match")
  func applicationSourceNotFound() async {
    let service = CaptureSourceService(
      contentProvider: FakeShareableContentProvider(displays: [], applications: []),
      microphoneDeviceProvider: FakeMicrophoneDeviceProvider(devices: [])
    )
    await #expect(throws: CaptureError.sourceNotFound(bundleID: "com.missing.app")) {
      _ = try await service.applicationSource(bundleID: "com.missing.app")
    }
  }

  @Test("CaptureSourceList.all returns combined sources")
  func captureSourceListAll() {
    let display = CaptureSource(
      id: "d1", kind: .display, displayName: "D", isActive: true, supportsSystemAudio: true)
    let app = CaptureSource(
      id: "a1", kind: .application, displayName: "App", bundleID: "com.a", isActive: true,
      supportsSystemAudio: true)
    let mic = CaptureSource(
      id: "m1", kind: .microphone, displayName: "Mic", isActive: true, supportsSystemAudio: false)
    let list = CaptureSourceList(displays: [display], applications: [app], microphones: [mic])
    #expect(list.all.count == 3)
  }
}
