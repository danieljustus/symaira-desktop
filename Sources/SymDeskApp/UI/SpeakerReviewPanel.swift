import SwiftUI
import SymDeskCore
import SymairaTheme

/// Interactive speaker-correction panel: rename (label), merge into
/// another speaker, and reset all speaker edits, each backed by the
/// corresponding `symmeet speaker` command through the review model. All
/// edits live in the symmeet edit layer; the raw artifact is never
/// mutated, and the model refreshes the note's transcript projection
/// after every applied correction.
struct SpeakerReviewPanel: View {
    @ObservedObject var model: MeetingReviewModel

    @State private var renamingSpeakerID: String?
    @State private var renameText = ""
    @State private var confirmingReset = false

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack {
                Text("Speakers")
                    .font(.subheadline)
                    .fontWeight(.semibold)
                    .foregroundColor(SymairaTheme.textSecondary)
                Spacer()
                if model.isCorrectingSpeaker {
                    ProgressView().controlSize(.mini)
                } else if !model.speakers.isEmpty {
                    Button("Reset Edits") { confirmingReset = true }
                        .buttonStyle(.plain)
                        .font(.caption)
                        .foregroundColor(SymairaTheme.textMuted)
                        .accessibilityLabel("Reset all speaker edits")
                }
            }

            if let error = model.speakersError {
                Label(error, systemImage: "person.crop.circle.badge.questionmark")
                    .font(.caption)
                    .foregroundColor(SymairaTheme.textMuted)
            } else if model.speakers.isEmpty {
                Text("No speakers recorded for this meeting.")
                    .font(.caption)
                    .foregroundColor(SymairaTheme.textMuted)
            } else {
                ForEach(model.speakers) { speaker in
                    speakerRow(speaker)
                }
            }

            if let actionError = model.speakerActionError {
                Text(actionError)
                    .font(.caption)
                    .foregroundColor(.red)
            }

            participantsSection
        }
        .confirmationDialog(
            "Reset all speaker edits for this meeting?",
            isPresented: $confirmingReset,
            titleVisibility: .visible
        ) {
            Button("Reset Edits", role: .destructive) {
                Task { await model.resetSpeakerEdits() }
            }
        } message: {
            Text("Labels, merges, and splits are discarded; the raw engine output is restored. The imported note itself is not changed until the transcript is refreshed.")
        }
    }

    private func speakerRow(_ speaker: MeetingSpeaker) -> some View {
        HStack(spacing: 8) {
            Image(systemName: "person.crop.circle")
                .foregroundColor(SymairaTheme.goldSecondary)

            if renamingSpeakerID == speaker.speakerID {
                TextField("Speaker name", text: $renameText)
                    .textFieldStyle(.roundedBorder)
                    .font(.callout)
                    .onSubmit { commitRename(speaker) }
                    .accessibilityLabel("New name for \(speaker.label)")
                Button("Save") { commitRename(speaker) }
                    .buttonStyle(.plain)
                    .font(.caption)
                    .foregroundColor(SymairaTheme.goldPrimary)
                    .keyboardShortcut(.defaultAction)
            } else {
                VStack(alignment: .leading, spacing: 2) {
                    Text(speaker.label)
                        .font(.callout)
                        .foregroundColor(SymairaTheme.textPrimary)
                    if speaker.label != speaker.speakerID {
                        Text(speaker.speakerID)
                            .font(.caption2)
                            .foregroundColor(SymairaTheme.textMuted)
                    }
                }
                Spacer()
                Menu {
                    Button("Rename…") {
                        renameText = speaker.label == speaker.speakerID ? "" : speaker.label
                        renamingSpeakerID = speaker.speakerID
                    }
                    if model.speakers.count > 1 {
                        Menu("Merge Into") {
                            ForEach(model.speakers.filter { $0.speakerID != speaker.speakerID }) { target in
                                Button(target.label) {
                                    Task { await model.mergeSpeaker(from: speaker.speakerID, into: target.speakerID) }
                                }
                            }
                        }
                    }
                    if let segmentID = model.selectedSegmentID,
                       model.segments.first(where: { $0.segmentID == segmentID })?.speakerID == speaker.speakerID {
                        Button("Split Selected Segment Away") {
                            Task { await model.splitSegment(segmentID: segmentID, from: speaker.speakerID) }
                        }
                    }
                } label: {
                    Image(systemName: "ellipsis.circle")
                        .foregroundColor(SymairaTheme.textMuted)
                }
                .menuStyle(.borderlessButton)
                .frame(width: 28)
                .accessibilityLabel("Correct speaker \(speaker.label)")
            }
        }
        .accessibilityElement(children: .contain)
    }

    private func commitRename(_ speaker: MeetingSpeaker) {
        let label = renameText.trimmingCharacters(in: .whitespacesAndNewlines)
        renamingSpeakerID = nil
        guard !label.isEmpty else { return }
        Task { await model.labelSpeaker(speakerID: speaker.speakerID, label: label) }
    }

    /// The note's reviewed participants (confirmed people), distinct from
    /// the artifact's raw speakers above. Confirmation itself happens in
    /// the separate participant-confirmation flow — never automatically.
    private var participantsSection: some View {
        let participants = model.selectedDetail?.frontmatter.participants ?? []
        return Group {
            if !participants.isEmpty {
                Divider()
                Text("Participants")
                    .font(.subheadline)
                    .fontWeight(.semibold)
                    .foregroundColor(SymairaTheme.textSecondary)
                ForEach(Array(participants.enumerated()), id: \.offset) { _, participant in
                    HStack(spacing: 8) {
                        Image(systemName: participant.entityID == nil || participant.entityID?.isEmpty == true ? "person.crop.circle" : "person.crop.circle.badge.checkmark")
                            .foregroundColor(participant.entityID == nil || participant.entityID?.isEmpty == true ? SymairaTheme.textMuted : SymairaTheme.goldPrimary)
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
                    .accessibilityLabel("\(participant.label.isEmpty ? "Unlabeled speaker" : participant.label), \(participant.entityID == nil || participant.entityID?.isEmpty == true ? "not linked to a confirmed person" : "linked to a confirmed person")")
                }
            }
        }
    }
}
