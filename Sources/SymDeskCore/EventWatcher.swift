import Foundation
import SymairaToolKit

public struct VaultEvent: Codable, Equatable, Identifiable {
    public let event: String
    public let path: String
    public let ts: Int64

    public init(event: String, path: String, ts: Int64) {
        self.event = event
        self.path = path
        self.ts = ts
    }

    public var id: String { "\(ts)-\(path)-\(event)" }
}

extension VaultEvent {
    /// The `events --json` stream writes `ts` as an RFC 3339 string (Go
    /// `time.RFC3339`, kept byte-for-byte by the Rust core). Older fixtures
    /// and tests used epoch seconds, so both shapes decode; the RFC 3339
    /// form is converted to epoch seconds. Without this a real event line
    /// failed `JSONDecoder` and was silently dropped by `EventWatcher`.
    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        let event = try container.decode(String.self, forKey: .event)
        let path = try container.decode(String.self, forKey: .path)
        let ts: Int64
        if let seconds = try? container.decode(Int64.self, forKey: .ts) {
            ts = seconds
        } else {
            let text = try container.decode(String.self, forKey: .ts)
            guard let date = ISO8601DateFormatter().date(from: text) else {
                throw DecodingError.dataCorruptedError(
                    forKey: .ts,
                    in: container,
                    debugDescription: "ts is neither epoch seconds nor RFC 3339: \(text)"
                )
            }
            ts = Int64(date.timeIntervalSince1970)
        }
        self.init(event: event, path: path, ts: ts)
    }
}

@MainActor
public final class EventWatcher: ObservableObject {
    public static let shared = EventWatcher()
    
    @Published public private(set) var latestEvent: VaultEvent?
    @Published public private(set) var allEvents: [VaultEvent] = []
    @Published public private(set) var isWatching = false
    
    private var process: Process?
    
    /// The 10 most recent events, newest first.
    public var recentActivity: [VaultEvent] {
        Array(allEvents.suffix(10).reversed())
    }
    
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

    /// Stops watching and drops all collected events. Used when the active
    /// vault changes so no events leak across vaults (issue #296).
    public func reset() {
        stop()
        latestEvent = nil
        allEvents = []
    }
}
