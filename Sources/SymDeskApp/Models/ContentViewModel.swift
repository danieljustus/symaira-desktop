import SwiftUI
import AppKit
import Combine
import SymDeskCore

/// Captures the full navigation context for history tracking.
struct NavEntry: Equatable {
    let displayMode: ContentDisplayMode
    let notePath: String?
    let docFilterID: String
    let tagFilter: String?
    let deepLinkDocPath: String?
    let deepLinkAnchor: SearchAnchor?
    let selectedViewID: String?
}

/// Navigation display modes supported by SymDesk.
enum ContentDisplayMode: Equatable {
    case dashboard
    case vault
    case graph
    case dbView
    case docs
    case discover
    case ingestQueue
    case reviewLane
    case rules
    case meetings
    case companionTools
    case history
    case trash
    case models
    case duplicates
    case notebooks
    case room
    case retrievalStatus
}

/// Ingest failure representation for alerts.
struct IngestFailure: Equatable {
    let url: URL
    let message: String
}

/// Drives the main workspace state, navigation, note editing, and async actions
/// for ContentView and its child feature views.
@MainActor
final class ContentViewModel: ObservableObject {
    typealias DisplayMode = ContentDisplayMode

    // MARK: - Notes and Vault State

    @Published var notes: [Note] = []
    @Published var noteLookup: [String: Note] = [:]
    @Published var selectedNote: Note? = nil
    @Published var noteContent: String = ""
    @Published var loadError: String? = nil
    /// Set together with loadError when the note's backing file could not be
    /// read — the banner then offers "Remove from index" (issue #650).
    @Published var loadErrorNote: Note? = nil

    // MARK: - Diagnostics State

    @Published var doctorStatus: String = "Checking..."
    @Published var doctorReport: DoctorReport? = nil
    @Published var isShowingDoctorPopover = false

    // MARK: - UI & Sheet State

    @Published var isShowingPalette = false
    @Published var isShowingInspector = false
    @Published var isShowingPreview = false
    @Published var isShowingAIDock = false
    @Published var isShowingNewNoteSheet = false
    @Published var newNoteTitle = ""
    @Published var backlinks: [String] = []
    @Published var backlinksError: String? = nil

    /// Ephemeral error banners shown at the top of the main content area.
    @Published var appErrors: [AppErrorMessage] = []

    // MARK: - Folder Tree State

    @Published var expandedFolders: Set<String> = []
    @Published var folderTree: [FolderNode] = []

    // MARK: - Mutations & Safety

    let mutationTracker = AsyncActionTracker<String>()
    @Published var ingestFailure: IngestFailure? = nil

    /// Note selected via a context menu for the version-history screen.
    @Published var historyInitialNotePath: String? = nil
    /// Note the user asked to move to the trash (context menu, issue #307).
    @Published var pendingTrashNote: Note? = nil

    // MARK: - Navigation State

    @Published var displayMode: ContentDisplayMode = .dashboard
    @Published var selectedViewID: String? = nil
    @Published var dbViews: [DbView] = []
    @Published var isShowingViewEditor = false
    @Published var editingDbView: DbView? = nil
    @Published var docFilterID: String = "all"
    @Published var docCounts: [String: Int] = [:]
    @Published var docTypeCounts: [String: Int] = [:]
    @Published var docTotalCount: Int = 0
    @Published var deepLinkDocPath: String? = nil
    @Published var deepLinkAnchor: SearchAnchor? = nil

    // Navigation history stacks
    @Published var navBackStack: [NavEntry] = []
    @Published var navForwardStack: [NavEntry] = []

    // Tag browsing
    @Published var tagCounts: [TagEntry] = []
    @Published var tagFilter: String? = nil

    // MARK: - Tasks & Monitors

    private var saveTask: Task<Void, Never>? = nil
    private var eventRefreshTask: Task<Void, Never>? = nil
    private var keyEventMonitor: Any?
    private var mutationTrackerCancellable: AnyCancellable?

    init() {
        mutationTrackerCancellable = mutationTracker.objectWillChange.sink { [weak self] _ in
            self?.objectWillChange.send()
        }
    }

    isolated deinit {
        saveTask?.cancel()
        eventRefreshTask?.cancel()
        if let keyEventMonitor {
            NSEvent.removeMonitor(keyEventMonitor)
        }
    }

    // MARK: - Navigation History Helpers

    var canGoBack: Bool { !navBackStack.isEmpty }
    var canGoForward: Bool { !navForwardStack.isEmpty }

