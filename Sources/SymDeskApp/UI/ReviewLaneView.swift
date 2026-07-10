import SwiftUI
import SymairaTheme
import SymDeskCore

struct ReviewLaneView: View {
    @EnvironmentObject var core: DeskCore
    @State private var reviewDocs: [ReviewDoc] = []
    @State private var isLoading = false
    @State private var selectedDoc: ReviewDoc? = nil

    // Editing State
    @State private var docStatus: String = "open"
    @State private var dueDateString: String = ""
    @State private var documentType: String = ""
    @State private var isSaving = false

    var body: some View {
        HStack(spacing: 0) {
            // Left List Lane
            VStack(spacing: 0) {
                headerView
                Divider()

                if isLoading && reviewDocs.isEmpty {
                    ProgressView("Finding documents for review…")
                        .tint(SymairaTheme.goldPrimary)
                        .foregroundColor(SymairaTheme.textSecondary)
                        .frame(maxWidth: .infinity, maxHeight: .infinity)
                } else if reviewDocs.isEmpty {
                    emptyState
                } else {
                    docsList
                }
            }
            .frame(minWidth: 320, maxWidth: 450)

            Divider()

            // Right Inspector/Edit lane
            if let doc = selectedDoc {
                editInspector(for: doc)
            } else {
                noSelectionState
            }
        }
        .task {
            await fetchReviewDocs()
        }
    }

    private var headerView: some View {
        HStack {
            VStack(alignment: .leading, spacing: 4) {
                Text("Review Lane")
                    .font(.title2)
                    .fontWeight(.bold)
                    .foregroundColor(SymairaTheme.textPrimary)
                Text("Approve low-confidence or metadata-missing files")
                    .font(.subheadline)
                    .foregroundColor(SymairaTheme.textSecondary)
            }
            Spacer()
            Button(action: {
                Task { await fetchReviewDocs() }
            }) {
                Image(systemName: "arrow.clockwise")
            }
            .buttonStyle(SymairaSecondaryButtonStyle())
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 16)
    }

