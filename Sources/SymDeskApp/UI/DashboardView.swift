import SwiftUI
import SymairaTheme
import SymDeskCore

// MARK: - DashboardView

struct DashboardView: View {
    @EnvironmentObject var core: DeskCore

    let docCounts: [String: Int]
    let docTotalCount: Int
    let notes: [Note]
    let doctorReport: DoctorReport?
    let onNavigate: (ContentView.DisplayMode) -> Void

    private var recentNotes: [Note] {
        notes.sorted { $0.modifiedAt > $1.modifiedAt }.prefix(5).map { $0 }
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 24) {
                headerSection

                quickStatsSection

                documentStatusSection

                recentNotesSection

                discoverLinkSection
            }
            .padding(28)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .navigationTitle("Dashboard")
    }

    // MARK: - Header

    private var headerSection: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text("Dashboard")
                    .font(.title2.bold())
                    .foregroundColor(SymairaTheme.textPrimary)
                Spacer()
                if let report = doctorReport {
                    HStack(spacing: 6) {
                        Image(systemName: report.overall == "ok" ? "checkmark.shield.fill" : "exclamationmark.shield.fill")
                            .foregroundColor(report.overall == "ok" ? SymairaTheme.goldPrimary : .orange)
                        Text(report.overall == "ok" ? "Healthy" : "Issues found")
                            .font(.caption)
                            .foregroundColor(SymairaTheme.textSecondary)
                    }
                }
            }
            Text("Your vault at a glance.")
                .font(.callout)
                .foregroundColor(SymairaTheme.textSecondary)
        }
    }

    // MARK: - Quick Stats

    private var quickStatsSection: some View {
        HStack(spacing: 14) {
            statCard(
                icon: "doc.text",
                label: "Documents",
                value: "\(docTotalCount)",
                color: SymairaTheme.goldPrimary
            )
            statCard(
                icon: "note.text",
                label: "Notes",
                value: "\(notes.count)",
                color: SymairaTheme.iceSecondary
            )
            if let needsReview = docCounts["needs_review"], needsReview > 0 {
                statCard(
                    icon: "exclamationmark.triangle",
                    label: "Needs Review",
                    value: "\(needsReview)",
                    color: .orange
                )
            }
            if let open = docCounts["open"], open > 0 {
                statCard(
                    icon: "circle",
                    label: "Open",
                    value: "\(open)",
                    color: SymairaTheme.goldSecondary
                )
            }
        }
    }

    private func statCard(icon: String, label: String, value: String, color: Color) -> some View {
        VStack(spacing: 6) {
            Image(systemName: icon)
                .font(.title2)
                .foregroundColor(color)
            Text(value)
                .font(.title.weight(.bold))
                .foregroundColor(SymairaTheme.textPrimary)
            Text(label)
                .font(.caption)
                .foregroundColor(SymairaTheme.textSecondary)
        }
        .frame(maxWidth: .infinity)
        .padding(.vertical, 16)
        .glassCard()
    }

    // MARK: - Document Status

    private var documentStatusSection: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Document Status")
                .font(.headline)
                .foregroundColor(SymairaTheme.goldPrimary)

            LazyVGrid(columns: [GridItem(.adaptive(minimum: 200, maximum: 300), spacing: 10)], spacing: 10) {
                ForEach(DocFilterPreset.defaults.dropFirst()) { preset in
                    let count = preset.status == nil ? docTotalCount : docCounts[preset.status!.rawValue] ?? 0
                    Button(action: {
                        onNavigate(.docs)
                    }) {
                        HStack {
                            Image(systemName: preset.status?.systemImage ?? "doc.text")
                                .foregroundColor(SymairaTheme.goldPrimary)
                            Text(preset.label)
                                .foregroundColor(SymairaTheme.textPrimary)
                            Spacer()
                            Text("\(count)")
                                .font(.caption.weight(.medium))
                                .foregroundColor(SymairaTheme.textSecondary)
                                .padding(.horizontal, 8)
                                .padding(.vertical, 3)
                                .background(Color.white.opacity(0.06))
                                .cornerRadius(4)
                        }
                        .padding(.horizontal, 12)
                        .padding(.vertical, 8)
                        .glassCard()
                    }
                    .buttonStyle(.plain)
                }
            }
        }
    }

    // MARK: - Recent Notes

    private var recentNotesSection: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("Recent Notes")
                .font(.headline)
                .foregroundColor(SymairaTheme.goldPrimary)

            if recentNotes.isEmpty {
                Text("No notes yet — create one from the sidebar.")
                    .font(.callout)
                    .foregroundColor(SymairaTheme.textMuted)
                    .padding(.vertical, 12)
            } else {
                ForEach(recentNotes) { note in
                    Button(action: {
                        onNavigate(.vault)
                    }) {
                        HStack {
                            Image(systemName: "doc.text")
                                .foregroundColor(SymairaTheme.iceSecondary)
                            Text(note.title)
                                .foregroundColor(SymairaTheme.textPrimary)
                                .lineLimit(1)
                            Spacer()
                            Text(shortDate(note.modifiedAt))
                                .font(.caption)
                                .foregroundColor(SymairaTheme.textMuted)
                        }
                        .padding(.horizontal, 12)
                        .padding(.vertical, 8)
                        .glassCard()
                    }
                    .buttonStyle(.plain)
                }
            }
        }
    }

    // MARK: - Discover Link

    private var discoverLinkSection: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("Explore")
                .font(.headline)
                .foregroundColor(SymairaTheme.goldPrimary)

            Button(action: {
                onNavigate(.discover)
            }) {
                HStack {
                    Image(systemName: "sparkles")
                        .foregroundColor(SymairaTheme.goldPrimary)
                    Text("Discover what SymDesk can do")
                        .foregroundColor(SymairaTheme.textPrimary)
                    Spacer()
                    Image(systemName: "chevron.right")
                        .foregroundColor(SymairaTheme.textMuted)
                }
                .padding(.horizontal, 12)
                .padding(.vertical, 10)
                .glassCard()
            }
            .buttonStyle(.plain)
        }
    }

    // MARK: - Helpers

    private func shortDate(_ raw: String) -> String {
        let f = ISO8601DateFormatter()
        f.formatOptions = [.withFullDate, .withTime, .withColonSeparatorInTime]
        guard let date = f.date(from: raw) else { return raw }
        let rel = RelativeDateTimeFormatter()
        rel.unitsStyle = .abbreviated
        return rel.localizedString(for: date, relativeTo: Date())
    }
}
