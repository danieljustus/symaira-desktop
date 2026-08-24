import Foundation

// MARK: - Preview block model

/// Block-level structure of a Markdown note as shown in the preview pane.
/// The parser understands the Obsidian-flavored constructs promised by the
/// vault contract: `![[note]]` transclusion embeds, `> [!type]` callouts,
/// Mermaid code fences and `$$ ... $$` math blocks — everything else is
/// passed through as regular Markdown blocks.
public enum PreviewBlock: Equatable, Sendable {
    case heading(level: Int, text: String)
    case paragraph(text: String)
    case quote(lines: [String])
    case callout(type: String, title: String, lines: [String])
    case embed(target: String, heading: String?)
    case baseEmbed(code: String)
    case mermaid(code: String)
    case mathBlock(tex: String)
    case codeBlock(language: String, code: String)
    case listItem(indent: Int, ordered: Bool, text: String)
    case table(rows: [[String]])
    case rule
    case blank
}

public struct MarkdownPreviewParser {

    /// Parses Markdown into preview blocks. Frontmatter is skipped.
    public static func parse(_ text: String) -> [PreviewBlock] {
        var lines = text.components(separatedBy: "\n")

        // Skip YAML frontmatter
        if lines.first?.trimmingCharacters(in: .whitespaces) == "---" {
            if let end = lines.dropFirst().firstIndex(where: { $0.trimmingCharacters(in: .whitespaces) == "---" }) {
                lines.removeSubrange(0...end)
            }
        }

        var blocks: [PreviewBlock] = []
        var i = 0
        while i < lines.count {
            let line = lines[i]
            let trimmed = line.trimmingCharacters(in: .whitespaces)

            // Blank line
            if trimmed.isEmpty {
                blocks.append(.blank)
                i += 1
                continue
            }

            // Fenced code block (``` or ~~~)
            if trimmed.hasPrefix("```") || trimmed.hasPrefix("~~~") {
                let fence = String(trimmed.prefix(3))
                let language = trimmed.dropFirst(3).trimmingCharacters(in: .whitespaces)
                var codeLines: [String] = []
                i += 1
                while i < lines.count, !lines[i].trimmingCharacters(in: .whitespaces).hasPrefix(fence) {
                    codeLines.append(lines[i])
                    i += 1
                }
                i += 1 // skip closing fence
                let code = codeLines.joined(separator: "\n")
                let langLower = language.lowercased()
                if langLower == "mermaid" {
                    blocks.append(.mermaid(code: code))
                } else if langLower == "symdesk-base" || langLower == "base" {
                    blocks.append(.baseEmbed(code: code))
                } else {
                    blocks.append(.codeBlock(language: language, code: code))
                }
                continue
            }

            // Math block: $$ ... $$
            if trimmed.hasPrefix("$$") {
                var inner = String(trimmed.dropFirst(2))
                if inner.hasSuffix("$$"), inner.count >= 2 {
                    // Single-line $$ x $$
                    inner = String(inner.dropLast(2))
                    blocks.append(.mathBlock(tex: inner.trimmingCharacters(in: .whitespaces)))
                    i += 1
                    continue
                }
                var mathLines: [String] = []
                if !inner.trimmingCharacters(in: .whitespaces).isEmpty {
                    mathLines.append(inner)
                }
                i += 1
                while i < lines.count {
                    let t = lines[i].trimmingCharacters(in: .whitespaces)
                    if t.hasSuffix("$$") {
                        let rest = String(t.dropLast(2)).trimmingCharacters(in: .whitespaces)
                        if !rest.isEmpty { mathLines.append(rest) }
                        i += 1
                        break
                    }
                    mathLines.append(lines[i])
                    i += 1
                }
                blocks.append(.mathBlock(tex: mathLines.joined(separator: "\n").trimmingCharacters(in: .whitespaces)))
                continue
            }

            // Transclusion embed on its own line: ![[target]] / ![[target#heading]]
            if let embed = parseEmbed(trimmed) {
                blocks.append(embed)
                i += 1
                continue
            }

            // Heading
            if trimmed.hasPrefix("#") {
                let level = trimmed.prefix(while: { $0 == "#" }).count
                if level <= 6 {
                    let rest = trimmed.dropFirst(level)
                    if rest.first == " " || rest.isEmpty {
                        blocks.append(.heading(level: level, text: rest.trimmingCharacters(in: .whitespaces)))
                        i += 1
                        continue
                    }
                }
            }

            // Horizontal rule
            if trimmed == "---" || trimmed == "***" || trimmed == "___" {
                blocks.append(.rule)
                i += 1
                continue
            }

            // Blockquote: callout or plain quote
            if trimmed.hasPrefix(">") {
                var quoteLines: [String] = []
                while i < lines.count {
                    let t = lines[i].trimmingCharacters(in: .whitespaces)
                    guard t.hasPrefix(">") else { break }
                    var content = String(t.dropFirst())
                    if content.hasPrefix(" ") { content = String(content.dropFirst()) }
                    quoteLines.append(content)
                    i += 1
                }
                if let (type, title) = parseCalloutHeader(quoteLines.first ?? "") {
                    blocks.append(.callout(type: type, title: title, lines: Array(quoteLines.dropFirst())))
                } else {
                    blocks.append(.quote(lines: quoteLines))
                }
                continue
            }

            // List item
            if let item = parseListItem(line) {
                blocks.append(item)
                i += 1
                continue
            }

            // Pipe table
            if trimmed.hasPrefix("|"), trimmed.hasSuffix("|") {
                var tableLines: [String] = []
                while i < lines.count {
                    let t = lines[i].trimmingCharacters(in: .whitespaces)
                    guard t.hasPrefix("|") else { break }
                    tableLines.append(t)
                    i += 1
                }
                let rows = tableLines
                    .filter { !isTableSeparator($0) }
                    .map { splitTableRow($0) }
                blocks.append(.table(rows: rows))
                continue
            }

            // Paragraph: gather consecutive plain lines
            var paraLines: [String] = []
            while i < lines.count {
                let t = lines[i].trimmingCharacters(in: .whitespaces)
                if t.isEmpty || t.hasPrefix(">") || t.hasPrefix("#") || t.hasPrefix("```")
                    || t.hasPrefix("$$") || parseEmbed(t) != nil || parseListItem(lines[i]) != nil
                    || (t.hasPrefix("|") && t.hasSuffix("|")) {
                    break
                }
                paraLines.append(t)
                i += 1
            }
            if paraLines.isEmpty {
                // Defensive: never loop forever
                paraLines.append(trimmed)
                i += 1
            }
            blocks.append(.paragraph(text: paraLines.joined(separator: " ")))
        }
        return blocks
    }

