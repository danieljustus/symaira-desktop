import Foundation
import Combine
import SymMeetCore
import SymMeetCapture

@MainActor
public final class AgentModel: ObservableObject {
  public enum AgentState: Equatable, Sendable {
    case idle
    case permissionRequired
    case consentConfirmation
    case starting
    case recording(elapsed: TimeInterval)
    case paused(elapsed: TimeInterval)
    case stopping
    case failed(String)
    case completed(meetingID: UUID)
  }

  @Published public private(set) var state: AgentState = .idle
  @Published public private(set) var microphoneAuthorized = false
  @Published public private(set) var screenRecordingAuthorized = false

  private let permissionService: CapturePermissionService
  private var auth: RecordingAuthorization?
  private var session: CaptureSession?
  private var timer: Timer?
  private var recordingStartTime: Date?
  private var accumulatedDuration: TimeInterval = 0
  private var currentMeetingID: UUID?
  private var currentSessionID: UUID?
  private var currentConsentRecord: ConsentRecord?
  private var currentPurpose: String = ""

  public init(
    permissionService: CapturePermissionService = CapturePermissionService()
  ) {
    self.permissionService = permissionService
    Task { @MainActor in
      await checkPermissions()
    }
  }

  // MARK: - Permissions

  public func checkPermissions() async {
    let snapshot = await permissionService.currentStatus()
    microphoneAuthorized = snapshot.microphone.status == .authorized
    screenRecordingAuthorized = snapshot.screenRecording.status == .authorized

    if !snapshot.allAuthorized {
      state = .permissionRequired
    } else if case .permissionRequired = state {
      state = .idle
    }
  }

  public func requestMicrophonePermission() async {
    _ = await permissionService.requestMicrophoneAccess()
    await checkPermissions()
  }

  public func requestScreenRecordingPermission() async {
    await permissionService.requestScreenRecordingAccess()
    await checkPermissions()
  }

  // MARK: - Recording Flow

  public func initiateRecording(purpose: String) async {
    await checkPermissions()
    guard microphoneAuthorized && screenRecordingAuthorized else {
      state = .permissionRequired
      return
    }
    currentPurpose = purpose
    state = .consentConfirmation
  }

  public func cancelConsent() {
    state = .idle
  }

  public func confirmConsent(attested: Bool) async {
    guard case .consentConfirmation = state else { return }
    state = .starting

    let meetingID = UUID()
    let sessionID = UUID()
    currentMeetingID = meetingID
    currentSessionID = sessionID

    struct AgentAuthorizer: InteractiveRecordingAuthorizer, Sendable {
      let attested: Bool
      func requestAuthorization(
        for request: RecordingAuthorizationRequest
      ) async throws -> InteractiveAuthorizationDecision {
        InteractiveAuthorizationDecision(
          operatorAttested: attested,
          noticeAt: Date(),
          scope: request.scope,
          expiresAt: Date().addingTimeInterval(300)
        )
      }
    }

    let authorizer = AgentAuthorizer(attested: attested)
    let auth = RecordingAuthorization(authorizer: authorizer)
    self.auth = auth

    let scope = RecordingScope(meetingID: meetingID, purpose: currentPurpose)
    do {
      let consent = try await auth.requestAuthorization(sessionID: sessionID, scope: scope)
      currentConsentRecord = consent

      let store = MeetingStore()
      let meetingDir = await store.layout.meetingDirectory(meetingID.uuidString.lowercased())

      let captureConfig = CaptureSessionConfiguration(
        sessionID: sessionID,
        authorization: consent,
        systemAudio: .allOutputs,
        microphone: .defaultDevice,
        outputDirectory: meetingDir
      )

      let session = CaptureSession()
      self.session = session

      try await auth.startRecording(sessionID: sessionID, authorization: consent)
      try await session.start(configuration: captureConfig)

      recordingStartTime = Date()
      accumulatedDuration = 0
      state = .recording(elapsed: 0)
      startTimer()
    } catch {
      state = .failed(error.localizedDescription)
    }
  }

  public func pause() async {
    guard case .recording = state, let session else { return }
    do {
      try await session.pause()
      stopTimer()
      if let start = recordingStartTime {
        accumulatedDuration += Date().timeIntervalSince(start)
      }
      recordingStartTime = nil
      state = .paused(elapsed: accumulatedDuration)
    } catch {
      state = .failed(error.localizedDescription)
    }
  }

  public func resume() async {
    guard case .paused = state, let session else { return }
    do {
      try await session.resume()
      recordingStartTime = Date()
      state = .recording(elapsed: accumulatedDuration)
      startTimer()
    } catch {
      state = .failed(error.localizedDescription)
    }
  }

  public func stop() async {
    guard let session, let auth, let meetingID = currentMeetingID, let sessionID = currentSessionID else { return }
    state = .stopping
    stopTimer()

    do {
      let result = try await session.stop()
      try await auth.stopRecording(sessionID: sessionID)

      // Build manifest and save
      var audioTracks: [AudioTrack] = []
      if result.systemTrackURL != nil {
        audioTracks.append(AudioTrack(
          trackID: UUID(),
          kind: .system,
          relativePath: "system-audio.caf"
        ))
      }
      if result.microphoneTrackURL != nil {
        audioTracks.append(AudioTrack(
          trackID: UUID(),
          kind: .microphone,
          relativePath: "microphone.caf"
        ))
      }

      let consentStatus: ConsentStatus = (currentConsentRecord?.operatorAttested ?? false) ? .authorized : .required
      let manifest = MeetingManifest(
        meetingID: meetingID,
        source: .liveCapture,
        createdAt: Date(),
        updatedAt: Date(),
        audioTracks: audioTracks,
        consent: ConsentState(
          status: consentStatus,
          authorizedAt: currentConsentRecord?.noticeAt ?? Date(),
          expiresAt: currentConsentRecord?.expiresAt ?? Date()
        ),
        retention: RetentionMetadata(policy: .keep)
      )

      let store = MeetingStore()
      try await store.create(manifest)

      state = .completed(meetingID: meetingID)
      cleanup()
    } catch {
      state = .failed(error.localizedDescription)
    }
  }

  public func resetToIdle() {
    state = .idle
  }

  // MARK: - Private Timer

  private func startTimer() {
    timer = Timer.scheduledTimer(withTimeInterval: 1.0, repeats: true) { [weak self] _ in
      guard let self else { return }
      Task { @MainActor in
        self.updateElapsed()
      }
    }
  }

  private func stopTimer() {
    timer?.invalidate()
    timer = nil
  }

  private func updateElapsed() {
    guard let start = recordingStartTime else { return }
    let currentElapsed = accumulatedDuration + Date().timeIntervalSince(start)
    if case .recording = state {
      state = .recording(elapsed: currentElapsed)
    }
  }

  private func cleanup() {
    stopTimer()
    auth = nil
    session = nil
    recordingStartTime = nil
    accumulatedDuration = 0
    currentConsentRecord = nil
    currentSessionID = nil
  }
}
