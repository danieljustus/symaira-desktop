import SwiftUI
import SymairaTheme
import SymDeskCore

/// Read-only Markdown preview with Obsidian-flavored rendering: transclusion
/// embeds (with cycle guard), callouts, Mermaid flowcharts and LaTeX math.
/// Everything renders natively and fully offline.
struct MarkdownPreviewView: View {
    let text: String
    /// Resolves a note title (or path without extension) to its Markdown content.
    var resolveNote: (String) -> String?
    /// Chain of embed targets above this view (cycle guard).
    var visited: [String] = []
    var onLinkClick: ((String) -> Void)? = nil
    @State private var blocks: [PreviewBlock]

    init(
        text: String,
        resolveNote: @escaping (String) -> String?,
        visited: [String] = [],
        onLinkClick: ((String) -> Void)? = nil
    ) {
        self.text = text
        self.resolveNote = resolveNote
        self.visited = visited
        self.onLinkClick = onLinkClick
        _blocks = State(initialValue: MarkdownPreviewParser.parse(text))
    }

    var body: some View {
        ScrollView {
            MarkdownBlocksView(
                blocks: blocks,
                resolveNote: resolveNote,
                visited: visited,
                onLinkClick: onLinkClick
            )
            .padding()
            .frame(maxWidth: .infinity, alignment: .leading)
        }
        .task(id: text) {
            do {
                try await Task.sleep(nanoseconds: 120_000_000)
            } catch {
                return
            }
            let source = text
            let parsed = await Task.detached(priority: .userInitiated) {
                MarkdownPreviewParser.parse(source)
            }.value
            guard !Task.isCancelled else { return }
            blocks = parsed
        }
    }
}

/// Renders a parsed block list (used both top-level and inside embeds).
private struct MarkdownBlocksView: View {
    let blocks: [PreviewBlock]
    var resolveNote: (String) -> String?
    var visited: [String]
    var onLinkClick: ((String) -> Void)?

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            ForEach(Array(blocks.enumerated()), id: \.offset) { _, block in
                blockView(block)
            }
        }
    }

    @ViewBuilder
    private func blockView(_ block: PreviewBlock) -> some View {
        switch block {
        case .heading(let level, let text):
            Text(inline(text))
                .font(headingFont(level))
                .foregroundStyle(SymairaTokens.ink)
        case .paragraph(let text):
            Text(inline(text))
                .symairaText(.body)
                .textSelection(.enabled)
        case .quote(let lines):
            HStack(alignment: .top, spacing: 8) {
                Rectangle().fill(SymairaTokens.line).frame(width: 3)
                VStack(alignment: .leading, spacing: 4) {
                    ForEach(Array(lines.enumerated()), id: \.offset) { _, line in
                        Text(inline(line)).italic()
                            .symairaText(.body, respectsForeground: false)
                            .foregroundStyle(SymairaTokens.ink2)
                    }
                }
            }
        case .callout(let type, let title, let lines):
            CalloutView(type: type, title: title, lines: lines)
        case .embed(let target, let heading):
            EmbedView(
                target: target,
                heading: heading,
                resolveNote: resolveNote,
                visited: visited,
                onLinkClick: onLinkClick
            )
        case .baseEmbed(let code):
            BaseEmbedView(spec: code, onLinkClick: onLinkClick)
        case .mermaid(let code):
            MermaidView(code: code)
        case .mathBlock(let tex):
            // Serif italic is the conventional math-typesetting face; not
            // covered by the Symaira text roles (issue #352 deviation). It
            // follows the body text style instead of a fixed point size.
            Text(MathTypesetter.render(tex))
                .font(.system(.body, design: .serif).italic())
                .foregroundStyle(SymairaTokens.ink)
                .frame(maxWidth: .infinity, alignment: .center)
                .padding(.vertical, 6)
        case .codeBlock(let language, let code):
            VStack(alignment: .leading, spacing: 4) {
                if !language.isEmpty {
                    Text(language)
                        .symairaText(.caption)
                }
                Text(code)
                    .symairaText(.mono)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(8)
                    .background(SymairaTokens.raised)
                    .cornerRadius(6)
            }
        case .listItem(let indent, let ordered, let text):
            HStack(alignment: .top, spacing: 6) {
                Text(ordered ? "•" : "•")
                    .symairaText(.body, respectsForeground: false)
                    .foregroundStyle(SymairaTokens.ink2)
                Text(inline(text)).symairaText(.body)
            }
            .padding(.leading, CGFloat(indent) * 6)
        case .table(let rows):
            PreviewTableView(rows: rows)
        case .rule:
            Divider()
        case .blank:
            Spacer().frame(height: 2)
        }
    }

    /// Inline rendering: bold/italic/code via AttributedString, inline math via MathTypesetter.
    private func inline(_ text: String) -> AttributedString {
        let mathRendered = MathTypesetter.renderInline(in: text)
        // Replace wikilinks with their display text so no raw [[...]] shows.
        let cleaned = replaceWikilinks(in: mathRendered)
        if let attr = try? AttributedString(
            markdown: cleaned,
            options: .init(interpretedSyntax: .inlineOnlyPreservingWhitespace)
        ) {
            return attr
        }
        return AttributedString(cleaned)
    }

    private func replaceWikilinks(in text: String) -> String {
        guard let regex = try? NSRegularExpression(pattern: "\\[\\[([^\\]]+)\\]\\]") else { return text }
        let ns = text as NSString
        var result = ""
        var last = 0
        for m in regex.matches(in: text, range: NSRange(location: 0, length: ns.length)) {
            result += ns.substring(with: NSRange(location: last, length: m.range.location - last))
            let inner = ns.substring(with: m.range(at: 1))
            let display = inner.components(separatedBy: "|").last ?? inner
            let target = inner.components(separatedBy: "|").first ?? inner
            let encoded = target.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) ?? target
            result += "[\(display)](symdesk://note/\(encoded))"
            last = m.range.location + m.range.length
        }
        result += ns.substring(from: last)
        return result
    }

    /// Heading font per markdown level, expressed through the shared type
    /// scale (issue #352) so headings scale with Dynamic Type.
    private func headingFont(_ level: Int) -> SymairaFontRole {
        switch level {
        case 1: return SymairaTextRole.heading.font
        case 2: return SymairaTextRole.title.font
        case 3: return SymairaTextRole.heading.font
        default: return SymairaTextRole.callout.font
        }
    }
}

