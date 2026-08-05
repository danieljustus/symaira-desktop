import UIKit
import Social
import UniformTypeIdentifiers

/// Share Extension: receives a URL / text / image from any app and drops
/// it into the shared App Group inbox. The main app picks the file up on
/// its next launch/foreground and enqueues it through the write layer
/// (server ingest or the vault's consume folder), so sharing works even
/// when the main app is not running.
final class ShareViewController: SLComposeServiceViewController {

    private static let appGroupID = "group.com.symaira.desktop.ios"

    override func isContentValid() -> Bool {
        // The content is valid as soon as there is anything to share; the
        // title field is optional.
        true
    }

    override func didSelectPost() {
        guard let item = extensionContext?.inputItems.first as? NSExtensionItem else {
            finish(with: false)
            return
        }
        let providers = item.attachments ?? []
        guard !providers.isEmpty else {
            finish(with: false)
            return
        }

        // Prefer a URL (links, files, PDFs…), then plain text, then an
        // image. The first resolvable provider wins.
        let urlProvider = providers.first { $0.hasItemConformingToTypeIdentifier(UTType.url.identifier) }
        let textProvider = providers.first { $0.hasItemConformingToTypeIdentifier(UTType.plainText.identifier) }
        let imageProvider = providers.first { $0.hasItemConformingToTypeIdentifier(UTType.image.identifier) }

        if let urlProvider {
            urlProvider.loadItem(forTypeIdentifier: UTType.url.identifier, options: nil) { [weak self] item, _ in
                if let url = item as? URL {
                    self?.saveItem(url: url)
                } else if let data = item as? Data, let url = URL(dataRepresentation: data, relativeTo: nil) {
                    self?.saveItem(url: url)
                } else {
                    self?.finish(with: false)
                }
            }
        } else if let textProvider {
            textProvider.loadItem(forTypeIdentifier: UTType.plainText.identifier, options: nil) { [weak self] item, _ in
                let text = item as? String ?? (item as? NSAttributedString)?.string
                if let text, !text.isEmpty {
                    self?.saveText(text)
                } else {
                    self?.finish(with: false)
                }
            }
        } else if let imageProvider {
            imageProvider.loadItem(forTypeIdentifier: UTType.image.identifier, options: nil) { [weak self] item, _ in
                if let url = item as? URL {
                    self?.saveItem(url: url)
                } else if let image = item as? UIImage {
                    self?.saveImage(image)
                } else if let data = item as? Data {
                    self?.saveData(data, ext: "jpg")
                } else {
                    self?.finish(with: false)
                }
            }
        } else {
            finish(with: false)
        }
    }

    override func configurationItems() -> [Any]! {
        []
    }

    // MARK: - Persistence into the App Group inbox

    private func saveItem(url: URL) {
        // Local files can be copied directly; remote URLs are saved as a
        // small descriptor the app resolves (curl-style download) later.
        if url.isFileURL {
            do {
                let data = try Data(contentsOf: url)
                saveData(data, ext: url.pathExtension.isEmpty ? "bin" : url.pathExtension)
            } catch {
                finish(with: false)
            }
        } else {
            let descriptor = "url: \(url.absoluteString)\n"
            saveData(Data(descriptor.utf8), ext: "url")
        }
    }

    private func saveText(_ text: String) {
        let content = "text: \(text)\n"
        saveData(Data(content.utf8), ext: "txt")
    }

    private func saveImage(_ image: UIImage) {
        guard let data = image.jpegData(compressionQuality: 0.9) else {
            finish(with: false)
            return
        }
        saveData(data, ext: "jpg")
    }

    private func saveData(_ data: Data, ext: String) {
        guard let container = FileManager.default.containerURL(
            forSecurityApplicationGroupIdentifier: Self.appGroupID
        ) else {
            finish(with: false)
            return
        }
        let inbox = container.appendingPathComponent("ShareInbox", isDirectory: true)
        do {
            try FileManager.default.createDirectory(at: inbox, withIntermediateDirectories: true)
            let name = "share-\(Int(Date().timeIntervalSince1970 * 1000)).\(ext)"
            try data.write(to: inbox.appendingPathComponent(name), options: .atomic)
            finish(with: true)
        } catch {
            finish(with: false)
        }
    }

    private func finish(with success: Bool) {
        let message = success
            ? "Saved to SymDesk inbox — it will be filed on the next app launch."
            : "Could not save this item to SymDesk."
        let alert = UIAlertController(title: success ? "Shared" : "Share failed", message: message, preferredStyle: .alert)
        alert.addAction(UIAlertAction(title: "OK", style: .default) { [weak self] _ in
            self?.extensionContext?.completeRequest(returningItems: nil)
        })
        present(alert, animated: true)
    }
}
