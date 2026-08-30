import XCTest
@testable import SymDesk
import SymDeskCore

private actor CallLog {
    private(set) var calls: [String] = []
    func record(_ call: String) { calls.append(call) }
}

/// Configurable stub conforming to `MeetingsDataSource`, so
/// `MeetingReviewModel` can be exercised without a real `symdesk` process.
private final class MockMeetingsDataSource: MeetingsDataSource, @unchecked Sendable {
    var meetingsListResult: Result<[MeetingNoteSummary], Error> = .success([])
    var meetingsListReportResult: Result<MeetingListResult, Error>?
    var meetingsAvailableResult: Result<[AvailableMeeting], Error> = .success([])
    var meetingShowResults: [String: Result<MeetingDetail, Error>] = [:]
    var meetingShowDelays: [String: UInt64] = [:]
    var meetingImportResult: Result<String, Error> = .success("meetings/meeting-m1.md")
    var meetingRefreshResult: Result<MeetingRefreshOutcome, Error> = .success(
        MeetingRefreshOutcome(path: "meetings/meeting-m1.md", changed: false, applied: false)
    )
    let log = CallLog()

    func meetingsList() async throws -> [MeetingNoteSummary] {
        await log.record("meetingsList")
        return try meetingsListResult.get()
    }

    func meetingsListReport() async throws -> MeetingListResult {
        await log.record("meetingsListReport")
        if let meetingsListReportResult {
            return try meetingsListReportResult.get()
        }
        return MeetingListResult(meetings: try meetingsListResult.get(), failures: [])
    }

    func meetingsAvailable() async throws -> [AvailableMeeting] {
        await log.record("meetingsAvailable")
        return try meetingsAvailableResult.get()
    }

    func meetingShow(path: String) async throws -> MeetingDetail {
        await log.record("meetingShow:\(path)")
        if let delay = meetingShowDelays[path] {
            try await Task.sleep(nanoseconds: delay)
        }
        guard let result = meetingShowResults[path] else {
            throw NSError(domain: "test", code: 1, userInfo: [NSLocalizedDescriptionKey: "no stub for \(path)"])
        }
        return try result.get()
    }

    @discardableResult
    func meetingImport(meetingID: String) async throws -> String {
        await log.record("meetingImport:\(meetingID)")
        return try meetingImportResult.get()
    }

    func meetingRefresh(path: String, apply: Bool) async throws -> MeetingRefreshOutcome {
        await log.record("meetingRefresh:\(path):\(apply)")
        return try meetingRefreshResult.get()
    }

    var meetingSegmentsResult: Result<[MeetingSegment], Error> = .success([])
    var meetingSpeakersResult: Result<[MeetingSpeaker], Error> = .success([])
    var speakerMutationError: Error?
    var markReviewedError: Error?

    func meetingSegments(path: String) async throws -> [MeetingSegment] {
        await log.record("meetingSegments:\(path)")
        return try meetingSegmentsResult.get()
    }

    func meetingSpeakers(path: String) async throws -> [MeetingSpeaker] {
        await log.record("meetingSpeakers:\(path)")
        return try meetingSpeakersResult.get()
    }

    func meetingSpeakerLabel(path: String, speakerID: String, label: String) async throws {
        await log.record("speakerLabel:\(speakerID):\(label)")
        if let speakerMutationError { throw speakerMutationError }
    }

    func meetingSpeakerMerge(path: String, fromSpeakerID: String, toSpeakerID: String) async throws {
        await log.record("speakerMerge:\(fromSpeakerID):\(toSpeakerID)")
        if let speakerMutationError { throw speakerMutationError }
    }

    func meetingSpeakerSplit(path: String, speakerID: String, segmentID: String) async throws {
        await log.record("speakerSplit:\(speakerID):\(segmentID)")
        if let speakerMutationError { throw speakerMutationError }
    }

    func meetingSpeakerReset(path: String) async throws {
        await log.record("speakerReset:\(path)")
        if let speakerMutationError { throw speakerMutationError }
    }

    func meetingMarkReviewed(path: String) async throws {
        await log.record("markReviewed:\(path)")
        if let markReviewedError { throw markReviewedError }
    }