    /// Parses `![[Target]]` / `![[Target#Heading]]` when it is the whole line.
    public static func parseEmbed(_ line: String) -> PreviewBlock? {
        let t = line.trimmingCharacters(in: .whitespaces)
        guard t.hasPrefix("![["), t.hasSuffix("]]") else { return nil }
        let inner = String(t.dropFirst(3).dropLast(2))
        guard !inner.isEmpty, !inner.contains("]]") else { return nil }
        // Strip optional |display alias
        let target = inner.components(separatedBy: "|").first ?? inner
        let parts = target.components(separatedBy: "#")
        let name = parts[0].trimmingCharacters(in: .whitespaces)
        guard !name.isEmpty else { return nil }
        let heading = parts.count > 1 ? parts.dropFirst().joined(separator: "#").trimmingCharacters(in: .whitespaces) : nil
        return .embed(target: name, heading: (heading?.isEmpty ?? true) ? nil : heading)
    }

    /// Parses the first line of a callout: `[!type] Optional Title`.
    /// Returns nil if the quote is not a callout.
    public static func parseCalloutHeader(_ firstLine: String) -> (type: String, title: String)? {
        let t = firstLine.trimmingCharacters(in: .whitespaces)
        guard t.hasPrefix("[!") else { return nil }
        guard let close = t.firstIndex(of: "]") else { return nil }
        var type = String(t[t.index(t.startIndex, offsetBy: 2)..<close]).lowercased()
        // Strip fold markers [!note]- / [!note]+
        var rest = String(t[t.index(after: close)...])
        if rest.hasPrefix("-") || rest.hasPrefix("+") { rest = String(rest.dropFirst()) }
        type = type.trimmingCharacters(in: .whitespaces)
        guard !type.isEmpty, type.allSatisfy({ $0.isLetter || $0.isNumber || $0 == "-" }) else { return nil }
        let title = rest.trimmingCharacters(in: .whitespaces)
        return (type, title.isEmpty ? type.capitalized : title)
    }

