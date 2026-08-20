import ArgumentParser
import Foundation
import SymMeetCapture
import SymMeetCore

extension SymMeet {
  struct Record: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      commandName: "record",
      abstract: "Record a meeting with system and microphone audio."
    )

    @Option(name: .shortAndLong, help: "The purpose/name of the meeting.")
    var purpose: String

    @Flag(name: .shortAndLong, help: "Attest operator consent automatically without prompting.")
    var yes = false

    @Flag(name: .long, help: "Emit machine-readable JSON summary on stdout.")
    var json = false

    @Option(name: .long, help: "Configure system audio source (all, disabled).")
    var systemAudio: String = "all"

    @Option(name: .long, help: "Configure microphone source (default, disabled, or device ID).")
    var microphone: String = "default"

    mutating func run() async throws {
      let meetingID = UUID()
      let sessionID = UUID()

      let authorizer = CLIInteractiveAuthorizer(
        autoConsent: yes, meetingID: meetingID, purpose: purpose)
      let auth = RecordingAuthorization(authorizer: authorizer)
      let scope = RecordingScope(meetingID: meetingID, purpose: purpose)

      // Request consent
      let consentRecord: ConsentRecord
      do {
        consentRecord = try await auth.requestAuthorization(sessionID: sessionID, scope: scope)
      } catch {
        throw CLIError.from(error, isJSON: json)
      }

      // Prepare target directories
      let store = MeetingStore()
      let meetingDir = await store.layout.meetingDirectory(meetingID.uuidString.lowercased())

      // Construct capture config
      let sysSource: CaptureSessionConfiguration.SystemAudioSource
      switch systemAudio.lowercased() {
      case "all":
        sysSource = .allOutputs
      case "disabled":
        sysSource = .disabled
      default:
        sysSource = .disabled
      }

      let micSource: CaptureSessionConfiguration.MicrophoneSource
      switch microphone.lowercased() {
      case "default":
        micSource = .defaultDevice
      case "disabled":
        micSource = .disabled
      default:
        micSource = .device(id: microphone)
      }

      let captureConfig = CaptureSessionConfiguration(
        sessionID: sessionID,
        authorization: consentRecord,
        systemAudio: sysSource,
        microphone: micSource,
        outputDirectory: meetingDir
      )

      let session = CaptureSession()

      // Print start sequence
      if !json {
        Output.writeError("Starting live capture session...")
      }

      do {
        try await auth.startRecording(sessionID: sessionID, authorization: consentRecord)
        try await session.start(configuration: captureConfig)
      } catch {
        throw CLIError.from(error, isJSON: json)
      }

      if !json {
        Output.writeError("Recording started. Press Enter to stop.")
      }

      // Read from stdin to wait
      _ = readLine()

      if !json {
        Output.writeError("Stopping capture...")
      }

      let result: CaptureResult
      do {
        result = try await session.stop()
        try await auth.stopRecording(sessionID: sessionID)
      } catch {
        throw CLIError.from(error, isJSON: json)
      }

      // Create tracks in the manifest
      var audioTracks: [AudioTrack] = []
      if result.systemTrackURL != nil {
        audioTracks.append(
          AudioTrack(
            trackID: UUID(),
            kind: .system,
            relativePath: "system-audio.caf"
          ))
      }
      if result.microphoneTrackURL != nil {
        audioTracks.append(
          AudioTrack(
            trackID: UUID(),
            kind: .microphone,
            relativePath: "microphone.caf"
          ))
      }

      // Write meeting manifest
      let consentStatus: ConsentStatus = consentRecord.operatorAttested ? .authorized : .required
      let manifest = MeetingManifest(
        meetingID: meetingID,
        source: .liveCapture,
        createdAt: Date(),
        updatedAt: Date(),
        audioTracks: audioTracks,
        consent: ConsentState(
          status: consentStatus,
          authorizedAt: consentRecord.noticeAt,
          expiresAt: consentRecord.expiresAt
        ),
        retention: RetentionMetadata(policy: .keep)
      )

      do {
        try await store.create(manifest)
      } catch {
        throw CLIError.from(error, isJSON: json)
      }

      // JSON or human readable stdout
      if json {
        let output = RecordOutput(
          meetingID: meetingID.uuidString.lowercased(),
          systemTrack: result.systemTrackURL != nil ? "system-audio.caf" : nil,
          microphoneTrack: result.microphoneTrackURL != nil ? "microphone.caf" : nil,
          status: "success"
        )
        try Output.writeJSON(output)
      } else {
        Output.writeLine(
          "Successfully recorded and stored meeting \(meetingID.uuidString.lowercased()).")
      }
    }
  }
}

private struct RecordOutput: Encodable {
  let meetingID: String
  let systemTrack: String?
  let microphoneTrack: String?
  let status: String

  private enum CodingKeys: String, CodingKey {
    case meetingID = "meeting_id"
    case systemTrack = "system_track"
    case microphoneTrack = "microphone_track"
    case status
  }
}

struct CLIInteractiveAuthorizer: InteractiveRecordingAuthorizer, Sendable {
  let autoConsent: Bool
  let meetingID: UUID
  let purpose: String

  func requestAuthorization(
    for request: RecordingAuthorizationRequest
  ) async throws -> InteractiveAuthorizationDecision {
    guard request.sessionID == request.sessionID else {
      throw PrivacyError.invalidInteractiveAttestation
    }

    if autoConsent {
      return InteractiveAuthorizationDecision(
        operatorAttested: true,
        noticeAt: Date(),
        scope: request.scope,
        expiresAt: Date().addingTimeInterval(300)  // 5 minutes expiration
      )
    }

    // Print notice/prompt to stderr so stdout is reserved for JSON if needed
    FileHandle.standardError.write(
      Data(
        ("\n=== RECORDING CONSENT ===\n"
          + "Symaira Meet is requesting authorization to capture audio.\n"
          + "Meeting ID: \(meetingID.uuidString.lowercased())\n" + "Purpose:    \(purpose)\n"
          + "Do you authorize this recording? (yes/no): ").utf8))

    guard let line = readLine() else {
      throw PrivacyError.invalidInteractiveAttestation
    }

    let trimmed = line.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
    guard trimmed == "yes" || trimmed == "y" else {
      throw PrivacyError.invalidInteractiveAttestation
    }

    return InteractiveAuthorizationDecision(
      operatorAttested: true,
      noticeAt: Date(),
      scope: request.scope,
      expiresAt: Date().addingTimeInterval(300)
    )
  }
}
