import SymairaTheme

/// The seven top-level destinations of the SymDesk workspace (GUIDELINES
/// SHELL-3, 3 to 7 destinations). Each groups one or more content modes; a
/// destination with several modes shows them as a segmented section bar.
/// Configuration and maintenance screens live in the Settings window
/// (`DeskSettingsTab`), never in the sidebar.
enum DeskDestination: String, CaseIterable, Identifiable, Hashable {
    case overview, notes, library, inbox, meetings, knowledge, safetyNet

    var id: String { rawValue }

    var title: String {
        switch self {
        case .overview: "Dashboard"
        case .notes: "Notes"
        case .library: "Library"
        case .inbox: "Inbox"
        case .meetings: "Meetings"
        case .knowledge: "Knowledge"
        case .safetyNet: "Safety Net"
        }
    }

    var icon: SymairaIcon {
        switch self {
        case .overview: .dashboard
        case .notes: .note
        case .library: .library
        case .inbox: .inbox
        case .meetings: .team
        case .knowledge: .ai
        case .safetyNet: .shield
        }
    }

    /// Content modes reachable inside this destination, in section-bar order.
    var modes: [ContentDisplayMode] {
        switch self {
        case .overview: [.dashboard]
        case .notes: [.vault, .graph]
        case .library: [.docs, .dbView]
        case .inbox: [.ingestQueue, .reviewLane]
        case .meetings: [.meetings]
        case .knowledge: [.discover, .notebooks, .room]
        case .safetyNet: [.history, .trash, .duplicates]
        }
    }

    /// The destination that owns a content mode, or nil for modes hosted in
    /// the Settings window.
    static func owning(_ mode: ContentDisplayMode) -> DeskDestination? {
        allCases.first { $0.modes.contains(mode) }
    }

    /// Ordered entries for `SymairaCommands` (⌘1 to ⌘7).
    static var commandEntries: [(value: DeskDestination, label: String)] {
        allCases.map { (value: $0, label: $0.title) }
    }
}

extension ContentDisplayMode {
    /// Section-bar label inside a multi-mode destination.
    var sectionTitle: String {
        switch self {
        case .dashboard: "Dashboard"
        case .vault: "Editor"
        case .graph: "Graph"
        case .docs: "Documents"
        case .dbView: "Saved Views"
        case .ingestQueue: "Ingest Queue"
        case .reviewLane: "Review Lane"
        case .meetings: "Meetings"
        case .discover: "Discover"
        case .notebooks: "Notebooks"
        case .room: "Project Journal"
        case .history: "Version History"
        case .trash: "Trash"
        case .duplicates: "Possible Duplicates"
        case .rules: "Rules"
        case .retrievalStatus: "Search Index"
        case .companionTools: "Companion Tools"
        case .models: "Local Models"
        }
    }

    /// The Settings tab that hosts this mode, if it is not a workspace page.
    var settingsTab: DeskSettingsTab? {
        switch self {
        case .rules: .rules
        case .retrievalStatus: .searchIndex
        case .companionTools: .companionTools
        default: nil
        }
    }
}

/// Tabs of the native Settings window (SHELL-7). The raw value is persisted
/// under `settings.selectedTab`.
enum DeskSettingsTab: String, CaseIterable, Identifiable {
    case general, rules, searchIndex, companionTools

    var id: String { rawValue }

    var title: String {
        switch self {
        case .general: "General"
        case .rules: "Rules"
        case .searchIndex: "Search Index"
        case .companionTools: "Companion Tools"
        }
    }

    var icon: SymairaIcon {
        switch self {
        case .general: .settings
        case .rules: .workflow
        case .searchIndex: .documentSearch
        case .companionTools: .tools
        }
    }
}
