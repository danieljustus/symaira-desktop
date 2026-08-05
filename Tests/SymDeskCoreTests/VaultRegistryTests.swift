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

    // MARK: - Issue #296 registry mutations

    func testUpsertAddsNewEntryAndReplacesExistingById() {
        let registry = VaultRegistry(defaults: makeDefaults())
        let first = VaultEntry.local(name: "Alpha", path: "/tmp/a")
        let second = VaultEntry.local(name: "Beta", path: "/tmp/b")

        registry.upsert(first)
        registry.upsert(second)

        var entries = registry.entries()
        XCTAssertEqual(entries.count, 2)
        XCTAssertEqual(entries.map(\.name), ["Alpha", "Beta"])

        let renamed = VaultEntry(
            id: first.id,
            name: "Alpha Renamed",
            kind: .local,
            path: "/tmp/a",
            lastOpenedAt: first.lastOpenedAt
        )
        registry.upsert(renamed)

        entries = registry.entries()
        XCTAssertEqual(entries.count, 2, "upsert must not duplicate an existing id")
        XCTAssertEqual(entries.first(where: { $0.id == first.id })?.name, "Alpha Renamed")
        XCTAssertEqual(entries.first(where: { $0.id == first.id })?.path, "/tmp/a")
    }

    func testRemoveForgetsEntryOnly() {
        let registry = VaultRegistry(defaults: makeDefaults())
        let local = VaultEntry.local(name: "Alpha", path: "/tmp/a")
        let server = VaultEntry.server(name: "Srv", url: URL(string: "https://s.example.test")!)
        registry.save([local, server])

        registry.remove(id: local.id)

        let entries = registry.entries()
        XCTAssertEqual(entries.map(\.id), [server.id])
        XCTAssertNil(registry.entry(id: local.id))
    }

    func testRenameUpdatesNameInPlace() {
        let registry = VaultRegistry(defaults: makeDefaults())
        let local = VaultEntry.local(name: "Alpha", path: "/tmp/a")
        registry.save([local])

        let updated = registry.rename(id: local.id, to: "Personal")

        XCTAssertEqual(updated?.name, "Personal")
        XCTAssertEqual(registry.entry(id: local.id)?.name, "Personal")
        XCTAssertEqual(registry.entry(id: local.id)?.path, "/tmp/a")
        XCTAssertNil(registry.rename(id: UUID(), to: "Nope"))
    }

    func testLocalEntryMatchesStandardizedPaths() {
        let registry = VaultRegistry(defaults: makeDefaults())
        let local = VaultEntry.local(name: "Alpha", path: "/tmp/a")
        registry.save([local])

        XCTAssertEqual(registry.localEntry(path: "/tmp/a")?.id, local.id)
        XCTAssertEqual(registry.localEntry(path: "/tmp/a/")?.id, local.id)
        XCTAssertNil(registry.localEntry(path: "/tmp/other"))
    }

    func testMostRecentlyOpenedReturnsLatestEntryAndIgnoresNeverOpened() {
        let registry = VaultRegistry(defaults: makeDefaults())
        let t1 = Date(timeIntervalSince1970: 1_700_000_000)
        let t2 = t1.addingTimeInterval(100)
        let old = VaultEntry.local(name: "Old", path: "/tmp/old", lastOpenedAt: t1)
        let fresh = VaultEntry.local(name: "Fresh", path: "/tmp/fresh", lastOpenedAt: t2)
        let never = VaultEntry.local(name: "Never", path: "/tmp/never")
        registry.save([old, fresh, never])

        XCTAssertEqual(registry.mostRecentlyOpened()?.id, fresh.id)
    }

    func testRegisterLocalReusesEntryForSameFolder() {
        let registry = VaultRegistry(defaults: makeDefaults())
        let first = registry.registerLocal(
            name: "Vault",
            path: "/tmp/one",
            bookmarkData: Data([0x01])
        )
        let again = registry.registerLocal(
            name: "Renamed",
            path: "/tmp/one/",
            bookmarkData: Data([0x02])
        )

        XCTAssertEqual(again.id, first.id, "same folder must reuse the same entry")
        XCTAssertEqual(again.name, "Renamed", "a provided name wins over the existing one")
        XCTAssertEqual(again.bookmarkData, Data([0x02]), "fresh bookmark wins")
        XCTAssertEqual(registry.entries().count, 1)
    }

    func testRegisterLocalDefaultsNameToFolderWhenEmpty() {
        let registry = VaultRegistry(defaults: makeDefaults())
        let entry = registry.registerLocal(
            name: "",
            path: "/tmp/My Vault",
            bookmarkData: nil
        )
        XCTAssertEqual(entry.name, "My Vault")
    }

    // MARK: - VaultConfig legacy key mapping

    func testApplyLocalKeysWritesLegacySingleVaultKeys() {
        let defaults = makeDefaults()
        let entry = VaultEntry.local(
            name: "Work",
            path: "/tmp/work-vault",
            bookmarkData: Data([0xAB, 0xCD]),
            isDemoMode: false
        )

        VaultConfig.applyLocalKeys(entry, defaults: defaults)

        XCTAssertEqual(defaults.string(forKey: "symdesk.vaultPath"), "/tmp/work-vault")
        XCTAssertEqual(defaults.data(forKey: "symdesk.vaultBookmark"), Data([0xAB, 0xCD]))
        XCTAssertEqual(defaults.bool(forKey: "symdesk.isDemoMode"), false)
    }

    func testApplyLocalKeysWritesDemoModeFlag() {
        let defaults = makeDefaults()
        let entry = VaultEntry.local(name: "Demo", path: "/tmp/demo", isDemoMode: true)

        VaultConfig.applyLocalKeys(entry, defaults: defaults)

        XCTAssertEqual(defaults.bool(forKey: "symdesk.isDemoMode"), true)
    }

    func testApplyLocalKeysPreservesKeysWhenEntryFieldsNil() {
        let defaults = makeDefaults()
        defaults.set("/keep/me", forKey: "symdesk.vaultPath")
        let entry = VaultEntry.local(name: "No path", path: nil)

        VaultConfig.applyLocalKeys(entry, defaults: defaults)

        XCTAssertEqual(defaults.string(forKey: "symdesk.vaultPath"), "/keep/me",
                       "nil entry fields must not clobber existing values")
    }

    private func makeDefaults() -> UserDefaults {
        let suiteName = "symdesk-vault-registry-tests-\(UUID().uuidString)"
        let defaults = UserDefaults(suiteName: suiteName)!
        defaults.removePersistentDomain(forName: suiteName)
        addTeardownBlock { [suiteName] in
            UserDefaults(suiteName: suiteName)?.removePersistentDomain(forName: suiteName)
        }
        return defaults
    }
}