    var participantCandidatesResult: Result<[ParticipantCandidate], Error> = .success([])
    var participantActionError: Error?
    var publishResult: Result<MeetingPublishOutcome, Error> = .success(
        MeetingPublishOutcome(meetingEntityID: "e-meeting", relationsCreated: 0, factsPublished: nil, factsSkipped: 0)
    )

    func meetingParticipantCandidates(label: String) async throws -> [ParticipantCandidate] {
        await log.record("candidates:\(label)")
        return try participantCandidatesResult.get()
    }

    func meetingParticipantConfirm(path: String, speakerID: String, entityID: String?) async throws {
        await log.record("confirm:\(speakerID):\(entityID ?? "<unlink>")")
        if let participantActionError { throw participantActionError }
    }

    @discardableResult
    func meetingParticipantCreate(path: String, speakerID: String, name: String) async throws -> String {
        await log.record("create:\(speakerID):\(name)")
        if let participantActionError { throw participantActionError }
        return "e-new"
    }

    func meetingPublish(path: String, facts: [String]) async throws -> MeetingPublishOutcome {
        await log.record("publish:\(path):\(facts.joined(separator: "|"))")
        return try publishResult.get()
    }
}

private func makeDetail(meetingID: String = "m1", body: String? = "\n<!-- symmeet-transcript:start -->\nAlice: Hello.\n<!-- symmeet-transcript:end -->\n") -> MeetingDetail {
    MeetingDetail(
        title: "Meeting",
        body: body ?? "",
        frontmatter: MeetingFrontmatter(
            meetingID: meetingID,
            startedAt: "2026-07-01T10:00:00Z",
            durationMS: 1800000,
            language: "en",
            participants: [MeetingParticipant(label: "Alice", speakerIDs: ["speaker_0"])]
        )
    )
}

@MainActor
final class MeetingReviewModelTests: XCTestCase {
    func testLoadLibrarySuccessPopulatesImportedAndAvailable() async {
        let source = MockMeetingsDataSource()
        source.meetingsListResult = .success([
            MeetingNoteSummary(path: "meetings/meeting-m1.md", title: "Standup", meetingID: "m1", startedAt: "2026-07-01T10:00:00Z", durationMS: 60000, language: "en", reviewState: "unreviewed")
        ])
        source.meetingsAvailableResult = .success([AvailableMeeting(meetingID: "m2", source: "recorded")])

        let model = MeetingReviewModel(dataSource: source)
        await model.loadLibrary()

        XCTAssertEqual(model.libraryState, .loaded)
        XCTAssertEqual(model.importedMeetings.count, 1)
        XCTAssertEqual(model.availableMeetings.count, 1)
        XCTAssertNil(model.availableMeetingsError)
    }

    func testMalformedMeetingFileIsRetainedAsActionableFailure() async {
        let source = MockMeetingsDataSource()
        source.meetingsListReportResult = .success(MeetingListResult(
            meetings: [],
            failures: [MeetingListFailure(path: "meetings/broken.md", message: "invalid frontmatter")]
        ))

        let model = MeetingReviewModel(dataSource: source)
        await model.loadLibrary()

        XCTAssertEqual(model.libraryFailures, [MeetingListFailure(path: "meetings/broken.md", message: "invalid frontmatter")])
        model.skipLibraryFailure(path: "meetings/broken.md")
        XCTAssertTrue(model.libraryFailures.isEmpty)
    }

    // symmeet being absent must not blank out an already-usable imported
    // library — only the "available to import" section degrades.
    func testSymmeetUnavailableDegradesOnlyAvailableSection() async {
        let source = MockMeetingsDataSource()
        source.meetingsListResult = .success([
            MeetingNoteSummary(path: "meetings/meeting-m1.md", title: "Standup", meetingID: "m1", startedAt: "2026-07-01T10:00:00Z", durationMS: 60000, language: "en", reviewState: "unreviewed")
        ])
        source.meetingsAvailableResult = .failure(NSError(domain: "test", code: 2, userInfo: [NSLocalizedDescriptionKey: "symmeet not found on PATH"]))

        let model = MeetingReviewModel(dataSource: source)
        await model.loadLibrary()

        XCTAssertEqual(model.libraryState, .loaded)
        XCTAssertEqual(model.importedMeetings.count, 1)
        XCTAssertTrue(model.availableMeetings.isEmpty)
        XCTAssertEqual(model.availableMeetingsError, "symmeet not found on PATH")
    }

