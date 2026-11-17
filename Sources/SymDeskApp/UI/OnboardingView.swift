import SwiftUI
import SymairaTheme
import SymDeskCore

struct OnboardingView: View {
    @EnvironmentObject var core: DeskCore

    /// Path of a previously opened vault whose folder is gone. Shown so the
    /// user learns why they are back here instead of in their vault (#444).
    var missingVaultPath: String?

    @State private var step: Step = .chooseLocation
    @State private var selectedSource: VaultSource?
    @State private var chosenURL: URL?
    @State private var isLoading = false
    @State private var progressMessage = ""
    @State private var errorMessage: String?
	@State private var isShowingServerConnection = false
	@State private var isShowingPaperlessImport = false

    enum Step {
        case chooseLocation
        case ready
    }

    /// Explains why the user is on the welcome screen when a vault had been
    /// configured: its folder is gone (issue #444). Split out of the body so
    /// the ViewBuilder type-checker stays within budget (#293, #296).
    @ViewBuilder
    private var missingVaultNotice: some View {
        if let path = missingVaultPath {
            MissingVaultNotice(path: path)
        }
    }

    enum VaultSource: String, CaseIterable, Identifiable {
        case icloudDrive = "iCloud Drive"
        case localFolder = "Local folder on this Mac"
        case customFolder = "Custom folder"
        case existingVault = "Existing vault"
		case selfHostedServer = "Self-hosted SymDesk Server"
        case demoData = "Try demo data"

        var id: String { rawValue }

        var icon: SymairaIcon {
            switch self {
            case .icloudDrive: return .cloud
            case .localFolder: return .storage
            case .customFolder: return .folder
            case .existingVault: return .library
			case .selfHostedServer: return .server
            case .demoData: return .magic
            }
        }

        var description: String {
            switch self {
            case .icloudDrive: return "Store in your iCloud Drive folder — syncs automatically across devices."
            case .localFolder: return "A folder anywhere on this Mac."
            case .customFolder: return "Dropbox, Synology, NAS, or any mounted drive."
            case .existingVault: return "Point to an existing Obsidian or Markdown vault and index it."
			case .selfHostedServer: return "Connect to a Raspberry Pi, Mac mini, NAS, or Home Assistant container."
            case .demoData: return "Explore with sample documents and notes."
            }
        }
    }

    var body: some View {
        SymairaScreen {
            VStack(spacing: 0) {
                switch step {
                case .chooseLocation:
                    chooseLocationStep
                case .ready:
                    readyStep
                }
            }
        }
        .frame(minWidth: 560, maxWidth: .infinity, minHeight: 540, maxHeight: .infinity)
		.sheet(isPresented: $isShowingServerConnection) {
			ServerConnectionSheet { url, token in
				isLoading = true
				progressMessage = "Connecting securely to SymDesk Server…"
				errorMessage = nil
				Task {
					do {
						try await core.connectToServer(url: url, token: token)
						isShowingServerConnection = false
						advanceToReady()
					} catch {
						errorMessage = error.localizedDescription
						isLoading = false
						isShowingServerConnection = false
					}
				}
			}
		}
		.sheet(isPresented: $isShowingPaperlessImport) {
			PaperlessImportView()
		}
    }

    // MARK: - Step 1: Choose Location

