import SwiftUI
import SymairaTheme
import SymDeskCore

struct DbViewGallery: View {
    let viewID: String
    @EnvironmentObject var core: DeskCore
    @State private var rows: [[String: Any]] = []
    @State private var isLoading = false

    var body: some View {
        Group {
            if isLoading { ProgressView().tint(SymairaTheme.goldPrimary) }
            else if rows.isEmpty { Text("No items found.").foregroundColor(SymairaTheme.textMuted) }
            else {
                ScrollView {
                    LazyVGrid(columns: [GridItem(.adaptive(minimum: 220), spacing: 16)], spacing: 16) {
                        ForEach(rows.indices, id: \.self) { index in
                            let row = rows[index]
                            VStack(alignment: .leading, spacing: 8) {
                                if let cover = (row["cover"] ?? row["image"] ?? row["asset"]) as? String, !cover.isEmpty {
                                    Text(cover).symairaText(.caption).lineLimit(1).foregroundColor(SymairaTheme.textMuted)
                                }
                                Text((row["_title"] as? String) ?? "Untitled").symairaText(.subheading).foregroundColor(SymairaTheme.textPrimary)
                                Text((row["_path"] as? String) ?? "").symairaText(.caption).lineLimit(1).foregroundColor(SymairaTheme.textMuted)
                            }
                            .padding().frame(maxWidth: .infinity, minHeight: 120, alignment: .topLeading).glassCard()
                        }
                    }.padding()
                }
            }
        }.task(id: viewID) { await loadData() }
    }

    private func loadData() async {
        isLoading = true; defer { isLoading = false }
        do {
            let data = try await core.viewsExec(id: viewID)
            rows = (try? JSONSerialization.jsonObject(with: data) as? [[String: Any]]) ?? []
        } catch { print("DbViewGallery Error: \(error)") }
    }
}
