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

            if let report {
                VStack(alignment: .leading, spacing: 8) {
                    statusRow(label: "Vault", status: report.vault?.status, detail: report.vault?.message ?? report.vault?.path)
                    statusRow(label: "Sidecar Index", status: report.sidecar?.status, detail: report.sidecar?.message)
                    statusRow(label: "Contract & ASN", status: report.contract?.status, detail: report.contract?.message ?? (report.contract?.filesFound.map { "\($0) files scanned" }))
                    if let ai = report.ai {
                        HStack {
                            Text("AI Provider:").symairaText(.secondary)
                            Text("\(ai.provider ?? "Ollama") \(ai.model ?? "")").symairaText(.callout)
                        }
                    }
                    Divider().overlay(SymairaTokens.line)
                    Text("Tools:").symairaText(.secondary)
                    VStack(alignment: .leading, spacing: 4) {
                        toolRow("symmemory", report: report)
                        toolRow("symvault", report: report)
                        toolRow("symbrowse", report: report)
                    }
                }
            } else {
                Text("Doctor report unavailable.")
                    .symairaText(.secondary)
            }
        }
        .padding(SymairaSpacing.large)
        .frame(width: 280)
    }

    private func statusRow(label: String, status: String?, detail: String?) -> some View {
        HStack(alignment: .top) {
            SymairaIconView(status == "ok" ? .success : .warning, size: 14)
                .foregroundStyle(status == "ok" ? SymairaTokens.positive : SymairaTokens.warning)
            VStack(alignment: .leading, spacing: 2) {
                Text("\(label): \(status ?? "unknown")")
                    .symairaText(.callout)
                if let detail, !detail.isEmpty {
                    Text(detail)
                        .symairaText(.secondary)
                }
            }
        }
    }

    private func toolRow(_ tool: String, report: DoctorReport) -> some View {
        let isAvail = report.tools.isAvailable(tool)
        let version = report.versions?[tool] ?? ""
        return HStack {
            SymairaIconView(isAvail ? .confirm : .close, size: 14)
                .foregroundStyle(isAvail ? SymairaTokens.positive : SymairaTokens.ink3)
            Text(tool)
                .symairaText(.callout)
            Spacer()
            if !version.isEmpty {
                Text("v\(version)")
                    .symairaText(.caption)
            }
        }
    }
}
