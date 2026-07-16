import Foundation
import SymDeskCore

/// Drives the Meetings library and review workspace: loading the imported
/// and available-to-import lists, loading one meeting's detail on
/// selection, importing, and refreshing. Selection/detail loads are
/// generation-guarded so that switching the selection mid-load never lets a
/// stale response overwrite state for a newer selection (the "refresh
/// conflicts" and "cancellation" acceptance criteria in #172).
@MainActor
final class MeetingReviewModel: ObservableObject {
    enum LoadState: Equatable {
        case idle
        case loading
        case loaded
        /// A friendly, non-technical message. `symmeet` being absent or a
        /// note failing to parse both land here rather than crashing the
        /// workspace — SymDesk must stay usable with `symmeet` absent.
        case failed(String)
    }

    @Published private(set) var importedMeetings: [MeetingNoteSummary] = []
    @Published private(set) var libraryState: LoadState = .idle

    /// Available-to-import meetings are tracked separately from
    /// `libraryState`: `symmeet` being absent must not blank out an already
    /// loaded list of imported meetings, so its failure is surfaced here
    /// instead of forcing the whole library into a failed state.
    @Published private(set) var availableMeetings: [AvailableMeeting] = []
    @Published private(set) var availableMeetingsError: String?

    @Published private(set) var selectedPath: String?
    @Published private(set) var selectedDetail: MeetingDetail?
    @Published private(set) var detailState: LoadState = .idle

    @Published private(set) var isImporting = false
    @Published private(set) var importError: String?

    @Published private(set) var isRefreshing = false
    @Published private(set) var refreshError: String?
    @Published private(set) var lastRefresh: MeetingRefreshOutcome?

    private let dataSource: MeetingsDataSource
    private var detailLoadToken = UUID()

    init(dataSource: MeetingsDataSource) {
        self.dataSource = dataSource
    }

    /// The reviewed transcript text for the current selection, or `nil`
    /// when the note has no transcript markers (missing/unavailable
    /// transcript data, not a decode error).
    var transcript: String? {
        guard let body = selectedDetail?.body else { return nil }
        return TranscriptExtractor.transcript(from: body)
    }

    func loadLibrary() async {
        libraryState = .loading
        do {
            importedMeetings = try await dataSource.meetingsList()
            libraryState = .loaded
        } catch {
            libraryState = .failed(Self.friendlyMessage(for: error))
        }

        do {
            availableMeetings = try await dataSource.meetingsAvailable()
            availableMeetingsError = nil
        } catch {
            // symmeet not being on PATH (or any other lookup failure) only
            // affects the "available to import" section; the imported
            // library above must remain usable regardless.
            availableMeetings = []
            availableMeetingsError = Self.friendlyMessage(for: error)
        }
    }

    /// Selects a meeting note by vault-relative path and loads its detail.
    /// A prior in-flight load for a different selection is superseded, not
    /// cancelled out from under a caller — its result is simply discarded
    /// when it lands after a newer selection has already been made.
    func selectMeeting(path: String) async {
        selectedPath = path
        selectedDetail = nil
        detailState = .loading
        let token = UUID()
        detailLoadToken = token

        do {
            let detail = try await dataSource.meetingShow(path: path)
            guard detailLoadToken == token else { return }
            selectedDetail = detail
            detailState = .loaded
        } catch {
            guard detailLoadToken == token else { return }
            detailState = .failed(Self.friendlyMessage(for: error))
        }
    }

    func clearSelection() {
        detailLoadToken = UUID()
        selectedPath = nil
        selectedDetail = nil
        detailState = .idle
    }

    /// Imports an available SymMeet meeting and, on success, refreshes the
    /// library and selects the newly imported note.
    func importMeeting(meetingID: String) async {
        guard !isImporting else { return }
        isImporting = true
        importError = nil
        defer { isImporting = false }

        do {
            let path = try await dataSource.meetingImport(meetingID: meetingID)
            await loadLibrary()
            await selectMeeting(path: path)
        } catch {
            importError = Self.friendlyMessage(for: error)
        }
    }

    /// Applies a transcript refresh for the current selection. Surfaces a
    /// conflict (the note changed on disk since it was read) as a clear
    /// failure rather than silently discarding the user's other edits.
    func refreshSelected(apply: Bool) async {
        guard let path = selectedPath, !isRefreshing else { return }
        isRefreshing = true
        refreshError = nil
        defer { isRefreshing = false }

        do {
            let outcome = try await dataSource.meetingRefresh(path: path, apply: apply)
            lastRefresh = outcome
            if outcome.applied {
                await selectMeeting(path: path)
            }
        } catch {
            refreshError = Self.friendlyMessage(for: error)
        }
    }

    /// Maps a transport/decode failure to a message safe to show directly
    /// in the UI. Decode failures (corrupt or schema-incompatible notes)
    /// and process failures (symmeet absent, non-zero exit) both resolve to
    /// their underlying description rather than a generic "something went
    /// wrong" — the acceptance criteria call for the actual reason
    /// (missing audio, incompatible schema, ...) to be visible.
    private static func friendlyMessage(for error: Error) -> String {
        if let decodingError = error as? DecodingError {
            return "This meeting note could not be read: \(decodingError.briefDescription)"
        }
        return error.localizedDescription
    }
}

private extension DecodingError {
    var briefDescription: String {
        switch self {
        case .keyNotFound(let key, _):
            return "missing expected field \"\(key.stringValue)\""
        case .typeMismatch(_, let context), .valueNotFound(_, let context), .dataCorrupted(let context):
            return context.debugDescription.isEmpty ? "malformed data" : context.debugDescription
        @unknown default:
            return "malformed data"
        }
    }
}
