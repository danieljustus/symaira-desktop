import SwiftUI
import AppKit
import SymairaTheme
import SymDeskCore

/// Vault switcher shown in the sidebar header: pick a registered vault,
/// open another folder, create a new vault or remove an entry from the list.
///
/// The registry (`VaultRegistry`) is the durable list of known vaults; this
/// view switches the *active* one via `DeskCore.switchVault` /
/// `DeskCore.createVault`, and posts `.vaultSwitched` so the app shell
/// restarts the event watcher and reloads all vault-derived state (issue #296).
struct VaultSwitcherView: View {
    @EnvironmentObject var core: DeskCore

    @State private var entries: [VaultEntry] = []
    @State private var isShowingCreateVault = false
    @State private var isShowingServerConnect = false
    @State private var pendingRemoval: VaultEntry?
    @State private var isBusy = false
    @State private var errorMessage: String?

    private var registry: VaultRegistry { VaultRegistry() }

    private var localEntries: [VaultEntry] {
        entries.filter { $0.kind == .local }
    }

    private var serverEntries: [VaultEntry] {
        entries.filter { $0.kind == .server }
    }

    private var activeLocalEntry: VaultEntry? {
        guard !core.isRemote, let path = core.vaultPath else { return nil }
        return registry.localEntry(path: path)
    }

    private var activeName: String {
        if core.isRemote {
            return core.serverURL?.host ?? "Server"
        }
        if let active = activeLocalEntry {
            return active.name
        }
        if let path = core.vaultPath {
            return URL(fileURLWithPath: path).lastPathComponent
        }
        return "Vault"
    }

