import SwiftUI
import SymroomKit

/// Filterable journal viewer over `symroom log --json` events.
struct JournalView: View {
    let events: [JournalEvent]
    @State private var filterText = ""
    @State private var kindFilter = "All"

    private var kinds: [String] {
        var set = Set(events.map(\.kind))
        set.insert("All")
        return set.sorted()
    }

    private var filtered: [JournalEvent] {
        events.filter { event in
            let kindMatches = kindFilter == "All" || event.kind == kindFilter
            guard kindMatches else { return false }
            guard !filterText.isEmpty else { return true }
            let haystack = "\(event.id) \(event.author) \(event.kind) \(event.body?.displayString ?? "")"
            return haystack.localizedCaseInsensitiveContains(filterText)
        }
    }

    var body: some View {
        VStack(spacing: 0) {
            HStack {
                TextField("Filter journal…", text: $filterText)
                    .textFieldStyle(.roundedBorder)
                    .frame(maxWidth: 260)
                Picker("Kind", selection: $kindFilter) {
                    ForEach(kinds, id: \.self) { kind in
                        Text(kind).tag(kind)
                    }
                }
                .pickerStyle(.menu)
                .frame(maxWidth: 180)
                Spacer()
                Text("\(filtered.count) of \(events.count)")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }
            .padding(8)

            Divider()

            if filtered.isEmpty {
                ContentUnavailableView("No journal entries", systemImage: "doc.text")
            } else {
                List(filtered) { event in
                    JournalRow(event: event)
                }
            }
        }
    }
}

private struct JournalRow: View {
    let event: JournalEvent

    var body: some View {
        VStack(alignment: .leading, spacing: 2) {
            HStack {
                Text(event.kind)
                    .font(.caption.weight(.semibold))
                    .padding(.horizontal, 6)
                    .padding(.vertical, 1)
                    .background(.quaternary, in: Capsule())
                Text(event.author)
                    .font(.caption)
                    .foregroundStyle(.secondary)
                Spacer()
                Text(event.ts)
                    .font(.caption2.monospaced())
                    .foregroundStyle(.tertiary)
            }
            Text(shortBody)
                .font(.callout)
                .lineLimit(2)
        }
        .padding(.vertical, 2)
    }

    private var shortBody: String {
        guard let eventBody = event.body else { return event.id }
        let text = eventBody.displayString
        return text.count > 120 ? String(text.prefix(120)) + "…" : text
    }
}