    /// Snapshots the current navigation state.
    private func makeNavEntry() -> NavEntry {
        NavEntry(
            displayMode: displayMode,
            notePath: selectedNote?.path,
            docFilterID: docFilterID,
            tagFilter: tagFilter,
            deepLinkDocPath: deepLinkDocPath,
            deepLinkAnchor: deepLinkAnchor,
            selectedViewID: selectedViewID
        )
    }

    /// Restores a navigation state, re-resolving the note from the current
    /// notes list since Note is a value type (new instance after refresh).
    private func applyNavEntry(_ entry: NavEntry) {
        displayMode = entry.displayMode
        selectedNote = entry.notePath.flatMap { path in notes.first(where: { $0.path == path }) }
        docFilterID = entry.docFilterID
        tagFilter = entry.tagFilter
        deepLinkDocPath = entry.deepLinkDocPath
        deepLinkAnchor = entry.deepLinkAnchor
        selectedViewID = entry.selectedViewID
    }

    /// Navigate to a new destination, pushing the current state onto the
    /// back stack so the user can return with the back button.
    func navigate(
        to mode: ContentDisplayMode,
        note: Note? = nil,
        docFilter: String? = nil,
        tagFilter: String? = nil,
        deepLinkPath: String? = nil,
        deepLinkAnchor: SearchAnchor? = nil,
        viewID: String? = nil
    ) {
        navBackStack.append(makeNavEntry())
        navForwardStack.removeAll()
        displayMode = mode
        if let note = note { selectedNote = note }
        if let docFilter = docFilter { docFilterID = docFilter }
        if let tagFilter = tagFilter { self.tagFilter = tagFilter }
        if let deepLinkPath = deepLinkPath { deepLinkDocPath = deepLinkPath }
        if let deepLinkAnchor = deepLinkAnchor { self.deepLinkAnchor = deepLinkAnchor }
        if let viewID = viewID { selectedViewID = viewID }
    }

    /// Go back one step in navigation history.
    func goBack() {
        guard let entry = navBackStack.popLast() else { return }
        navForwardStack.append(makeNavEntry())
        applyNavEntry(entry)
    }

    /// Go forward one step in navigation history.
    func goForward() {
        guard let entry = navForwardStack.popLast() else { return }
        navBackStack.append(makeNavEntry())
        applyNavEntry(entry)
    }

    func navigateToNote(title: String) {
        if let found = noteLookup[title.lowercased()] {
            navigate(to: .vault, note: found)
        }
    }

    /// Opens a notebook source or citation by its vault-relative path
    /// (issue #427) — unlike `navigateToNote`, which matches by title.
    func openNotebookSourcePath(_ path: String) {
        if let found = notes.first(where: { $0.path == path }) {
            navigate(to: .vault, note: found)
        }
    }

    /// Opens a Markdown document from the document library in the note
    /// editor (issue #648): resolves the document to a note and navigates.
    func openDocumentInEditor(_ doc: DocumentItem) {
        guard let found = notes.first(where: { $0.path == doc.path }) else { return }
        navigate(to: .vault, note: found)
    }

    func openDiscover() { navigate(to: .discover) }
    func openDashboard() { navigate(to: .dashboard) }

    // MARK: - Editor Surface Toggles (issue #651)

    /// Opens the AI chat dock (and focuses the inspector beside it). Shared
    /// by the toolbar button and the View-menu command so both stay in sync.
    func openAIDock() {
        isShowingAIDock = true
        isShowingInspector = true
    }

    /// Shows or hides the properties inspector. Shared by the toolbar button
    /// and the View-menu command (issue #651).
    func toggleInspector() {
        isShowingAIDock = false
        isShowingInspector.toggle()
    }

    // MARK: - Initial Load & Refresh

    func initialLoad(core: DeskCore) async {
        await fetchNotes(core: core)
        await fetchViews(core: core)
        await fetchDoctor(core: core)
        await fetchDocCounts(core: core)
        await fetchTagCounts(core: core)
        await reconcileIndex(core: core)
        restoreExpandedFolders()
    }

    func restoreExpandedFolders() {
        if let data = UserDefaults.standard.data(forKey: "sidebarExpandedFolders"),
           let folders = try? JSONDecoder().decode(Set<String>.self, from: data) {
            expandedFolders = folders
        }
    }

    func persistExpandedFolders(_ newValue: Set<String>) {
        if let data = try? JSONEncoder().encode(newValue) {
            UserDefaults.standard.set(data, forKey: "sidebarExpandedFolders")
        }
    }