    var body: some View {
        Menu {
            if !localEntries.isEmpty {
                Section("Vaults") {
                    ForEach(localEntries) { entry in
                        Button {
                            switchToLocal(entry)
                        } label: {
                            Label(entry.name, systemImage: isActiveLocal(entry) ? "checkmark" : "folder")
                        }
                    }
                }
            }

            Section("Server") {
                if core.isRemote, let url = core.serverURL {
                    Button {
                        // Already connected — keep the entry visible as the
                        // active peer.
                    } label: {
                        Label("Server · \(url.host ?? url.absoluteString)", systemImage: "checkmark")
                    }
                } else if let connection = ServerConnectionConfig.connection() {
                    Button {
                        reconnectServer(connection)
                    } label: {
                        Label("Server · \(connection.url.host ?? connection.url.absoluteString)", systemImage: "server.rack")
                    }
                } else {
                    Button {
                        isShowingServerConnect = true
                    } label: {
                        Label("Connect to Server…", systemImage: "server.rack")
                    }
                }
            }

            Divider()

            Button {
                openOtherVault()
            } label: {
                Label("Open Other Vault…", systemImage: "folder.badge.plus")
            }

            Button {
                isShowingCreateVault = true
            } label: {
                Label("Create New Vault…", systemImage: "folder.badge.plus")
            }

            if let active = activeLocalEntry {
                Divider()
                Button(role: .destructive) {
                    pendingRemoval = active
                } label: {
                    Label("Remove “\(active.name)” from List", systemImage: "minus.circle")
                }
            }
        } label: {
            HStack(spacing: 6) {
                Image(systemName: core.isRemote ? "server.rack" : "internaldrive")
                    .symairaText(.caption)
                    .foregroundStyle(SymairaTheme.goldPrimary)
                Text(activeName)
                    .symairaText(.subheading).bold()
                    .foregroundStyle(SymairaTheme.textPrimary)
                    .lineLimit(1)
                    .truncationMode(.middle)
                Image(systemName: "chevron.down")
                    .symairaText(.caption)
                    .foregroundStyle(SymairaTheme.textSecondary)
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .contentShape(Rectangle())
        }
        .menuStyle(.borderlessButton)
        .fixedSize(horizontal: true, vertical: false)
        .help("Switch vault")
        .onAppear(perform: reloadEntries)
        .onReceive(NotificationCenter.default.publisher(for: .vaultSwitched)) { _ in
            reloadEntries()
        }
        .onReceive(NotificationCenter.default.publisher(for: .vaultReset)) { _ in
            reloadEntries()
        }
        .sheet(isPresented: $isShowingCreateVault) {
            CreateVaultSheet { name, url in
                await createVault(named: name, at: url)
            }
        }
        .sheet(isPresented: $isShowingServerConnect) {
            ServerConnectionSheet { url, token in
                connectToServer(url: url, token: token)
            }
        }
        .confirmationDialog(
            "Remove “\(pendingRemoval?.name ?? "")” from the list?",
            isPresented: Binding(
                get: { pendingRemoval != nil },
                set: { if !$0 { pendingRemoval = nil } }
            ),
            titleVisibility: .visible
        ) {
            Button("Remove from List", role: .destructive) {
                if let entry = pendingRemoval {
                    removeFromList(entry)
                }
                pendingRemoval = nil
            }
            Button("Cancel", role: .cancel) { pendingRemoval = nil }
        } message: {
            Text("The vault folder on disk and all its files are left untouched.")
        }
        .overlay(alignment: .bottomLeading) {
            if let errorMessage {
                Text(errorMessage)
                    .symairaText(.caption)
                    .foregroundStyle(.red)
                    .padding(6)
                    .background(Color.red.opacity(0.12))
                    .cornerRadius(6)
            }
        }
    }

    // MARK: - Actions

    private func isActiveLocal(_ entry: VaultEntry) -> Bool {
        guard !core.isRemote, let path = core.vaultPath else { return false }
        return registry.localEntry(path: path)?.id == entry.id
    }

    private func reloadEntries() {
        entries = registry.entries()
        errorMessage = nil
    }

    private func switchToLocal(_ entry: VaultEntry) {
        guard entry.kind == .local else { return }
        core.switchVault(to: entry)
        reloadEntries()
    }

    private func reconnectServer(_ connection: ServerConnection) {
        Task { @MainActor in
            isBusy = true
            defer { isBusy = false }
            do {
                try await core.connectToServer(url: connection.url.absoluteString, token: connection.token)
            } catch {
                errorMessage = error.localizedDescription
            }
            reloadEntries()
        }
    }

    private func connectToServer(url: String, token: String) {
        isShowingServerConnect = false
        Task { @MainActor in
            isBusy = true
            defer { isBusy = false }
            do {
                try await core.connectToServer(url: url, token: token)
            } catch {
                errorMessage = error.localizedDescription
            }
            reloadEntries()
        }
    }

    private func openOtherVault() {
        let panel = NSOpenPanel()
        panel.title = "Open Other Vault"
        panel.canChooseFiles = false
        panel.canChooseDirectories = true
        panel.canCreateDirectories = true
        panel.allowsMultipleSelection = false
        panel.begin { response in
            guard response == .OK, let url = panel.url else { return }
            Task { @MainActor in
                do {
                    let path = url.path
                    let bookmarkData = try? url.bookmarkData(
                        options: .withSecurityScope,
                        includingResourceValuesForKeys: nil,
                        relativeTo: nil
                    )
                    let entry = registry.registerLocal(
                        name: url.lastPathComponent,
                        path: path,
                        bookmarkData: bookmarkData
                    )
                    core.switchVault(to: entry)
                }
                reloadEntries()
            }
        }
    }

    private func createVault(named name: String, at url: URL) async {
        do {
            let entry = try await core.createVault(named: name, at: url)
            isShowingCreateVault = false
            reloadEntries()
            _ = entry
        } catch {
            errorMessage = "Failed to create vault: \(error.localizedDescription)"
        }
    }

    private func removeFromList(_ entry: VaultEntry) {
        core.removeVaultFromRegistry(id: entry.id)
        reloadEntries()
    }
}

/// Sheet for creating a new vault: name + target folder. The folder is
/// scaffolded with the contract layout (templates/, assets/) and indexed by
/// `DeskCore.createVault`.
private struct CreateVaultSheet: View {
    @Environment(\.dismiss) private var dismiss
    @State private var name = ""
    @State private var pickedURL: URL?
    @State private var isPicking = false
    let onCreate: (String, URL) async -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 22) {
            HStack(alignment: .top, spacing: 14) {
                Image(systemName: "folder.badge.plus")
                    .font(.system(size: 28))
                    .foregroundStyle(SymairaTheme.goldPrimary)
                VStack(alignment: .leading, spacing: 4) {
                    Text("Create New Vault").symairaText(.title).bold()
                    Text("A new empty vault is scaffolded with the contract layout and indexed immediately.")
                        .foregroundStyle(SymairaTheme.textSecondary)
                }
            }

            VStack(alignment: .leading, spacing: 14) {
                LabeledContent("Name") {
                    TextField("My Vault", text: $name)
                        .textFieldStyle(.roundedBorder)
                        .frame(width: 330)
                }
                LabeledContent("Location") {
                    HStack(spacing: 8) {
                        Text(pickedURL?.path ?? "Choose a folder…")
                            .symairaText(.callout)
                            .foregroundStyle(pickedURL == nil ? SymairaTheme.textSecondary : SymairaTheme.textPrimary)
                            .lineLimit(1)
                            .truncationMode(.middle)
                            .frame(width: 220, alignment: .leading)
                        Button("Choose…") { pickFolder() }
                            .buttonStyle(.bordered)
                            .controlSize(.small)
                    }
                }
            }
            .padding(18)
            .glassmorphicPanel()

            HStack {
                Spacer()
                Button("Cancel") { dismiss() }
                    .buttonStyle(.bordered)
                Button("Create") {
                    if let pickedURL {
                        let target = pickedURL.appendingPathComponent(name.trimmingCharacters(in: .whitespaces), isDirectory: true)
                        Task { await onCreate(name.trimmingCharacters(in: .whitespaces), target) }
                    }
                }
                .buttonStyle(.borderedProminent)
                .tint(SymairaTheme.goldPrimary)
                .disabled(name.trimmingCharacters(in: .whitespaces).isEmpty || pickedURL == nil || isPicking)
            }
        }
        .padding(28)
        .frame(width: 590)
        .background(SymairaTheme.bgDark)
    }

    private func pickFolder() {
        isPicking = true
        let panel = NSOpenPanel()
        panel.title = "Choose Parent Folder for New Vault"
        panel.canChooseFiles = false
        panel.canChooseDirectories = true
        panel.canCreateDirectories = true
        panel.allowsMultipleSelection = false
        panel.begin { response in
            isPicking = false
            guard response == .OK else { return }
            pickedURL = panel.url
        }
    }
}