    private static func parseListItem(_ line: String) -> PreviewBlock? {
        let indent = line.prefix(while: { $0 == " " || $0 == "\t" }).count
        let t = line.trimmingCharacters(in: .whitespaces)
        for marker in ["- ", "* ", "+ "] where t.hasPrefix(marker) {
            return .listItem(indent: indent, ordered: false, text: String(t.dropFirst(2)))
        }
        // Ordered: 1. text
        if let dot = t.firstIndex(of: "."), t[..<dot].allSatisfy({ $0.isNumber }), !t[..<dot].isEmpty {
            let rest = t[t.index(after: dot)...]
            if rest.hasPrefix(" ") {
                return .listItem(indent: indent, ordered: true, text: rest.trimmingCharacters(in: .whitespaces))
            }
        }
        return nil
    }

    private static func isTableSeparator(_ line: String) -> Bool {
        let inner = line.trimmingCharacters(in: CharacterSet(charactersIn: "| "))
        guard !inner.isEmpty else { return false }
        return inner.allSatisfy { "-:| ".contains($0) } && inner.contains("-")
    }

    private static func splitTableRow(_ line: String) -> [String] {
        var t = line.trimmingCharacters(in: .whitespaces)
        if t.hasPrefix("|") { t = String(t.dropFirst()) }
        if t.hasSuffix("|") { t = String(t.dropLast()) }
        return t.components(separatedBy: "|").map { $0.trimmingCharacters(in: .whitespaces) }
    }
}

// MARK: - Transclusion resolution

/// Resolves `![[note]]` embeds against a note-content lookup, guarding
/// against cycles and runaway depth. Rendering is read-only.
public struct TransclusionResolver {
    public static let maxDepth = 5

    public enum Result: Equatable, Sendable {
        case resolved(content: String)
        case cycle(target: String)
        case notFound(target: String)
        case tooDeep(target: String)
    }

    /// Resolves one embed. `lookup` maps a note title (or relative path
    /// without extension) to its Markdown content. `visited` carries the
    /// chain of already-embedded targets (case-insensitive).
    public static func resolve(
        target: String,
        heading: String?,
        visited: [String],
        lookup: (String) -> String?
    ) -> Result {
        let key = target.lowercased()
        if visited.contains(where: { $0.lowercased() == key }) {
            return .cycle(target: target)
        }
        if visited.count >= maxDepth {
            return .tooDeep(target: target)
        }
        guard var content = lookup(target) else {
            return .notFound(target: target)
        }
        if let heading {
            content = extractSection(heading: heading, from: content)
        }
        return .resolved(content: content)
    }

