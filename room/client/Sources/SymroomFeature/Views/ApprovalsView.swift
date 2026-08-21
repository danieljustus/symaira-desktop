import SwiftUI
import SymroomKit

/// Pending runs with an approve/deny sheet showing scope and TTL.
struct ApprovalsView: View {
    let runs: [Run]
    @State private var selectedRun: Run?

    var body: some View {
        Group {
            if runs.isEmpty {
                ContentUnavailableView("No pending approvals", systemImage: "checkmark.circle")
            } else {
                List(runs) { run in
                    Button {
                        selectedRun = run
                    } label: {
                        PendingRunRow(run: run)
                    }
                    .buttonStyle(.plain)
                }
            }
        }
        .sheet(item: $selectedRun) { run in
            ApprovalSheet(run: run)
        }
    }
}

private struct PendingRunRow: View {
    let run: Run

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: "clock.badge.questionmark")
                .foregroundStyle(.orange)
                .frame(width: 20)
            VStack(alignment: .leading, spacing: 2) {
                Text(run.title)
                    .font(.body)
                Text(run.id)
                    .font(.caption2.monospaced())
                    .foregroundStyle(.tertiary)
            }
            Spacer()
            Text(run.author)
                .font(.caption)
                .foregroundStyle(.secondary)
            if let adapter = run.adapter, !adapter.isEmpty {
                Text(adapter)
                    .font(.caption2)
                    .padding(.horizontal, 6)
                    .padding(.vertical, 1)
                    .background(.quaternary, in: Capsule())
            }
        }
        .padding(.vertical, 2)
    }
}

/// Approve/deny sheet: shows the requested scope and TTL, lets the human
/// adjust scope and TTL before approving — same signed events as the CLI.
struct ApprovalSheet: View {
    @Environment(\.dismiss) private var dismiss
    @Environment(RoomAppState.self) private var appState

    let run: Run

    @State private var scope: String = ""
    @State private var ttl: String = "30m"
    @State private var reason: String = ""
    @State private var isBusy = false
    @State private var errorText: String?

    private static let ttlOptions = ["15m", "30m", "1h", "6h", "24h"]

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            Text("Approve Run")
                .font(.headline)

            VStack(alignment: .leading, spacing: 4) {
                Text(run.title).font(.body.weight(.medium))
                Text(run.id).font(.caption2.monospaced()).foregroundStyle(.tertiary)
                if let author = run.author.isEmpty ? nil : run.author {
                    Text("Requested by \(author)").font(.caption).foregroundStyle(.secondary)
                }
            }

            if let planFile = run.planFile, !planFile.isEmpty {
                Label("Plan: \(planFile)", systemImage: "doc.plaintext")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            // Requested scope (from the run) + editable scope.
            TextField("Scope", text: $scope)
                .textFieldStyle(.roundedBorder)
            if let requestedScope = run.scope, !requestedScope.isEmpty, scope.isEmpty {
                Text("Requested scope: \(requestedScope)")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            Picker("TTL", selection: $ttl) {
                ForEach(Self.ttlOptions, id: \.self) { option in
                    Text(option).tag(option)
                }
                Text("custom").tag("custom")
            }
            .pickerStyle(.segmented)

            if ttl == "custom" {
                TextField("Custom TTL (e.g. 90m)", text: $ttl)
                    .textFieldStyle(.roundedBorder)
            }

            if errorText != nil {
                Text(errorText ?? "")
                    .font(.caption)
                    .foregroundStyle(.red)
            }

            HStack {
                Spacer()
                Button("Deny") {
                    deny()
                }
                .keyboardShortcut(.cancelAction)
                .disabled(isBusy)
                Button("Approve") {
                    approve()
                }
                .keyboardShortcut(.defaultAction)
                .disabled(isBusy)
            }
        }
        .padding(20)
        .frame(width: 420)
        .onAppear {
            scope = run.scope ?? ""
            if let expires = run.expiresAt, !expires.isEmpty {
                ttl = "custom"
            }
        }
    }

    private func approve() {
        isBusy = true
        errorText = nil
        let effectiveTTL = ttl == "custom" ? nil : ttl
        Task {
            let result = await appState.approve(runID: run.id, scope: scope.isEmpty ? nil : scope, ttl: effectiveTTL)
            if result != nil {
                dismiss()
            } else {
                errorText = appState.lastError ?? "Approval failed"
                isBusy = false
            }
        }
    }

    private func deny() {
        isBusy = true
        errorText = nil
        Task {
            let result = await appState.deny(runID: run.id, reason: reason.isEmpty ? "Denied from hub module" : reason)
            if result != nil {
                dismiss()
            } else {
                errorText = appState.lastError ?? "Deny failed"
                isBusy = false
            }
        }
    }
}
