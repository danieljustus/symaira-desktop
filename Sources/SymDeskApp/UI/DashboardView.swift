import SwiftUI
import SymairaTheme
import SymDeskCore

// MARK: - DashboardView

struct DashboardView: View {
    @EnvironmentObject var core: DeskCore

    let docCounts: [String: Int]
    let docTypeCounts: [String: Int]
    let docTotalCount: Int
    let notes: [Note]
    let doctorReport: DoctorReport?
    let onNavigate: (ContentView.DisplayMode) -> Void
    var onOpenNote: ((Note) -> Void)? = nil

    private var recentNotes: [Note] {
        notes.sorted { $0.modifiedAt > $1.modifiedAt }.prefix(5).map { $0 }
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: SymairaSpacing.xLarge) {
                headerSection

                quickStatsSection

                documentStatusSection

                recentNotesSection

                discoverLinkSection
            }
            .padding(SymairaSpacing.xLarge)
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .navigationTitle("Dashboard")
    }

    // MARK: - Header

    private var headerSection: some View {
        VStack(alignment: .leading, spacing: SymairaSpacing.small) {
            HStack {
                Text("Dashboard")
                    .symairaText(.display)
                    .topoProtected()
                Spacer()
                if let report = doctorReport {
                    SymairaStatusLabel(
                        report.overall == "ok" ? "Healthy" : "Issues found",
                        tone: report.overall == "ok" ? .positive : .warning
                    )
                    .topoProtected()
                }
            }
            Text("Your vault at a glance.")
                .symairaText(.secondary)
                .topoProtected()
        }
    }

    // MARK: - Quick Stats

    private var quickStatsSection: some View {
        HStack(spacing: SymairaSpacing.medium) {
            SymairaStatTile(value: "\(docTotalCount)", label: "Documents")
            SymairaStatTile(value: "\(notes.count)", label: "Notes")
            if let needsReview = docCounts["needs_review"], needsReview > 0 {
                SymairaStatTile(value: "\(needsReview)", label: "Needs Review")
            }
            if let open = docCounts["open"], open > 0 {
                SymairaStatTile(value: "\(open)", label: "Open")
            }
        }
    }

    // MARK: - Document Status

    private var documentStatusSection: some View {
        VStack(alignment: .leading, spacing: SymairaSpacing.medium) {
            Text("Document Status")
                .symairaText(.subheading)
                .topoProtected()

            LazyVGrid(
                columns: [GridItem(.adaptive(minimum: 200, maximum: 300), spacing: SymairaSpacing.medium)],
                spacing: SymairaSpacing.medium
            ) {
                ForEach(DocFilterPreset.defaults.dropFirst()) { preset in
                    let count = preset.displayCount(
                        statusCounts: docCounts,
                        typeCounts: docTypeCounts,
                        total: docTotalCount
                    )
                    Button(action: {
                        onNavigate(.docs)
                    }) {
                        HStack {
                            SymairaIconView(preset.status?.symairaIcon ?? presetTypeIcon(preset.fileType), size: 16)
                                .foregroundStyle(SymairaTokens.ink2)
                            Text(preset.label)
                                .symairaText(.body)
                            Spacer()
                            Text("\(count)")
                                .symairaText(.caption)
                                .monospacedDigit()
                                .padding(.horizontal, SymairaSpacing.small)
                                .padding(.vertical, 3)
                                .background(SymairaTokens.raised, in: RoundedRectangle(cornerRadius: SymairaTokens.Radius.sm))
                        }
                        .symairaCard(padding: SymairaSpacing.medium)
                        .topoProtected(hill: true)
                    }
                    .buttonStyle(.plain)
                }
            }
        }
    }

    // MARK: - Recent Notes

    private var recentNotesSection: some View {
        VStack(alignment: .leading, spacing: SymairaSpacing.medium) {
            Text("Recent Notes")
                .symairaText(.subheading)
                .topoProtected()

            if recentNotes.isEmpty {
                Text("No notes yet — create one from the sidebar.")
                    .symairaText(.secondary)
                    .padding(.vertical, SymairaSpacing.medium)
                    .topoProtected()
            } else {
                ForEach(recentNotes) { note in
                    Button(action: {
                        // Open the note itself, not an empty Vault view (issue #649).
                        if let onOpenNote {
                            onOpenNote(note)
                        } else {
                            onNavigate(.vault)
                        }
                    }) {
                        HStack {
                            SymairaIconView(.document, size: 16)
                                .foregroundStyle(SymairaTokens.ink2)
                            Text(note.title)
                                .symairaText(.body)
                                .lineLimit(1)
                            Spacer()
                            Text(shortDate(note.modifiedAt))
                                .symairaText(.caption)
                        }
                        .symairaCard(padding: SymairaSpacing.medium)
                        .topoProtected(hill: true)
                    }
                    .buttonStyle(.plain)
                }
            }
        }
    }

    // MARK: - Discover Link

    private var discoverLinkSection: some View {
        VStack(alignment: .leading, spacing: SymairaSpacing.small) {
            Text("Explore")
                .symairaText(.subheading)
                .topoProtected()

            Button(action: {
                onNavigate(.discover)
            }) {
                HStack {
                    SymairaIconView(.ai, size: 16)
                        .foregroundStyle(SymairaTokens.ink2)
                    Text("Discover what SymDesk can do")
                        .symairaText(.body)
                    Spacer()
                    SymairaIconView(.forward, size: 16)
                        .foregroundStyle(SymairaTokens.ink3)
                }
                .symairaCard(padding: SymairaSpacing.medium)
                .topoProtected(hill: true)
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

/// Icon for a type-only filter preset (no workflow status).
private func presetTypeIcon(_ fileType: String?) -> SymairaIcon {
    switch fileType {
    case "meeting": .team
    case "note": .note
    default: .document
    }
}