    /// Extracts the section starting at `## heading` (any level) up to the
    /// next heading of the same or higher level. Falls back to the full
    /// content when the heading is missing.
    public static func extractSection(heading: String, from content: String) -> String {
        let lines = content.components(separatedBy: "\n")
        var startIdx: Int? = nil
        var startLevel = 0
        for (idx, line) in lines.enumerated() {
            let t = line.trimmingCharacters(in: .whitespaces)
            guard t.hasPrefix("#") else { continue }
            let level = t.prefix(while: { $0 == "#" }).count
            let text = t.dropFirst(level).trimmingCharacters(in: .whitespaces)
            if let s = startIdx {
                if level <= startLevel {
                    return lines[s..<idx].joined(separator: "\n")
                }
                continue
            }
            if text.caseInsensitiveCompare(heading) == .orderedSame {
                startIdx = idx
                startLevel = level
            }
        }
        if let s = startIdx {
            return lines[s...].joined(separator: "\n")
        }
        return content
    }
}

// MARK: - Offline math rendering

/// Converts common LaTeX to Unicode text so math renders offline without a
/// web view. Covers Greek letters, super-/subscripts, fractions, roots,
/// operators and decorations — unknown commands degrade to their name.
public struct MathTypesetter {

    public static func render(_ tex: String) -> String {
        var s = tex

        // \frac{a}{b} -> a⁄b (recursively, innermost first)
        while let r = renderFrac(in: s) { s = r }
        // \sqrt{x} -> √(x)
        while let r = renderCommand(in: s, name: "sqrt", transform: { "√(\($0))" }) { s = r }
        // \text{...} / \mathrm{...} -> plain
        while let r = renderCommand(in: s, name: "text", transform: { $0 }) { s = r }
        while let r = renderCommand(in: s, name: "mathrm", transform: { $0 }) { s = r }

        // Symbol commands
        for (cmd, sym) in Self.symbols.sorted(by: { $0.key.count > $1.key.count }) {
            s = s.replacingOccurrences(of: "\\" + cmd, with: sym)
        }

        // Superscripts / subscripts: x^2, x^{10}, x_1, x_{ab}
        s = renderScripts(s, marker: "^", map: Self.superscripts)
        s = renderScripts(s, marker: "_", map: Self.subscripts)

        // Cleanup braces and leftover backslashes
        s = s.replacingOccurrences(of: "{", with: "")
        s = s.replacingOccurrences(of: "}", with: "")
        s = s.replacingOccurrences(of: "\\", with: "")
        return s.trimmingCharacters(in: .whitespaces)
    }

    private static func renderFrac(in s: String) -> String? {
        guard let range = s.range(of: "\\frac") else { return nil }
        var rest = String(s[range.upperBound...])
        guard let (num, afterNum) = takeGroup(rest) else {
            return String(s[..<range.lowerBound]) + "frac" + rest
        }
        rest = afterNum
        guard let (den, afterDen) = takeGroup(rest) else {
            return String(s[..<range.lowerBound]) + num + "/" + rest
        }
        return String(s[..<range.lowerBound]) + "(\(render(num))⁄\(render(den)))" + afterDen
    }

    private static func renderCommand(in s: String, name: String, transform: (String) -> String) -> String? {
        guard let range = s.range(of: "\\" + name) else { return nil }
        let rest = String(s[range.upperBound...])
        guard let (arg, after) = takeGroup(rest) else {
            return String(s[..<range.lowerBound]) + name + rest
        }
        return String(s[..<range.lowerBound]) + transform(render(arg)) + after
    }

    /// Takes a `{...}` group (brace-balanced) or a single character.
    private static func takeGroup(_ s: String) -> (String, String)? {
        var s = s
        while s.first == " " { s = String(s.dropFirst()) }
        guard let first = s.first else { return nil }
        if first != "{" {
            return (String(first), String(s.dropFirst()))
        }
        var depth = 0
        var content = ""
        var idx = s.startIndex
        while idx < s.endIndex {
            let c = s[idx]
            if c == "{" {
                depth += 1
                if depth == 1 { idx = s.index(after: idx); continue }
            } else if c == "}" {
                depth -= 1
                if depth == 0 {
                    return (content, String(s[s.index(after: idx)...]))
                }
            }
            content.append(c)
            idx = s.index(after: idx)
        }
        return nil
    }

