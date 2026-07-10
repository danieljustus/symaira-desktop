import SwiftUI
import SymairaTheme
import SymDeskCore

struct DbViewList: View {
    let viewID: String
    @EnvironmentObject var core: DeskCore
    @State private var rows: [[String: Any]] = []

    var body: some View {
        List(rows.indices, id: \.self) { index in
            let row = rows[index]
            VStack(alignment: .leading) {
                Text((row["_title"] as? String) ?? "Untitled").foregroundColor(SymairaTheme.textPrimary)
                Text((row["_path"] as? String) ?? "").font(.caption).foregroundColor(SymairaTheme.textMuted)
            }
        }
        .scrollContentBackground(.hidden)
        .task(id: viewID) { await loadData() }
    }

    private func loadData() async {
        do {
            let data = try await core.viewsExec(id: viewID)
            rows = (try? JSONSerialization.jsonObject(with: data) as? [[String: Any]]) ?? []
        } catch { print("DbViewList Error: \(error)") }
    }
}
