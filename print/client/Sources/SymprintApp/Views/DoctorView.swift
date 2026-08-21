import SwiftUI

struct DoctorView: View {
    @State private var result: DoctorResult?
    @State private var isLoading = true
    
    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 20) {
                HStack {
                    VStack(alignment: .leading, spacing: 4) {
                        Text("Environment Diagnostics")
                            .font(.title2.bold())
                            .foregroundStyle(Theme.textPrimary)
                        Text("Health status of the rendering engine and document validation tools.")
                            .font(.subheadline)
                            .foregroundStyle(Theme.textSecondary)
                    }
                    Spacer()
                    Button {
                        Task { await checkHealth() }
                    } label: {
                        Label("Run Diagnostics", systemImage: "arrow.clockwise")
                    }
                    .buttonStyle(.bordered)
                    .controlSize(.regular)
                }
                .padding(.bottom, 8)
                
                if isLoading {
                    HStack {
                        Spacer()
                        ProgressView("Analyzing environment…")
                            .foregroundStyle(Theme.textSecondary)
                        Spacer()
                    }
                    .padding(.top, 40)
                } else if let result {
                    VStack(spacing: 16) {
                        // Typst
                        DoctorToolCard(
                            title: "Typst",
                            subtitle: "Main typesetting engine (required)",
                            info: result.typst,
                            installHint: "brew install typst"
                        )
                        
                        // Pandoc
                        DoctorToolCard(
                            title: "Pandoc",
                            subtitle: "High-fidelity Markdown path (optional)",
                            info: result.pandoc,
                            installHint: "brew install pandoc"
                        )
                        
                        // VeraPDF
                        DoctorToolCard(
                            title: "VeraPDF",
                            subtitle: "PDF/A + PDF/UA compliance checker (optional)",
                            info: result.verapdf,
                            installHint: "brew install verapdf"
                        )
                    }
                } else {
                    ContentUnavailableView("Failed to run doctor", systemImage: "exclamationmark.triangle")
                }
            }
            .padding(24)
        }
        .task {
            await checkHealth()
        }
    }
    
    private func checkHealth() async {
        isLoading = true
        result = await CliManager.shared.getDoctor()
        isLoading = false
    }
}

struct DoctorToolCard: View {
    let title: String
    let subtitle: String
    let info: EngineInfo
    let installHint: String
    
    @State private var isHovered = false
    
    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                VStack(alignment: .leading, spacing: 2) {
                    Text(title)
                        .font(.headline)
                        .foregroundStyle(Theme.textPrimary)
                    Text(subtitle)
                        .font(.caption)
                        .foregroundStyle(Theme.textSecondary)
                }
                Spacer()
                
                HStack(spacing: 8) {
                    if info.available {
                        Image(systemName: "checkmark.circle.fill")
                            .foregroundStyle(.green)
                            .font(.title2)
                        Text("Available")
                            .font(.subheadline.weight(.semibold))
                            .foregroundStyle(.green)
                    } else {
                        Image(systemName: "xmark.circle.fill")
                            .foregroundStyle(.red)
                            .font(.title2)
                        Text("Missing")
                            .font(.subheadline.weight(.semibold))
                            .foregroundStyle(.red)
                    }
                }
            }
            
            if info.available {
                Divider()
                    .background(Theme.borderGlass)
                VStack(alignment: .leading, spacing: 6) {
                    if let version = info.version {
                        LabeledInfo(label: "Version", value: version)
                    }
                    if let path = info.path {
                        LabeledInfo(label: "Path", value: path, isCode: true)
                    }
                }
            } else {
                Divider()
                    .background(Theme.borderGlass)
                
                VStack(alignment: .leading, spacing: 8) {
                    if let hint = info.hint, !hint.isEmpty {
                        Text(hint)
                            .font(.caption)
                            .foregroundStyle(Theme.textSecondary)
                    }
                    
                    HStack {
                        Text("Install:")
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(Theme.textMuted)
                        
                        Text(installHint)
                            .font(.system(.caption, design: .monospaced))
                            .padding(.horizontal, 6)
                            .padding(.vertical, 2)
                            .background(Color.white.opacity(0.06))
                            .cornerRadius(4)
                            .foregroundStyle(Theme.goldPrimary)
                        
                        Spacer()
                        
                        Button {
                            let pb = NSPasteboard.general
                            pb.clearContents()
                            pb.setString(installHint, forType: .string)
                        } label: {
                            Image(systemName: "doc.on.doc")
                                .foregroundStyle(Theme.textSecondary)
                        }
                        .buttonStyle(.plain)
                        .accessibilityLabel("Copy install command")
                    }
                }
            }
        }
        .padding(16)
        .glassCard(isHovered: isHovered)
        .onHover { h in isHovered = h }
    }
}

struct LabeledInfo: View {
    let label: String
    let value: String
    var isCode: Bool = false
    
    var body: some View {
        HStack(alignment: .top) {
            Text(label + ":")
                .font(.caption.weight(.semibold))
                .foregroundStyle(Theme.textMuted)
                .frame(width: 80, alignment: .leading)
            
            Text(value)
                .font(isCode ? .system(.caption, design: .monospaced) : .caption)
                .foregroundStyle(Theme.textPrimary)
                .lineLimit(1)
                .truncationMode(.middle)
        }
    }
}