    func testLoadLibraryFailureSurfacesFriendlyState() async {
        let source = MockMeetingsDataSource()
        source.meetingsListResult = .failure(NSError(domain: "test", code: 3, userInfo: [NSLocalizedDescriptionKey: "vault unreadable"]))

        let model = MeetingReviewModel(dataSource: source)
        await model.loadLibrary()

        guard case .failed(let message) = model.libraryState else {
            return XCTFail("expected .failed, got \(model.libraryState)")
        }
        XCTAssertEqual(message, "vault unreadable")
    }

    func testSelectMeetingLoadsDetailAndTranscript() async {
        let source = MockMeetingsDataSource()
        source.meetingShowResults["meetings/meeting-m1.md"] = .success(makeDetail())

        let model = MeetingReviewModel(dataSource: source)
        await model.selectMeeting(path: "meetings/meeting-m1.md")

        XCTAssertEqual(model.detailState, .loaded)
        XCTAssertEqual(model.selectedDetail?.frontmatter.meetingID, "m1")
        XCTAssertEqual(model.transcript, "Alice: Hello.")
    }

    // Acceptance criterion: "Missing raw audio/transcript data is shown as
    // unavailable, not treated as note corruption."
    func testMissingTranscriptMarkersReportUnavailableNotError() async {
        let source = MockMeetingsDataSource()
        source.meetingShowResults["meetings/meeting-m1.md"] = .success(makeDetail(body: "no markers here"))

        let model = MeetingReviewModel(dataSource: source)
        await model.selectMeeting(path: "meetings/meeting-m1.md")

        XCTAssertEqual(model.detailState, .loaded)
        XCTAssertNil(model.transcript)
    }

    // Acceptance criterion: corrupt/incompatible note data must not crash
    // or silently show wrong content — it must surface as a clear failure.
    func testCorruptNoteDataSurfacesAsFailureNotCrash() async {
        let source = MockMeetingsDataSource()
        source.meetingShowResults["meetings/meeting-m1.md"] = .failure(
            DecodingError.dataCorrupted(
                DecodingError.Context(codingPath: [], debugDescription: "missing Frontmatter")
            )
        )

        let model = MeetingReviewModel(dataSource: source)
        await model.selectMeeting(path: "meetings/meeting-m1.md")

        guard case .failed(let message) = model.detailState else {
            return XCTFail("expected .failed, got \(model.detailState)")
        }
        XCTAssertTrue(message.contains("could not be read"), "expected a friendly decode-failure message, got \(message)")
        XCTAssertNil(model.selectedDetail)
    }

    // Cancellation / refresh-conflict acceptance criterion: selecting a
    // second meeting while the first is still loading must not let the
    // first (slower) response clobber the second (newer) selection.
    func testStaleSelectionResultIsDiscardedAfterNewerSelection() async {
        let source = MockMeetingsDataSource()
        source.meetingShowDelays["meetings/meeting-slow.md"] = 200_000_000 // 200ms
        source.meetingShowResults["meetings/meeting-slow.md"] = .success(makeDetail(meetingID: "slow"))
        source.meetingShowResults["meetings/meeting-fast.md"] = .success(makeDetail(meetingID: "fast"))

        let model = MeetingReviewModel(dataSource: source)

        let slowLoad = Task { await model.selectMeeting(path: "meetings/meeting-slow.md") }
        try? await Task.sleep(nanoseconds: 20_000_000) // let the slow load start first
        await model.selectMeeting(path: "meetings/meeting-fast.md")
        await slowLoad.value

        XCTAssertEqual(model.selectedPath, "meetings/meeting-fast.md")
        XCTAssertEqual(model.selectedDetail?.frontmatter.meetingID, "fast")
    }

