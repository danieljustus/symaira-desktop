import SwiftUI
import SymairaTheme
import SymDeskCore

struct OnboardingView: View {
    @EnvironmentObject var core: DeskCore

    @State private var step: Step = .chooseLocation
    @State private var selectedSource: VaultSource?
    @State private var chosenURL: URL?
    @State private var isLoading = false
    @State private var progressMessage = ""
    @State private var errorMessage: String?
	@State private var isShowingServerConnection = false

    enum Step {
        case chooseLocation
        case ready
    }

    enum VaultSource: String, CaseIterable, Identifiable {
        case icloudDrive = "iCloud Drive"
        case localFolder = "Local folder on this Mac"
        case customFolder = "Custom folder"
        case existingVault = "Existing vault"
		case selfHostedServer = "Self-hosted SymDesk Server"
        case demoData = "Try demo data"

        var id: String { rawValue }

        var icon: String {
            switch self {
            case .icloudDrive: return "icloud"
            case .localFolder: return "internaldrive"
            case .customFolder: return "folder"
            case .existingVault: return "text.book.closed"
			case .selfHostedServer: return "server.rack"
            case .demoData: return "wand.and.stars"
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
    }

    // MARK: - Step 1: Choose Location

    private var chooseLocationStep: some View {
        VStack(spacing: 24) {
            VStack(spacing: 8) {
                Image(systemName: "folder.badge.plus")
                    .font(.system(size: 40))
                    .foregroundColor(SymairaTheme.goldPrimary)
                    .shadow(color: SymairaTheme.glowIntense, radius: 12)
                Text("Welcome to SymDesk")
                    .font(.largeTitle.bold())
                    .foregroundColor(SymairaTheme.textPrimary)
                Text("Where should your documents and notes live?")
                    .font(.title3)
                    .foregroundColor(SymairaTheme.textSecondary)
                    .multilineTextAlignment(.center)
            }
            .padding(.top, 32)
            .padding(.horizontal, 40)

            if isLoading {
                VStack(spacing: 12) {
                    ProgressView()
                        .controlSize(.large)
                        .tint(SymairaTheme.goldPrimary)
                    Text(progressMessage)
                        .foregroundColor(SymairaTheme.textSecondary)
                }
                .frame(maxWidth: .infinity, maxHeight: .infinity)
            } else {
                ScrollView {
                    VStack(spacing: 10) {
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
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: "exclamationmark.triangle.fill")
                .foregroundColor(.red)
                .padding(.top, 2)
            ScrollView {
                Text(message)
                    .foregroundColor(.red)
                    .font(.callout)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
            .frame(maxHeight: 96)
        }
        .padding(12)
        .background(
            RoundedRectangle(cornerRadius: 10)
                .fill(Color.red.opacity(0.08))
                .overlay(
                    RoundedRectangle(cornerRadius: 10)
                        .stroke(Color.red.opacity(0.35), lineWidth: 1)
                )
        )
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
                HStack(spacing: 14) {
                    Image(systemName: source.icon)
                        .font(.title3)
                        .foregroundColor(SymairaTheme.goldPrimary)
                        .frame(width: 28)
                    VStack(alignment: .leading, spacing: 2) {
                        Text(source.rawValue)
                            .font(.body.weight(.medium))
                            .foregroundColor(SymairaTheme.textPrimary)
                        Text(source.description)
                            .font(.caption)
                            .foregroundColor(SymairaTheme.textSecondary)
                    }
                    Spacer()
                    Image(systemName: "chevron.right")
                        .font(.caption)
                        .foregroundColor(SymairaTheme.textMuted)
                }
                .padding(12)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .glassCard(isHovered: isHovering)
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
        VStack(spacing: 24) {
            Spacer()

            Image(systemName: "checkmark.circle.fill")
                .font(.system(size: 56))
                .foregroundColor(SymairaTheme.goldPrimary)
                .shadow(color: SymairaTheme.glowIntense, radius: 16)

            Text("You're all set!")
                .font(.largeTitle.bold())
                .foregroundColor(SymairaTheme.textPrimary)

            Text("SymDesk is ready to explore your vault.")
                .font(.title3)
                .foregroundColor(SymairaTheme.textSecondary)

            VStack(alignment: .leading, spacing: 12) {
                capabilityRow(icon: "folder", title: "Watch Folder", desc: "Auto-detects changes in your vault")
                capabilityRow(icon: "terminal", title: "CLI Access", desc: "Run `symdesk` commands from Terminal")
                capabilityRow(icon: "cpu", title: "MCP / Agents", desc: "Connect AI agents to your knowledge")
                capabilityRow(icon: "magnifyingglass", title: "Search", desc: "Full-text search across all documents")
            }
            .padding(20)
            .glassmorphicPanel()
            .frame(maxWidth: 420)

            if core.isDemoMode {
                Text("Demo mode — sample data loaded.")
                    .font(.callout)
                    .foregroundColor(SymairaTheme.textSecondary)
            }

            Spacer()

            HStack(spacing: 12) {
                Button("Explore Capabilities") {
                    NotificationCenter.default.post(name: .openDiscover, object: nil)
                    dismissOnboarding()
                }
                .buttonStyle(SymairaSecondaryButtonStyle())

                Button("Get Started") {
                    dismissOnboarding()
                }
                .buttonStyle(SymairaPrimaryButtonStyle())
            }
            .padding(.bottom, 32)
        }
    }

    private func capabilityRow(icon: String, title: String, desc: String) -> some View {
        HStack(spacing: 12) {
            Image(systemName: icon)
                .frame(width: 20)
                .foregroundColor(SymairaTheme.goldPrimary)
            VStack(alignment: .leading, spacing: 1) {
                Text(title)
                    .font(.body.weight(.medium))
                    .foregroundColor(SymairaTheme.textPrimary)
                Text(desc)
                    .font(.caption)
                    .foregroundColor(SymairaTheme.textSecondary)
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

private struct ServerConnectionSheet: View {
	@Environment(\.dismiss) private var dismiss
	@State private var serverURL = ""
	@State private var token = ""
	let connect: (String, String) -> Void

	var body: some View {
		VStack(alignment: .leading, spacing: 22) {
			HStack(alignment: .top, spacing: 14) {
				Image(systemName: "server.rack")
					.font(.system(size: 28))
					.foregroundStyle(SymairaTheme.goldPrimary)
				VStack(alignment: .leading, spacing: 4) {
					Text("Connect to SymDesk Server").font(.title2.bold())
					Text("Documents stay on your server. This Mac becomes a fast native frontend.")
						.foregroundStyle(SymairaTheme.textSecondary)
				}
			}

			VStack(alignment: .leading, spacing: 14) {
				LabeledContent("Server URL") {
					TextField("https://symdesk.example.net", text: $serverURL)
						.textFieldStyle(.roundedBorder)
						.frame(width: 330)
				}
				LabeledContent("Access token") {
					SecureField("At least 32 characters", text: $token)
						.textFieldStyle(.roundedBorder)
						.frame(width: 330)
				}
			}
			.padding(18)
			.glassmorphicPanel()

			Label("Use HTTPS or a trusted VPN outside your home network. The token is stored in Keychain.", systemImage: "lock.shield")
				.font(.callout)
				.foregroundStyle(SymairaTheme.textSecondary)

			HStack {
				Spacer()
				Button("Cancel") { dismiss() }
					.buttonStyle(.bordered)
				Button("Connect") { connect(serverURL, token) }
					.buttonStyle(.borderedProminent)
					.tint(SymairaTheme.goldPrimary)
					.disabled(ServerConnectionConfig.normalizedURL(serverURL) == nil || token.count < 32)
			}
		}
		.padding(28)
		.frame(width: 590)
		.background(SymairaTheme.bgDark)
	}
}

extension Notification.Name {
    static let onboardingComplete = Notification.Name("symdesk.onboardingComplete")
    static let openDiscover = Notification.Name("symdesk.openDiscover")
}