// MARK: - Callouts

private struct CalloutView: View {
    let type: String
    let title: String
    let lines: [String]

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 6) {
                SymairaIconView(icon, size: 14)
                Text(title).symairaText(.bodyEmphasized, respectsForeground: false)
            }
            .foregroundStyle(tint)
            ForEach(Array(lines.enumerated()), id: \.offset) { _, line in
                Text(line).symairaText(.body)
            }
        }
        .padding(10)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(SymairaTokens.raised)
        .overlay(
            RoundedRectangle(cornerRadius: 6).stroke(tint.opacity(0.4), lineWidth: 1)
        )
        .cornerRadius(6)
    }

    private var icon: SymairaIcon {
        switch type {
        case "warning", "caution", "attention": return .warning
        case "danger", "error", "bug": return .error
        case "tip", "hint", "important": return .tip
        case "question", "help", "faq": return .help
        case "success", "check", "done": return .success
        case "example": return .list
        case "quote", "cite": return .quote
        case "abstract", "summary", "tldr": return .document
        case "info", "todo": return .info
        default: return .edit
        }
    }

    private var tint: Color {
        switch type {
        case "warning", "caution", "attention": return SymairaTokens.warning
        case "danger", "error", "bug": return SymairaTokens.critical
        case "tip", "hint", "important": return SymairaTokens.ink
        case "success", "check", "done": return SymairaTokens.positive
        case "question", "help", "faq": return SymairaTokens.warning
        default: return SymairaTokens.ink2
        }
    }
}

// MARK: - Transclusion embeds

private struct EmbedView: View {
    let target: String
    let heading: String?
    var resolveNote: (String) -> String?
    var visited: [String]
    var onLinkClick: ((String) -> Void)?

    var body: some View {
        let result = TransclusionResolver.resolve(
            target: target,
            heading: heading,
            visited: visited,
            lookup: resolveNote
        )
        return VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 4) {
                SymairaIconView(.embed, size: 12)
                Button(action: { onLinkClick?(target) }) {
                    Text(heading.map { "\(target) › \($0)" } ?? target)
                        .symairaText(.caption)
                }
                .buttonStyle(.plain)
            }
            .foregroundStyle(SymairaTokens.ink3)

