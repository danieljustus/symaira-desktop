import SwiftUI

struct ProfilesView: View {
    @State private var profiles: [CliProfile] = []
    @State private var isLoading = true
    @State private var selectedProfile: CliProfile?
    
    var body: some View {
        HStack(spacing: 0) {
            // Main List
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    Text("Built-in Profiles")
                        .font(.title2.bold())
                        .foregroundStyle(Theme.textPrimary)
                    
                    Text("Profiles configure visual guarantees, DIN geometry rules, and compliance standards.")
                        .font(.subheadline)
                        .foregroundStyle(Theme.textSecondary)
                        .padding(.bottom, 8)
                    
                    if isLoading {
                        HStack {
                            Spacer()
                            ProgressView("Fetching profiles…")
                                .foregroundStyle(Theme.textSecondary)
                            Spacer()
                        }
                        .padding(.top, 40)
                    } else if profiles.isEmpty {
                        ContentUnavailableView("No profiles found", systemImage: "square.stack.3d.up.fill")
                    } else {
                        LazyVGrid(columns: [GridItem(.adaptive(minimum: 240, maximum: 360))], spacing: 16) {
                            ForEach(profiles) { profile in
                                ProfileCard(profile: profile, isSelected: selectedProfile?.id == profile.id) {
                                    selectedProfile = profile
                                }
                            }
                        }
                    }
                }
                .padding(24)
            }
            .frame(maxWidth: .infinity)
            
            // Detail Panel
            if let profile = selectedProfile {
                Divider()
                    .background(Theme.borderGlass)
                
                ProfileDetailPanel(profile: profile) {
                    selectedProfile = nil
                }
                .frame(width: 320)
                .background(Theme.bgDark.opacity(0.35))
                .transition(.move(edge: .trailing))
            }
        }
        .task {
            profiles = await CliManager.shared.getProfiles()
            isLoading = false
            if selectedProfile == nil, let first = profiles.first {
                selectedProfile = first
            }
        }
    }
}

struct ProfileCard: View {
    let profile: CliProfile
    let isSelected: Bool
    let action: () -> Void
    
    @State private var isHovered = false
    
    var body: some View {
        Button(action: action) {
            VStack(alignment: .leading, spacing: 12) {
                HStack {
                    Text(profile.title)
                        .font(.headline)
                        .foregroundStyle(Theme.textPrimary)
                        .lineLimit(1)
                    Spacer()
                    StabilityBadge(stability: profile.stability)
                }
                
                Text(profile.description)
                    .font(.caption)
                    .foregroundStyle(Theme.textSecondary)
                    .lineLimit(3)
                    .multilineTextAlignment(.leading)
                
                Divider()
                    .background(Theme.borderGlass)
                
                HStack {
                    Label(profile.engine, systemImage: "gearshape")
                        .font(.caption2.monospaced())
                        .foregroundStyle(Theme.textMuted)
                    Spacer()
                    if let standard = profile.pdfStandard, !standard.isEmpty {
                        Text(standard.joined(separator: "+").uppercased())
                            .font(.system(size: 9, weight: .bold))
                            .padding(.horizontal, 6)
                            .padding(.vertical, 2)
                            .background(Theme.goldPrimary.opacity(0.15))
                            .foregroundStyle(Theme.goldPrimary)
                            .cornerRadius(4)
                    } else {
                        Text("Tagged PDF")
                            .font(.system(size: 9, weight: .semibold))
                            .foregroundStyle(Theme.textMuted)
                    }
                }
            }
            .padding(16)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .glassCard(isHovered: isHovered || isSelected)
        .overlay(
            RoundedRectangle(cornerRadius: 12)
                .stroke(isSelected ? Theme.goldPrimary.opacity(0.6) : Color.clear, lineWidth: 1.5)
        )
        .onHover { h in isHovered = h }
    }
}

struct StabilityBadge: View {
    let stability: String
    
    var body: some View {
        Text(stability.uppercased())
            .font(.system(size: 8, weight: .bold))
            .padding(.horizontal, 6)
            .padding(.vertical, 2)
            .background(badgeBg)
            .foregroundStyle(badgeFg)
            .cornerRadius(4)
    }
    