    func testImportSuccessRefreshesLibraryAndSelectsImportedMeeting() async {
        let source = MockMeetingsDataSource()
        source.meetingImportResult = .success("meetings/meeting-m1.md")
        source.meetingsListResult = .success([
            MeetingNoteSummary(path: "meetings/meeting-m1.md", title: "Standup", meetingID: "m1", startedAt: "2026-07-01T10:00:00Z", durationMS: 60000, language: "en", reviewState: "unreviewed")
        ])
        source.meetingShowResults["meetings/meeting-m1.md"] = .success(makeDetail())

        let model = MeetingReviewModel(dataSource: source)
        await model.importMeeting(meetingID: "m1")

        XCTAssertNil(model.importError)
        XCTAssertFalse(model.isImporting)
        XCTAssertEqual(model.selectedPath, "meetings/meeting-m1.md")
        XCTAssertEqual(model.importedMeetings.count, 1)
    }

    // Acceptance criterion: an incompatible artifact schema must be a clear
    // review error, not a partial/silent import.
    func testImportIncompatibleSchemaSurfacesError() async {
        let source = MockMeetingsDataSource()
        source.meetingImportResult = .failure(NSError(domain: "test", code: 4, userInfo: [NSLocalizedDescriptionKey: "unsupported meeting artifact schema version 2 (symdesk supports 1)"]))

        let model = MeetingReviewModel(dataSource: source)
        await model.importMeeting(meetingID: "m1")

        XCTAssertEqual(model.importError, "unsupported meeting artifact schema version 2 (symdesk supports 1)")
        XCTAssertFalse(model.isImporting)
        XCTAssertNil(model.selectedPath)
    }

    // Refresh conflict: the note changed on disk since it was read. Must
    // surface as a clear failure, not a silently discarded refresh.
    func testRefreshConflictSurfacesErrorWithoutClearingSelection() async {
        let source = MockMeetingsDataSource()
        source.meetingShowResults["meetings/meeting-m1.md"] = .success(makeDetail())
        source.meetingRefreshResult = .failure(NSError(domain: "test", code: 5, userInfo: [NSLocalizedDescriptionKey: "meetings/meeting-m1.md changed on disk since it was read; re-run refresh"]))

        let model = MeetingReviewModel(dataSource: source)
        await model.selectMeeting(path: "meetings/meeting-m1.md")
        await model.refreshSelected(apply: true)

        XCTAssertEqual(model.refreshError, "meetings/meeting-m1.md changed on disk since it was read; re-run refresh")
        XCTAssertEqual(model.selectedPath, "meetings/meeting-m1.md")
        XCTAssertNotNil(model.selectedDetail)
    }

    func testRefreshAppliedReloadsSelectedDetail() async {
        let source = MockMeetingsDataSource()
        source.meetingShowResults["meetings/meeting-m1.md"] = .success(makeDetail(body: "\n<!-- symmeet-transcript:start -->\nAlice: Hello.\n<!-- symmeet-transcript:end -->\n"))
        source.meetingRefreshResult = .success(MeetingRefreshOutcome(path: "meetings/meeting-m1.md", changed: true, applied: true))

        let model = MeetingReviewModel(dataSource: source)
        await model.selectMeeting(path: "meetings/meeting-m1.md")

        source.meetingShowResults["meetings/meeting-m1.md"] = .success(makeDetail(body: "\n<!-- symmeet-transcript:start -->\nAlice: Hello, corrected.\n<!-- symmeet-transcript:end -->\n"))
        await model.refreshSelected(apply: true)

        XCTAssertNil(model.refreshError)
        XCTAssertEqual(model.transcript, "Alice: Hello, corrected.")
    }

    // Segments and speakers are best-effort overlays: their failure must
    // degrade to "segments unavailable", never fail the detail view.
    func testSegmentsUnavailableDoesNotFailDetail() async {
        let source = MockMeetingsDataSource()
        source.meetingShowResults["meetings/meeting-m1.md"] = .success(makeDetail())
        source.meetingSegmentsResult = .failure(NSError(domain: "test", code: 6, userInfo: [NSLocalizedDescriptionKey: "symmeet not found on PATH"]))
        source.meetingSpeakersResult = .failure(NSError(domain: "test", code: 6, userInfo: [NSLocalizedDescriptionKey: "symmeet not found on PATH"]))

        let model = MeetingReviewModel(dataSource: source)
        await model.selectMeeting(path: "meetings/meeting-m1.md")

        XCTAssertEqual(model.detailState, .loaded)
        XCTAssertTrue(model.segments.isEmpty)
        XCTAssertEqual(model.segmentsError, "symmeet not found on PATH")
        XCTAssertEqual(model.speakersError, "symmeet not found on PATH")
        XCTAssertEqual(model.transcript, "Alice: Hello.")
    }

