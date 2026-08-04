import SwiftUI
import SymDeskCore
import SymairaTheme

/// Reviewed Memory publish for one meeting: the reviewer authors the facts
/// (decisions, action items, durable facts) to publish, sees the exact
/// Memory writes the apply would perform, and applies explicitly.
///
/// Safety properties enforced here:
/// - nothing reaches Memory before the explicit Apply click; closing the
///   sheet (rejecting the proposal) changes nothing
/// - the preview lists every write verbatim: one attended-relation per
///   confirmed participant and one memory per fact, each carrying the
///   source meeting ID as evidence
/// - repeat applies are idempotent (the backend skips already-published
///   facts; relations are idempotent), so a partial failure is safely
///   retried with the same proposal
/// - facts are reviewer-authored, never generated: this view offers the
///   selected transcript segment's timestamp as evidence to include, but
///   never invents content
struct MeetingKnowledgeReviewView: View {
    @ObservedObject var model: MeetingReviewModel
    @Environment(\.dismiss) private var dismiss

    @State private var facts: [String] = []
    @State private var newFact = ""

    private var confirmedParticipants: [MeetingParticipant] {
        (model.selectedDetail?.frontmatter.participants ?? []).filter { !($0.entityID ?? "").isEmpty }
    }

    private var meetingID: String {
        model.selectedDetail?.frontmatter.meetingID ?? ""
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("Publish to Memory")
                .symairaText(.heading).fontWeight(.bold)
                .foregroundColor(SymairaTheme.textPrimary)

            factEditor

            Divider()

            previewSection

            if let error = model.publishError {
                Text(error)
                    .symairaText(.caption)
                    .foregroundColor(.red)
            }
            if let result = model.lastPublish {
                resultSummary(result)
            }

            Spacer()

            HStack {
                Button("Close") { dismiss() }
                    .buttonStyle(SymairaSecondaryButtonStyle())
                Spacer()
                if model.isPublishing {
                    ProgressView().controlSize(.small)
                } else {
                    Button("Apply to Memory") {
                        Task { await model.publish(facts: facts) }
                    }
                    .buttonStyle(SymairaPrimaryButtonStyle())
                    .disabled(facts.isEmpty && confirmedParticipants.isEmpty)
                    .accessibilityLabel("Apply the reviewed proposal to Memory")
                    .accessibilityHint("Writes exactly the previewed relations and facts; nothing else")
                }
            }
        }
        .padding(20)
        .frame(minWidth: 480, minHeight: 480)
    }

    private var factEditor: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Reviewed facts, decisions, and action items")
                .symairaText(.subheading)
                .foregroundColor(SymairaTheme.textSecondary)

            ForEach(Array(facts.enumerated()), id: \.offset) { index, fact in
                HStack {
                    Text(fact)
                        .symairaText(.callout)
                        .foregroundColor(SymairaTheme.textPrimary)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    Button {
                        facts.remove(at: index)
                    } label: {
                        Image(systemName: "trash")
                            .foregroundColor(SymairaTheme.textMuted)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Remove fact")
                }
                .padding(6)
                .background(SymairaTheme.goldPrimary.opacity(0.06))
                .cornerRadius(6)
            }

            HStack {
                TextField("e.g. Decision: ship the beta on Friday", text: $newFact)
                    .textFieldStyle(.symaira)
                    .onSubmit { addFact() }
                    .accessibilityLabel("New fact to publish")
                Button("Add") { addFact() }
                    .buttonStyle(SymairaSecondaryButtonStyle())
                    .controlSize(.small)
                    .disabled(newFact.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            }

            if let segmentID = model.selectedSegmentID,
               let segment = model.segments.first(where: { $0.segmentID == segmentID }) {
                Button("Insert evidence timestamp (\(TranscriptTimelineView.formatTimestamp(segment.startMS)))") {
                    newFact += " [evidence: \(TranscriptTimelineView.formatTimestamp(segment.startMS))]"
                }
                .buttonStyle(.plain)
                .symairaText(.caption)
                .foregroundColor(SymairaTheme.goldSecondary)
                .accessibilityLabel("Insert the selected segment's timestamp as evidence")
            }
        }
    }

    private var previewSection: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text("Exact Memory writes on apply")
                .symairaText(.subheading)
                .foregroundColor(SymairaTheme.textSecondary)

            if confirmedParticipants.isEmpty && facts.isEmpty {
                Text("Nothing selected yet — confirm participants or add facts above. Applying an empty proposal writes nothing.")
                    .symairaText(.caption)
                    .foregroundColor(SymairaTheme.textMuted)
            } else {
                ScrollView {
                    VStack(alignment: .leading, spacing: 4) {
                        ForEach(confirmedParticipants, id: \.label) { participant in
                            previewLine("relate: \(participant.label) --attended--> Meeting \(meetingID)")
                        }
                        ForEach(facts, id: \.self) { fact in
                            previewLine("memory: \"\(fact)\" (scope: project, entity: Meeting \(meetingID))")
                        }
                    }
                }
                .frame(maxHeight: 140)
            }
        }
    }

    private func previewLine(_ text: String) -> some View {
        Text(text)
            .symairaText(.caption).monospaced()
            .foregroundColor(SymairaTheme.textPrimary)
            .frame(maxWidth: .infinity, alignment: .leading)
            .textSelection(.enabled)
    }

    private func resultSummary(_ result: MeetingPublishOutcome) -> some View {
        let published = result.factsPublished?.count ?? 0
        return Label(
            "Applied: \(result.relationsCreated) relation(s), \(published) fact(s) published, \(result.factsSkipped) already published and skipped.",
            systemImage: "checkmark.circle"
        )
        .symairaText(.caption)
        .foregroundColor(.green)
        .accessibilityLabel("Publish applied. \(result.relationsCreated) relations, \(published) new facts, \(result.factsSkipped) skipped as already published.")
    }

    private func addFact() {
        let fact = newFact.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !fact.isEmpty else { return }
        facts.append(fact)
        newFact = ""
    }
}
