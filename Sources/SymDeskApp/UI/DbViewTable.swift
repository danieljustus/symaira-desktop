import SwiftUI
import SymairaTheme
import SymDeskCore

struct DbViewTable: View {
    let viewID: String
    @EnvironmentObject var core: DeskCore
    @State private var rows: [[String: Any]] = []
    @State private var columns: [String] = []
    @State private var isLoading = false
    
    // For sorting
    @State private var sortOrder = [KeyPathComparator(\TableRow.id)]
    
    // We convert dictionary to identifiable structs for Table
    struct TableRow: Identifiable {
        let id: String // path
        let data: [String: Any]
    }
    
    @State private var editingRow: String?
    @State private var editingCol: String?
    @State private var editValue: String = ""
    
    var sortedRows: [TableRow] {
        rows.map { TableRow(id: $0["_path"] as? String ?? UUID().uuidString, data: $0) }
    }
    
    var body: some View {
        VStack {
            if isLoading {
                ProgressView()
                    .tint(SymairaTheme.goldPrimary)
            } else if columns.isEmpty {
                Text("View is empty or not found.")
                    .foregroundColor(SymairaTheme.textMuted)
            } else {
                ScrollView([.horizontal, .vertical]) {
                    Grid(alignment: .leading, horizontalSpacing: 16, verticalSpacing: 8) {
                        GridRow {
                            ForEach(columns, id: \.self) { col in
                                Text(col.uppercased())
                                    .symairaText(.subheading)
                                    .bold()
                                    .foregroundColor(SymairaTheme.goldPrimary)
                            }
                        }
                        Divider()
                            .overlay(SymairaTheme.borderGlass)
                        
                        ForEach(sortedRows) { row in
                            GridRow {
                                ForEach(columns, id: \.self) { col in
                                    if col == "_path" || col == "_title" {
                                        Text("\(row.data[col] ?? "")")
                                            .lineLimit(1)
                                            .truncationMode(.tail)
                                    } else {
                                        if editingRow == row.id && editingCol == col {
                                            TextField("Value", text: $editValue)
                                                .onSubmit {
                                                    Task {
                                                        await saveEdit(path: row.id, key: col, value: editValue)
                                                    }
                                                }
                                                .onExitCommand {
                                                    clearEdit()
                                                }
                                                .textFieldStyle(.symaira)
                                        } else {
                                            Text("\(row.data[col] ?? "")")
                                                .lineLimit(1)
                                                .truncationMode(.tail)
                                                .onTapGesture(count: 2) {
                                                    editingRow = row.id
                                                    editingCol = col
                                                    editValue = "\(row.data[col] ?? "")"
                                                }
                                        }
                                    }
                                }
                            }
                            Divider()
                        }
                    }
                    .padding()
                }
            }
        }
        .task(id: viewID) {
            await loadData()
        }
    }
    
    private func clearEdit() {
        editingRow = nil
        editingCol = nil
        editValue = ""
    }
    
    private func saveEdit(path: String, key: String, value: String) async {
        do {
            try await core.noteEditProperty(path: path, key: key, value: value)
            clearEdit()
            await loadData()
        } catch {
            print("Failed to save property: \(error)")
        }
    }
    
    private func loadData() async {
        isLoading = true
        defer { isLoading = false }
        
        do {
            // First get the view definition to know which columns to show
            let view = try await core.viewsGet(id: viewID)
            self.columns = view.columns
            if !columns.contains("_title") {
                columns.insert("_title", at: 0)
            }
            
            let data = try await core.viewsExec(id: viewID)
            if let decoded = try? JSONSerialization.jsonObject(with: data) as? [[String: Any]] {
                self.rows = decoded
            }
        } catch {
            print("DbViewTable Error: \(error)")
        }
    }
}