    func testSelectMeetingLoadsSegmentsAndSpeakers() async {
        let source = MockMeetingsDataSource()
        source.meetingShowResults["meetings/meeting-m1.md"] = .success(makeDetail())
        source.meetingSegmentsResult = .success([
            MeetingSegment(segmentID: "seg-1", speakerID: "speaker_0", startMS: 0, endMS: 1500, engineText: "Hello."),
            MeetingSegment(segmentID: "seg-2", speakerID: "speaker_1", startMS: 1500, endMS: 4000, engineText: "Hi.", editedText: "Hi!", revision: "user_corrected"),
        ])
        source.meetingSpeakersResult = .success([
            MeetingSpeaker(speakerID: "speaker_0", label: "Alice"),
            MeetingSpeaker(speakerID: "speaker_1", label: "speaker_1"),
        ])

        let model = MeetingReviewModel(dataSource: source)
        await model.selectMeeting(path: "meetings/meeting-m1.md")

        XCTAssertEqual(model.segments.count, 2)
        XCTAssertEqual(model.segments[1].displayText, "Hi!")
        XCTAssertEqual(model.speakers.count, 2)
        XCTAssertNil(model.segmentsError)
    }

    // Speaker corrections must refresh the transcript projection so the
    // applied edit is visible, and surface failures without losing state.
    func testLabelSpeakerRefreshesProjection() async {
        let source = MockMeetingsDataSource()
        source.meetingShowResults["meetings/meeting-m1.md"] = .success(makeDetail())
        source.meetingSpeakersResult = .success([MeetingSpeaker(speakerID: "speaker_0", label: "speaker_0")])

        let model = MeetingReviewModel(dataSource: source)
        await model.selectMeeting(path: "meetings/meeting-m1.md")

        source.meetingSpeakersResult = .success([MeetingSpeaker(speakerID: "speaker_0", label: "Bob")])
        await model.labelSpeaker(speakerID: "speaker_0", label: "Bob")

        XCTAssertNil(model.speakerActionError)
        XCTAssertEqual(model.speakers.first?.label, "Bob")
        let calls = await source.log.calls
        XCTAssertTrue(calls.contains("speakerLabel:speaker_0:Bob"))
        XCTAssertTrue(calls.contains("meetingRefresh:meetings/meeting-m1.md:true"), "expected the transcript to refresh after a correction, calls: \(calls)")
    }

    func testSpeakerMutationFailureSurfacesError() async {
        let source = MockMeetingsDataSource()
        source.meetingShowResults["meetings/meeting-m1.md"] = .success(makeDetail())
        source.speakerMutationError = NSError(domain: "test", code: 7, userInfo: [NSLocalizedDescriptionKey: "unknown speaker id"])

        let model = MeetingReviewModel(dataSource: source)
        await model.selectMeeting(path: "meetings/meeting-m1.md")
        await model.mergeSpeaker(from: "speaker_9", into: "speaker_0")

        XCTAssertEqual(model.speakerActionError, "unknown speaker id")
        XCTAssertFalse(model.isCorrectingSpeaker)
    }

    // Review save: reloads the note (badge flips to reviewed) and the
    // library; a failure surfaces without corrupting state.
    func testMarkReviewedReloadsDetailAndLibrary() async {
        let source = MockMeetingsDataSource()
        source.meetingShowResults["meetings/meeting-m1.md"] = .success(makeDetail())

        let model = MeetingReviewModel(dataSource: source)
        await model.selectMeeting(path: "meetings/meeting-m1.md")
        await model.markReviewed()

        XCTAssertNil(model.reviewSaveError)
        XCTAssertFalse(model.isSavingReview)
        let calls = await source.log.calls
        XCTAssertTrue(calls.contains("markReviewed:meetings/meeting-m1.md"))
        XCTAssertEqual(calls.filter { $0 == "meetingsListReport" }.count, 1, "expected the library to reload after a review save")
    }

