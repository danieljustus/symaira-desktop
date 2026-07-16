import Foundation
import SymDeskCore

/// Narrow, injectable contract for the meeting operations the Meetings
/// workspace needs from `DeskCore`. Mirrors the pattern `DeskTransport`
/// establishes for the CLI/HTTP boundary: view-models depend on this
/// protocol instead of the concrete singleton, so tests can substitute a
/// mock instead of shelling out to a real `symdesk` process.
protocol MeetingsDataSource: Sendable {
    func meetingsList() async throws -> [MeetingNoteSummary]
    func meetingsAvailable() async throws -> [AvailableMeeting]
    func meetingShow(path: String) async throws -> MeetingDetail
    @discardableResult
    func meetingImport(meetingID: String) async throws -> String
    func meetingRefresh(path: String, apply: Bool) async throws -> MeetingRefreshOutcome
    func meetingSegments(path: String) async throws -> [MeetingSegment]
    func meetingSpeakers(path: String) async throws -> [MeetingSpeaker]
    func meetingSpeakerLabel(path: String, speakerID: String, label: String) async throws
    func meetingSpeakerMerge(path: String, fromSpeakerID: String, toSpeakerID: String) async throws
    func meetingSpeakerSplit(path: String, speakerID: String, segmentID: String) async throws
    func meetingSpeakerReset(path: String) async throws
    func meetingMarkReviewed(path: String) async throws
    func meetingParticipantCandidates(label: String) async throws -> [ParticipantCandidate]
    func meetingParticipantConfirm(path: String, speakerID: String, entityID: String?) async throws
    @discardableResult
    func meetingParticipantCreate(path: String, speakerID: String, name: String) async throws -> String
    func meetingPublish(path: String, facts: [String]) async throws -> MeetingPublishOutcome
}

extension DeskCore: MeetingsDataSource {}

/// Extracts the reviewed transcript from a meeting note's body, which wraps
/// it between `<!-- symmeet-transcript:start -->` / `:end -->` markers (see
/// VAULT.md section 8 and `service.wrapTranscript`). Returns `nil` when the
/// markers are missing, so callers can show "transcript unavailable"
/// instead of misinterpreting unrelated body content as the transcript.
enum TranscriptExtractor {
    private static let startMarker = "<!-- symmeet-transcript:start -->"
    private static let endMarker = "<!-- symmeet-transcript:end -->"

    static func transcript(from body: String) -> String? {
        guard let startRange = body.range(of: startMarker) else { return nil }
        guard let endRange = body.range(of: endMarker, range: startRange.upperBound..<body.endIndex) else { return nil }
        return body[startRange.upperBound..<endRange.lowerBound].trimmingCharacters(in: .whitespacesAndNewlines)
    }
}