    private static func renderScripts(_ s: String, marker: Character, map: [Character: Character]) -> String {
        var result = ""
        var rest = Substring(s)
        while let idx = rest.firstIndex(of: marker) {
            result += rest[..<idx]
            let after = String(rest[rest.index(after: idx)...])
            guard let (group, remainder) = takeGroup(after) else {
                result.append(marker)
                rest = rest[rest.index(after: idx)...]
                continue
            }
            var converted = ""
            var allMapped = true
            for ch in group {
                if let m = map[ch] { converted.append(m) } else { allMapped = false; break }
            }
            if allMapped {
                result += converted
            } else {
                result += (marker == "^" ? "^" : "_") + group
            }
            rest = Substring(remainder)
        }
        result += rest
        return result
    }

    static let symbols: [String: String] = [
        "alpha": "α", "beta": "β", "gamma": "γ", "delta": "δ", "epsilon": "ε",
        "zeta": "ζ", "eta": "η", "theta": "θ", "iota": "ι", "kappa": "κ",
        "lambda": "λ", "mu": "μ", "nu": "ν", "xi": "ξ", "pi": "π", "rho": "ρ",
        "sigma": "σ", "tau": "τ", "phi": "φ", "chi": "χ", "psi": "ψ", "omega": "ω",
        "Gamma": "Γ", "Delta": "Δ", "Theta": "Θ", "Lambda": "Λ", "Xi": "Ξ",
        "Pi": "Π", "Sigma": "Σ", "Phi": "Φ", "Psi": "Ψ", "Omega": "Ω",
        "times": "×", "cdot": "·", "div": "÷", "pm": "±", "mp": "∓",
        "leq": "≤", "le": "≤", "geq": "≥", "ge": "≥", "neq": "≠", "ne": "≠",
        "approx": "≈", "equiv": "≡", "infty": "∞", "partial": "∂", "nabla": "∇",
        "sum": "∑", "prod": "∏", "int": "∫", "in": "∈", "notin": "∉",
        "subset": "⊂", "supset": "⊃", "cup": "∪", "cap": "∩", "emptyset": "∅",
        "forall": "∀", "exists": "∃", "rightarrow": "→", "to": "→",
        "leftarrow": "←", "Rightarrow": "⇒", "Leftarrow": "⇐", "leftrightarrow": "↔",
        "sqrt": "√", "propto": "∝", "sim": "∼", "perp": "⊥", "parallel": "∥",
        "angle": "∠", "hbar": "ℏ", "ell": "ℓ", "Re": "ℜ", "Im": "ℑ",
        "dots": "…", "ldots": "…", "cdots": "⋯", "prime": "′", "circ": "∘",
    ]

    static let superscripts: [Character: Character] = [
        "0": "⁰", "1": "¹", "2": "²", "3": "³", "4": "⁴", "5": "⁵", "6": "⁶",
        "7": "⁷", "8": "⁸", "9": "⁹", "+": "⁺", "-": "⁻", "=": "⁼", "(": "⁽",
        ")": "⁾", "n": "ⁿ", "i": "ⁱ", "x": "ˣ", "T": "ᵀ",
    ]

    static let subscripts: [Character: Character] = [
        "0": "₀", "1": "₁", "2": "₂", "3": "₃", "4": "₄", "5": "₅", "6": "₆",
        "7": "₇", "8": "₈", "9": "₉", "+": "₊", "-": "₋", "=": "₌", "(": "₍",
        ")": "₎", "a": "ₐ", "e": "ₑ", "i": "ᵢ", "j": "ⱼ", "k": "ₖ", "m": "ₘ",
        "n": "ₙ", "o": "ₒ", "p": "ₚ", "s": "ₛ", "t": "ₜ", "x": "ₓ",
    ]

