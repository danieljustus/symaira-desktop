import Foundation

enum BlockType: Equatable {
    case frontmatter
    case heading(level: Int)
    case list
    case codeFence(language: String)
    case quote
    case paragraph
    case raw
}

struct BlockNode: Identifiable, Equatable {
    let id = UUID()
    var type: BlockType
    var content: String
    var rawPrefix: String = "" // Used to store exactly "# " or "- " to ensure lossless round-trip
    var rawSuffix: String = "" // Used for closing tags like "```" or "---"
}

class BlockParser {
    static func parse(_ markdown: String) -> [BlockNode] {
        var nodes: [BlockNode] = []
        
        let lines = markdown.components(separatedBy: .newlines)
        var currentLines: [String] = []
        var inFrontmatter = false
        var inCodeFence = false
        var codeFenceLang = ""
        
        // Helper to flush paragraph
        func flush() {
            if !currentLines.isEmpty {
                let joined = currentLines.joined(separator: "\n")
                if inFrontmatter {
                    nodes.append(BlockNode(type: .frontmatter, content: joined, rawPrefix: "---", rawSuffix: "---"))
                } else if inCodeFence {
                    nodes.append(BlockNode(type: .codeFence(language: codeFenceLang), content: joined, rawPrefix: "```\(codeFenceLang)", rawSuffix: "```"))
                } else {
                    // Try to guess if it's a heading or list based on the first line
                    if let first = currentLines.first {
                        if first.hasPrefix("#") {
                            let prefix = first.prefix(while: { $0 == "#" })
                            if first.dropFirst(prefix.count).hasPrefix(" ") {
                                // It's a heading
                                let level = min(prefix.count, 6)
                                let prefixStr = String(first.prefix(level + 1)) // include the space
                                var contentLines = currentLines
                                contentLines[0] = String(contentLines[0].dropFirst(prefixStr.count))
                                nodes.append(BlockNode(type: .heading(level: level), content: contentLines.joined(separator: "\n"), rawPrefix: prefixStr))
                                currentLines = []
                                return
                            }
                        } else if first.hasPrefix("- ") || first.hasPrefix("* ") {
                            nodes.append(BlockNode(type: .list, content: currentLines.joined(separator: "\n")))
                            currentLines = []
                            return
                        } else if first.hasPrefix("> ") {
                            nodes.append(BlockNode(type: .quote, content: currentLines.joined(separator: "\n")))
                            currentLines = []
                            return
                        }
                    }
                    
                    // Fallback to paragraph
                    nodes.append(BlockNode(type: .paragraph, content: joined))
                }
                currentLines = []
            }
        }

        var i = 0
        while i < lines.count {
            let line = lines[i]
            
            if i == 0 && line == "---" {
                inFrontmatter = true
                i += 1
                continue
            }
            
            if inFrontmatter {
                if line == "---" {
                    flush()
                    inFrontmatter = false
                } else {
                    currentLines.append(line)
                }
                i += 1
                continue
            }
            
            if line.hasPrefix("```") {
                if inCodeFence {
                    flush()
                    inCodeFence = false
                } else {
                    flush()
                    inCodeFence = true
                    codeFenceLang = String(line.dropFirst(3)).trimmingCharacters(in: .whitespaces)
                }
                i += 1
                continue
            }
            
            if inCodeFence {
                currentLines.append(line)
                i += 1
                continue
            }
            
            // Empty lines usually separate blocks in Markdown
            if line.trimmingCharacters(in: .whitespaces).isEmpty {
                flush()
                // Keep empty lines as raw blocks to ensure exact round-trip spacing
                nodes.append(BlockNode(type: .raw, content: line))
            } else {
                currentLines.append(line)
            }
            
            i += 1
        }
        
        flush()
        
        return nodes
    }
    
    static func serialize(_ nodes: [BlockNode]) -> String {
        var result = ""
        for (index, node) in nodes.enumerated() {
            switch node.type {
            case .frontmatter:
                result += "---\n\(node.content)\n---"
            case .heading(_):
                result += "\(node.rawPrefix)\(node.content)"
            case .codeFence(let lang):
                result += "```\(lang)\n\(node.content)\n```"
            case .paragraph, .list, .quote:
                result += node.content
            case .raw:
                result += node.content
            }
            
            if index < nodes.count - 1 && node.type != .raw {
                // If the next node isn't a raw newline, we might need to add one?
                // Wait, if we keep empty lines as .raw blocks, we just append exactly.
                // We shouldn't inject newlines between blocks if we rely on .raw to preserve them.
                // Let's just do a newline to separate blocks if we didn't preserve them in .raw.
                // Since our parser splits by lines, a paragraph ends without a newline. We need to add \n.
                result += "\n"
            } else if node.type == .raw && index < nodes.count - 1 {
                result += "\n"
            }
        }
        return result
    }
}