    private var badgeBg: Color {
        switch stability {
        case "stable": return Color.green.opacity(0.12)
        case "beta": return Color.orange.opacity(0.12)
        default: return Color.blue.opacity(0.12)
        }
    }
    
    private var badgeFg: Color {
        switch stability {
        case "stable": return .green
        case "beta": return .orange
        default: return .blue
        }
    }
}

struct ProfileDetailPanel: View {
    let profile: CliProfile
    let onClose: () -> Void
    
    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 20) {
                HStack {
                    Text(profile.name)
                        .font(.title3.bold().monospaced())
                        .foregroundStyle(Theme.goldPrimary)
                    Spacer()
                    Button {
                        withAnimation { onClose() }
                    } label: {
                        Image(systemName: "xmark.circle.fill")
                            .foregroundStyle(Theme.textMuted)
                            .font(.title3)
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Close profile details")
                }
                
                Text(profile.description)
                    .font(.body)
                    .foregroundStyle(Theme.textSecondary)
                
                Divider()
                    .background(Theme.borderGlass)
                
                VStack(alignment: .leading, spacing: 14) {
                    DetailRow(label: "Title", value: profile.title)
                    DetailRow(label: "Template File", value: profile.template, isCode: true)
                    DetailRow(label: "Engine", value: profile.engine)
                    DetailRow(label: "Form Style", value: profile.form ?? "—")
                    DetailRow(label: "Reproducible", value: profile.reproducible ? "Yes" : "No")
                    
                    if let standard = profile.pdfStandard, !standard.isEmpty {
                        DetailRow(label: "PDF Standard", value: standard.joined(separator: ", "))
                    }
                    
                    if let req = profile.requiredFields, !req.isEmpty {
                        VStack(alignment: .leading, spacing: 6) {
                            Text("Required Frontmatter Fields")
                                .font(.caption.weight(.bold))
                                .foregroundStyle(Theme.textSecondary)
                            
                            FlowLayout(spacing: 6) {
                                ForEach(req, id: \.self) { field in
                                    Text(field)
                                        .font(.caption2.monospaced())
                                        .padding(.horizontal, 8)
                                        .padding(.vertical, 3)
                                        .background(Color.white.opacity(0.06))
                                        .foregroundStyle(Theme.textPrimary)
                                        .cornerRadius(6)
                                }
                            }
                        }
                    }
                }
            }
            .padding(20)
        }
    }
}

struct DetailRow: View {
    let label: String
    let value: String
    var isCode: Bool = false
    
    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(label)
                .font(.caption.weight(.semibold))
                .foregroundStyle(Theme.textMuted)
            Text(value)
                .font(isCode ? .system(.subheadline, design: .monospaced) : .subheadline)
                .foregroundStyle(Theme.textPrimary)
        }
    }
}

struct FlowLayout: Layout {
    var spacing: CGFloat
    
    func sizeThatFits(proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) -> CGSize {
        let width = proposal.width ?? .infinity
        var currentX: CGFloat = 0
        var currentY: CGFloat = 0
        var maxRowHeight: CGFloat = 0
        var maxX: CGFloat = 0
        
        for view in subviews {
            let size = view.sizeThatFits(.unspecified)
            if currentX + size.width > width {
                currentX = 0
                currentY += maxRowHeight + spacing
                maxRowHeight = 0
            }
            maxRowHeight = max(maxRowHeight, size.height)
            currentX += size.width + spacing
            maxX = max(maxX, currentX)
        }
        return CGSize(width: maxX, height: currentY + maxRowHeight)
    }
    
    func placeSubviews(in bounds: CGRect, proposal: ProposedViewSize, subviews: Subviews, cache: inout ()) {
        var currentX: CGFloat = bounds.minX
        var currentY: CGFloat = bounds.minY
        var maxRowHeight: CGFloat = 0
        
        for view in subviews {
            let size = view.sizeThatFits(.unspecified)
            if currentX + size.width > bounds.maxX {
                currentX = bounds.minX
                currentY += maxRowHeight + spacing
                maxRowHeight = 0
            }
            view.place(at: CGPoint(x: currentX, y: currentY), proposal: ProposedViewSize(size))
            maxRowHeight = max(maxRowHeight, size.height)
            currentX += size.width + spacing
        }
    }
}
