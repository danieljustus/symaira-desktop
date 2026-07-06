import SwiftUI
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
    
    var sortedRows: [TableRow] {
        rows.map { TableRow(id: $0["_path"] as? String ?? UUID().uuidString, data: $0) }
            // Sort logic would go here based on sortOrder if we used typed models
    }
    
    var body: some View {
        VStack {
            if isLoading {
                ProgressView()
            } else if columns.isEmpty {
                Text("View is empty or not found.")
            } else {
                // In macOS 13/14, Table requires statically defined columns or a ForEach on columns if possible.
                // Dynamic columns in SwiftUI Table can be tricky. We can use a Grid or List with HStack.
                ScrollView([.horizontal, .vertical]) {
                    Grid(alignment: .leading, horizontalSpacing: 16, verticalSpacing: 8) {
                        GridRow {
                            ForEach(columns, id: \.self) { col in
                                Text(col.uppercased())
                                    .font(.headline)
                                    .bold()
                            }
                        }
                        Divider()
                        
                        ForEach(sortedRows) { row in
                            GridRow {
                                ForEach(columns, id: \.self) { col in
                                    Text("\(row.data[col] ?? "")")
                                        .lineLimit(1)
                                        .truncationMode(.tail)
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


