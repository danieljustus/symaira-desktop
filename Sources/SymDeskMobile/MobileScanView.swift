import SwiftUI
import VisionKit

/// Scan surface: capture via the system document camera (multi-page),
/// review thumbnails with reorder/rescan, then submit as one PDF through
/// the write layer — queued offline, uploaded in server mode, written into
/// the consume folder in Files mode. Per-scan state is visible through the
/// outbox banner and this screen's own status line.
struct MobileScanView: View {
    @EnvironmentObject private var vault: MobileVaultStore
    @Environment(\.dismiss) private var dismiss

    @State private var pages: [UIImage] = []
    @State private var isScanning = false
    @State private var isSubmitting = false
    @State private var outputFormat: ScanFormat = .pdf
    @State private var statusMessage: String?
    @State private var errorMessage: String?

    private enum ScanFormat: String, CaseIterable, Identifiable {
        case pdf = "PDF (recommended)"
        case jpeg = "Photo (JPEG)"
        var id: String { rawValue }
    }

    private let scanner = MobileDocumentScanner()

    var body: some View {
        NavigationStack {
            MobileBackdrop {
                VStack(spacing: 0) {
                    if pages.isEmpty {
                        emptyState
                    } else {
                        reviewGrid
                    }

                    if let errorMessage {
                        Label(errorMessage, systemImage: "exclamationmark.triangle.fill")
                            .font(.caption)
                            .foregroundStyle(.red)
                            .padding(.horizontal, 16)
                            .padding(.vertical, 6)
                    }
                    if let statusMessage {
                        Label(statusMessage, systemImage: "checkmark.circle.fill")
                            .font(.caption)
                            .foregroundStyle(MobileTheme.goldSoft)
                            .padding(.horizontal, 16)
                            .padding(.vertical, 6)
                    }
                }
            }
            .navigationTitle("Scan document")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) {
                    Button("Cancel") { dismiss() }
                }
                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        isScanning = true
                        presentScanner()
                    } label: {
                        Image(systemName: "camera")
                    }
                    .accessibilityLabel("Scan another page")
                }
            }
            .sheet(isPresented: $isScanning) {
                ScannerHost(scanner: scanner)
                    .ignoresSafeArea()
            }
            .task {
                scanner.onComplete = { captured in
                    Task { @MainActor in
                        pages.append(contentsOf: captured)
                        isScanning = false
                        statusMessage = nil
                        errorMessage = nil
                    }
                }
                scanner.onCancel = {
                    Task { @MainActor in isScanning = false }
                }
                isScanning = true
                presentScanner()
            }
        }
    }

    private var emptyState: some View {
        VStack(spacing: 16) {
            Image(systemName: "doc.viewfinder")
                .font(.system(size: 44))
                .foregroundStyle(MobileTheme.gold)
            Text("Scan a paper document")
                .font(.title3.bold())
                .foregroundStyle(MobileTheme.textPrimary)
            Text("Multi-page scans become one PDF handed to the ingest pipeline — OCR, classification and archiving run on the server or desktop.")
                .font(.subheadline)
                .foregroundStyle(MobileTheme.textSecondary)
                .multilineTextAlignment(.center)
                .padding(.horizontal, 32)
        }
        .frame(maxWidth: .infinity, maxHeight: .infinity)
    }

    private var reviewGrid: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 14) {
                Text("\(pages.count) page\(pages.count == 1 ? "" : "s")")
                    .font(.headline)
                    .foregroundStyle(MobileTheme.textPrimary)

                LazyVGrid(columns: [GridItem(.adaptive(minimum: 100), spacing: 10)], spacing: 10) {
                    ForEach(Array(pages.enumerated()), id: \.offset) { index, page in
                        ThumbnailCell(
                            index: index,
                            image: page,
                            canMoveLeft: index > 0,
                            canMoveRight: index < pages.count - 1,
                            onMoveLeft: { movePage(from: index, to: index - 1) },
                            onMoveRight: { movePage(from: index, to: index + 1) },
                            onRescan: { rescanPage(at: index) }
                        )
                    }
                }

                HStack {
                    Picker("Format", selection: $outputFormat) {
                        ForEach(ScanFormat.allCases) { format in
                            Text(format.rawValue).tag(format)
                        }
                    }
                    .pickerStyle(.menu)
                    .labelsHidden()

                    Spacer()

                    Button {
                        Task { await submit() }
                    } label: {
                        if isSubmitting {
                            ProgressView().frame(maxWidth: .infinity)
                        } else {
                            Label("Submit", systemImage: "arrow.up.circle.fill")
                                .frame(maxWidth: .infinity)
                        }
                    }
                    .buttonStyle(.borderedProminent)
                    .tint(MobileTheme.gold)
                    .foregroundStyle(.black)
                    .disabled(isSubmitting)
                }
                .padding(.top, 6)
            }
            .padding(16)
            .frame(maxWidth: 680)
            .frame(maxWidth: .infinity)
        }
    }

    private func presentScanner() {
        guard VNDocumentCameraViewController.isSupported else {
            errorMessage = "Document scanning is not available on this device."
            isScanning = false
            return
        }
    }

    private func movePage(from: Int, to: Int) {
        guard from >= 0, to >= 0, from < pages.count, to < pages.count else { return }
        pages.swapAt(from, to)
    }

    private func rescanPage(at index: Int) {
        // Simplest honest flow: capture a replacement page, then swap it
        // into the same slot. Reorder is supported via the move buttons.
        scanner.onComplete = { captured in
            Task { @MainActor in
                if let replacement = captured.first {
                    pages[index] = replacement
                }
                isScanning = false
            }
        }
        scanner.onCancel = {
            Task { @MainActor in isScanning = false }
        }
        isScanning = true
        presentScanner()
    }

    private func submit() async {
        guard let first = pages.first else { return }
        isSubmitting = true
        errorMessage = nil
        statusMessage = nil
        do {
            let data: Data
            let filename: String
            let consumeFolder: String?
            switch outputFormat {
            case .pdf:
                guard let pdf = MobileScanPDFBuilder.pdf(from: pages) else {
                    throw MobileWriteError.invalidContent(reason: "could not render the scan to PDF")
                }
                data = pdf
                filename = "scan-\(Self.timestamp()).pdf"
                consumeFolder = nil
            case .jpeg:
                guard let jpeg = MobileScanPDFBuilder.jpeg(from: first) else {
                    throw MobileWriteError.invalidContent(reason: "could not render the scan to JPEG")
                }
                data = jpeg
                filename = "scan-\(Self.timestamp()).jpg"
                consumeFolder = nil
            }
            try await vault.enqueueUpload(data: data, filename: filename, consumeFolder: consumeFolder)
            statusMessage = "Queued — will upload when connected."
            pages = []
            try? await Task.sleep(for: .milliseconds(1200))
            dismiss()
        } catch {
            errorMessage = error.localizedDescription
            isSubmitting = false
        }
    }

    private static func timestamp() -> String {
        let formatter = DateFormatter()
        formatter.dateFormat = "yyyyMMdd-HHmmss"
        return formatter.string(from: Date())
    }
}

