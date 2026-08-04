import SwiftUI
import SymairaTheme
import SymDeskCore

struct DbViewTimeline: View {
    let viewID: String
    @EnvironmentObject var core: DeskCore
    @State private var rows: [[String: Any]] = []
    @State private var dateProperty = "date"

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                ForEach(rows.indices, id: \.self) { index in
                    let row = rows[index]
                    HStack(alignment: .top, spacing: 12) {
                        Text((row[dateProperty] as? String) ?? "No date").symairaText(.caption).frame(width: 100, alignment: .leading).foregroundColor(SymairaTheme.textMuted)
                        VStack(alignment: .leading) {
                            Text((row["_title"] as? String) ?? "Untitled").foregroundColor(SymairaTheme.textPrimary)
                            Rectangle().fill(SymairaTheme.goldPrimary).frame(height: 6).cornerRadius(3)
                        }
                    }.padding().glassCard()
                }
            }.padding()
        }.task(id: viewID) { await loadData() }
    }

    private func loadData() async {
        do {
            let view = try await core.viewsGet(id: viewID)
            dateProperty = view.dateProperty ?? "date"
            let data = try await core.viewsExec(id: viewID)
            rows = (try? JSONSerialization.jsonObject(with: data) as? [[String: Any]]) ?? []
        } catch { print("DbViewTimeline Error: \(error)") }
    }
}
