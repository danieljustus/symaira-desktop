import XCTest
@testable import SymDesk
import SymDeskCore

/// Issue #646: the sidebar Notes section was always empty because
/// `buildFolderTree` dropped every note whose path did not start with the
/// absolute vault path — but `symdesk ls --json` returns vault-relative
/// paths. These tests feed the tree builder the exact JSON shape the CLI
/// returns and pin the fixed behaviour.
final class NoteFolderTreeTests: XCTestCase {
    private func notes(fromJSON json: String) throws -> [Note] {
        let data = Data(json.utf8)
        return try JSONDecoder().decode([Note].self, from: data)
    }

    func testVaultRelativePathsAreNotFilteredOut() throws {
        // Exactly what `symdesk ls --json` emits: vault-relative paths.
        let json = """
        [
          {"path":"test01.md","title":"Test 01","modified_at":"2026-08-27T10:00:00Z"},
          {"path":"projects/alpha.md","title":"Alpha","modified_at":"2026-08-27T10:01:00Z"},
          {"path":"projects/deep/beta.md","title":"Beta","modified_at":"2026-08-27T10:02:00Z"}
        ]
        """
        let notes = try notes(fromJSON: json)

        let tree = buildFolderTree(from: notes, vaultPath: "/Users/test/Vaults/MyVault")

        // All three notes must appear — none may be filtered out.
        let leafIDs = Set(Self.leaves(of: tree).map(\.id))
        XCTAssertTrue(leafIDs.contains("test01.md"), "vault-root note missing from the tree")
        XCTAssertTrue(leafIDs.contains("projects/alpha.md"), "note in folder missing from the tree")
        XCTAssertTrue(leafIDs.contains("projects/deep/beta.md"), "nested note missing from the tree")

        // Folder structure is built from the relative components.
        let projects = tree.first { $0.isFolder && $0.name == "projects" }
        XCTAssertNotNil(projects, "projects folder missing from the tree")
        XCTAssertTrue(projects?.children.contains { $0.isFolder && $0.name == "deep" } ?? false,
                      "nested folder missing from the tree")
    }

    func testAbsolutePathsStillNestUnderVault() throws {
        let json = """
        [
          {"path":"/Users/test/Vaults/MyVault/test01.md","title":"Test 01","modified_at":"2026-08-27T10:00:00Z"}
        ]
        """
        let notes = try notes(fromJSON: json)

        let tree = buildFolderTree(from: notes, vaultPath: "/Users/test/Vaults/MyVault")
        let leafIDs = Set(Self.leaves(of: tree).map(\.id))
        XCTAssertTrue(leafIDs.contains("/Users/test/Vaults/MyVault/test01.md"),
                      "absolute-path note missing from the tree")
    }

    func testNoVaultPathYieldsFlatList() throws {
        let json = """
        [
          {"path":"test01.md","title":"Test 01","modified_at":"2026-08-27T10:00:00Z"}
        ]
        """
        let notes = try notes(fromJSON: json)

        let tree = buildFolderTree(from: notes, vaultPath: nil)
        XCTAssertEqual(tree.count, 1)
        XCTAssertEqual(tree.first?.name, "Test 01")
        XCTAssertNil(tree.first?.containingFolder)
    }

    private static func leaves(of tree: [FolderNode]) -> [FolderNode] {
        tree.flatMap { node in
            node.isFolder ? leaves(of: node.children) : [node]
        }
    }
}
