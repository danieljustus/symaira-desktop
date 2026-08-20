import ArgumentParser
import Foundation
import SymMeetCapture

extension SymMeet {
  /// `symmeet permissions`
  struct Permissions: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      commandName: "permissions",
      abstract: "Check and request macOS capture permissions.",
      subcommands: [StatusCommand.self, RequestCommand.self],
      defaultSubcommand: StatusCommand.self
    )
  }
}

extension SymMeet.Permissions {
  // MARK: - symmeet permissions status

  struct StatusCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      commandName: "status",
      abstract: "Print current microphone and screen recording authorization status."
    )

    @Flag(name: .long, help: "Emit one machine-readable JSON document.")
    var json = false

    mutating func run() async throws {
      let service = CapturePermissionService()
      let snapshot = await service.currentStatus()

      if json {
        try Output.writeJSON(PermissionsStatusOutput(snapshot: snapshot))
      } else {
        Output.writeLine("Microphone:       \(snapshot.microphone.status.rawValue)")
        Output.writeLine("Screen Recording: \(snapshot.screenRecording.status.rawValue)")
        if !snapshot.allAuthorized {
          Output.writeError(
            "One or more permissions are not granted. "
              + "Open System Settings › Privacy & Security to review.")
        }
      }
    }
  }

  // MARK: - symmeet permissions request

  struct RequestCommand: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      commandName: "request",
      abstract: "Request a capture permission explicitly."
    )

    @Argument(help: "The permission to request: microphone or screen-recording.")
    var permission: PermissionName

    mutating func run() async throws {
      let service = CapturePermissionService()
      switch permission {
      case .microphone:
        let result = await service.requestMicrophoneAccess()
        Output.writeLine("Microphone: \(result.rawValue)")
      case .screenRecording:
        Output.writeLine(
          "Requesting Screen Recording permission — the system dialog will appear.")
        await service.requestScreenRecordingAccess()
        let status = await service.currentStatus()
        Output.writeLine("Screen Recording: \(status.screenRecording.status.rawValue)")
      }
    }
  }
}

// MARK: - Supporting types

enum PermissionName: String, ExpressibleByArgument, CaseIterable, Sendable {
  case microphone
  case screenRecording = "screen-recording"
}

private struct PermissionsStatusOutput: Encodable {
  let microphone: String
  let screenRecording: String
  let allAuthorized: Bool

  init(snapshot: CaptureCapabilitySnapshot) {
    microphone = snapshot.microphone.status.rawValue
    screenRecording = snapshot.screenRecording.status.rawValue
    allAuthorized = snapshot.allAuthorized
  }
}