            switch result {
            case .resolved(let content):
                MarkdownBlocksView(
                    blocks: MarkdownPreviewParser.parse(content),
                    resolveNote: resolveNote,
                    visited: visited + [target],
                    onLinkClick: onLinkClick
                )
            case .cycle:
                Label("Circular embed skipped", symairaIcon: .sync)
                    .symairaText(.caption, respectsForeground: false)
                    .foregroundStyle(SymairaTokens.warning)
            case .tooDeep:
                Label("Embed nesting too deep", symairaIcon: .stack)
                    .symairaText(.caption, respectsForeground: false)
                    .foregroundStyle(SymairaTokens.warning)
            case .notFound:
                Label("Note \u{201C}\(target)\u{201D} not found", symairaIcon: .help)
                    .symairaText(.caption)
            }
        }
        .padding(8)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(SymairaTokens.raised)
        .overlay(
            RoundedRectangle(cornerRadius: 6)
                .stroke(SymairaTokens.line, lineWidth: 1)
        )
        .cornerRadius(6)
    }
}

// MARK: - Mermaid (native, offline)

private struct MermaidView: View {
    let code: String

    var body: some View {
        if let graph = MermaidLite.parse(code) {
            MermaidGraphView(graph: graph)
        } else {
            VStack(alignment: .leading, spacing: 4) {
                Label("Diagram (unsupported Mermaid type)", symairaIcon: .workflow)
                    .symairaText(.caption)
                Text(code)
                    .symairaText(.monoSmall)
                    .padding(8)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(SymairaTokens.raised)
                    .cornerRadius(6)
            }
        }
    }
}

private struct MermaidGraphView: View {
    let graph: MermaidLite.Graph

    private let nodeSize = CGSize(width: 130, height: 40)
    private let gap: CGFloat = 40

    var body: some View {
        let layers = graph.layers()
        let horizontal = graph.direction.hasPrefix("LR") || graph.direction.hasPrefix("RL")
        let positions = nodePositions(layers: layers, horizontal: horizontal)
        let size = canvasSize(layers: layers, horizontal: horizontal)

        ScrollView(.horizontal, showsIndicators: false) {
            ZStack(alignment: .topLeading) {
                // Edges
                Canvas { ctx, _ in
                    for (index, edge) in graph.edges.enumerated() {
                        guard let from = positions[edge.from], let to = positions[edge.to] else { continue }
                        var path = Path()
                        path.move(to: from)
                        path.addLine(to: to)
                        ctx.stroke(path, with: .color(SymairaTokens.ink3), lineWidth: 1)
                        // Arrowhead
                        let angle = atan2(to.y - from.y, to.x - from.x)
                        let tip = CGPoint(
                            x: to.x - cos(angle) * (nodeSize.height / 2 + 2),
                            y: to.y - sin(angle) * (nodeSize.height / 2 + 2)
                        )
                        var arrow = Path()
                        arrow.move(to: tip)
                        arrow.addLine(to: CGPoint(x: tip.x - 8 * cos(angle - 0.4), y: tip.y - 8 * sin(angle - 0.4)))
                        arrow.addLine(to: CGPoint(x: tip.x - 8 * cos(angle + 0.4), y: tip.y - 8 * sin(angle + 0.4)))
                        arrow.closeSubpath()
                        ctx.fill(arrow, with: .color(SymairaTokens.ink3))
                        if let label = edge.label {
                            let mid = CGPoint(x: (from.x + to.x) / 2, y: (from.y + to.y) / 2 - 8)
                            // Edge labels are canvas symbols: a font role is
                            // resolved by a view, not by GraphicsContext
                            // (issue #352).
                            if let symbol = ctx.resolveSymbol(id: index) {
                                ctx.draw(symbol, at: mid)
                            }
                        }
                    }
                } symbols: {
                    ForEach(Array(graph.edges.enumerated()), id: \.offset) { index, edge in
                        if let label = edge.label {
                            Text(label)
                                .symairaText(.caption)
                                .tag(index)
                        }
                    }
                }
                .frame(width: size.width, height: size.height)

                // Nodes
                ForEach(graph.nodes, id: \.id) { node in
                    if let pos = positions[node.id] {
                        Text(node.label)
                            .symairaText(.callout)
                            .lineLimit(2)
                            .multilineTextAlignment(.center)
                            .frame(width: nodeSize.width, height: nodeSize.height)
                            .background(SymairaTokens.raised)
                            .overlay(
                                RoundedRectangle(cornerRadius: 6)
                                    .stroke(SymairaTokens.ink3, lineWidth: 1)
                            )
                            .cornerRadius(6)
                            .position(pos)
                    }
                }
            }
            .frame(width: size.width, height: size.height)
        }
        .padding(.vertical, 6)
    }

