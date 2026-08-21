import SwiftUI
import SymroomKit

/// Participant list with roles (`symroom member list --json`).
struct ParticipantsView: View {
    let members: [RoomMember]

    private var sorted: [RoomMember] {
        members.sorted { $0.name.localizedCaseInsensitiveCompare($1.name) == .orderedAscending }
    }

    var body: some View {
        if members.isEmpty {
            ContentUnavailableView("No participants", systemImage: "person.2")
        } else {
            List(sorted) { member in
                HStack(spacing: 10) {
                    Image(systemName: member.kind == "agent" ? "cpu" : "person.fill")
                        .foregroundStyle(member.kind == "agent" ? .purple : .blue)
                        .frame(width: 20)
                    VStack(alignment: .leading, spacing: 1) {
                        Text(member.name)
                            .font(.body)
                        Text(member.id)
                            .font(.caption2.monospaced())
                            .foregroundStyle(.tertiary)
                    }
                    Spacer()
                    Text(member.role)
                        .font(.caption.weight(.medium))
                        .padding(.horizontal, 8)
                        .padding(.vertical, 2)
                        .background(roleColor(member.role), in: Capsule())
                    Text(member.kind)
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
                .padding(.vertical, 2)
            }
        }
    }

    private func roleColor(_ role: String) -> Color {
        switch role {
        case "owner": .orange.opacity(0.2)
        case "member": .green.opacity(0.2)
        case "observer": .gray.opacity(0.2)
        default: .blue.opacity(0.15)
        }
    }
}