    func testMarkReviewedFailureSurfacesError() async {
        let source = MockMeetingsDataSource()
        source.meetingShowResults["meetings/meeting-m1.md"] = .success(makeDetail())
        source.markReviewedError = NSError(domain: "test", code: 8, userInfo: [NSLocalizedDescriptionKey: "note changed on disk"])

        let model = MeetingReviewModel(dataSource: source)
        await model.selectMeeting(path: "meetings/meeting-m1.md")
        await model.markReviewed()

        XCTAssertEqual(model.reviewSaveError, "note changed on disk")
        XCTAssertFalse(model.isSavingReview)
    }

    // Segment navigation: stepping clamps at both ends and playback-time
    // lookup finds the containing segment (the highlight source).
    func testSegmentNavigationAndPlaybackLookup() async {
        let source = MockMeetingsDataSource()
        source.meetingShowResults["meetings/meeting-m1.md"] = .success(makeDetail())
        source.meetingSegmentsResult = .success([
            MeetingSegment(segmentID: "seg-1", speakerID: "speaker_0", startMS: 0, endMS: 1500, engineText: "One."),
            MeetingSegment(segmentID: "seg-2", speakerID: "speaker_0", startMS: 1500, endMS: 4000, engineText: "Two."),
        ])

        let model = MeetingReviewModel(dataSource: source)
        await model.selectMeeting(path: "meetings/meeting-m1.md")

        XCTAssertEqual(model.stepSegment(1)?.segmentID, "seg-1")
        XCTAssertEqual(model.stepSegment(1)?.segmentID, "seg-2")
        XCTAssertEqual(model.stepSegment(1)?.segmentID, "seg-2", "stepping past the end must clamp")
        XCTAssertEqual(model.stepSegment(-1)?.segmentID, "seg-1")
        XCTAssertEqual(model.stepSegment(-1)?.segmentID, "seg-1", "stepping before the start must clamp")

        XCTAssertEqual(model.segment(at: 0)?.segmentID, "seg-1")
        XCTAssertEqual(model.segment(at: 1500)?.segmentID, "seg-2")
        XCTAssertNil(model.segment(at: 4000))
    }

    func testClearSelectionResetsDetailState() async {
        let source = MockMeetingsDataSource()
        source.meetingShowResults["meetings/meeting-m1.md"] = .success(makeDetail())

        let model = MeetingReviewModel(dataSource: source)
        await model.selectMeeting(path: "meetings/meeting-m1.md")
        model.clearSelection()

        XCTAssertNil(model.selectedPath)
        XCTAssertNil(model.selectedDetail)
        XCTAssertEqual(model.detailState, .idle)
        XCTAssertTrue(model.segments.isEmpty)
        XCTAssertTrue(model.speakers.isEmpty)
        XCTAssertNil(model.selectedSegmentID)
    }

    // MARK: - Participant confirmation and publish

    func testParticipantCandidatesReturnsCandidatesFromDataSource() async throws {
        let source = MockMeetingsDataSource()
        let expected = ParticipantCandidate(entityID: "e-alice", name: "Alice Example", matchReason: "exact_name")
        source.participantCandidatesResult = .success([expected])

        let model = MeetingReviewModel(dataSource: source)
        let candidates = try await model.participantCandidates(label: "Alice")

        XCTAssertEqual(candidates, [expected])
        let calls = await source.log.calls
        XCTAssertTrue(calls.contains("candidates:Alice"))
    }

    func testConfirmParticipantLinksAndReloadsDetail() async {
        let source = MockMeetingsDataSource()
        source.meetingShowResults["meetings/meeting-m1.md"] = .success(makeDetail())

        let model = MeetingReviewModel(dataSource: source)
        await model.selectMeeting(path: "meetings/meeting-m1.md")
        await model.confirmParticipant(speakerID: "speaker_0", entityID: "e-alice")

        XCTAssertNil(model.participantActionError)
        XCTAssertFalse(model.isConfirmingParticipant)
        let calls = await source.log.calls
        XCTAssertTrue(calls.contains("confirm:speaker_0:e-alice"))
        XCTAssertEqual(calls.filter { $0 == "meetingShow:meetings/meeting-m1.md" }.count, 2, "expected the detail to reload after a successful confirm, calls: \(calls)")
    }

