import SwiftUI
import SymairaTheme
import SymDeskCore

/// Displays all tags in the vault with file counts, sorted by count or
/// alphabetically, with a filter field. Clicking a tag navigates to the
/// document grid filtered by that tag.
struct TagBrowserView: View {
    let tags: [TagEntry]
    let onTagClick: (String) -> Void

    @State private var filterText = ""
    @State private var sortByCount = true

    private var filteredTags: [TagEntry] {
        let result: [TagEntry]
        if filterText.isEmpty {
            result = tags
        } else {
            let q = filterText.lowercased()
            result = tags.filter { $0.name.lowercased().contains(q) }
        }
        if sortByCount {
            return result.sorted { $0.count == $1.count ? $0.name < $1.name : $0.count > $1.count }
        } else {
            return result.sorted { $0.name.localizedCompare($1.name) == .orderedAscending }
        }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            // Filter field
            HStack(spacing: 4) {
                Image(systemName: "magnifyingglass")
                    .symairaText(.caption)
                    .foregroundColor(SymairaTheme.textMuted)
                TextField("Filter tags…", text: $filterText)
                    .textFieldStyle(.plain)
                    .symairaText(.caption)
                    .foregroundColor(SymairaTheme.textPrimary)
                if !filterText.isEmpty {
                    Button(action: { filterText = "" }) {
                        Image(systemName: "xmark.circle.fill")
                            .symairaText(.caption)
                            .foregroundColor(SymairaTheme.textMuted)
                    }
                    .buttonStyle(.plain)
                }
            }
            .padding(.horizontal, 8)
            .padding(.vertical, 6)

            // Sort toggle
            HStack(spacing: 4) {
                Button(action: { sortByCount = true }) {
                    Text("Count")
                        .symairaText(.caption)
                        .foregroundColor(sortByCount ? SymairaTheme.goldPrimary : SymairaTheme.textMuted)
                }
                .buttonStyle(.plain)
                Text("·")
                    .symairaText(.caption)
                    .foregroundColor(SymairaTheme.textMuted)
                Button(action: { sortByCount = false }) {
                    Text("A–Z")
                        .symairaText(.caption)
                        .foregroundColor(!sortByCount ? SymairaTheme.goldPrimary : SymairaTheme.textMuted)
                }
                .buttonStyle(.plain)
                Spacer()
                Text("\(filteredTags.count) tag\(filteredTags.count == 1 ? "" : "s")")
                    .symairaText(.caption)
                    .foregroundColor(SymairaTheme.textMuted)
            }
            .padding(.horizontal, 8)
            .padding(.bottom, 4)

            // Tag list
            if filteredTags.isEmpty {
                VStack(spacing: 6) {
                    Image(systemName: "tag")
                        .symairaText(.heading)
                        .foregroundColor(SymairaTheme.textMuted)
                    Text(tags.isEmpty ? "No tags yet" : "No tags match")
                        .symairaText(.caption)
                        .foregroundColor(SymairaTheme.textMuted)
                }
                .frame(maxWidth: .infinity)
                .padding(.vertical, 16)
            } else {
                ScrollView {
                    LazyVStack(alignment: .leading, spacing: 1) {
                        ForEach(filteredTags) { tag in
                            Button(action: { onTagClick(tag.name) }) {
                                HStack(spacing: 6) {
                                    Image(systemName: "tag.fill")
                                        .symairaText(.caption)
                                        .foregroundColor(SymairaTheme.goldSecondary)
                                    Text(tag.name)
                                        .symairaText(.caption)
                                        .foregroundColor(SymairaTheme.textPrimary)
                                        .lineLimit(1)
                                    Spacer()
                                    Text("\\(tag.count)")
                                        .symairaText(.caption)
                                        .foregroundColor(SymairaTheme.textSecondary)
                                        .padding(.horizontal, 5)
                                        .padding(.vertical, 1)
                                        .background(Color.white.opacity(0.06))
                                        .cornerRadius(3)
                                }
                                .padding(.horizontal, 8)
                                .padding(.vertical, 3)
                                .contentShape(Rectangle())
                            }
                            .buttonStyle(.plain)
                        }
                    }
                }
                .scrollContentBackground(.hidden)
            }
        }
    }
}
