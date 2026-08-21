import SwiftUI

/// Public root view of the Symseek feature module — the single entry point
/// consumed by the standalone app AND the Symaira Hub (Module Integration
/// Contract, see symaira-hub/AGENTS.md). Internal views stay module-private.
public struct SymseekModuleView: View {
    public init() {}

    public var body: some View {
        ContentView()
            .preferredColorScheme(.dark)
    }
}

/// Contract metadata for hub embedding.
public enum SymseekModule {
    /// CLI JSON schema version this module expects. 0 until symseek ships
    /// corekit versionkit (`version --json`); bump together with the CLI.
    public static let expectedSchemaVersion = 1
}
