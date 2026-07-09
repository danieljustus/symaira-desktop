import Foundation

public enum UpdateInstallError: Error, Sendable, Equatable {
    case appTranslocated
    case installLocationNotWritable
    case downloadFailed
    case extractionFailed
    case noAppBundleInArchive
    case signatureVerificationFailed
    case teamIdentifierMismatch
    case swapFailed
}

public enum InstallStage: Equatable, Sendable {
    case idle
    case downloading
    case verifying
    case swapping
    case relaunching
    case succeeded
    case failed(UpdateInstallError)
}

/// Filesystem operations the installer needs, injectable so tests never touch the real disk
/// for the move/rollback logic (the archive is still extracted to a real temp directory).
public protocol UpdateFileSystem: Sendable {
    func fileExists(atPath path: String) -> Bool
    func isWritableFile(atPath path: String) -> Bool
    func moveItem(at src: URL, to dst: URL) throws
    func removeItem(at url: URL) throws
}

public struct DefaultUpdateFileSystem: UpdateFileSystem {
    public init() {}

    public func fileExists(atPath path: String) -> Bool {
        FileManager.default.fileExists(atPath: path)
    }

    public func isWritableFile(atPath path: String) -> Bool {
        FileManager.default.isWritableFile(atPath: path)
    }

    public func moveItem(at src: URL, to dst: URL) throws {
        try FileManager.default.moveItem(at: src, to: dst)
    }

    public func removeItem(at url: URL) throws {
        try FileManager.default.removeItem(at: url)
    }
}

/// Runs a subprocess and captures its combined stdout+stderr, injectable so tests can simulate
/// `ditto`/`codesign` outcomes (including side effects like writing a dummy extracted `.app`)
/// without invoking the real tools.
public protocol UpdateProcessRunner: Sendable {
    func run(_ launchPath: String, _ arguments: [String]) throws -> (status: Int32, output: String)
}

public struct DefaultUpdateProcessRunner: UpdateProcessRunner {
    public init() {}

    public func run(_ launchPath: String, _ arguments: [String]) throws -> (status: Int32, output: String) {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: launchPath)
        process.arguments = arguments

        let pipe = Pipe()
        process.standardOutput = pipe
        process.standardError = pipe

        try process.run()
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        process.waitUntilExit()

        let output = String(data: data, encoding: .utf8) ?? ""
        return (process.terminationStatus, output)
    }
}

/// Downloads the release asset to a local temporary file, injectable so tests never hit the network.
public protocol UpdateAssetDownloader: Sendable {
    func download(from url: URL) async throws -> URL
}

public struct URLSessionAssetDownloader: UpdateAssetDownloader {
    public init() {}

    public func download(from url: URL) async throws -> URL {
        let (tempURL, response) = try await URLSession.shared.download(from: url)
        if let http = response as? HTTPURLResponse, !(200..<300).contains(http.statusCode) {
            throw UpdateInstallError.downloadFailed
        }
        return tempURL
    }
}

/// Relaunches the app after a successful swap, injectable so tests don't actually exit the process.
public protocol AppRelauncher: Sendable {
    func relaunch(appURL: URL)
}

public struct ProcessAppRelauncher: AppRelauncher {
    public init() {}

    public func relaunch(appURL: URL) {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/open")
        process.arguments = ["-n", appURL.path]
        try? process.run()
        exit(0)
    }
}

/// Downloads, verifies and atomically swaps in a newer build of the app, with rollback on failure.
///
/// `performInstall` is a `nonisolated static` function so a future headless `symdesk update` path
/// can call it without deadlocking on the main thread; `UpdateInstaller` is the `@MainActor`
/// `ObservableObject` wrapper that surfaces `stage` to SwiftUI.
@MainActor
public final class UpdateInstaller: ObservableObject {
    public static let shared = UpdateInstaller()

    @Published public private(set) var stage: InstallStage = .idle

    private init() {}

    public func install(
        downloadURL: URL,
        installedAppURL: URL,
        expectedTeamIdentifier: String?,
        fileSystem: any UpdateFileSystem = DefaultUpdateFileSystem(),
        processRunner: any UpdateProcessRunner = DefaultUpdateProcessRunner(),
        downloader: any UpdateAssetDownloader = URLSessionAssetDownloader(),
        relauncher: any AppRelauncher = ProcessAppRelauncher()
    ) async {
        do {
            try await Self.performInstall(
                downloadURL: downloadURL,
                installedAppURL: installedAppURL,
                expectedTeamIdentifier: expectedTeamIdentifier,
                fileSystem: fileSystem,
                processRunner: processRunner,
                downloader: downloader,
                relauncher: relauncher,
                progress: { [weak self] newStage in
                    Task { @MainActor in self?.stage = newStage }
                }
            )
        } catch let error as UpdateInstallError {
            stage = .failed(error)
        } catch {
            stage = .failed(.downloadFailed)
        }
    }

