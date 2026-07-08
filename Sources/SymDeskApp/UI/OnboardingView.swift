import SwiftUI
import SymDeskCore

struct OnboardingView: View {
    @EnvironmentObject var core: DeskCore

    @State private var step: Step = .chooseLocation
    @State private var selectedSource: VaultSource?
    @State private var chosenURL: URL?
    @State private var isLoading = false
    @State private var progressMessage = ""
    @State private var errorMessage: String?

    enum Step {
        case chooseLocation
        case ready
    }

    enum VaultSource: String, CaseIterable, Identifiable {
        case icloudDrive = "iCloud Drive"
        case localFolder = "Local folder on this Mac"
        case customFolder = "Custom folder"
        case existingVault = "Existing vault"
        case demoData = "Try demo data"

        var id: String { rawValue }

        var icon: String {
            switch self {
            case .icloudDrive: return "icloud"
            case .localFolder: return "internaldrive"
            case .customFolder: return "folder"
            case .existingVault: return "text.book.closed"
            case .demoData: return "wand.and.stars"
            }
        }

        var description: String {
            switch self {
            case .icloudDrive: return "Store in your iCloud Drive folder — syncs automatically across devices."
            case .localFolder: return "A folder anywhere on this Mac."
            case .customFolder: return "Dropbox, Synology, NAS, or any mounted drive."
            case .existingVault: return "Point to an existing Obsidian or Markdown vault and index it."
            case .demoData: return "Explore with sample documents and notes."
            }
        }
    }

    var body: some View {
        VStack(spacing: 0) {
            switch step {
            case .chooseLocation:
                chooseLocationStep
            case .ready:
                readyStep
            }
        }
        .frame(width: 580, height: 520)
    }

    // MARK: - Step 1: Choose Location

    private var chooseLocationStep: some View {
        VStack(spacing: 28) {
            VStack(spacing: 8) {
                Image(systemName: "folder.badge.plus")
                    .font(.system(size: 40))
                    .foregroundColor(.accentColor)
                Text("Welcome to SymDesk")
                    .font(.largeTitle.bold())
                Text("Where should your documents and notes live?")
                    .font(.title3)
                    .foregroundColor(.secondary)
            }
            .padding(.top, 32)

            if isLoading {
                VStack(spacing: 12) {
                    ProgressView()
                        .controlSize(.large)
                    Text(progressMessage)
                        .foregroundColor(.secondary)
                    if let err = errorMessage {
                        Text(err)
                            .foregroundColor(.red)
                            .font(.callout)
                    }
                }
                .frame(maxHeight: .infinity)
            } else {
                ScrollView {
                    VStack(spacing: 10) {
                        ForEach(VaultSource.allCases) { source in
                            sourceButton(source)
                        }
                    }
                    .padding(.horizontal, 40)
                }
                .frame(maxHeight: .infinity)
            }

            if let err = errorMessage, !isLoading {
                Text(err)
                    .foregroundColor(.red)
                    .font(.callout)
                    .padding(.horizontal, 40)
            }
        }
    }

    private func sourceButton(_ source: VaultSource) -> some View {
        Button {
            selectedSource = source
            handleSourceChoice(source)
        } label: {
            HStack(spacing: 14) {
                Image(systemName: source.icon)
                    .font(.title3)
                    .frame(width: 28)
                VStack(alignment: .leading, spacing: 2) {
                    Text(source.rawValue).font(.body.weight(.medium))
                    Text(source.description)
                        .font(.caption)
                        .foregroundColor(.secondary)
                }
                Spacer()
                Image(systemName: "chevron.right")
                    .font(.caption)
                    .foregroundColor(.secondary)
            }
            .padding(12)
            .background(Color.primary.opacity(0.04))
            .cornerRadius(10)
            .overlay(
                RoundedRectangle(cornerRadius: 10)
                    .stroke(Color.primary.opacity(0.08), lineWidth: 1)
            )
        }
        .buttonStyle(.plain)
        .disabled(isLoading)
    }

    // MARK: - Step 2: Ready