    private func nodePositions(layers: [[MermaidLite.Node]], horizontal: Bool) -> [String: CGPoint] {
        var positions: [String: CGPoint] = [:]
        for (layerIdx, layer) in layers.enumerated() {
            for (nodeIdx, node) in layer.enumerated() {
                let along = CGFloat(layerIdx) * (horizontal ? nodeSize.width + gap : nodeSize.height + gap)
                let across = CGFloat(nodeIdx) * (horizontal ? nodeSize.height + gap : nodeSize.width + gap)
                if horizontal {
                    positions[node.id] = CGPoint(x: along + nodeSize.width / 2, y: across + nodeSize.height / 2)
                } else {
                    positions[node.id] = CGPoint(x: across + nodeSize.width / 2, y: along + nodeSize.height / 2)
                }
            }
        }
        return positions
    }

    private func canvasSize(layers: [[MermaidLite.Node]], horizontal: Bool) -> CGSize {
        let depth = CGFloat(layers.count)
        let breadth = CGFloat(layers.map(\.count).max() ?? 1)
        let along = depth * (horizontal ? nodeSize.width + gap : nodeSize.height + gap)
        let across = breadth * (horizontal ? nodeSize.height + gap : nodeSize.width + gap)
        return horizontal ? CGSize(width: along, height: across) : CGSize(width: across, height: along)
    }
}

// MARK: - Tables

private struct PreviewTableView: View {
    let rows: [[String]]

    var body: some View {
        let columns = rows.map(\.count).max() ?? 0
        Grid(alignment: .leading, horizontalSpacing: 16, verticalSpacing: 6) {
            ForEach(Array(rows.enumerated()), id: \.offset) { rowIdx, row in
                GridRow {
                    ForEach(0..<columns, id: \.self) { col in
                        Text(col < row.count ? row[col] : "")
                            .symairaText(rowIdx == 0 ? SymairaTextRole.bodyEmphasized : SymairaTextRole.body)
                    }
                }
                if rowIdx == 0 {
                    Divider()
                }
            }
        }
        .padding(8)
        .background(SymairaTokens.raised)
        .cornerRadius(6)
    }
}

// MARK: - Base Embeds (Issue #554)

private struct BaseEmbedView: View {
    let spec: String
    var onLinkClick: ((String) -> Void)?

    @EnvironmentObject var core: DeskCore
    @State private var result: BaseEmbedResult?
    @State private var errorMessage: String?
    @State private var isLoading = true

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            if isLoading {
                SymairaLoadingState("Rendering preview…")
                    .padding(8)
            } else if let err = errorMessage {
                SymairaNotice(message: err, tone: .warning)
                    .padding(8)
            } else if let res = result {
                HStack(spacing: 4) {
                    SymairaIconView(.table, size: 14)
                    Button(action: { onLinkClick?(res.basePath) }) {
                        Text("\(res.baseTitle) › \(res.viewName)")
                            .symairaText(.caption)
                    }
                    .buttonStyle(.plain)
                }
                .foregroundStyle(SymairaTokens.ink3)

                // Parse and render inert markdown table
                MarkdownBlocksView(
                    blocks: MarkdownPreviewParser.parse(res.markdown),
                    resolveNote: { _ in nil },
                    visited: [],
                    onLinkClick: onLinkClick
                )
            }
        }
        .padding(8)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(SymairaTokens.raised)
        .overlay(
            RoundedRectangle(cornerRadius: 6)
                .stroke(SymairaTokens.line, lineWidth: 1)
        )
        .cornerRadius(6)
        .task(id: spec) {
            isLoading = true
            defer { isLoading = false }
            do {
                let res = try await core.viewsExecuteEmbed(spec: spec)
                result = res
                errorMessage = nil
            } catch {
                errorMessage = error.localizedDescription
                result = nil
            }
        }
    }
}

