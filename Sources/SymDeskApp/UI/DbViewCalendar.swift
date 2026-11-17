import SwiftUI
import SymairaTheme
import SymDeskCore

struct DbViewCalendar: View {
    let viewID: String
    @EnvironmentObject var core: DeskCore
    @State private var items: [BoardItem] = []
    @State private var isLoading = false
    @State private var dateProperty: String = "document_date"

    // Simple identifiable item
    struct BoardItem: Identifiable, Equatable {
        let id: String // path
        var data: [String: Any]
        
        static func == (lhs: BoardItem, rhs: BoardItem) -> Bool {
            lhs.id == rhs.id
        }
    }

    private var groupedItems: [(key: String, value: [BoardItem])] {
        var groups = [String: [BoardItem]]()
        for item in items {
            // ISO-8601 date YYYY-MM-DD
            let fullDate = (item.data[dateProperty] as? String) ?? ""
            let key = fullDate.isEmpty ? "No Date" : String(fullDate.prefix(7)) // YYYY-MM
            groups[key, default: []].append(item)
        }
        return groups.sorted { $0.key > $1.key } // newest first
    }

    var body: some View {
        VStack {
            if isLoading {
                SymairaLoadingState("Loading items…")
            } else if items.isEmpty {
                SymairaEmptyState(icon: .calendar, title: "No Items Found", message: "This view has no matching documents yet.")
            } else {
                ScrollView {
                    VStack(alignment: .leading, spacing: 20) {
                        ForEach(groupedItems, id: \.key) { group in
                            VStack(alignment: .leading) {
                                Text(group.key)
                                    .monospacedDigit()
                                    .symairaText(.title)
                                    .padding(.vertical, 8)

                                LazyVGrid(columns: [GridItem(.adaptive(minimum: 150))], spacing: 12) {
                                    ForEach(group.value) { item in
                                        let title = (item.data["_title"] as? String) ?? "Untitled"
                                        let fullDate = (item.data[dateProperty] as? String) ?? ""
                                        VStack(alignment: .leading) {
                                            Text(title).symairaText(.bodyEmphasized).lineLimit(2)
                                            Text(fullDate).monospacedDigit().symairaText(.secondary)
                                        }
                                        .padding()
                                        .frame(maxWidth: .infinity, minHeight: 80, alignment: .topLeading)
                                        .symairaCard(padding: 0)
                                    }
                                }
                            }
                            .padding()
                            .symairaCard(padding: 0)
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
            let view = try await core.viewsGet(id: viewID)
            self.dateProperty = view.dateProperty ?? "document_date"
            
            let data = try await core.viewsExec(id: viewID)
            if let decoded = try? JSONSerialization.jsonObject(with: data) as? [[String: Any]] {
                self.items = decoded.map { row in
                    BoardItem(id: (row["_path"] as? String) ?? UUID().uuidString, data: row)
                }
            }
        } catch {
            print("DbViewCalendar Error: \(error)")
        }
    }
}