    func testConfirmParticipantUnlinkPassesNilEntityID() async {
        let source = MockMeetingsDataSource()
        source.meetingShowResults["meetings/meeting-m1.md"] = .success(makeDetail())

        let model = MeetingReviewModel(dataSource: source)
        await model.selectMeeting(path: "meetings/meeting-m1.md")
        await model.confirmParticipant(speakerID: "speaker_0", entityID: nil)

        let calls = await source.log.calls
        XCTAssertTrue(calls.contains("confirm:speaker_0:<unlink>"))
    }

    func testCreateParticipantCreatesAndReloadsDetail() async {
        let source = MockMeetingsDataSource()
        source.meetingShowResults["meetings/meeting-m1.md"] = .success(makeDetail())

        let model = MeetingReviewModel(dataSource: source)
        await model.selectMeeting(path: "meetings/meeting-m1.md")
        await model.createParticipant(speakerID: "speaker_0", name: "New Person")

        XCTAssertNil(model.participantActionError)
        let calls = await source.log.calls
        XCTAssertTrue(calls.contains("create:speaker_0:New Person"))
    }

    func testConfirmParticipantFailureSurfacesError() async {
        let source = MockMeetingsDataSource()
        source.meetingShowResults["meetings/meeting-m1.md"] = .success(makeDetail())
        source.participantActionError = NSError(domain: "test", code: 9, userInfo: [NSLocalizedDescriptionKey: "unknown entity id"])

        let model = MeetingReviewModel(dataSource: source)
        await model.selectMeeting(path: "meetings/meeting-m1.md")
        await model.confirmParticipant(speakerID: "speaker_0", entityID: "e-alice")

        XCTAssertEqual(model.participantActionError, "unknown entity id")
        XCTAssertFalse(model.isConfirmingParticipant)
    }

    func testPublishSendsFactsAndStoresOutcome() async {
        let source = MockMeetingsDataSource()
        source.meetingShowResults["meetings/meeting-m1.md"] = .success(makeDetail())
        source.publishResult = .success(
            MeetingPublishOutcome(meetingEntityID: "e-meeting", relationsCreated: 1, factsPublished: ["mem-1"], factsSkipped: 0)
        )

        let model = MeetingReviewModel(dataSource: source)
        await model.selectMeeting(path: "meetings/meeting-m1.md")
        await model.publish(facts: ["Alice proposed the roadmap."])

        XCTAssertNil(model.publishError)
        XCTAssertEqual(model.lastPublish?.relationsCreated, 1)
        XCTAssertEqual(model.lastPublish?.factsPublished, ["mem-1"])
        let calls = await source.log.calls
        XCTAssertTrue(calls.contains("publish:meetings/meeting-m1.md:Alice proposed the roadmap."))
    }

    // Doc-comment guarantee: a failed publish may still have written some
    // items, so the error must not clobber a previously stored outcome.
    func testPublishFailureAfterPriorSuccessDoesNotClearLastPublish() async {
        let source = MockMeetingsDataSource()
        source.meetingShowResults["meetings/meeting-m1.md"] = .success(makeDetail())

        let model = MeetingReviewModel(dataSource: source)
        await model.selectMeeting(path: "meetings/meeting-m1.md")

        let firstOutcome = MeetingPublishOutcome(meetingEntityID: "e-meeting", relationsCreated: 1, factsPublished: ["mem-1"], factsSkipped: 0)
        source.publishResult = .success(firstOutcome)
        await model.publish(facts: ["Alice proposed the roadmap."])
        XCTAssertEqual(model.lastPublish, firstOutcome)

        source.publishResult = .failure(NSError(domain: "test", code: 10, userInfo: [NSLocalizedDescriptionKey: "symmemory not found on PATH"]))
        await model.publish(facts: ["Bob agreed to the timeline."])

        XCTAssertEqual(model.publishError, "symmemory not found on PATH")
        XCTAssertEqual(model.lastPublish, firstOutcome, "a failed publish must not clear a prior successful outcome")
    }
}
