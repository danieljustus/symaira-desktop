import SwiftUI
import SymairaTheme
import SymDeskCore

/// Workspace sidebar (GUIDELINES SHELL-3, SHELL-6): the vault as product
/// header, the seven destinations, and one compact vault-health status line.
/// Notes, tags and saved views moved into their destinations' pages.
struct ContentSidebarView: View {
    @ObservedObject var model: ContentViewModel
    let showsLabels: Bool
    @EnvironmentObject var core: DeskCore
    @EnvironmentObject var watcher: EventWatcher

    var body: some View {
        VStack(alignment: showsLabels ? .leading : .center, spacing: SymairaSpacing.large) {
            header

            ScrollView {
                VStack(spacing: SymairaSpacing.xSmall) {
                    ForEach(DeskDestination.allCases) { item in
                        SymairaNavigationButton(
                            item.title, icon: item.icon,
                            isSelected: model.destination == item, showsLabel: showsLabels
                        ) {
                            model.select(item)
                        }
                        .accessibilityIdentifier("symdesk-nav-\(item.id)")
                    }
                }
                .padding(3)
            }

            Divider().overlay(SymairaTokens.line)
            statusLine
        }
        .padding(SymairaSpacing.medium)
        .frame(maxHeight: .infinity)
    }

    /// Product identity: the active vault, switchable in place.
    @ViewBuilder
    private var header: some View {
        if showsLabels {
            VaultSwitcherView()
                .frame(maxWidth: .infinity, alignment: .leading)
        } else {
            SymairaIconView(.productDesk)
                .foregroundStyle(SymairaTokens.ink2)
                .frame(width: 32, height: 36)
                .help(core.vaultPath.map { ($0 as NSString).lastPathComponent } ?? "SymDesk")
                .accessibilityLabel("SymDesk")
        }
    }

    private var statusTone: SymairaTone {
        guard let report = model.doctorReport else { return .warning }
        return report.overall == "ok" ? .positive : .warning
    }

    private var statusHelp: String {
        guard let ev = watcher.latestEvent else { return model.doctorSummaryText }
        return model.doctorSummaryText
            + "\nLast event: \(ev.event) on \(model.vaultRelativePath(ev.path, vaultPath: core.vaultPath))"
    }

    /// The single status line (SHELL-6, STATE-4): vault health from
    /// `symdesk doctor`; a click shows the full report.
    private var statusLine: some View {
        Button {
            model.isShowingDoctorPopover.toggle()
        } label: {
            if showsLabels {
                SymairaStatusLabel(model.doctorSummaryText, tone: statusTone)
                    .lineLimit(2)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.horizontal, SymairaSpacing.small)
            } else {
                SymairaStatusDot(tone: statusTone, accessibilityLabel: model.doctorSummaryText)
            }
        }
        .buttonStyle(.plain)
        .help(statusHelp)
        .accessibilityLabel(model.doctorSummaryText)
        .accessibilityHint("Shows the vault health report")
        .accessibilityIdentifier("symdesk-vault-status")
        .popover(isPresented: $model.isShowingDoctorPopover) {
            DoctorReportPopoverView(report: model.doctorReport)
        }
    }
}

/// The note tree beside the editor on the Notes page (formerly in the
/// sidebar). Folder expansion persists under `sidebarExpandedFolders`.
struct NoteTreeColumn: View {
    @ObservedObject var model: ContentViewModel

    var body: some View {
        Group {
            if model.folderTree.isEmpty {
                SymairaEmptyState(
                    icon: .note,
                    title: "No Notes",
                    message: "Create a note or drop Markdown files into the vault folder."
                )
                .padding(SymairaSpacing.medium)
                .frame(maxHeight: .infinity, alignment: .top)
            } else {
                List {
                    ForEach(model.folderTree) { node in
                        treeNode(node)
                    }
                }
                .listStyle(.sidebar)
                .scrollContentBackground(.hidden)
                .buttonStyle(.plain)
            }
        }
        .background(SymairaTokens.surface)
        .accessibilityIdentifier("symdesk-note-tree")
    }

    /// Recursively renders a folder tree node (folder or note leaf).
    @ViewBuilder
    private func treeNode(_ node: FolderNode) -> some View {
        if node.isFolder {
            DisclosureGroup(
                isExpanded: Binding(
                    get: { model.expandedFolders.contains(node.id) },
                    set: { isExpanded in
                        if isExpanded {
                            model.expandedFolders.insert(node.id)
                        } else {
                            model.expandedFolders.remove(node.id)
                        }
                    }
                ),
                content: {
                    ForEach(node.children) { child in
                        AnyView(treeNode(child))
                    }
                },
                label: {
                    Label {
                        Text(node.name).symairaText(.body)
                    } icon: {
                        SymairaIconView(.folder, size: 16)
                            .foregroundStyle(SymairaTokens.ink2)
                    }
                }
            )
        } else {
            let isSelected = node.note.map { $0.path == model.selectedNote?.path } ?? false
            Button {
                if let note = node.note {
                    model.navigate(to: .vault, note: note)
                }
            } label: {
                VStack(alignment: .leading, spacing: 1) {
                    Text(node.name)
                        .symairaText(isSelected ? .bodyEmphasized : .body)
                        .lineLimit(1)
                        .truncationMode(.tail)
                    if let folder = node.containingFolder {
                        Text(folder)
                            .symairaText(.caption)
                            .lineLimit(1)
                            .truncationMode(.tail)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .contentShape(Rectangle())
            }
            .accessibilityAddTraits(isSelected ? .isSelected : [])
            .listRowBackground(
                RoundedRectangle(cornerRadius: SymairaRadius.control, style: .continuous)
                    .fill(isSelected ? SymairaTokens.selection : Color.clear)
                    .padding(.horizontal, SymairaSpacing.xSmall)
            )
            .contextMenu {
                if let note = node.note {
                    Button {
                        model.historyInitialNotePath = note.path
                        model.navigate(to: .history)
                    } label: {
                        Label { Text("Show Version History") } icon: { Image(symairaIcon: .history, size: 16) }
                    }
                    Divider()
                    Button(role: .destructive) {
                        model.pendingTrashNote = note
                    } label: {
                        Label { Text("Move to Trash") } icon: { Image(symairaIcon: .delete, size: 16) }
                    }
                }
            }
        }
    }
}