    /// The active vault changed: drop all per-vault state and reload so no
    /// rows leak across vaults (issue #296).
    func reloadAfterVaultSwitch(core: DeskCore) {
        selectedNote = nil
        notes = []
        noteLookup = [:]
        noteContent = ""
        folderTree = []
        expandedFolders = []
        tagCounts = []
        Task {
            await fetchNotes(core: core)
            await fetchViews(core: core)
            await fetchDoctor(core: core)
            await fetchDocCounts(core: core)
            await fetchTagCounts(core: core)
            await reconcileIndex(core: core)
        }
    }

    /// Applies a freshly created note: refreshes the note lists and selects
    /// the new note right away instead of waiting for the watcher (#647).
    func applyCreatedNote(_ refreshedNotes: [Note], created: Note?, vaultPath: String?) {
        self.notes = refreshedNotes
        self.folderTree = buildFolderTree(from: refreshedNotes, vaultPath: vaultPath)
        if let created {
            self.selectedNote = created
            self.displayMode = .vault
        }
    }

    /// File watchers commonly emit several events for one atomic save. Wait
    /// for the burst to settle before refreshing lists, counts, and reminders.
    func scheduleEventRefresh(_ event: VaultEvent?, core: DeskCore, notificationManager: NotificationManager) {
        eventRefreshTask?.cancel()
        eventRefreshTask = Task {
            try? await Task.sleep(nanoseconds: 250_000_000)
            guard !Task.isCancelled else { return }
            await fetchNotes(core: core)
            await fetchDocCounts(core: core)
            await fetchTagCounts(core: core)
            if let selected = selectedNote, event?.path == selected.path {
                await loadContent(for: selected, core: core)
            }
            await notificationManager.refreshNotifications(with: core)
        }
    }

    // MARK: - Data Fetching

    func fetchNotes(core: DeskCore) async {
        do {
            let loadedNotes = try await core.listFiles()
            self.notes = loadedNotes
            self.folderTree = buildFolderTree(from: loadedNotes, vaultPath: core.vaultPath)
            var lookup: [String: Note] = [:]
            lookup.reserveCapacity(loadedNotes.count * 3)
            for note in loadedNotes {
                lookup[note.title.lowercased()] = note
                let base = (note.path as NSString).lastPathComponent
                lookup[base.lowercased()] = note
                lookup[(base as NSString).deletingPathExtension.lowercased()] = note
            }
            noteLookup = lookup
        } catch {
            appErrors.append(AppErrorMessage(
                message: "Failed to load notes: \(error.localizedDescription)",
                detail: "The vault may be inaccessible or the core CLI may not be running."
            ))
        }
    }

    func fetchViews(core: DeskCore) async {
        do {
            self.dbViews = try await core.viewsList()
        } catch {
            appErrors.append(AppErrorMessage(
                message: "Failed to load saved views: \(error.localizedDescription)"
            ))
        }
    }

    var doctorSummaryText: String {
        guard let report = doctorReport else {
            return "Vault check unavailable — run `symdesk doctor`"
        }
        var issues: [String] = []
        if let v = report.vault, v.status != "ok" { issues.append("Vault") }
        if let s = report.sidecar, s.status != "ok" { issues.append("Sidecar") }
        if let c = report.contract, c.status != "ok" { issues.append("Contract") }
        
        let aiProvider = (report.ai?.provider ?? "Ollama").capitalized
        if issues.isEmpty && report.overall == "ok" {
            return "Vault healthy · AI: \(aiProvider)"
        } else if !issues.isEmpty {
            return "Vault: \(issues.joined(separator: ", ")) issue · AI: \(aiProvider)"
        } else {
            return "Vault warning · AI: \(aiProvider)"
        }
    }

    func fetchDoctor(core: DeskCore) async {
        do {
            let report = try await core.getDoctorReport()
            self.doctorReport = report
            self.doctorStatus = doctorSummaryText
        } catch {
            self.doctorReport = nil
            self.doctorStatus = "Vault check unavailable"
            appErrors.append(AppErrorMessage(
                message: "Doctor check failed: \(error.localizedDescription)",
                detail: "Run `symdesk doctor` in a terminal for detailed diagnostics."
            ))
        }
    }