    /// Replaces inline `$...$` math in a text line with rendered Unicode.
    public static func renderInline(in line: String) -> String {
        var result = ""
        var rest = Substring(line)
        while let start = rest.firstIndex(of: "$") {
            let afterStart = rest.index(after: start)
            guard afterStart < rest.endIndex,
                  let end = rest[afterStart...].firstIndex(of: "$"),
                  end > afterStart else {
                result += rest[..<rest.index(after: start)]
                rest = rest[rest.index(after: start)...]
                continue
            }
            result += rest[..<start]
            let tex = String(rest[afterStart..<end])
            result += render(tex)
            rest = rest[rest.index(after: end)...]
        }
        result += rest
        return result
    }
}

// MARK: - Offline Mermaid (flowchart subset)

/// Minimal, fully offline Mermaid parser for the flowchart family
/// (`graph TD`, `flowchart LR`, ...). Produces a node/edge model the app
/// renders natively; unsupported diagram types return nil so the UI can
/// fall back to a labeled code view.
public struct MermaidLite {

    public struct Node: Equatable, Sendable, Hashable {
        public let id: String
        public let label: String
        public init(id: String, label: String) {
            self.id = id
            self.label = label
        }
    }

    public struct Edge: Equatable, Sendable {
        public let from: String
        public let to: String
        public let label: String?
        public init(from: String, to: String, label: String? = nil) {
            self.from = from
            self.to = to
            self.label = label
        }
    }

    public struct Graph: Equatable, Sendable {
        public let direction: String // "TD", "LR", ...
        public let nodes: [Node]
        public let edges: [Edge]

        /// Assigns each node a layer (longest-path layering) for layout.
        public func layers() -> [[Node]] {
            var depth: [String: Int] = [:]
            for n in nodes { depth[n.id] = 0 }
            // Relax edges up to node-count times (handles chains; cycles capped)
            for _ in 0..<max(nodes.count, 1) {
                var changed = false
                for e in edges {
                    let d = (depth[e.from] ?? 0) + 1
                    if d > (depth[e.to] ?? 0), d < nodes.count + 1 {
                        depth[e.to] = d
                        changed = true
                    }
                }
                if !changed { break }
            }
            let maxDepth = depth.values.max() ?? 0
            var layers: [[Node]] = Array(repeating: [], count: maxDepth + 1)
            for n in nodes { layers[depth[n.id] ?? 0].append(n) }
            return layers.filter { !$0.isEmpty }
        }
    }

    public static func parse(_ code: String) -> Graph? {
        let lines = code.components(separatedBy: "\n")
            .map { $0.trimmingCharacters(in: .whitespaces) }
            .filter { !$0.isEmpty && !$0.hasPrefix("%%") }
        guard let header = lines.first else { return nil }
        let headerParts = header.components(separatedBy: .whitespaces)
        guard let kind = headerParts.first?.lowercased(),
              kind == "graph" || kind == "flowchart" else { return nil }
        let direction = headerParts.count > 1 ? headerParts[1].uppercased() : "TD"

        var nodeMap: [String: String] = [:] // id -> label
        var order: [String] = []
        var edges: [Edge] = []

        func addNode(_ id: String, label: String?) {
            if nodeMap[id] == nil {
                nodeMap[id] = label ?? id
                order.append(id)
            } else if let label, nodeMap[id] == id {
                nodeMap[id] = label
            }
        }

        for raw in lines.dropFirst() {
            // Skip subgraph / style / class directives (unsupported but harmless)
            let lower = raw.lowercased()
            if lower.hasPrefix("subgraph") || lower == "end" || lower.hasPrefix("style")
                || lower.hasPrefix("classdef") || lower.hasPrefix("class ") || lower.hasPrefix("click") {
                continue
            }
            // Edge lines may chain: A --> B --> C. Split on arrow tokens.
            let segments = splitEdgeLine(raw)
            if segments.count >= 2 {
                var previous: (id: String, label: String?)? = nil
                for seg in segments {
                    guard let node = parseNodeRef(seg.text) else { previous = nil; continue }
                    addNode(node.id, label: node.label)
                    if let p = previous {
                        edges.append(Edge(from: p.id, to: node.id, label: seg.edgeLabel))
                    }
                    previous = (node.id, node.label)
                }
            } else if let node = parseNodeRef(raw) {
                addNode(node.id, label: node.label)
            }
        }

        guard !nodeMap.isEmpty else { return nil }
        let nodes = order.map { Node(id: $0, label: nodeMap[$0] ?? $0) }
        return Graph(direction: direction, nodes: nodes, edges: edges)
    }

