import XCTest
@testable import SymDesk

/// Regression coverage for the "Request Recording" alert content when the
/// SymMeet menu-bar agent is not installed. Previously this path only ever
/// produced a plain-text message with no actionable next step; now it must
/// also carry a link the user can follow to install SymMeet.
final class MeetingsViewTests: XCTestCase {
    func testRecordingAlertContentWhenAgentNotInstalledIncludesInstallLink() {
        let content = MeetingsView.recordingAlertContent(agentInstalled: false)

        XCTAssertNotNil(content.installURL, "the 'not installed' alert must offer an actionable install link")
        XCTAssertEqual(content.installURL, URL(string: "https://github.com/danieljustus/symaira-meet"))
        XCTAssertTrue(content.message.contains("not installed"))
    }

    func testRecordingAlertContentWhenAgentInstalledHasNoInstallLink() {
        let content = MeetingsView.recordingAlertContent(agentInstalled: true)

        XCTAssertNil(content.installURL)
        XCTAssertTrue(content.message.contains("SymMeet"))
    }
}
