import SwiftUI
import SymairaTheme
import SymDeskCore

/// Form-based editor for saved database views. Lets users create and edit
/// views (name, type, columns, sorts, filters and an all/any filter group)
/// without touching JSON.
struct DbViewEditor: View {
    let existing: DbView?
    var onSaved: () -> Void

    @EnvironmentObject var core: DeskCore
    @Environment(\.dismiss) private var dismiss

    struct EditableFilter: Identifiable {
        let id = UUID()
        var key = ""
        var op = ""
        var value = ""
    }

    struct EditableSort: Identifiable {
        let id = UUID()
        var key = ""
        var ascending = true
    }

    static let viewTypes = ["table", "list", "board", "gallery", "calendar", "timeline"]
    static let operators: [(String, String)] = [
        ("", "equals"),
        ("not_equals", "does not equal"),
        ("contains", "contains"),
        ("not_contains", "does not contain"),
        ("greater_than", "greater than"),
        ("less_than", "less than"),
        ("is_empty", "is empty"),
        ("is_not_empty", "is not empty"),
    ]

    @State private var name = ""
    @State private var type = "table"
    @State private var source = ""
    @State private var columnsText = ""
    @State private var groupBy = ""
    @State private var dateProperty = ""
    @State private var filters: [EditableFilter] = []
    @State private var groupOperator = "all"
    @State private var groupFilters: [EditableFilter] = []
    @State private var errorMessage: String?
    @State private var isSaving = false

    init(existing: DbView? = nil, onSaved: @escaping () -> Void) {
        self.existing = existing
        self.onSaved = onSaved
    }

    var body: some View {
        VStack(spacing: 0) {
            Form {
                Section("View") {
                    TextField("Name", text: $name)
                    Picker("Type", selection: $type) {
                        ForEach(Self.viewTypes, id: \.self) { Text($0.capitalized).tag($0) }
                    }
                    TextField("Source (optional)", text: $source)
                    TextField("Columns (comma separated)", text: $columnsText)
                    if type == "board" {
                        TextField("Group by property", text: $groupBy)
                    }
                    if type == "calendar" || type == "timeline" {
                        TextField("Date property", text: $dateProperty)
                    }
                }

                Section("Filters (all must match)") {
                    filterRows($filters)
                    Button("Add Filter") { filters.append(EditableFilter()) }
                }

                Section("Condition Group") {
                    Picker("Match", selection: $groupOperator) {
                        Text("All conditions").tag("all")
                        Text("Any condition").tag("any")
                    }
                    filterRows($groupFilters)
                    Button("Add Condition") { groupFilters.append(EditableFilter()) }
                }

                if let errorMessage {
                    Text(errorMessage)
                        .foregroundColor(.red)
                }
            }
            .formStyle(.grouped)

            Divider()
            HStack {
                if existing != nil {
                    Button("Delete View", role: .destructive) {
                        Task { await deleteView() }
                    }
                }
                Spacer()
                Button("Cancel") { dismiss() }
                Button(existing == nil ? "Create View" : "Save View") {
                    Task { await save() }
                }
                .keyboardShortcut(.defaultAction)
                .disabled(name.trimmingCharacters(in: .whitespaces).isEmpty || isSaving)
            }
            .padding()
        }
        .frame(minWidth: 520, minHeight: 480)
        .onAppear { populate() }
    }

    @ViewBuilder
    private func filterRows(_ items: Binding<[EditableFilter]>) -> some View {
        ForEach(items) { $filter in
            HStack {
                TextField("Property", text: $filter.key)
                    .frame(minWidth: 100)
                Picker("", selection: $filter.op) {
                    ForEach(Self.operators, id: \.0) { op in
                        Text(op.1).tag(op.0)
                    }
                }
                .labelsHidden()
                if filter.op != "is_empty" && filter.op != "is_not_empty" {
                    TextField("Value", text: $filter.value)
                        .frame(minWidth: 100)
                }
                Button {
                    items.wrappedValue.removeAll { $0.id == filter.id }
                } label: {
                    Image(systemName: "minus.circle")
                }
                .buttonStyle(.plain)
            }
        }
    }

    private func populate() {
        guard let view = existing else { return }
        name = view.name
        type = view.type ?? "table"
        source = view.source ?? ""
        columnsText = view.columns.joined(separator: ", ")
        groupBy = view.groupBy ?? ""
        dateProperty = view.dateProperty ?? ""
        filters = view.filters.map { EditableFilter(key: $0.key, op: $0.operatorString, value: $0.value) }
        if let group = view.filterGroup {
            groupOperator = group.operatorString.isEmpty ? "all" : group.operatorString
            groupFilters = (group.filters ?? []).map { EditableFilter(key: $0.key, op: $0.operatorString, value: $0.value) }
        }
    }

    private func buildView() -> DbView {
        let columns = columnsText
            .split(separator: ",")
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty }
        let flatFilters = filters
            .filter { !$0.key.isEmpty }
            .map { DbFilter(key: $0.key, operatorString: $0.op, value: $0.value) }
        let groupItems = groupFilters
            .filter { !$0.key.isEmpty }
            .map { DbFilter(key: $0.key, operatorString: $0.op, value: $0.value) }
        // Preserve nested sub-groups that the form does not surface yet.
        let existingSubGroups = existing?.filterGroup?.groups
        let group: DbFilterGroup? = (groupItems.isEmpty && (existingSubGroups ?? []).isEmpty)
            ? nil
            : DbFilterGroup(operatorString: groupOperator, filters: groupItems, groups: existingSubGroups)

        return DbView(
            id: existing?.id ?? "",
            name: name.trimmingCharacters(in: .whitespaces),
            type: type,
            groupBy: groupBy.isEmpty ? nil : groupBy,
            dateProperty: dateProperty.isEmpty ? nil : dateProperty,
            computed: existing?.computed,
            filters: flatFilters,
            filterGroup: group,
            sorts: existing?.sorts ?? [],
            columns: columns,
            source: source.isEmpty ? nil : source,
            template: existing?.template
        )
    }

    private func save() async {
        isSaving = true
        defer { isSaving = false }
        do {
            try await core.viewsSave(buildView())
            onSaved()
            dismiss()
        } catch {
            errorMessage = "Failed to save view: \(error.localizedDescription)"
        }
    }

    private func deleteView() async {
        guard let view = existing else { return }
        do {
            try await core.viewsDelete(id: view.id)
            onSaved()
            dismiss()
        } catch {
            errorMessage = "Failed to delete view: \(error.localizedDescription)"
        }
    }
}
