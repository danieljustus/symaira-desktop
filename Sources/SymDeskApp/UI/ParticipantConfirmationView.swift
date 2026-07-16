import SwiftUI
import SymDeskCore
import SymairaTheme

/// Reviewed participant confirmation for one speaker: shows deterministic
/// Memory candidates (with their match reason) for the participant's
/// label, and lets the reviewer confirm an existing entity, create a new
/// person from a typed name, or keep/return the speaker to anonymous.
///
/// Safety properties this view enforces by construction:
/// - no automatic identity creation: creating a person requires a typed,
///   explicitly submitted name
/// - no auto-selection: candidates — including duplicate aliases — are
///   always listed for an explicit click, never pre-chosen
/// - anonymity is a first-class choice: "Keep Anonymous" unlinks
struct ParticipantConfirmationView: View {
    @ObservedObject var model: MeetingReviewModel
    let participant: MeetingParticipant
    @Environment(\.dismiss) private var dismiss

    @State private var candidates: [ParticipantCandidate] = []
    @State private var candidatesState: LoadState = .loading
    @State private var newPersonName = ""

    enum LoadState: Equatable {
        case loading
        case loaded
        case failed(String)
    }

    private var speakerID: String { participant.speakerIDs.first ?? "" }
    private var isConfirmed: Bool { !(participant.entityID ?? "").isEmpty }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            header

            candidateSection

            Divider()

            createSection

            Divider()

            anonymousSection

            if let error = model.participantActionError {
                Text(error)
                    .font(.caption)
                    .foregroundColor(.red)
            }

            Spacer()
        }
        .padding(20)
        .frame(minWidth: 420, minHeight: 420)
        .task { await loadCandidates() }
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text("Confirm Participant")
                .font(.title3.weight(.bold))
                .foregroundColor(SymairaTheme.textPrimary)
            Text("\(participant.label.isEmpty ? "Unlabeled speaker" : participant.label) (\(participant.speakerIDs.joined(separator: ", ")))")
                .font(.caption)
                .foregroundColor(SymairaTheme.textSecondary)
            if isConfirmed {
                Text("Currently linked to a confirmed person.")
                    .font(.caption)
                    .foregroundColor(SymairaTheme.goldPrimary)
            }
        }
        .accessibilityElement(children: .combine)
    }

    private var candidateSection: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Matching people in Memory")
                .font(.subheadline.weight(.semibold))
                .foregroundColor(SymairaTheme.textSecondary)

            switch candidatesState {
            case .loading:
                ProgressView().controlSize(.small)
            case .failed(let message):
                Text(message)
                    .font(.caption)
                    .foregroundColor(SymairaTheme.textMuted)
            case .loaded:
                if candidates.isEmpty {
                    Text("No exact or alias matches. Create a new person below, or keep the speaker anonymous.")
                        .font(.caption)
                        .foregroundColor(SymairaTheme.textMuted)
                } else {
                    ForEach(candidates) { candidate in
                        candidateRow(candidate)
                    }
                }
            }
        }
    }

    private func candidateRow(_ candidate: ParticipantCandidate) -> some View {
        HStack {
            VStack(alignment: .leading, spacing: 2) {
                Text(candidate.name)
                    .font(.callout)
                    .foregroundColor(SymairaTheme.textPrimary)
                Text(candidate.matchReason)
                    .font(.caption2)
                    .foregroundColor(SymairaTheme.textMuted)
            }
            Spacer()
            Button("Confirm") {
                Task {
                    await model.confirmParticipant(speakerID: speakerID, entityID: candidate.entityID)
                    if model.participantActionError == nil { dismiss() }
                }
            }
            .buttonStyle(SymairaSecondaryButtonStyle())
            .controlSize(.small)
            .disabled(model.isConfirmingParticipant)
            .accessibilityLabel("Confirm \(candidate.name), matched by \(candidate.matchReason)")
        }
        .padding(8)
        .background(SymairaTheme.goldPrimary.opacity(0.06))
        .cornerRadius(6)
    }

    private var createSection: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Create a new person")
                .font(.subheadline.weight(.semibold))
                .foregroundColor(SymairaTheme.textSecondary)
            HStack {
                TextField("Full name", text: $newPersonName)
                    .textFieldStyle(.roundedBorder)
                    .accessibilityLabel("Name of the new person")
                Button("Create & Confirm") {
                    let name = newPersonName.trimmingCharacters(in: .whitespacesAndNewlines)
                    guard !name.isEmpty else { return }
                    Task {
                        await model.createParticipant(speakerID: speakerID, name: name)
                        if model.participantActionError == nil { dismiss() }
                    }
                }
                .buttonStyle(SymairaSecondaryButtonStyle())
                .controlSize(.small)
                .disabled(newPersonName.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty || model.isConfirmingParticipant)
            }
        }
    }

    private var anonymousSection: some View {
        HStack {
            VStack(alignment: .leading, spacing: 2) {
                Text("Keep anonymous")
                    .font(.subheadline.weight(.semibold))
                    .foregroundColor(SymairaTheme.textSecondary)
                Text("Anonymous speakers stay fully usable in the transcript, search, and export.")
                    .font(.caption2)
                    .foregroundColor(SymairaTheme.textMuted)
            }
            Spacer()
            Button(isConfirmed ? "Unlink" : "Keep Anonymous") {
                Task {
                    if isConfirmed {
                        await model.confirmParticipant(speakerID: speakerID, entityID: nil)
                        if model.participantActionError == nil { dismiss() }
                    } else {
                        dismiss()
                    }
                }
            }
            .buttonStyle(SymairaSecondaryButtonStyle())
            .controlSize(.small)
            .disabled(model.isConfirmingParticipant)
            .accessibilityLabel(isConfirmed ? "Unlink this participant from the confirmed person" : "Keep this speaker anonymous")
        }
    }

    private func loadCandidates() async {
        candidatesState = .loading
        do {
            candidates = try await model.participantCandidates(label: participant.label)
            candidatesState = .loaded
        } catch {
            candidates = []
            candidatesState = .failed(error.localizedDescription)
        }
    }
}
