import SwiftUI
import SymairaTheme

// MARK: - Filter Models

/// Supported filter fields for the search query language.
enum SearchFilterField: String, CaseIterable, Identifiable {
    case tag
    case type
    case status
    case correspondent
    case folder
    case path
    case date

    var id: String { rawValue }

    var label: String {
        switch self {
        case .tag: return "Tag"
        case .type: return "Type"
        case .status: return "Status"
        case .correspondent: return "Correspondent"
        case .folder: return "Folder"
        case .path: return "Path"
        case .date: return "Date"
        }
    }

    var icon: String {
        switch self {
        case .tag: return "tag"
        case .type: return "doc.text"
        case .status: return "flag"
        case .correspondent: return "person"
        case .folder: return "folder"
        case .path: return "link"
        case .date: return "calendar"
        }
    }
}

/// A single filter condition that compiles to the existing query language.
struct SearchFilter: Identifiable, Equatable {
    let id: String
    let field: SearchFilterField
    let value: String

    init(field: SearchFilterField, value: String) {
        self.id = "\(field.rawValue):\(value)"
        self.field = field
        self.value = value
    }

    /// Query-language fragment, e.g. `tag:important` or `type:invoice`.
    var queryString: String {
        "\(field.rawValue):\(value)"
    }

    var displayLabel: String {
        "\(field.label): \(value)"
    }
}

// MARK: - Flow Layout

/// A simple wrapping flow layout that places subviews left-to-right and wraps
/// when they exceed the available width.
struct FilterFlowLayout: Layout {
    var spacing: CGFloat = 8

    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let width = proposal.width ?? 0
        guard width > 0 else { return .zero }

        var height: CGFloat = 0
        var x: CGFloat = 0
        var y: CGFloat = 0
        var rowHeight: CGFloat = 0

        for subview in subviews {
            let size = subview.sizeThatFits(.unspecified)
            if x + size.width > width, rowHeight > 0 {
                y += rowHeight + spacing
                x = 0
                rowHeight = 0
            }
            rowHeight = max(rowHeight, size.height)
            x += size.width + spacing
        }
        height = y + rowHeight
        return CGSize(width: width, height: max(height, 0))
    }

    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var x: CGFloat = bounds.minX
        var y: CGFloat = bounds.minY
        var rowHeight: CGFloat = 0

        for subview in subviews {
            let size = subview.sizeThatFits(.unspecified)
            if x + size.width > bounds.maxX, rowHeight > 0 {
                y += rowHeight + spacing
                x = bounds.minX
                rowHeight = 0
            }
            subview.place(at: CGPoint(x: x, y: y), proposal: .unspecified)
            rowHeight = max(rowHeight, size.height)
            x += size.width + spacing
        }
    }
}

// MARK: - FilterChipsView

/// A shared filter chip / token field that compiles down to the existing query
/// language. Chips display active filters with a remove button; an "Add Filter"
/// button opens a popover to pick field → value.
///
/// Used by the command palette, the document library grid, and note list views.
struct FilterChipsView: View {
    @Binding var filters: [SearchFilter]

    /// Available values shown in the value picker for each field type.
    var availableTypes: [String] = []
    var availableStatuses: [String] = []
    var availableCorrespondents: [String] = []
    var availableFolders: [String] = []

    @State private var showPopover = false
    @State private var pickerStep: PickerStep = .selectingField
    @State private var customText = ""

    private enum PickerStep {
        case selectingField
        case selectingValue(SearchFilterField)
    }

    var body: some View {
        FilterFlowLayout(spacing: 6) {
            ForEach(filters) { filter in
                filterChipView(filter)
            }
            addFilterButton
        }
        .popover(isPresented: $showPopover, arrowEdge: .bottom) {
            popoverContent
                .frame(minWidth: 220, idealWidth: 240)
                .padding(12)
        }
    }

    // MARK: - Chip Rendering

    private func filterChipView(_ filter: SearchFilter) -> some View {
        HStack(spacing: 4) {
            Image(systemName: filter.field.icon)
                .font(.caption2)
                .foregroundColor(SymairaTheme.goldPrimary)
            Text(filter.field.label + ":")
                .font(.caption.weight(.medium))
                .foregroundColor(SymairaTheme.goldSecondary)
            Text(filter.value)
                .font(.caption)
                .foregroundColor(SymairaTheme.textPrimary)
                .lineLimit(1)
            Button(action: { removeFilter(filter) }) {
                Image(systemName: "xmark")
                    .font(.caption2)
                    .foregroundColor(SymairaTheme.textMuted)
            }
            .buttonStyle(.plain)
            .help("Remove \(filter.displayLabel)")
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 5)
        .background(SymairaTheme.bgCardHover.opacity(0.7))
        .cornerRadius(8)
        .overlay(
            RoundedRectangle(cornerRadius: 8)
                .stroke(SymairaTheme.goldPrimary.opacity(0.25), lineWidth: 1)
        )
        .transition(.scale.combined(with: .opacity))
    }

    // MARK: - Add Filter Button

    private var addFilterButton: some View {
        Button(action: {
            pickerStep = .selectingField
            customText = ""
            showPopover = true
        }) {
            HStack(spacing: 3) {
                Image(systemName: "plus.circle")
                    .font(.caption)
                Text("Filter")
                    .font(.caption)
            }
            .foregroundColor(SymairaTheme.goldSecondary)
            .padding(.horizontal, 8)
            .padding(.vertical, 4)
            .background(SymairaTheme.bgCardHover.opacity(0.4))
            .cornerRadius(8)
            .overlay(
                RoundedRectangle(cornerRadius: 8)
                    .stroke(SymairaTheme.borderGlassHover, lineWidth: 0.5)
            )
        }
        .buttonStyle(.plain)
        .help("Add a filter")
    }

