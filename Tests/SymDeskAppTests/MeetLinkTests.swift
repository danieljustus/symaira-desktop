@preconcurrency import AVFoundation
import SymDictationCore
import SymMeetCapture
import SymMeetCore
import XCTest

/// Proves the Meet package products are linked into the app test bundle.
/// Uses injected providers only, so nothing here touches TCC, opens an
/// audio device or shows a permission prompt.
final class MeetLinkTests: XCTestCase {
    private struct FakeMicrophone: MicrophoneAuthorizationProvider {
        let status: AVAuthorizationStatus
        func authorizationStatus() -> AVAuthorizationStatus { status }
        func requestAccess() async -> Bool { false }
    }

    private struct FakeScreenRecording: ScreenRecordingAuthorizationProvider {
        let authorized: Bool
        func isAuthorized() -> Bool { authorized }
        func requestAuthorization() async {}
    }

    func testCapturePermissionServiceReportsInjectedStatusWithoutPrompting() async {
        let service = CapturePermissionService(
            microphoneProvider: FakeMicrophone(status: .authorized),
            screenRecordingProvider: FakeScreenRecording(authorized: false)
        )

        let snapshot = await service.currentStatus()

        XCTAssertEqual(snapshot.microphone.status, .authorized)
        XCTAssertEqual(snapshot.screenRecording.status, .denied)
        XCTAssertFalse(snapshot.allAuthorized)
    }

    func testMeetCoreModelCatalogIsReachable() {
        // SymDeskCore declares ModelCatalog/ModelDescriptor as well. This file
        // imports only SymMeetCore, so the name is unambiguous here. The module
        // name cannot serve as a qualifier: SymMeetCore also contains a type
        // named SymMeetCore, so `SymMeetCore.ModelCatalog` does not resolve.
        let catalog = ModelCatalog.beta
        XCTAssertFalse(catalog.descriptors.isEmpty)
    }

    func testDictationCoreValueTypeIsLinked() {
        let context = DictationContext(appBundleID: "com.example.app", host: nil)
        XCTAssertEqual(context.appBundleID, "com.example.app")
    }
}
