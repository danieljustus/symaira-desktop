import SwiftUI
import SymroomKit

/// Public root view of the Symroom feature module — the single entry point
/// consumed by the Symaira Hub (Module Integration Contract, see
/// symaira-hub/AGENTS.md). Owns its own AppState; internal views stay
/// module-private.
public struct SymroomModuleView: View {
    @State private var appState = RoomAppState()

    public init() {}

    public var body: some View {
        RoomDashboardView()
            .environment(appState)
    }
}

/// Contract metadata for hub embedding.
public enum SymroomModule {
    /// CLI JSON schema version this module expects; must match
    /// `internal/version.SchemaVersion` in the symroom CLI.
    public static let expectedSchemaVersion = 1
}
