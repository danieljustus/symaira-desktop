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
            VStack(alignment: .leading, spacing: 20) {
                header

                if ModelCatalog.all.isEmpty {
                    emptyState
                } else {
                    ForEach(ModelCatalog.all) { model in
                        modelRow(model)
                    }
                    Divider().overlay(SymairaTheme.borderGlass)
                    storageNote
                }
            }
            .padding(20)
        }
        .background(SymairaTheme.bgDark)
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
        .alert("Model Download", isPresented: Binding(
            get: { pendingError != nil },
            set: { if !$0 { pendingError = nil } }
        )) {
            Button("OK", role: .cancel) {}
        } message: {
            Text(pendingError ?? "")
        }
    }

    private var header: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 8) {
                Image(systemName: "shippingbox")
                    .foregroundStyle(SymairaTheme.goldPrimary)
                Text("Local Models")
                    .symairaText(.title).bold()
                    .foregroundStyle(SymairaTheme.textPrimary)
            }
            Text("On-device models are downloaded on demand into your Application Support folder — never into the app bundle, so the app stays signature-valid.")
                .symairaText(.subheading)
                .foregroundStyle(SymairaTheme.textSecondary)
        }
    }

    private var emptyState: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text("No models available yet")
                .symairaText(.subheading)
                .foregroundStyle(SymairaTheme.textPrimary)
            Text("The download mechanism is ready — pinned revision, checksum verification, license confirmation, progress, cancel/resume and removal. Concrete models will be published here as their selection issues land.")
                .symairaText(.subheading)
                .foregroundStyle(SymairaTheme.textSecondary)
            Text("Models are stored in: \(manager.modelsDirectory.path)")
                .symairaText(.caption)
                .foregroundStyle(SymairaTheme.textSecondary)
                .textSelection(.enabled)
        }
        .padding(16)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(RoundedRectangle(cornerRadius: 10).fill(SymairaTheme.bgCard))
    }

    private var storageNote: some View {
        Text("Model storage: \(manager.modelsDirectory.path) · Downloads never modify the app bundle.")
            .symairaText(.caption)
            .foregroundStyle(SymairaTheme.textSecondary)
            .textSelection(.enabled)
    }

    private func modelRow(_ model: ModelDescriptor) -> some View {
        let state = manager.state(for: model)
        return VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .firstTextBaseline) {
                Text(model.displayName)
                    .symairaText(.subheading)
                    .foregroundStyle(SymairaTheme.textPrimary)
                Spacer()
                Text(ByteCountFormatter.string(fromByteCount: model.sizeBytes, countStyle: .file))
                    .symairaText(.caption)
                    .foregroundStyle(SymairaTheme.textSecondary)
            }
            HStack(spacing: 6) {
                Text(model.licenseName)
                Link("License", destination: model.licenseURL)
                    .symairaText(.subheading)
                Spacer()
                stateControls(model, state: state)
            }
            if case .downloading(let progress) = state {
                ProgressView(value: progress)
                    .tint(SymairaTheme.goldPrimary)
            }
            if case .failed(let message) = state {
                Text(message)
                    .symairaText(.caption)
                    .foregroundStyle(.red)
            }
        }
        .padding(14)
        .background(RoundedRectangle(cornerRadius: 10).fill(SymairaTheme.bgCard))
    }

    @ViewBuilder
    private func stateControls(_ model: ModelDescriptor, state: ModelInstallState) -> some View {
        switch state {
        case .notInstalled:
            Button("Download") { pendingModel = model }
                .buttonStyle(.borderedProminent)
                .tint(SymairaTheme.goldPrimary)
        case .downloading:
            Button("Cancel") { manager.cancel(model) }
                .buttonStyle(.bordered)
        case .paused:
            Button("Resume") {
                try? manager.install(model, licenseAccepted: true)
            }
            .buttonStyle(.borderedProminent)
            .tint(SymairaTheme.goldPrimary)
            Button("Remove") { pendingRemoval = model }
                .buttonStyle(.bordered)
        case .installed:
            Button("Remove") { pendingRemoval = model }
                .buttonStyle(.bordered)
        case .failed:
            Button("Retry") { pendingModel = model }
                .buttonStyle(.borderedProminent)
                .tint(SymairaTheme.goldPrimary)
            Button("Remove") { pendingRemoval = model }
                .buttonStyle(.bordered)
        }
    }

    private func licenseSheet(_ model: ModelDescriptor) -> some View {
        VStack(alignment: .leading, spacing: 16) {
            Text("License required")
                .symairaText(.subheading)
                .foregroundStyle(SymairaTheme.textPrimary)
            Text("Before downloading \(model.displayName) (\(model.licenseName)), please review the model's license.")
                .symairaText(.subheading)
                .foregroundStyle(SymairaTheme.textSecondary)
            Link("Open license: \(model.licenseName)", destination: model.licenseURL)
                .symairaText(.subheading)
            HStack {
                Button("Cancel") { pendingModel = nil }
                    .buttonStyle(.bordered)
                Spacer()
                Button("Accept & Download") {
                    let descriptor = model
                    pendingModel = nil
                    do {
                        try manager.install(descriptor, licenseAccepted: true)
                    } catch {
                        pendingError = (error as? ModelInstallError)?.errorDescription ?? error.localizedDescription
                    }
                }
                .buttonStyle(.borderedProminent)
                .tint(SymairaTheme.goldPrimary)
            }
        }
        .padding(20)
        .frame(width: 420)
        .background(SymairaTheme.bgCard)
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
