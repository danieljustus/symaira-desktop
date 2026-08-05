import SwiftUI
import SymairaTheme

struct BlockEditorView: View {
    @Binding var text: String
    @State private var blocks: [BlockNode] = []
    
    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 12) {
                ForEach($blocks) { $block in
                    BlockRow(block: $block, onUpdate: {
                        syncToText()
                    })
                }
            }
            .padding()
        }
        .onAppear {
            blocks = BlockParser.parse(text)
        }
        .onChange(of: text) { newText in
            // Only reparse if the serialized version differs to prevent cursor jumping
            let currentText = BlockParser.serialize(blocks)
            if currentText != newText {
                blocks = BlockParser.parse(newText)
            }
        }
    }
    
    private func syncToText() {
        let serialized = BlockParser.serialize(blocks)
        if serialized != text {
            text = serialized
        }
    }
}

struct BlockRow: View {
    @Binding var block: BlockNode
    var onUpdate: () -> Void
    
    var body: some View {
        HStack(alignment: .top) {
            // Optional: Block type indicator or drag handle could go here
            
            switch block.type {
            case .frontmatter:
                VStack(alignment: .leading, spacing: 4) {
                    Text("Frontmatter")
                        .symairaText(.caption)
                        .foregroundColor(SymairaTheme.textMuted)
                    TextField("Frontmatter", text: $block.content, axis: .vertical)
                        .symairaText(.mono)
                        .padding(8)
                        .background(Color.white.opacity(0.04))
                        .cornerRadius(4)
                        .onChange(of: block.content) { _ in onUpdate() }
                }
            case .heading(let level):
                HStack {
                    Text(String(repeating: "#", count: level))
                        .foregroundColor(SymairaTheme.goldPrimary)
                        .font(headingFont(for: level))
                    TextField("Heading", text: $block.content, axis: .vertical)
                        .font(headingFont(for: level))
                        .textFieldStyle(.plain)
                        .onChange(of: block.content) { _ in onUpdate() }
                }
            case .list:
                HStack(alignment: .top) {
                    Text("•")
                        .symairaText(.body)
                        .padding(.top, 2)
                    TextField("List item", text: $block.content, axis: .vertical)
                        .symairaText(.body)
                        .textFieldStyle(.plain)
                        .onChange(of: block.content) { _ in onUpdate() }
                }
            case .quote:
                HStack(alignment: .top) {
                    Rectangle()
                        .fill(SymairaTheme.goldShadow)
                        .frame(width: 4)
                    TextField("Quote", text: $block.content, axis: .vertical)
                        .symairaText(.body)
                        .italic()
                        .textFieldStyle(.plain)
                        .onChange(of: block.content) { _ in onUpdate() }
                }
                .padding(.leading, 8)
            case .codeFence(let lang):
                VStack(alignment: .leading, spacing: 4) {
                    Text(lang.isEmpty ? "Code" : "Code (\(lang))")
                        .symairaText(.caption)
                        .foregroundColor(SymairaTheme.textMuted)
                    TextField("Code", text: $block.content, axis: .vertical)
                        .symairaText(.mono)
                        .padding(8)
                        .background(Color.white.opacity(0.04))
                        .cornerRadius(4)
                        .onChange(of: block.content) { _ in onUpdate() }
                }
            case .paragraph:
                TextField("Type '/' for commands", text: $block.content, axis: .vertical)
                    .symairaText(.body)
                    .textFieldStyle(.plain)
                    .onChange(of: block.content) { _ in onUpdate() }
            case .raw:
                // Raw blocks (like empty lines) are not editable directly in block mode
                // but we keep them for serialization
                EmptyView()
            }
        }
    }
    
    /// Heading font per markdown level, expressed through the shared type
    /// scale (issue #352) so headings scale with Dynamic Type.
    private func headingFont(for level: Int) -> Font {
        switch level {
        case 1: return SymairaTextRole.title.font
        case 2: return SymairaTextRole.heading.font
        case 3: return SymairaTextRole.heading.font
        case 4: return SymairaTextRole.callout.font
        default: return SymairaTextRole.callout.font
        }
    }
}