    // MARK: - Popover Content

    @ViewBuilder
    private var popoverContent: some View {
        switch pickerStep {
        case .selectingField:
            fieldPickerView
        case .selectingValue(let field):
            valuePickerView(for: field)
        }
    }

    private var fieldPickerView: some View {
        VStack(alignment: .leading, spacing: 6) {
            Text("Add Filter")
                .font(.headline)
                .foregroundColor(SymairaTheme.goldPrimary)

            Text("Select a field to filter by")
                .font(.caption)
                .foregroundColor(SymairaTheme.textMuted)

            Divider()
                .background(SymairaTheme.borderGlassHover)

            ForEach(SearchFilterField.allCases) { field in
                Button(action: {
                    pickerStep = .selectingValue(field)
                }) {
                    HStack(spacing: 8) {
                        Image(systemName: field.icon)
                            .foregroundColor(SymairaTheme.goldPrimary)
                            .frame(width: 16)
                        Text(field.label)
                            .foregroundColor(SymairaTheme.textPrimary)
                        Spacer()
                        Image(systemName: "chevron.right")
                            .font(.caption2)
                            .foregroundColor(SymairaTheme.textMuted)
                    }
                    .padding(.vertical, 6)
                    .padding(.horizontal, 4)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
            }
        }
    }

    @ViewBuilder
    private func valuePickerView(for field: SearchFilterField) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack {
                Button(action: { pickerStep = .selectingField }) {
                    HStack(spacing: 4) {
                        Image(systemName: "chevron.left")
                            .font(.caption)
                        Text("Back")
                            .font(.caption)
                    }
                    .foregroundColor(SymairaTheme.goldSecondary)
                }
                .buttonStyle(.plain)

                Spacer()

                Text(field.label)
                    .font(.headline)
                    .foregroundColor(SymairaTheme.goldPrimary)
            }

            Divider()
                .background(SymairaTheme.borderGlassHover)

            switch field {
            case .tag, .correspondent, .folder, .path:
                customValueField(for: field)
            case .type:
                valueList(values: availableTypes)
            case .status:
                valueList(values: availableStatuses)
            case .date:
                Text("Date range filter coming soon")
                    .font(.caption)
                    .foregroundColor(SymairaTheme.textMuted)
                    .padding(.vertical, 8)
            }
        }
    }

    private func customValueField(for field: SearchFilterField) -> some View {
        VStack(spacing: 8) {
            TextField("Enter \(field.label.lowercased()) name…", text: $customText)
                .textFieldStyle(.plain)
                .padding(8)
                .background(SymairaTheme.bgCard.opacity(0.5))
                .cornerRadius(6)
                .overlay(
                    RoundedRectangle(cornerRadius: 6)
                        .stroke(SymairaTheme.borderGlassHover, lineWidth: 0.5)
                )
                .onSubmit { addCustomValue(field: field) }

            Button("Add \(field.label)") { addCustomValue(field: field) }
                .buttonStyle(.borderedProminent)
                .tint(SymairaTheme.goldPrimary.opacity(0.8))
                .controlSize(.small)
                .disabled(customText.trimmingCharacters(in: .whitespaces).isEmpty)
        }
    }

    private func valueList(values: [String]) -> some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 2) {
                let filtered = values
                    .filter { !$0.isEmpty }
                    .reduce(into: [String]()) { result, value in
                        if !result.contains(value) { result.append(value) }
                    }
                    .sorted()

                if filtered.isEmpty {
                    Text("No values available")
                        .font(.caption)
                        .foregroundColor(SymairaTheme.textMuted)
                        .padding(.vertical, 8)
                } else {
                    ForEach(filtered, id: \.self) { value in
                        Button(action: {
                            addFilter(SearchFilter(field: fieldFromValues, value: value))
                        }) {
                            HStack {
                                Text(value)
                                    .foregroundColor(SymairaTheme.textPrimary)
                                    .font(.callout)
                                Spacer()
                                if filters.contains(where: { $0.value == value }) {
                                    Image(systemName: "checkmark")
                                        .font(.caption)
                                        .foregroundColor(SymairaTheme.goldPrimary)
                                }
                            }
                            .padding(.vertical, 5)
                            .padding(.horizontal, 4)
                            .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                    }
                }
            }
        }
        .frame(maxHeight: 180)
    }

    /// The field being filtered by in the current value-picker step. Used
    /// inside `valueList` to construct the `SearchFilter`.
    private var fieldFromValues: SearchFilterField {
        if case .selectingValue(let field) = pickerStep {
            return field
        }
        return .tag
    }

    // MARK: - Actions

    private func removeFilter(_ filter: SearchFilter) {
        withAnimation(.easeInOut(duration: 0.15)) {
            filters.removeAll { $0.id == filter.id }
        }
    }

    private func addFilter(_ filter: SearchFilter) {
        guard !filters.contains(where: { $0.id == filter.id }) else {
            showPopover = false
            return
        }
        withAnimation(.easeInOut(duration: 0.15)) {
            filters.append(filter)
        }
        showPopover = false
        customText = ""
    }

    private func addCustomValue(field: SearchFilterField) {
        let text = customText.trimmingCharacters(in: .whitespaces)
        guard !text.isEmpty else { return }
        addFilter(SearchFilter(field: field, value: text))
    }
}
