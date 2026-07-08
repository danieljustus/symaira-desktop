import SwiftUI
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
                Text("Approve low-confidence or metadata-missing files")
                    .font(.subheadline)
                    .foregroundColor(.secondary)
            }
            Spacer()
            Button(action: {
                Task { await fetchReviewDocs() }
            }) {
                Image(systemName: "arrow.clockwise")
            }
            .buttonStyle(.bordered)
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 16)
        .background(Color(nsColor: .windowBackgroundColor))
    }

    private var emptyState: some View {
        VStack(spacing: 12) {
            Image(systemName: "checkmark.shield.fill")
                .font(.system(size: 48))
                .foregroundColor(.green)
            Text("All caught up!")
                .font(.title3)
                .fontWeight(.medium)
            Text("No documents require manual metadata review.")
                .font(.caption)
                .foregroundColor(.secondary)
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
                            .lineLimit(1)
                        Spacer()
                        confidenceBadge(doc.confidence)
                    }

                    Text(doc.path)
                        .font(.caption2)
                        .foregroundColor(.secondary)
                        .lineLimit(1)

                    ForEach(doc.reasons, id: \.self) { reason in
                        HStack(spacing: 4) {
                            Image(systemName: "exclamationmark.circle")
                                .font(.caption2)
                                .foregroundColor(.orange)
                            Text(reason)
                                .font(.caption)
                                .foregroundColor(.secondary)
                        }
                    }
                }
                .padding(.vertical, 8)
            }
            .buttonStyle(.plain)
            .background(selectedDoc?.id == doc.id ? Color.accentColor.opacity(0.15) : Color.clear)
            .cornerRadius(8)
            Divider()
        }
        .listStyle(.plain)
    }

    private var noSelectionState: some View {
        VStack(spacing: 12) {
            Image(systemName: "doc.text.magnifyingglass")
                .font(.system(size: 48))
                .foregroundColor(.secondary)
            Text("Select a document to review")
                .font(.title3)
                .foregroundColor(.secondary)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
        .background(Color(nsColor: .windowBackgroundColor))
    }

    private func editInspector(for doc: ReviewDoc) -> some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 20) {
                Text("Document Metadata Review")
                    .font(.title3)
                    .fontWeight(.bold)

                Text(doc.title)
                    .font(.headline)
                
                Text(doc.path)
                    .font(.caption)
                    .foregroundColor(.secondary)

                Divider()

                VStack(alignment: .leading, spacing: 8) {
                    Text("Document Type")
                        .font(.subheadline)
                        .fontWeight(.semibold)
                    TextField("Invoice, Contract, Tax, Insurance, etc.", text: $documentType)
                        .textFieldStyle(.roundedBorder)
                }

                VStack(alignment: .leading, spacing: 8) {
                    Text("Status")
                        .font(.subheadline)
                        .fontWeight(.semibold)
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
                    TextField("YYYY-MM-DD", text: $dueDateString)
                        .textFieldStyle(.roundedBorder)
                }

                Divider()

                HStack {
                    if isSaving {
                        ProgressView()
                            .controlSize(.small)
                    }
                    Spacer()
                    Button("Approve & Resolve") {
                        Task { await saveChanges(for: doc) }
                    }
                    .buttonStyle(.borderedProminent)
                    .disabled(isSaving)
                }
            }
            .padding(24)
        }
        .background(Color(nsColor: .windowBackgroundColor))
    }

    private func confidenceBadge(_ confidence: Int) -> some View {
        let color: Color = confidence >= 80 ? .green : (confidence >= 50 ? .orange : .red)
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
