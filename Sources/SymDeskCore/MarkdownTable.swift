import Foundation

// MARK: - Pipe table model

/// A parsed Markdown pipe table. Serialization always produces a valid,
/// column-padded pipe table, so structured edits never corrupt the file.
public struct MarkdownTable: Equatable, Sendable {
    public var header: [String]
    /// Raw alignment cells from the separator row (e.g. `---`, `:--`, `:-:`).
    public var alignments: [String]
    public var rows: [[String]]

    public var columnCount: Int {
        max(header.count, rows.map(\.count).max() ?? 0)
    }

    public init(header: [String], alignments: [String], rows: [[String]]) {
        self.header = header
        self.alignments = alignments
        self.rows = rows
    }

    /// Whether the line looks like part of a pipe table.
    public static func isTableLine(_ line: String) -> Bool {
        let t = line.trimmingCharacters(in: .whitespaces)
        return t.hasPrefix("|") && t.count > 1
    }

    static func isSeparatorLine(_ line: String) -> Bool {
        let inner = line.trimmingCharacters(in: CharacterSet(charactersIn: "| "))
        guard inner.contains("-") else { return false }
        return inner.allSatisfy { "-:| ".contains($0) }
    }

    static func splitRow(_ line: String) -> [String] {
        var t = line.trimmingCharacters(in: .whitespaces)
        if t.hasPrefix("|") { t = String(t.dropFirst()) }
        if t.hasSuffix("|") { t = String(t.dropLast()) }
        return t.components(separatedBy: "|").map { $0.trimmingCharacters(in: .whitespaces) }
    }

    /// Parses consecutive table lines. Requires at least a header row; a
    /// missing separator is tolerated and regenerated on serialize.
    public static func parse(lines: [String]) -> MarkdownTable? {
        let tableLines = lines.filter { isTableLine($0) }
        guard !tableLines.isEmpty else { return nil }
        let header = splitRow(tableLines[0])
        var alignments: [String] = []
        var dataStart = 1
        if tableLines.count > 1, isSeparatorLine(tableLines[1]) {
            alignments = splitRow(tableLines[1])
            dataStart = 2
        }
        let rows = tableLines.dropFirst(dataStart).map { splitRow($0) }
        return MarkdownTable(header: header, alignments: alignments, rows: Array(rows))
    }

    /// Serializes to a normalized, padded pipe table (always valid Markdown).
    public func serialize() -> String {
        let cols = columnCount
        var widths = [Int](repeating: 3, count: cols)
        func measure(_ row: [String]) {
            for (i, cell) in row.enumerated() where i < cols {
                widths[i] = max(widths[i], cell.count)
            }
        }
        measure(header)
        for row in rows { measure(row) }

        func pad(_ s: String, _ w: Int) -> String {
            s + String(repeating: " ", count: max(0, w - s.count))
        }
        func line(_ row: [String]) -> String {
            var cells: [String] = []
            for i in 0..<cols {
                cells.append(pad(i < row.count ? row[i] : "", widths[i]))
            }
            return "| " + cells.joined(separator: " | ") + " |"
        }
        func separatorCell(_ i: Int) -> String {
            let raw = i < alignments.count ? alignments[i] : "---"
            let left = raw.hasPrefix(":")
            let right = raw.hasSuffix(":")
            let dashes = String(repeating: "-", count: max(3, widths[i]) - (left ? 1 : 0) - (right ? 1 : 0))
            return (left ? ":" : "") + dashes + (right ? ":" : "")
        }
        var out = [line(header)]
        var sepCells: [String] = []
        for i in 0..<cols { sepCells.append(separatorCell(i)) }
        out.append("| " + sepCells.joined(separator: " | ") + " |")
        for row in rows { out.append(line(row)) }
        return out.joined(separator: "\n")
    }
}

// MARK: - Cursor-aware document editing

/// Structured edits on pipe tables inside a full document. All offsets are
/// UTF-16 units (matching NSTextView selection ranges). Every operation
/// returns the rewritten document plus the new cursor position, or nil when
/// the cursor is not inside a table.
public struct MarkdownTableEditor {

    public struct Edit: Equatable, Sendable {
        public let text: String
        public let cursor: Int
        public init(text: String, cursor: Int) {
            self.text = text
            self.cursor = cursor
        }
    }

