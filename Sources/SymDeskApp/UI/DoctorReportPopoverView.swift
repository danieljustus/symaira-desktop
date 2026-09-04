import SwiftUI
import SymairaTheme
import SymDeskCore

// MARK: - Doctor Report Popover View

struct DoctorReportPopoverView: View {
    let report: DoctorReport?

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("System Diagnostics")
                .symairaText(.subheading)
                .foregroundColor(SymairaTheme.textPrimary)

            if let report {
                VStack(alignment: .leading, spacing: 8) {
                    statusRow(label: "Vault", status: report.vault?.status, detail: report.vault?.message ?? report.vault?.path)
                    statusRow(label: "Sidecar Index", status: report.sidecar?.status, detail: report.sidecar?.message)
                    statusRow(label: "Contract & ASN", status: report.contract?.status, detail: report.contract?.message ?? (report.contract?.filesFound.map { "\($0) files scanned" }))
                    if let ai = report.ai {
                        HStack {
                            Text("AI Provider:").symairaText(.caption).fontWeight(.medium).foregroundColor(SymairaTheme.textSecondary)
                            Text("\(ai.provider ?? "Ollama") \(ai.model ?? "")").symairaText(.caption).foregroundColor(SymairaTheme.textPrimary)
                        }
                    }
                    Divider()
                    Text("Tools:").symairaText(.caption).fontWeight(.semibold).foregroundColor(SymairaTheme.textSecondary)
                    VStack(alignment: .leading, spacing: 4) {
                        toolRow("symmemory", report: report)
                        toolRow("symvault", report: report)
                        toolRow("symbrowse", report: report)
                    }
                }
            } else {
                Text("Doctor report unavailable.")
                    .symairaText(.caption)
                    .foregroundColor(SymairaTheme.textSecondary)
            }
        }
        .padding(16)
        .frame(width: 280)
    }

    private func statusRow(label: String, status: String?, detail: String?) -> some View {
        HStack(alignment: .top) {
            Image(systemName: status == "ok" ? "checkmark.circle.fill" : "exclamationmark.triangle.fill")
                .foregroundColor(status == "ok" ? .green : .orange)
                .symairaText(.caption)
            VStack(alignment: .leading, spacing: 2) {
                Text("\(label): \(status ?? "unknown")")
                    .symairaText(.caption).fontWeight(.medium)
                    .foregroundColor(SymairaTheme.textPrimary)
                if let detail, !detail.isEmpty {
                    Text(detail)
                        .symairaText(.caption)
                        .foregroundColor(SymairaTheme.textSecondary)
                }
            }
        }
    }

    private func toolRow(_ tool: String, report: DoctorReport) -> some View {
        let isAvail = report.tools.isAvailable(tool)
        let version = report.versions?[tool] ?? ""
        return HStack {
            Image(systemName: isAvail ? "checkmark" : "xmark")
                .symairaText(.caption)
                .foregroundColor(isAvail ? .green : .secondary)
            Text(tool)
                .symairaText(.caption)
                .foregroundColor(SymairaTheme.textPrimary)
            Spacer()
            if !version.isEmpty {
                Text("v\(version)")
                    .symairaText(.caption)
                    .foregroundColor(SymairaTheme.textMuted)
            }
        }
    }
}
