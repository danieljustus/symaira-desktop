import SwiftUI

struct ContentView: View {
    enum SidebarItem: String, CaseIterable, Identifiable {
        case render = "workspace"
        case profiles = "profiles"
        case doctor = "doctor"
        case config = "config"
        
        var id: String { rawValue }
        
        var title: String {
            switch self {
            case .render: return "Workspace"
            case .profiles: return "Profiles"
            case .doctor: return "Doctor"
            case .config: return "Configuration"
            }
        }
        
        var icon: String {
            switch self {
            case .render: return "doc.richtext"
            case .profiles: return "square.stack.3d.up.fill"
            case .doctor: return "stethoscope"
            case .config: return "gearshape.2.fill"
            }
        }
    }
    
    @State private var selectedItem: SidebarItem? = .render
    
    var body: some View {
        NavigationSplitView {
            List(SidebarItem.allCases, selection: $selectedItem) { item in
                NavigationLink(value: item) {
                    HStack(spacing: 8) {
                        Image(systemName: item.icon)
                            .foregroundStyle(selectedItem == item ? Theme.goldPrimary : Theme.textSecondary)
                            .font(.system(size: 14, weight: .semibold))
                        Text(item.title)
                            .font(.system(size: 13, weight: .medium))
                            .foregroundStyle(selectedItem == item ? Theme.textPrimary : Theme.textSecondary)
                    }
                    .padding(.vertical, 4)
                    .accessibilityLabel(item.title)
                }
            }
            .listStyle(.sidebar)
            .navigationTitle("symprint")
            .safeAreaInset(edge: .bottom) {
                VStack(alignment: .leading, spacing: 4) {
                    Divider()
                        .background(Theme.borderGlass)
                    HStack {
                        Image(systemName: "bolt.fill")
                            .foregroundStyle(Theme.goldPrimary)
                        Text("Symaira Ecosystem")
                            .font(.caption2)
                            .foregroundStyle(Theme.textMuted)
                        Spacer()
                    }
                    .padding(12)
                }
            }
        } detail: {
            ZStack {
                BlueprintBackground()
                
                Group {
                    if let selectedItem {
                        switch selectedItem {
                        case .render:
                            RenderView()
                        case .profiles:
                            ProfilesView()
                        case .doctor:
                            DoctorView()
                        case .config:
                            ConfigView()
                        }
                    } else {
                        ContentUnavailableView("Select a workspace", systemImage: "doc.text")
                    }
                }
                .transition(.opacity.animation(.easeInOut(duration: 0.15)))
            }
            .navigationTitle(selectedItem?.title ?? "")
        }
        .preferredColorScheme(.dark)
    }
}