    /// Position of the cursor within a table.
    struct Locator {
        let lineRange: Range<Int>       // line indices of the table
        let lines: [String]             // all document lines
        let table: MarkdownTable
        let row: Int                    // -1 = header, 0.. = data row
        let column: Int
        let startOffset: Int            // UTF-16 offset of table start
        let endOffset: Int              // UTF-16 offset past last table line
    }

    /// True when the UTF-16 `cursor` sits on a pipe-table line.
    public static func isInTable(_ text: String, cursor: Int) -> Bool {
        locate(text, cursor: cursor) != nil
    }

    static func locate(_ text: String, cursor: Int) -> Locator? {
        let ns = text as NSString
        guard cursor >= 0, cursor <= ns.length else { return nil }
        let lines = text.components(separatedBy: "\n")
        var starts: [Int] = []
        var pos = 0
        for line in lines {
            starts.append(pos)
            pos += (line as NSString).length + 1 // + newline
        }
        // Find line containing cursor
        var lineIdx = lines.count - 1
        for (i, start) in starts.enumerated() {
            let end = start + (lines[i] as NSString).length
            if cursor >= start, cursor <= end { lineIdx = i; break }
        }
        guard lineIdx < lines.count, MarkdownTable.isTableLine(lines[lineIdx]) else { return nil }

        var first = lineIdx
        while first > 0, MarkdownTable.isTableLine(lines[first - 1]) { first -= 1 }
        var last = lineIdx
        while last + 1 < lines.count, MarkdownTable.isTableLine(lines[last + 1]) { last += 1 }

        let tableLines = Array(lines[first...last])
        guard let table = MarkdownTable.parse(lines: tableLines) else { return nil }

        // Which parsed row is the cursor line?
        var rowIndex = -1 // header
        var seen = -1
        for i in first...lineIdx where MarkdownTable.isTableLine(lines[i]) && !MarkdownTable.isSeparatorLine(lines[i]) {
            seen += 1
        }
        rowIndex = seen <= 0 ? -1 : seen - 1
        if MarkdownTable.isSeparatorLine(lines[lineIdx]) { rowIndex = -1 }
        if rowIndex >= table.rows.count { rowIndex = table.rows.count - 1 }

        // Column: count unescaped pipes before the cursor within the line
        let lineStart = starts[lineIdx]
        let inLine = max(0, cursor - lineStart)
        let nsLine = lines[lineIdx] as NSString
        var pipes = 0
        for i in 0..<min(inLine, nsLine.length) where nsLine.character(at: i) == 0x7C {
            pipes += 1
        }
        var column = max(0, pipes - 1)
        column = min(column, max(0, table.columnCount - 1))

        let endOffset = starts[last] + (lines[last] as NSString).length
        return Locator(
            lineRange: first..<(last + 1),
            lines: lines,
            table: table,
            row: rowIndex,
            column: column,
            startOffset: starts[first],
            endOffset: endOffset
        )
    }

    /// Rewrites the document with `table` replacing the located one and puts
    /// the cursor at the end of cell (row, column). row == -1 targets the header.
    private static func apply(_ loc: Locator, table: MarkdownTable, row: Int, column: Int, in text: String) -> Edit {
        let serialized = table.serialize()
        let ns = text as NSString
        let before = ns.substring(to: loc.startOffset)
        let after = ns.substring(from: min(loc.endOffset, ns.length))
        let newText = before + serialized + after

        // Cursor: find target line inside serialized table
        let tableNS = serialized as NSString
        let sLines = serialized.components(separatedBy: "\n")
        let targetLine = row < 0 ? 0 : row + 2 // header, separator, rows...
        var offset = 0
        for i in 0..<min(targetLine, sLines.count) {
            offset += (sLines[i] as NSString).length + 1
        }
        // Within the line: end of cell `column` content
        let lineStr = targetLine < sLines.count ? sLines[targetLine] : (sLines.last ?? "")
        let cellEnd = cellContentEnd(line: lineStr, column: column)
        let cursor = loc.startOffset + min(offset + cellEnd, tableNS.length)
        return Edit(text: newText, cursor: cursor)
    }