    private struct EdgeSegment {
        let text: String
        /// Label of the edge arriving at this segment (from `-->|label|` or `-- label -->`).
        let edgeLabel: String?
    }

    private static func splitEdgeLine(_ line: String) -> [EdgeSegment] {
        // Normalize arrow variants to a single delimiter
        var s = line
        for arrow in ["<-->", "-->", "---", "-.->", "==>", "--o", "--x"] {
            s = s.replacingOccurrences(of: arrow, with: "\u{1}")
        }
        guard s.contains("\u{1}") else { return [EdgeSegment(text: line, edgeLabel: nil)] }
        var segments: [EdgeSegment] = []
        for (idx, part) in s.components(separatedBy: "\u{1}").enumerated() {
            var text = part.trimmingCharacters(in: .whitespaces)
            var label: String? = nil
            // |label| prefix after an arrow
            if idx > 0, text.hasPrefix("|"), let close = text.dropFirst().firstIndex(of: "|") {
                label = String(text[text.index(after: text.startIndex)..<close])
                text = String(text[text.index(after: close)...]).trimmingCharacters(in: .whitespaces)
            }
            // `-- label` remnants before arrows: "A -- yes" -> text "A", label "yes"
            if let dashRange = text.range(of: " -- ") {
                let maybeLabel = String(text[dashRange.upperBound...]).trimmingCharacters(in: .whitespaces)
                text = String(text[..<dashRange.lowerBound]).trimmingCharacters(in: .whitespaces)
                if segments.isEmpty { label = nil } // label belongs to the following edge; keep simple
                _ = maybeLabel
            }
            guard !text.isEmpty else { continue }
            segments.append(EdgeSegment(text: text, edgeLabel: label))
        }
        return segments
    }

    /// Parses `id`, `id[Label]`, `id(Label)`, `id((Label))`, `id{Label}`, `id>Label]`.
    private static func parseNodeRef(_ s: String) -> (id: String, label: String?)? {
        let t = s.trimmingCharacters(in: .whitespaces)
        guard !t.isEmpty else { return nil }
        let idEnd = t.firstIndex(where: { !($0.isLetter || $0.isNumber || $0 == "_" || $0 == "-") }) ?? t.endIndex
        let id = String(t[..<idEnd])
        guard !id.isEmpty else { return nil }
        var rest = String(t[idEnd...]).trimmingCharacters(in: .whitespaces)
        guard !rest.isEmpty else { return (id, nil) }
        // Strip bracket shapes
        let pairs: [(String, String)] = [("((", "))"), ("([", "])"), ("[[", "]]"), ("[", "]"), ("(", ")"), ("{{", "}}"), ("{", "}"), (">", "]")]
        for (open, close) in pairs {
            if rest.hasPrefix(open), rest.hasSuffix(close), rest.count > open.count + close.count - 1 {
                rest = String(rest.dropFirst(open.count).dropLast(close.count))
                let label = rest.trimmingCharacters(in: CharacterSet(charactersIn: "\" "))
                return (id, label.isEmpty ? nil : label)
            }
        }
        return (id, nil)
    }
}
