import AppIntents
import SwiftUI
import WidgetKit

/// App intent backing the (non-configurable) widget timeline. Exists so
/// `AppIntentConfiguration` has an intent type; the real work is done by
/// the action button intents.
struct VaultWidgetConfigurationIntent: WidgetConfigurationIntent {
    static var title: LocalizedStringResource { "SymDesk Vault" }
}

@main
struct SymDeskWidgetBundle: WidgetBundle {
    var body: some Widget {
        SymDeskVaultWidget()
    }
}

/// Home-screen widget: recent notes (shared App Group container) plus
/// quick actions "New note" and "Scan document". The lock-screen
/// accessory offers the scan action in a single tap.
struct SymDeskVaultWidget: Widget {
    var body: some WidgetConfiguration {
        AppIntentConfiguration(
            kind: "SymDeskVaultWidget",
            intent: VaultWidgetConfigurationIntent.self,
            provider: RecentsTimelineProvider()
        ) { entry in
            SymDeskVaultWidgetView(entry: entry)
        }
        .configurationDisplayName("SymDesk Vault")
        .description("Recent notes and quick actions — new note or scan document.")
        .supportedFamilies([.systemSmall, .systemMedium, .accessoryCircular])
    }
}

struct RecentsEntry: TimelineEntry {
    let date: Date
    let recents: [MobileRecentsStore.RecentItem]
}

struct RecentsTimelineProvider: AppIntentTimelineProvider {
    func placeholder(in context: Context) -> RecentsEntry {
        RecentsEntry(date: .now, recents: [])
    }

    func snapshot(for configuration: VaultWidgetConfigurationIntent, in context: Context) async -> RecentsEntry {
        RecentsEntry(date: .now, recents: MobileRecentsStore.read())
    }

    func timeline(for configuration: VaultWidgetConfigurationIntent, in context: Context) async -> Timeline<RecentsEntry> {
        // The app reloads timelines whenever recents change, so a single
        // never-expiring entry is enough.
        let entry = RecentsEntry(date: .now, recents: MobileRecentsStore.read())
        return Timeline(entries: [entry], policy: .never)
    }
}

struct SymDeskVaultWidgetView: View {
    @Environment(\.widgetFamily) private var family
    let entry: RecentsEntry

    var body: some View {
        switch family {
        case .accessoryCircular:
            Button(intent: OpenScanDocumentIntent()) {
                Image(systemName: "doc.viewfinder")
                    .font(.system(size: 22, weight: .semibold))
            }
            .buttonStyle(.plain)
            .widgetLabel {
                Text("Scan")
            }
            .containerBackground(for: .widget) { Color.black }
        case .systemMedium:
            HStack(spacing: 12) {
                recentsColumn
                Divider()
                actionsColumn
            }
            .containerBackground(for: .widget) { widgetBackground }
        default:
            VStack(alignment: .leading, spacing: 10) {
                HStack {
                    Text("SymDesk")
                        .font(.headline)
                    Spacer()
                    Button(intent: OpenNewNoteIntent()) {
                        Image(systemName: "square.and.pencil")
                    }
                    .buttonStyle(.plain)
                }
                recentsColumn
            }
            .containerBackground(for: .widget) { widgetBackground }
        }
    }

    private var widgetBackground: some View {
        LinearGradient(
            colors: [
                Color(red: 27 / 255, green: 25 / 255, blue: 21 / 255),
                Color(red: 13 / 255, green: 12 / 255, blue: 10 / 255)
            ],
            startPoint: .top,
            endPoint: .bottom
        )
    }

    private var recentsColumn: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("Recent")
                .font(.caption2.weight(.semibold))
                .foregroundStyle(.secondary)
            if entry.recents.isEmpty {
                Text("No recent notes")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            } else {
                ForEach(entry.recents.prefix(3)) { item in
                    Label(item.title, systemImage: "note.text")
                        .font(.caption)
                        .lineLimit(1)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private var actionsColumn: some View {
        VStack(spacing: 8) {
            Button(intent: OpenNewNoteIntent()) {
                Label("New note", systemImage: "square.and.pencil")
                    .font(.caption.weight(.medium))
                    .frame(maxWidth: .infinity)
            }
            .buttonStyle(.bordered)
            Button(intent: OpenScanDocumentIntent()) {
                Label("Scan", systemImage: "doc.viewfinder")
                    .font(.caption.weight(.medium))
                    .frame(maxWidth: .infinity)
            }
            .buttonStyle(.bordered)
        }
        .frame(maxWidth: .infinity)
    }
}
