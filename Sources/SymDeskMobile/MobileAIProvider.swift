import Foundation

/// The client-side AI protocol both providers implement. The chat surface
/// only ever talks to a `MobileAIProvider`; which concrete provider backs
/// it is decided by `MobileAIProviderFactory` (server when reachable,
/// on-device otherwise) and is always visible in the UI.
protocol MobileAIProvider: Sendable {
    /// A stable, user-visible provider name ("Server", "On-device").
    var displayName: String { get }
    /// True when the provider runs entirely on this device.
    var isOnDevice: Bool { get }
    /// Streams an answer to `query`. Events arrive in order; the method
    /// returns when the stream ends. Throws on transport/HTTP/model
    /// errors (mid-stream failures surface after delivered events).
    func ask(query: String, onEvent: @escaping @Sendable (MobileAIEvent) -> Void) async throws

    /// Streams an intent-based transformation of `text` (`summarize |
    /// rewrite | continue`, desktop-compatible values). Operates purely
    /// on the provided text; the vault is never touched.
    func transform(text: String, intent: String, onEvent: @escaping @Sendable (MobileAIEvent) -> Void) async throws
}

/// Automatic provider selection: the server wins when a connection is
/// stored; otherwise the on-device provider is used when the device
/// supports it. `unavailableReason` explains why *no* provider exists —
/// the UI shows this instead of failing silently.
enum MobileAIProviderFactory {
    struct Selection: Sendable {
        let provider: MobileAIProvider?
        let unavailableReason: String?
    }

    static func select(
        connection: MobileServerConnection?,
        vaultNotes: [MobileNote],
        onDeviceModel: MobileOnDeviceModelProtocol = MobileOnDeviceModel()
    ) -> Selection {
        if let connection {
            return Selection(
                provider: MobileServerAIProvider(connection: connection),
                unavailableReason: nil
            )
        }
        // Files/iCloud mode (or lost server config): fall back to the
        // device model.
        let onDevice = MobileOnDeviceAIProvider(vaultNotes: vaultNotes, model: onDeviceModel)
        if onDevice.isAvailable {
            return Selection(provider: onDevice, unavailableReason: nil)
        }
        return Selection(
            provider: nil,
            unavailableReason: "This device has no on-device AI model, and no server is connected. Connect a server in Settings to ask your vault."
        )
    }
}