/// Bridges the UIKit document camera into SwiftUI.
private struct ScannerHost: UIViewControllerRepresentable {
    let scanner: MobileDocumentScanner

    func makeUIViewController(context: Context) -> VNDocumentCameraViewController {
        scanner.makeViewController()
    }

    func updateUIViewController(_ controller: VNDocumentCameraViewController, context: Context) {}
}

private struct ThumbnailCell: View {
    let index: Int
    let image: UIImage
    let canMoveLeft: Bool
    let canMoveRight: Bool
    let onMoveLeft: () -> Void
    let onMoveRight: () -> Void
    let onRescan: () -> Void

    var body: some View {
        VStack(spacing: 6) {
            Image(uiImage: image)
                .resizable()
                .scaledToFill()
                .frame(height: 120)
                .clipShape(RoundedRectangle(cornerRadius: 12, style: .continuous))
                .overlay(alignment: .topLeading) {
                    Text("\(index + 1)")
                        .font(.caption2.bold())
                        .foregroundStyle(.black)
                        .padding(5)
                        .background(MobileTheme.gold, in: Circle())
                        .padding(5)
                }
            HStack(spacing: 10) {
                Button(action: onMoveLeft) {
                    Image(systemName: "arrow.left")
                }
                .disabled(!canMoveLeft)
                Button(action: onMoveRight) {
                    Image(systemName: "arrow.right")
                }
                .disabled(!canMoveRight)
                Button("Rescan", action: onRescan)
                    .font(.caption)
            }
            .font(.caption)
            .buttonStyle(.borderless)
        }
    }
}
