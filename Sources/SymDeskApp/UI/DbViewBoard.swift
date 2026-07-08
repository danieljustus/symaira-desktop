import SwiftUI
import SymDeskCore
import UniformTypeIdentifiers

struct DbViewBoard: View {
    let viewID: String
    @EnvironmentObject var core: DeskCore
    @State private var rows: [[String: Any]] = []
    @State private var columns: [String] = []
    @State private var isLoading = false
    @State private var groupBy: String = "status"

    // We convert dictionary to identifiable structs for Board
    struct BoardItem: Identifiable, Equatable {
        let id: String // path
        var data: [String: Any]
        
        static func == (lhs: BoardItem, rhs: BoardItem) -> Bool {
            lhs.id == rhs.id
        }
    }
    
    @State private var items: [BoardItem] = []

    private var groupedItems: [(key: String, value: [BoardItem])] {
        var groups = [String: [BoardItem]]()
        // Pre-fill some standard status groups if grouping by status to keep order nice, else dynamic
        if groupBy == "status" {
            for s in DocumentStatus.allCases {
                groups[s.rawValue] = []
            }
        }
        
        for item in items {
            let key = (item.data[groupBy] as? String) ?? "Unset"
            let strKey = key.isEmpty ? "Unset" : key
            groups[strKey, default: []].append(item)
        }
        
        if groupBy == "status" {
            // Sort according to DocumentStatus.allCases order
            let statusOrder = DocumentStatus.allCases.map { $0.rawValue }
            return groups.sorted { a, b in
                let idxA = statusOrder.firstIndex(of: a.key) ?? 999
                let idxB = statusOrder.firstIndex(of: b.key) ?? 999
                if idxA == idxB { return a.key < b.key }
                return idxA < idxB
            }
        }
        
        return groups.sorted { $0.key < $1.key }
    }

    var body: some View {
        VStack {
            if isLoading {
                ProgressView()
            } else if items.isEmpty {
                Text("No items found.")
            } else {
                ScrollView(.horizontal) {
                    HStack(alignment: .top, spacing: 20) {
                        ForEach(groupedItems, id: \.key) { group in
                            VStack(alignment: .leading) {
                                Text(group.key.uppercased())
                                    .font(.headline)
                                    .padding(.bottom, 8)
                                
                                ForEach(group.value) { item in
                                    let title = (item.data["_title"] as? String) ?? "Untitled"
                                    VStack(alignment: .leading) {
                                        Text(title).bold()
                                        Text(item.id).font(.caption).foregroundColor(.secondary)
                                    }
                                    .padding()
                                    .frame(width: 250, alignment: .leading)
                                    .background(Color(NSColor.controlBackgroundColor))
                                    .cornerRadius(8)
                                    .shadow(radius: 2)
                                    .onDrag {
                                        NSItemProvider(object: item.id as NSString)
                                    }
                                }
                            }
                            .padding()
                            .frame(width: 280, alignment: .top)
                            .background(Color(NSColor.windowBackgroundColor))
                            .cornerRadius(12)
                            .onDrop(of: [.plainText], isTargeted: nil) { providers in
                                handleDrop(providers: providers, toGroup: group.key)
                            }
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
    
    private func handleDrop(providers: [NSItemProvider], toGroup newGroup: String) -> Bool {
        guard let provider = providers.first else { return false }
        provider.loadItem(forTypeIdentifier: UTType.plainText.identifier, options: nil) { item, error in
            guard let data = item as? Data, let path = String(data: data, encoding: .utf8) else { return }
            
            DispatchQueue.main.async {
                Task {
                    await moveItem(path: path, toGroup: newGroup)
                }
            }
        }
        return true
    }
    
    private func moveItem(path: String, toGroup newGroup: String) async {
        // Optimistic UI update
        if let idx = items.firstIndex(where: { $0.id == path }) {
            items[idx].data[groupBy] = newGroup == "Unset" ? "" : newGroup
        }
        
        do {
            try await core.noteEditProperty(path: path, key: groupBy, value: newGroup == "Unset" ? "" : newGroup)
        } catch {
            print("Failed to update property: \(error)")
            // Revert on error
            await loadData()
        }
    }

    private func loadData() async {
        isLoading = true
        defer { isLoading = false }
        do {
            let view = try await core.viewsGet(id: viewID)
            self.columns = view.columns
            self.groupBy = view.groupBy ?? "status"
            
            let data = try await core.viewsExec(id: viewID)
            if let decoded = try? JSONSerialization.jsonObject(with: data) as? [[String: Any]] {
                self.items = decoded.map { row in
                    BoardItem(id: (row["_path"] as? String) ?? UUID().uuidString, data: row)
                }
            }
        } catch {
            print("DbViewBoard Error: \(error)")
        }
    }
}