    private var readyStep: some View {
        VStack(spacing: 24) {
            Spacer()

            Image(systemName: "checkmark.circle.fill")
                .font(.system(size: 56))
                .foregroundColor(.green)

            Text("You're all set!")
                .font(.largeTitle.bold())

            Text("SymDesk is ready to explore your vault.")
                .font(.title3)
                .foregroundColor(.secondary)

            VStack(alignment: .leading, spacing: 12) {
                capabilityRow(icon: "folder", title: "Watch Folder", desc: "Auto-detects changes in your vault")
                capabilityRow(icon: "terminal", title: "CLI Access", desc: "Run `symdesk` commands from Terminal")
                capabilityRow(icon: "cpu", title: "MCP / Agents", desc: "Connect AI agents to your knowledge")
                capabilityRow(icon: "magnifyingglass", title: "Search", desc: "Full-text search across all documents")
            }
            .padding(20)
            .background(Color.primary.opacity(0.04))
            .cornerRadius(12)
            .frame(maxWidth: 420)

            if core.isDemoMode {
                Text("Demo mode — sample data loaded.")
                    .font(.callout)
                    .foregroundColor(.secondary)
            }

            Spacer()

            Button("Get Started") {
                dismissOnboarding()
            }
            .buttonStyle(.borderedProminent)
            .controlSize(.large)
            .padding(.bottom, 32)
        }
    }

    private func capabilityRow(icon: String, title: String, desc: String) -> some View {
        HStack(spacing: 12) {
            Image(systemName: icon)
                .frame(width: 20)
                .foregroundColor(.accentColor)
            VStack(alignment: .leading, spacing: 1) {
                Text(title).font(.body.weight(.medium))
                Text(desc).font(.caption).foregroundColor(.secondary)
            }
        }
    }

    // MARK: - Actions

    private func handleSourceChoice(_ source: VaultSource) {
        switch source {
        case .icloudDrive, .localFolder, .customFolder:
            pickFolder(preferICloud: source == .icloudDrive)
        case .existingVault:
            pickFolder(preferICloud: false, isExistingVault: true)
        case .demoData:
            initDemo()
        }
    }

    private func pickFolder(preferICloud: Bool = false, isExistingVault: Bool = false) {
        let panel = NSOpenPanel()
        panel.title = isExistingVault ? "Select Existing Vault" : "Choose Vault Location"
        panel.canChooseFiles = false
        panel.canChooseDirectories = true
        panel.canCreateDirectories = !isExistingVault
        panel.allowsMultipleSelection = false

        if preferICloud {
            let iCloudURL = FileManager.default.url(forUbiquityContainerIdentifier: nil)?
                .appendingPathComponent("Documents")
            if let iCloudURL {
                panel.directoryURL = iCloudURL
            }
        }

        panel.begin { response in
            guard response == .OK, let url = panel.url else { return }
            Task { @MainActor in
                self.chosenURL = url
                self.progressMessage = "Configuring vault…"
                self.isLoading = true
                self.errorMessage = nil

                do {
                    VaultConfig.setVault(url: url)
                    core.vaultPath = url.path

                    if isExistingVault {
                        progressMessage = "Indexing vault…"
                        _ = try await core.indexVault(path: url.path)
                    }

                    dismissOnboarding()
                } catch {
                    self.errorMessage = "Failed to set up vault: \(error.localizedDescription)"
                    self.isLoading = false
                }
            }
        }
    }

    private func initDemo() {
        isLoading = true
        errorMessage = nil
        progressMessage = "Setting up demo vault…"

        let appSupport = FileManager.default.urls(for: .applicationSupportDirectory, in: .userDomainMask).first!
        let demoDir = appSupport.appendingPathComponent("SymDesk").appendingPathComponent("DemoVault")

        Task {
            do {
                _ = try FileManager.default.createDirectory(at: demoDir, withIntermediateDirectories: true)
                let vaultPath = try await core.initDemo(into: demoDir.path)

                let vaultURL = URL(fileURLWithPath: vaultPath)
                VaultConfig.setDemoVault(url: vaultURL)
                core.vaultPath = vaultPath

                dismissOnboarding()
            } catch {
                self.errorMessage = "Demo init failed: \(error.localizedDescription)"
                self.isLoading = false
            }
        }
    }

    private func dismissOnboarding() {
        NotificationCenter.default.post(name: .onboardingComplete, object: nil)
    }
}

extension Notification.Name {
    static let onboardingComplete = Notification.Name("symdesk.onboardingComplete")
}
