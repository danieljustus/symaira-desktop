import SwiftUI
import SymairaTheme
import SymDeskCore

extension DocumentStatus {
    /// Symaira icon for this status; `DocumentStatus.systemImage` stays in
    /// SymDeskCore for the iOS app.
    var symairaIcon: SymairaIcon {
        switch self {
        case .open: return .pending
        case .paid: return .confirm
        case .submitted: return .send
        case .done: return .success
        case .needsReview: return .warning
        case .waitingForReply: return .time
        }
    }
}