    private var emptyState: some View {
        VStack(spacing: 12) {
            Image(systemName: "checkmark.shield.fill")
                .font(.system(size: 48))
                .foregroundColor(SymairaTheme.goldPrimary)
                .shadow(color: SymairaTheme.glowIntense, radius: 14)
            Text("All caught up!")
                .font(.title3)
                .fontWeight(.medium)
                .foregroundColor(SymairaTheme.textPrimary)
            Text("No documents require manual metadata review.")
                .font(.caption)
                .foregroundColor(SymairaTheme.textSecondary)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private var docsList: some View {
        List(reviewDocs) { doc in
            Button(action: {
                selectDoc(doc)
            }) {
                VStack(alignment: .leading, spacing: 6) {
                    HStack {
                        Text(doc.title)
                            .font(.headline)
                            .foregroundColor(SymairaTheme.textPrimary)
                            .lineLimit(1)
                        Spacer()
                        confidenceBadge(doc.confidence)
                    }

                    Text(doc.path)
                        .font(.caption2)
                        .foregroundColor(SymairaTheme.textMuted)
                        .lineLimit(1)

                    ForEach(doc.reasons, id: \.self) { reason in
                        HStack(spacing: 4) {
                            Image(systemName: "exclamationmark.circle")
                                .font(.caption2)
                                .foregroundColor(SymairaTheme.goldSecondary)
                            Text(reason)
                                .font(.caption)
                                .foregroundColor(SymairaTheme.textSecondary)
                        }
                    }
                }
                .padding(.vertical, 8)
            }
            .buttonStyle(.plain)
            .background(selectedDoc?.id == doc.id ? SymairaTheme.goldPrimary.opacity(0.12) : Color.clear)
            .cornerRadius(8)
            Divider()
                .overlay(SymairaTheme.borderGlass)
        }
        .listStyle(.plain)
        .scrollContentBackground(.hidden)
    }

    private var noSelectionState: some View {
        VStack(spacing: 12) {
            Image(systemName: "doc.text.magnifyingglass")
                .font(.system(size: 48))
                .foregroundColor(SymairaTheme.textMuted)
            Text("Select a document to review")
                .font(.title3)
                .foregroundColor(SymairaTheme.textSecondary)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private func editInspector(for doc: ReviewDoc) -> some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 20) {
                Text("Document Metadata Review")
                    .font(.title3)
                    .fontWeight(.bold)
                    .foregroundColor(SymairaTheme.goldPrimary)

                Text(doc.title)
                    .font(.headline)
                    .foregroundColor(SymairaTheme.textPrimary)

                Text(doc.path)
                    .font(.caption)
                    .foregroundColor(SymairaTheme.textMuted)

                Divider()
                    .overlay(SymairaTheme.borderGlass)

                VStack(alignment: .leading, spacing: 8) {
                    Text("Document Type")
                        .font(.subheadline)
                        .fontWeight(.semibold)
                        .foregroundColor(SymairaTheme.textSecondary)
                    TextField("Invoice, Contract, Tax, Insurance, etc.", text: $documentType)
                        .textFieldStyle(.roundedBorder)
                }

                VStack(alignment: .leading, spacing: 8) {
                    Text("Status")
                        .font(.subheadline)
                        .fontWeight(.semibold)
                        .foregroundColor(SymairaTheme.textSecondary)
                    Picker("Status", selection: $docStatus) {
                        ForEach(DocumentStatus.allCases) { status in
                            Text(status.label).tag(status.rawValue)
                        }
                    }
                    .pickerStyle(.menu)
                    .labelsHidden()
                }

                VStack(alignment: .leading, spacing: 8) {
                    Text("Due Date")
                        .font(.subheadline)
                        .fontWeight(.semibold)
                        .foregroundColor(SymairaTheme.textSecondary)
                    TextField("YYYY-MM-DD", text: $dueDateString)
                        .textFieldStyle(.roundedBorder)
                }

                Divider()
                    .overlay(SymairaTheme.borderGlass)

                HStack {
                    if isSaving {
                        ProgressView()
                            .controlSize(.small)
                            .tint(SymairaTheme.goldPrimary)
                    }
                    Spacer()
                    Button("Approve & Resolve") {
                        Task { await saveChanges(for: doc) }
                    }
                    .buttonStyle(SymairaPrimaryButtonStyle())
                    .disabled(isSaving)
                }
            }
            .padding(24)
        }
    }

    private func confidenceBadge(_ confidence: Int) -> some View {
        let color: Color = confidence >= 80 ? SymairaTheme.goldPrimary : (confidence >= 50 ? .orange : .red)
        return Text("\(confidence)%")
            .font(.system(.caption, design: .monospaced))
            .fontWeight(.bold)
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(color.opacity(0.15))
            .foregroundColor(color)
            .cornerRadius(4)
    }

    private func fetchReviewDocs() async {
        isLoading = true
        do {
            self.reviewDocs = try await core.docsReview()
        } catch {
            print("docsReview failed: \(error)")
        }
        isLoading = false
    }

    private func selectDoc(_ doc: ReviewDoc) {
        selectedDoc = doc
        docStatus = doc.status.isEmpty ? "open" : doc.status
        dueDateString = ""
        documentType = doc.documentType

        // Fetch properties to get current due date if set
        Task {
            if let props = try? await core.docProps(path: doc.path) {
                if let due = props["due_date"] {
                    dueDateString = due
                }
            }
        }
    }

    private func saveChanges(for doc: ReviewDoc) async {
        isSaving = true
        defer { isSaving = false }
        do {
            // Apply Document Type
            try await core.docSetType(path: doc.path, type: documentType)
            // Apply Status
            try await core.docSetStatus(path: doc.path, status: docStatus)
            // Apply Due Date if set
            if !dueDateString.isEmpty {
                try await core.docSetDue(path: doc.path, date: dueDateString)
            }
            
            // Success: clear selected doc, refetch
            selectedDoc = nil
            await fetchReviewDocs()
        } catch {
            print("Failed to save reviewed metadata: \(error)")
        }
    }
}