    private var chooseLocationStep: some View {
        VStack(spacing: SymairaSpacing.xLarge) {
            VStack(spacing: SymairaSpacing.small) {
                SymairaIconView(.folderAdd, size: 24)
                    .foregroundStyle(SymairaTokens.ink2)
                Text("Welcome to SymDesk")
                    .symairaText(.display)
                Text("Where should your documents and notes live?")
                    .symairaText(.heading, respectsForeground: false)
                    .foregroundStyle(SymairaTokens.ink2)
                    .multilineTextAlignment(.center)
            }
            .padding(.top, 32)
            .padding(.horizontal, 40)

            missingVaultNotice

            if isLoading {
                SymairaLoadingState(progressMessage)
            } else {
                ScrollView {
                    VStack(spacing: SymairaSpacing.medium) {
                        ForEach(VaultSource.allCases) { source in
                            sourceButton(source)
                        }
                    }
                    .padding(.horizontal, 40)
                    .padding(.bottom, 8)
                    .frame(maxWidth: 720)
                    .frame(maxWidth: .infinity)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            }

            if let err = errorMessage {
                errorBox(err)
                    .padding(.horizontal, 40)
                    .padding(.bottom, 24)
                    .frame(maxWidth: 720)
            }
        }
    }

    private func errorBox(_ message: String) -> some View {
        SymairaNotice(message: message, tone: .critical, onDismiss: { errorMessage = nil })
    }

    private func sourceButton(_ source: VaultSource) -> some View {
        SourceButtonRow(source: source, isLoading: isLoading) {
            selectedSource = source
            handleSourceChoice(source)
        }
    }

    private struct SourceButtonRow: View {
        let source: VaultSource
        let isLoading: Bool
        let action: () -> Void

        @State private var isHovering = false

        var body: some View {
            Button(action: action) {
                HStack(spacing: SymairaSpacing.large) {
                    SymairaIconView(source.icon, size: 20)
                        .foregroundStyle(SymairaTokens.ink2)
                        .frame(width: 28)
                    VStack(alignment: .leading, spacing: SymairaSpacing.xSmall) {
                        Text(source.rawValue)
                            .symairaText(.bodyEmphasized)
                        Text(source.description)
                            .symairaText(.secondary)
                    }
                    Spacer()
                    SymairaIconView(.forward, size: 14)
                        .foregroundStyle(SymairaTokens.ink3)
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .symairaCard(padding: SymairaSpacing.medium)
            .overlay(
                RoundedRectangle(cornerRadius: SymairaTokens.Radius.lg, style: .continuous)
                    .stroke(isHovering ? SymairaTokens.focus : Color.clear, lineWidth: 1)
            )
            .onHover { hovering in
                withAnimation(SymairaTheme.transitionFast) {
                    isHovering = hovering
                }
            }
            .disabled(isLoading)
        }
    }

    // MARK: - Step 2: Ready

    private var readyStep: some View {
        VStack(spacing: SymairaSpacing.xLarge) {
            Spacer()

            SymairaIconView(.success, size: 30)
                .foregroundStyle(SymairaTokens.positive)

            Text("You're all set!")
                .symairaText(.display)

            Text("SymDesk is ready to explore your vault.")
                .symairaText(.heading, respectsForeground: false)
                .foregroundStyle(SymairaTokens.ink2)

            VStack(alignment: .leading, spacing: SymairaSpacing.medium) {
                capabilityRow(icon: .folder, title: "Watch Folder", desc: "Auto-detects changes in your vault")
                capabilityRow(icon: .terminal, title: "CLI Access", desc: "Run `symdesk` commands from Terminal")
                capabilityRow(icon: .cpu, title: "MCP / Agents", desc: "Connect AI agents to your knowledge")
                capabilityRow(icon: .search, title: "Search", desc: "Full-text search across all documents")
            }
            .symairaCard(padding: SymairaSpacing.xLarge)
            .frame(maxWidth: 420)

            if core.isDemoMode {
                Text("Demo mode — sample data loaded.")
                    .symairaText(.secondary)
            }

            // Migration path for Paperless-ngx users: discoverable during
            // onboarding, exactly where new users set up their vault (issue #307).
            if !core.isDemoMode {
                Button {
                    isShowingPaperlessImport = true
                } label: {
                    Label("Import from Paperless-ngx…", symairaIcon: .download)
                }
                .symairaButtonStyle(.secondary)
                .help("Migrate an existing Paperless-ngx export into this vault")
            }

            Spacer()

            HStack(spacing: SymairaSpacing.medium) {
                Button("Explore Capabilities") {
                    NotificationCenter.default.post(name: .openDiscover, object: nil)
                    dismissOnboarding()
                }
                .symairaButtonStyle(.secondary)

                Button("Get Started") {
                    dismissOnboarding()
                }
                .symairaButtonStyle(.primary)
            }
            .padding(.bottom, 32)
        }
    }

    private func capabilityRow(icon: SymairaIcon, title: String, desc: String) -> some View {
        HStack(spacing: SymairaSpacing.medium) {
            SymairaIconView(icon, size: 16)
                .frame(width: 20)
                .foregroundStyle(SymairaTokens.ink2)
            VStack(alignment: .leading, spacing: SymairaSpacing.xSmall) {
                Text(title)
                    .symairaText(.bodyEmphasized)
                Text(desc)
                    .symairaText(.secondary)
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
		case .selfHostedServer:
			isShowingServerConnection = true
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

                    advanceToReady()
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
                let vaultPath: String
                let markerURL = demoDir.appendingPathComponent(".symdesk")
                if FileManager.default.fileExists(atPath: markerURL.path) {
                    // Demo vault was already materialised in a previous run —
                    // `symdesk demo init` refuses non-empty directories, so reuse it.
                    // Unlike the fresh-creation branch below, reuse does not get
                    // indexed as a side effect of `initDemo`, so index it explicitly.
                    vaultPath = demoDir.path
                    progressMessage = "Indexing vault…"
                    _ = try await core.indexVault(path: vaultPath)
                } else {
                    _ = try FileManager.default.createDirectory(at: demoDir, withIntermediateDirectories: true)
                    vaultPath = try await core.initDemo(into: demoDir.path)
                }

                let vaultURL = URL(fileURLWithPath: vaultPath)
                VaultConfig.setDemoVault(url: vaultURL)
                core.vaultPath = vaultPath

                advanceToReady()
            } catch {
                self.errorMessage = "Demo init failed: \(error.localizedDescription)"
                self.isLoading = false
            }
        }
    }

    /// Move from vault setup into the "You're all set!" step instead of
    /// dismissing onboarding immediately — every source (folder, existing
    /// vault, demo data, self-hosted server) routes through this so the
    /// completion screen's "Get Started" / "Explore Capabilities" buttons
    /// are what actually dismiss onboarding.
    private func advanceToReady() {
        isLoading = false
        step = .ready
    }

    private func dismissOnboarding() {
        NotificationCenter.default.post(name: .onboardingComplete, object: nil)
    }
}

struct ServerConnectionSheet: View {
	@Environment(\.dismiss) private var dismiss
	@State private var serverURL = ""
	@State private var token = ""
	let connect: (String, String) -> Void

	var body: some View {
		SymairaSheetScaffold(
			title: "Connect to SymDesk Server",
			dismiss: { dismiss() },
			primaryTitle: "Connect",
			primaryDisabled: ServerConnectionConfig.normalizedURL(serverURL) == nil || token.count < 32,
			primary: { connect(serverURL, token) }
		) {
			Text("Documents stay on your server. This Mac becomes a fast native frontend.")
				.symairaText(.secondary)

			SymairaFormSection {
				SymairaFormRow("Server URL") {
					TextField("https://symdesk.example.net", text: $serverURL)
						.textFieldStyle(.symaira)
						.frame(width: 300)
				}
				SymairaFormDivider()
				SymairaFormRow("Access Token") {
					SecureField("At least 32 characters", text: $token)
						.textFieldStyle(.symaira)
						.frame(width: 300)
				}
			}

			Label("Use HTTPS or a trusted VPN outside your home network. The token is stored in Keychain.", symairaIcon: .lock)
				.symairaText(.secondary)
		}
		.frame(width: 590, height: 380)
	}
}

/// Tells the user why they are back on the welcome screen: the vault they had
/// open is no longer where it was (issue #444). Its own view so the welcome
/// screen's ViewBuilder stays within the type-checker budget (#293, #296).
private struct MissingVaultNotice: View {
    let path: String

    var body: some View {
        SymairaNotice(
            title: "Your last vault is no longer there",
            message: "SymDesk did not change or delete anything. Pick the folder again if it moved, or choose another vault below. Previous location: \(path)",
            tone: .warning
        )
        .padding(.horizontal, 40)
    }
}

extension Notification.Name {
    static let onboardingComplete = Notification.Name("symdesk.onboardingComplete")
    static let openDiscover = Notification.Name("symdesk.openDiscover")
    /// Opens the Dashboard. Distinct from `openDiscover`, which opens the
    /// capabilities screen — the View menu item conflated the two (issue #443).
    static let openDashboard = Notification.Name("symdesk.openDashboard")
    /// Posted by menu bar commands to trigger UI actions in ContentView.
    static let openCommandPalette = Notification.Name("symdesk.openCommandPalette")
    static let toggleEditorPreview = Notification.Name("symdesk.toggleEditorPreview")
    static let toggleEditorInspector = Notification.Name("symdesk.toggleEditorInspector")
    static let openEditorAIDock = Notification.Name("symdesk.openEditorAIDock")
    static let openNewNoteSheet = Notification.Name("symdesk.openNewNoteSheet")
    static let openRulesSettings = Notification.Name("symdesk.openRulesSettings")
}