    func fetchDocCounts(core: DeskCore) async {
        do {
            let all = try await core.docsList()
            docTotalCount = all.count
            var counts: [String: Int] = [:]
            var typeCounts: [String: Int] = [:]
            for doc in all {
                let key = doc.status.isEmpty ? "unset" : doc.status
                counts[key, default: 0] += 1
                if !doc.type.isEmpty {
                    typeCounts[doc.type, default: 0] += 1
                }
            }
            docCounts = counts
            docTypeCounts = typeCounts
        } catch {
            appErrors.append(AppErrorMessage(
                message: "Failed to load document counts: \(error.localizedDescription)"
            ))
        }
    }

    func fetchTagCounts(core: DeskCore) async {
        guard !notes.isEmpty else { return }
        do {
            tagCounts = try await TagStore.aggregate(from: notes, vaultPath: core.vaultPath, isRemote: core.isRemote, core: core)
        } catch {
            print("fetchTagCounts failed: \(error)")
        }
    }

    func reconcileIndex(core: DeskCore) async {
        _ = try? await core.reindexVault(prune: true)
        await fetchNotes(core: core)
    }

    // MARK: - Content Loading & Saving

    func loadContent(for note: Note, core: DeskCore) async {
        self.loadError = nil
        self.loadErrorNote = nil
        if core.isRemote {
            do {
                self.noteContent = try await core.docNoteContent(path: note.path)
            } catch {
                self.loadError = "Error reading file \(note.path): \(error.localizedDescription)"
                self.loadErrorNote = note
            }
            return
        }
        guard let path = absoluteNotePath(note.path, vaultPath: core.vaultPath) else {
            self.loadError = "Error reading file \(note.path): no vault is configured."
            return
        }
        if let data = FileManager.default.contents(atPath: path),
           let string = String(data: data, encoding: .utf8) {
            self.noteContent = string
        } else {
            // The file is gone or unreadable — name it and offer a way out
            // instead of a dead-end banner (issue #650).
            self.loadError = "Error reading file \(note.path): the file may have been deleted or moved outside the app."
            self.loadErrorNote = note
        }
    }

    func loadBacklinks(for note: Note, core: DeskCore) async {
        do {
            self.backlinks = try await core.backlinks(for: note.path)
            self.backlinksError = nil
        } catch {
            self.backlinks = []
            self.backlinksError = "Could not load backlinks: \(error.localizedDescription)"
        }
    }

    func debouncedSave(note: Note, content: String, core: DeskCore) {
        saveTask?.cancel()
        saveTask = Task {
            try? await Task.sleep(nanoseconds: 500_000_000) // 500ms
            guard !Task.isCancelled else { return }
            await performSave(note: note, content: content, core: core)
        }
    }

    func saveActionID(_ note: Note) -> String {
        "save:\(note.path)"
    }

    func performSave(note: Note, content: String, core: DeskCore) async {
        await mutationTracker.run(saveActionID(note)) {
            if core.isRemote {
                try await core.saveNoteContent(path: note.path, content: content)
            } else if let path = absoluteNotePath(note.path, vaultPath: core.vaultPath) {
                try await core.saveNoteContent(path: path, content: content)
            }
        }
    }

    func viewDeleteActionID(_ view: DbView) -> String {
        "view-delete:\(view.id)"
    }

    func deleteView(_ view: DbView, core: DeskCore) async {
        let succeeded = await mutationTracker.run(viewDeleteActionID(view)) {
            try await core.viewsDelete(id: view.id)
        }
        guard succeeded else { return }
        if selectedViewID == view.id { selectedViewID = nil }
        await fetchViews(core: core)
    }

    func conflictActionID(note: Note, action: String) -> String {
        "conflict:\(note.path):\(action)"
    }

    func resolveConflict(note: Note, action: String, core: DeskCore) async {
        let succeeded = await mutationTracker.run(conflictActionID(note: note, action: action)) {
            try await core.resolveConflict(path: note.path, action: action)
        }
        guard succeeded else { return }
        if selectedNote?.path == note.path {
            self.selectedNote = nil
        }
        await fetchNotes(core: core)
    }

    func ingestActionID(_ url: URL) -> String {
        "ingest:\(url.path)"
    }

    func handleDrop(_ providers: [NSItemProvider], core: DeskCore) -> Bool {
        for provider in providers {
            provider.loadItem(forTypeIdentifier: "public.file-url", options: nil) { item, _ in
                guard let data = item as? Data,
                      let url = URL(dataRepresentation: data, relativeTo: nil) else { return }
                Task { @MainActor in
                    await self.ingestFile(url, core: core)
                }
            }
        }
        return true
    }