    /// UTF-16 offset of the end of the trimmed content of cell `column` in a
    /// serialized table line.
    static func cellContentEnd(line: String, column: Int) -> Int {
        let ns = line as NSString
        var pipeIdx: [Int] = []
        for i in 0..<ns.length where ns.character(at: i) == 0x7C {
            pipeIdx.append(i)
        }
        guard pipeIdx.count >= 2 else { return ns.length }
        let c = min(column, pipeIdx.count - 2)
        let start = pipeIdx[c] + 1
        let end = pipeIdx[c + 1]
        // Walk back over padding spaces
        var e = end
        while e > start, ns.character(at: e - 1) == 0x20 { e -= 1 }
        if e == start { e = start + 1 } // empty cell: sit after "| "
        return min(e, ns.length)
    }

    // MARK: Operations

    /// Tab: move to the next cell; wraps to the next row and appends a new
    /// empty row when tabbing past the last cell of the last row.
    public static func nextCell(in text: String, cursor: Int) -> Edit? {
        guard let loc = locate(text, cursor: cursor) else { return nil }
        var table = loc.table
        var row = loc.row
        var col = loc.column
        if col + 1 < table.columnCount {
            col += 1
        } else {
            col = 0
            if row < 0 {
                row = 0
                if table.rows.isEmpty {
                    table.rows.append([String](repeating: "", count: table.columnCount))
                }
            } else if row + 1 < table.rows.count {
                row += 1
            } else {
                table.rows.append([String](repeating: "", count: table.columnCount))
                row = table.rows.count - 1
            }
        }
        return apply(loc, table: table, row: row, column: col, in: text)
    }

    /// Shift-Tab: move to the previous cell; wraps to the previous row.
    /// Returns nil at the very first header cell.
    public static func previousCell(in text: String, cursor: Int) -> Edit? {
        guard let loc = locate(text, cursor: cursor) else { return nil }
        let table = loc.table
        var row = loc.row
        var col = loc.column
        if col > 0 {
            col -= 1
        } else if row >= 0 {
            row = row == 0 ? -1 : row - 1
            col = table.columnCount - 1
        } else {
            return nil
        }
        return apply(loc, table: table, row: row, column: col, in: text)
    }

    /// Enter inside a table: insert an empty row below the current one and
    /// move to its first cell.
    public static func insertRowBelow(in text: String, cursor: Int) -> Edit? {
        guard let loc = locate(text, cursor: cursor) else { return nil }
        var table = loc.table
        let insertAt = loc.row < 0 ? 0 : loc.row + 1
        table.rows.insert([String](repeating: "", count: table.columnCount), at: min(insertAt, table.rows.count))
        return apply(loc, table: table, row: insertAt, column: 0, in: text)
    }

    /// Adds an empty column right of the cursor column.
    public static func addColumn(in text: String, cursor: Int) -> Edit? {
        guard let loc = locate(text, cursor: cursor) else { return nil }
        var table = loc.table
        let at = loc.column + 1
        let columnCount = table.columnCount
        func insert(_ row: inout [String]) {
            while row.count < columnCount { row.append("") }
            row.insert("", at: min(at, row.count))
        }
        insert(&table.header)
        if !table.alignments.isEmpty {
            while table.alignments.count < columnCount { table.alignments.append("---") }
            table.alignments.insert("---", at: min(at, table.alignments.count))
        }
        for i in table.rows.indices { insert(&table.rows[i]) }
        return apply(loc, table: table, row: loc.row, column: at, in: text)
    }

    /// Removes the cursor column. Refuses to remove the last column.
    public static func removeColumn(in text: String, cursor: Int) -> Edit? {
        guard let loc = locate(text, cursor: cursor) else { return nil }
        var table = loc.table
        guard table.columnCount > 1 else { return nil }
        let at = loc.column
        func drop(_ row: inout [String]) {
            if at < row.count { row.remove(at: at) }
        }
        drop(&table.header)
        if at < table.alignments.count { table.alignments.remove(at: at) }
        for i in table.rows.indices { drop(&table.rows[i]) }
        let col = min(at, table.columnCount - 1)
        return apply(loc, table: table, row: loc.row, column: col, in: text)
    }
}
