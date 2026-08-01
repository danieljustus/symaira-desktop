import Foundation
import XCTest
@testable import SymDeskCore

final class VaultRegistryTests: XCTestCase {
    func testMigratesLegacyLocalVaultPreservingPathBookmarkAndDemoMode() {
        let defaults = makeDefaults()
        let legacyPath = "/tmp/legacy-vault"
        let legacyBookmark = Data([0x01, 0x02, 0x03])
        let migratedAt = Date(timeIntervalSince1970: 1_700_000_000)

        defaults.set(legacyPath, forKey: "symdesk.vaultPath")
        defaults.set(legacyBookmark, forKey: "symdesk.vaultBookmark")
        defaults.set(true, forKey: "symdesk.isDemoMode")

        let registry = VaultRegistry(defaults: defaults)
        let entries = registry.entries(now: migratedAt)

        XCTAssertEqual(entries.count, 1)
        XCTAssertEqual(entries[0].kind, .local)
        XCTAssertEqual(entries[0].name, "legacy-vault")
        XCTAssertEqual(entries[0].path, legacyPath)
        XCTAssertEqual(entries[0].bookmarkData, legacyBookmark)
        XCTAssertTrue(entries[0].isDemoMode)
        XCTAssertEqual(entries[0].lastOpenedAt, migratedAt)

        let migratedID = entries[0].id
        let reread = registry.entries(now: migratedAt.addingTimeInterval(60))
        XCTAssertEqual(reread.map(\.id), [migratedID])
        XCTAssertEqual(reread[0].lastOpenedAt, migratedAt)

        XCTAssertEqual(defaults.string(forKey: "symdesk.vaultPath"), legacyPath)
        XCTAssertEqual(defaults.data(forKey: "symdesk.vaultBookmark"), legacyBookmark)
        XCTAssertNotNil(defaults.data(forKey: VaultRegistry.registryDefaultsKey))
    }

    func testInvalidRegistryDataIsNotOverwrittenDuringMigration() {
        let defaults = makeDefaults()
        let invalidData = Data("not-json".utf8)
        defaults.set(invalidData, forKey: VaultRegistry.registryDefaultsKey)
        defaults.set("/tmp/legacy-vault", forKey: "symdesk.vaultPath")

        let registry = VaultRegistry(defaults: defaults)
        XCTAssertTrue(registry.entries(now: Date(timeIntervalSince1970: 1_700_000_000)).isEmpty)
        XCTAssertEqual(defaults.data(forKey: VaultRegistry.registryDefaultsKey), invalidData)
    }

    func testNamedVaultsPreserveServerPeerAndUpdateLastOpened() {
        let defaults = makeDefaults()
        let initialDate = Date(timeIntervalSince1970: 1_700_000_000)
        let reopenedDate = initialDate.addingTimeInterval(300)
        let local = VaultEntry.local(
            name: "Work",
            path: "/tmp/work-vault",
            bookmarkData: Data([0x04]),
            lastOpenedAt: initialDate
        )
        let server = VaultEntry.server(
            name: "Home server",
            url: URL(string: "https://symdesk.example.test")!,
            lastOpenedAt: initialDate
        )
        let registry = VaultRegistry(defaults: defaults)

        registry.save([local, server])
        _ = registry.recordOpened(local.id, at: reopenedDate)

        let entries = registry.entries()
        XCTAssertEqual(entries.count, 2)
        XCTAssertEqual(entries.first(where: { $0.id == local.id })?.lastOpenedAt, reopenedDate)
        XCTAssertEqual(entries.first(where: { $0.id == server.id })?.kind, .server)
        XCTAssertEqual(entries.first(where: { $0.id == server.id })?.serverURL, server.serverURL)

        let storedJSON = String(data: defaults.data(forKey: VaultRegistry.registryDefaultsKey)!, encoding: .utf8)!
        XCTAssertFalse(storedJSON.localizedCaseInsensitiveContains("token"))
    }

    private func makeDefaults() -> UserDefaults {
        let suiteName = "symdesk-vault-registry-tests-\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suiteName)!
        defaults.removePersistentDomain(forName: suiteName)
        addTeardownBlock {
            defaults.removePersistentDomain(forName: suiteName)
        }
        return defaults
    }
}
