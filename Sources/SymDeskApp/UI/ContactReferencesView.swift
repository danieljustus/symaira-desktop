import SwiftUI
import SymairaTheme
import SymDeskCore

/// Everything the vault holds for one correspondent (issue #516).
///
/// A document's correspondent is free text. The contact store, which ships
/// in-process since the repo consolidation, may know that name as a reviewed
/// identity — and if it does, the meetings linked to that identity belong in
/// the same picture as the documents filed under the name. This sheet is that
/// picture. It resolves only: nothing here creates or links a contact.
struct ContactReferencesView: View {
    let name: String
    var onOpenPath: (String) -> Void

    @EnvironmentObject var core: DeskCore
    @Environment(\.dismiss) private var dismiss

    @State private var references: DeskCore.ContactReferences?
    @State private var loadError: String?

    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack {
                VStack(alignment: .leading, spacing: 4) {
                    Text(name)
                        .symairaText(.title).bold()
                        .foregroundColor(SymairaTheme.textPrimary)
                    identityLine
                }
                Spacer()
                Button("Done") { dismiss() }
            }

            if let loadError {
                Label(loadError, systemImage: "exclamationmark.triangle")
                    .symairaText(.caption)
                    .foregroundColor(.red)
            } else if let references {
                content(references)
            } else {
                ProgressView()
                    .tint(SymairaTheme.goldPrimary)
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
            }

            Spacer(minLength: 0)
        }
        .padding(24)
        .frame(minWidth: 460, minHeight: 380)
        .task { await load() }
    }

    @ViewBuilder
    private var identityLine: some View {
        if let references {
            if !references.storeAvailable {
                Label("Contact store unavailable — identity unknown", systemImage: "questionmark.circle")
                    .symairaText(.caption)
                    .foregroundColor(SymairaTheme.textMuted)
            } else if let ref = references.refs.first {
                Label(
                    "Linked contact: \(ref.displayName ?? ref.id) (\(ref.kind))",
                    systemImage: "person.crop.circle.badge.checkmark"
                )
                .symairaText(.caption)
                .foregroundColor(.green)
            } else {
                Label("No contact carries this name", systemImage: "person.crop.circle")
                    .symairaText(.caption)
                    .foregroundColor(SymairaTheme.textSecondary)
            }
        }
    }

    @ViewBuilder
    private func content(_ references: DeskCore.ContactReferences) -> some View {
        List {
            Section("Documents (\(references.documents.count))") {
                if references.documents.isEmpty {
                    Text("No documents are filed under this correspondent.")
                        .symairaText(.caption)
                        .foregroundColor(SymairaTheme.textMuted)
                }
                ForEach(references.documents) { doc in
                    Button {
                        onOpenPath(doc.path)
                        dismiss()
                    } label: {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(doc.title).symairaText(.body)
                            Text(doc.path).symairaText(.caption).foregroundColor(SymairaTheme.textMuted)
                        }
                    }
                    .buttonStyle(.plain)
                }
            }

            Section("Meetings (\(references.meetings.count))") {
                if references.meetings.isEmpty {
                    Text(references.refs.isEmpty
                         ? "Meetings are matched by linked contact, and no contact carries this name yet."
                         : "No meeting links this contact.")
                        .symairaText(.caption)
                        .foregroundColor(SymairaTheme.textMuted)
                }
                ForEach(references.meetings) { meeting in
                    Button {
                        onOpenPath(meeting.path)
                        dismiss()
                    } label: {
                        VStack(alignment: .leading, spacing: 2) {
                            Text(meeting.title).symairaText(.body)
                            if let participant = meeting.participant, !participant.isEmpty {
                                Text("as \(participant)")
                                    .symairaText(.caption)
                                    .foregroundColor(SymairaTheme.textMuted)
                            }
                        }
                    }
                    .buttonStyle(.plain)
                }
            }
        }
        .listStyle(.inset)
    }

    private func load() async {
        do {
            references = try await core.contactReferences(name: name)
            loadError = nil
        } catch {
            references = nil
            loadError = "Could not resolve this correspondent: \(error.localizedDescription)"
        }
    }
}
