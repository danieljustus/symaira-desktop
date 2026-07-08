import Foundation
import SymairaToolKit

public struct VaultEvent: Codable, Equatable {
    public let event: String
    public let path: String
    public let ts: Int64
}

@MainActor
public final class EventWatcher: ObservableObject {
    public static let shared = EventWatcher()
    
    @Published public private(set) var latestEvent: VaultEvent?
    @Published public private(set) var allEvents: [VaultEvent] = []
    
    private var process: Process?
    private var isWatching = false
    
    private init() {}
    
    public func start(tool: DetectedTool, vaultPath: String? = nil) {
        guard !isWatching else { return }
        isWatching = true
        
        let p = Process()
        p.executableURL = tool.location.url
        var args = ["events", "--json"]
        if let vaultPath, !vaultPath.isEmpty {
            args += ["--vault", vaultPath]
        }
        p.arguments = args
        
        let pipe = Pipe()
        p.standardOutput = pipe
        p.standardError = FileHandle.standardError // or pipe to hide
        
        self.process = p
        
        Task {
            do {
                try p.run()
                for try await line in pipe.fileHandleForReading.bytes.lines {
                    if let data = line.data(using: .utf8),
                       let ev = try? JSONDecoder().decode(VaultEvent.self, from: data) {
                        await MainActor.run {
                            self.latestEvent = ev
                            self.allEvents.append(ev)
                        }
                    }
                }
            } catch {
                print("EventWatcher failed: \(error)")
            }
            await MainActor.run {
                self.isWatching = false
            }
        }
    }
    
    public func stop() {
        process?.terminate()
        process = nil
        isWatching = false
    }
}
