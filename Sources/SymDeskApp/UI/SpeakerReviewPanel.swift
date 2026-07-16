import SwiftUI
import SymDeskCore
import SymairaTheme

/// Read-only view of a meeting's reviewed participants. Editing (label,
/// merge, split, reset) is not wired up yet: it depends on `symmeet`
/// speaker-correction commands that exist upstream (symaira-meet#17) but
/// have no confirmed CLI contract in this repository yet — see the #172
/// follow-up note. Showing current labels without a half-working edit
/// affordance is preferable to a control that silently does nothing.
struct SpeakerReviewPanel: View {
    let participants: [MeetingParticipant]

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Participants")
                .font(.subheadline)
                .fontWeight(.semibold)
                .foregroundColor(SymairaTheme.textSecondary)

            if participants.isEmpty {
                Text("No participants recorded for this meeting.")
                    .font(.caption)
                    .foregroundColor(SymairaTheme.textMuted)
            } else {
                ForEach(Array(participants.enumerated()), id: \.offset) { _, participant in
                    HStack(spacing: 8) {
                        Image(systemName: participant.entityID == nil ? "person.crop.circle" : "person.crop.circle.badge.checkmark")
                            .foregroundColor(participant.entityID == nil ? SymairaTheme.textMuted : SymairaTheme.goldPrimary)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(participant.label.isEmpty ? "Unlabeled speaker" : participant.label)
                                .font(.callout)
                                .foregroundColor(SymairaTheme.textPrimary)
                            Text(participant.speakerIDs.joined(separator: ", "))
                                .font(.caption2)
                                .foregroundColor(SymairaTheme.textMuted)
                        }
                        Spacer()
                    }
                    .accessibilityElement(children: .combine)
                    .accessibilityLabel("\(participant.label.isEmpty ? "Unlabeled speaker" : participant.label), \(participant.entityID == nil ? "not linked to a confirmed person" : "linked to a confirmed person")")
                }
            }
        }
    }
}