    /// The team identifier of the currently running app, or `nil` if unsigned — the team check
    /// no-ops when this is `nil`, so the whole flow works correctly against an unsigned build.
    nonisolated public static func currentTeamIdentifier(
        appURL: URL = Bundle.main.bundleURL,
        processRunner: any UpdateProcessRunner = DefaultUpdateProcessRunner()
    ) -> String? {
        guard let result = try? processRunner.run("/usr/bin/codesign", ["-dvv", appURL.path]) else {
            return nil
        }
        return parseTeamIdentifier(from: result.output)
    }

    nonisolated static func parseTeamIdentifier(from codesignOutput: String) -> String? {
        for line in codesignOutput.split(separator: "\n") {
            guard line.hasPrefix("TeamIdentifier=") else { continue }
            let value = line.dropFirst("TeamIdentifier=".count)
            return value == "not set" ? nil : String(value)
        }
        return nil
    }

    nonisolated static func isTranslocated(appPath: String) -> Bool {
        appPath.contains("/AppTranslocation/")
    }

    nonisolated public static func performInstall(
        downloadURL: URL,
        installedAppURL: URL,
        expectedTeamIdentifier: String?,
        fileSystem: any UpdateFileSystem = DefaultUpdateFileSystem(),
        processRunner: any UpdateProcessRunner = DefaultUpdateProcessRunner(),
        downloader: any UpdateAssetDownloader = URLSessionAssetDownloader(),
        relauncher: any AppRelauncher = ProcessAppRelauncher(),
        progress: @Sendable (InstallStage) -> Void = { _ in }
    ) async throws {
        if isTranslocated(appPath: installedAppURL.path) {
            throw UpdateInstallError.appTranslocated
        }

        let installDir = installedAppURL.deletingLastPathComponent()
        guard fileSystem.isWritableFile(atPath: installDir.path) else {
            throw UpdateInstallError.installLocationNotWritable
        }

        progress(.downloading)
        let zipURL: URL
        do {
            zipURL = try await downloader.download(from: downloadURL)
        } catch {
            throw UpdateInstallError.downloadFailed
        }

        let extractDir = FileManager.default.temporaryDirectory
            .appendingPathComponent("symdesk-update-\(UUID().uuidString)", isDirectory: true)
        try FileManager.default.createDirectory(at: extractDir, withIntermediateDirectories: true)
        defer { try? FileManager.default.removeItem(at: extractDir) }

        let extractResult = try processRunner.run("/usr/bin/ditto", ["-x", "-k", zipURL.path, extractDir.path])
        guard extractResult.status == 0 else {
            throw UpdateInstallError.extractionFailed
        }

        guard let extractedApp = (try? FileManager.default.contentsOfDirectory(
            at: extractDir, includingPropertiesForKeys: nil
        ))?.first(where: { $0.pathExtension == "app" }) else {
            throw UpdateInstallError.noAppBundleInArchive
        }

        progress(.verifying)
        let verifyResult = try processRunner.run(
            "/usr/bin/codesign", ["--verify", "--strict", "--deep", extractedApp.path]
        )
        guard verifyResult.status == 0 else {
            throw UpdateInstallError.signatureVerificationFailed
        }

        if let expectedTeamIdentifier {
            let infoResult = try processRunner.run("/usr/bin/codesign", ["-dvv", extractedApp.path])
            guard parseTeamIdentifier(from: infoResult.output) == expectedTeamIdentifier else {
                throw UpdateInstallError.teamIdentifierMismatch
            }
        }

        progress(.swapping)
        let backupURL = installedAppURL.deletingLastPathComponent()
            .appendingPathComponent(installedAppURL.lastPathComponent + ".backup")
        if fileSystem.fileExists(atPath: backupURL.path) {
            try? fileSystem.removeItem(at: backupURL)
        }
        try fileSystem.moveItem(at: installedAppURL, to: backupURL)
        do {
            try fileSystem.moveItem(at: extractedApp, to: installedAppURL)
        } catch {
            try? fileSystem.moveItem(at: backupURL, to: installedAppURL)
            throw UpdateInstallError.swapFailed
        }
        try? fileSystem.removeItem(at: backupURL)

        progress(.relaunching)
        relauncher.relaunch(appURL: installedAppURL)
        progress(.succeeded)
    }
}
