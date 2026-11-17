import SwiftUI
import SymairaTheme
import SymDeskCore

/// Settings surface for on-device model downloads (#348).
///
/// The catalog (`ModelCatalog`) is intentionally empty until the model
/// selection issues land — the UI renders a documented empty state and every
/// interaction path (license gate, progress, cancel, resume, remove) is wired
/// and unit-tested against synthetic descriptors.
struct ModelsView: View {
    @StateObject private var manager = ModelDownloadManager()

    @State private var pendingModel: ModelDescriptor?
    @State private var pendingRemoval: ModelDescriptor?
    @State private var pendingError: String?

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: SymairaSpacing.xLarge) {
                header

                if let pendingError {
                    SymairaNotice(
                        title: "Model Download",
                        message: pendingError,
                        tone: .critical,
                        onDismiss: { self.pendingError = nil }
                    )
                }

                if ModelCatalog.all.isEmpty {
                    emptyState
                } else {
                    ForEach(ModelCatalog.all) { model in
                        modelRow(model)
                    }
                    Divider().overlay(SymairaTokens.line)
                    storageNote
                }
            }
            .padding(SymairaSpacing.xLarge)
        }
        .background(SymairaTokens.canvas)
        .sheet(item: $pendingModel) { model in
            licenseSheet(model)
        }
        .confirmationDialog(
            "Remove model?",
            isPresented: Binding(
                get: { pendingRemoval != nil },
                set: { if !$0 { pendingRemoval = nil } }
            ),
            titleVisibility: .visible
        ) {
            if let model = pendingRemoval {
                Button("Remove \(model.displayName)", role: .destructive) {
                    remove(model)
                }
                Button("Cancel", role: .cancel) {}
            }
        } message: {
            if let model = pendingRemoval {
                Text("The downloaded files for \(model.displayName) will be deleted. The app itself is never modified.")
            }
        }
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: SymairaSpacing.small) {
            HStack(spacing: SymairaSpacing.small) {
                SymairaIconView(.package, size: 16)
                    .foregroundStyle(SymairaTokens.ink2)
                Text("Local Models")
                    .symairaText(.title)
            }
            Text("On-device models are downloaded on demand into your Application Support folder — never into the app bundle, so the app stays signature-valid.")
                .symairaText(.secondary)
        }
    }

    private var emptyState: some View {
        VStack(alignment: .leading, spacing: SymairaSpacing.medium) {
            Text("No models available yet")
                .symairaText(.subheading)
            Text("The download mechanism is ready — pinned revision, checksum verification, license confirmation, progress, cancel/resume and removal. Concrete models will be published here as their selection issues land.")
                .symairaText(.secondary)
            Text("Models are stored in: \(manager.modelsDirectory.path)")
                .symairaText(.caption)
                .textSelection(.enabled)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .symairaCard()
    }

    private var storageNote: some View {
        Text("Model storage: \(manager.modelsDirectory.path) · Downloads never modify the app bundle.")
            .symairaText(.caption)
            .textSelection(.enabled)
    }

    private func modelRow(_ model: ModelDescriptor) -> some View {
        let state = manager.state(for: model)
        return VStack(alignment: .leading, spacing: SymairaSpacing.medium) {
            HStack(alignment: .firstTextBaseline) {
                Text(model.displayName)
                    .symairaText(.subheading)
                Spacer()
                Text(ByteCountFormatter.string(fromByteCount: model.sizeBytes, countStyle: .file))
                    .symairaText(.caption)
                    .monospacedDigit()
            }
            HStack(spacing: SymairaSpacing.small) {
                Text(model.licenseName)
                    .symairaText(.secondary)
                Link("License", destination: model.licenseURL)
                    .symairaText(.callout)
                Spacer()
                stateControls(model, state: state)
            }
            if case .downloading(let progress) = state {
                ProgressView(value: progress)
                    .progressViewStyle(SymairaProgressStyle())
                    .accessibilityLabel("Download progress for \(model.displayName)")
            }
            if case .failed(let message) = state {
                HStack(alignment: .firstTextBaseline, spacing: SymairaSpacing.xSmall) {
                    SymairaIconView(.error, size: 14)
                    Text(message)
                        .symairaText(.caption, respectsForeground: false)
                }
                .foregroundStyle(SymairaTokens.critical)
            }
        }
        .symairaCard()
    }

    @ViewBuilder
    private func stateControls(_ model: ModelDescriptor, state: ModelInstallState) -> some View {
        switch state {
        case .notInstalled:
            Button("Download") { pendingModel = model }
                .symairaButtonStyle(.secondary)
        case .downloading:
            Button("Cancel") { manager.cancel(model) }
                .symairaButtonStyle(.secondary)
        case .paused:
            Button("Resume") {
                try? manager.install(model, licenseAccepted: true)
            }
            .symairaButtonStyle(.secondary)
            Button("Remove") { pendingRemoval = model }
                .symairaButtonStyle(.secondary)
        case .installed:
            Button("Remove") { pendingRemoval = model }
                .symairaButtonStyle(.secondary)
        case .failed:
            Button("Retry") { pendingModel = model }
                .symairaButtonStyle(.secondary)
            Button("Remove") { pendingRemoval = model }
                .symairaButtonStyle(.secondary)
        }
    }

    private func licenseSheet(_ model: ModelDescriptor) -> some View {
        SymairaSheetScaffold(
            title: "License Required",
            dismiss: { pendingModel = nil },
            primaryTitle: "Accept & Download",
            primary: {
                let descriptor = model
                pendingModel = nil
                do {
                    try manager.install(descriptor, licenseAccepted: true)
                } catch {
                    pendingError = (error as? ModelInstallError)?.errorDescription ?? error.localizedDescription
                }
            }
        ) {
            Text("Before downloading \(model.displayName) (\(model.licenseName)), please review the model's license.")
                .symairaText(.secondary)
            Link("Open license: \(model.licenseName)", destination: model.licenseURL)
                .symairaText(.callout)
        }
        .frame(width: 420, height: 260)
    }

    private func remove(_ model: ModelDescriptor) {
        pendingRemoval = nil
        do {
            try manager.remove(model)
        } catch {
            pendingError = (error as? ModelInstallError)?.errorDescription ?? error.localizedDescription
        }
    }
}