    func ingestFile(_ url: URL, core: DeskCore) async {
        let actionID = ingestActionID(url)
        await mutationTracker.run(actionID) {
            _ = try await core.ingest(fileURL: url)
        }
        if let message = mutationTracker.failureMessage(for: actionID) {
            ingestFailure = IngestFailure(url: url, message: message)
            mutationTracker.clearFailure(for: actionID)
        } else if ingestFailure?.url == url {
            ingestFailure = nil
        }
    }

    /// Resolves a transclusion target ("Note Title" or relative path without
    /// extension) to the note's raw Markdown for the preview embeds.
    func resolveNoteContent(_ target: String, vaultPath: String?, isRemote: Bool) -> String? {
        guard !isRemote else { return nil }
        guard let found = noteLookup[target.lowercased()],
              let path = absoluteNotePath(found.path, vaultPath: vaultPath),
              let data = FileManager.default.contents(atPath: path) else { return nil }
        return String(data: data, encoding: .utf8)
    }

    func absoluteNotePath(_ path: String, vaultPath: String?) -> String? {
        DocumentPreviewResolver.noteURL(documentPath: path, vaultPath: vaultPath)?.path
    }

    /// Converts an absolute note path into a vault-relative path expected by
    /// the core's property mutation commands.
    func vaultRelativePath(_ path: String, vaultPath: String?) -> String {
        guard let vault = vaultPath, !vault.isEmpty else { return path }
        let prefix = vault.hasSuffix("/") ? vault : vault + "/"
        if path.hasPrefix(prefix) {
            return String(path.dropFirst(prefix.count))
        }
        return path
    }

    func isConflicted(_ note: Note) -> Bool {
        return note.path.contains(" 2.md") || note.path.contains("conflicted copy")
    }

    /// Moves a note to the vault trash (restorable via the Trash screen) and
    /// refreshes the note list. Failures surface as a visible banner
    /// (issue #307).
    func moveToTrash(_ note: Note, core: DeskCore) async {
        do {
            try await core.noteDelete(path: note.path)
            if selectedNote?.id == note.id {
                selectedNote = nil
                noteContent = ""
            }
            await fetchNotes(core: core)
            await fetchTagCounts(core: core)
            await fetchDocCounts(core: core)
        } catch {
            appErrors.append(AppErrorMessage(
                message: "Could not move note to trash: \(error.localizedDescription)",
                detail: "The note was left in place."
            ))
        }
    }

    /// Runs a vault-wide tag operation (rename/merge/delete), refreshes the
    /// tag list and notes, and surfaces failures as a visible banner
    /// (issue #306).
    func runTagOperation(_ operation: () async throws -> Void, core: DeskCore) async {
        do {
            try await operation()
            await fetchNotes(core: core)
            await fetchTagCounts(core: core)
            await fetchDocCounts(core: core)
        } catch {
            appErrors.append(AppErrorMessage(
                message: "Tag operation failed: \(error.localizedDescription)",
                detail: "No files were changed by the failed operation."
            ))
        }
    }

    func dismissAppError(_ err: AppErrorMessage) {
        appErrors.removeAll { $0.id == err.id }
    }

    func aiDockContext(vaultPath: String?) -> DeskChatContext? {
        let document = selectedNote.map { vaultRelativePath($0.path, vaultPath: vaultPath) }
        let excerpt = DeskChatContext.boundedExcerpt(noteContent)
        let scope: String? = {
            if let selectedViewID {
                return "\(displayMode) / view \(selectedViewID)"
            }
            return String(describing: displayMode)
        }()
        let recent = notes.prefix(4).map { vaultRelativePath($0.path, vaultPath: vaultPath) }
        let context = DeskChatContext(
            activeDocument: document,
            visibleExcerpt: excerpt,
            scope: scope,
            recentDocuments: Array(recent)
        )
        return context.isEmpty ? nil : context
    }

    // MARK: - Key Event Monitor

    func installKeyEventMonitor() {
        guard keyEventMonitor == nil else { return }
        keyEventMonitor = NSEvent.addLocalMonitorForEvents(matching: .keyDown) { [weak self] event in
            if event.modifierFlags.contains(.command) && event.charactersIgnoringModifiers == "k" {
                self?.isShowingPalette.toggle()
                return nil
            }
            return event
        }
    }

    func cleanup() {
        saveTask?.cancel()
        saveTask = nil
        eventRefreshTask?.cancel()
        eventRefreshTask = nil
        if let keyEventMonitor {
            NSEvent.removeMonitor(keyEventMonitor)
            self.keyEventMonitor = nil
        }
    }
}
