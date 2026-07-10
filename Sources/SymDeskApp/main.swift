import SwiftUI

// Custom entry point so that CLI flags like --version are handled before
// the SwiftUI app lifecycle initializes NSApplication and opens a window.

let arguments = CommandLine.arguments.dropFirst()

if arguments.contains("--version") {
    let version = Bundle.main.object(forInfoDictionaryKey: "CFBundleShortVersionString") as? String ?? "unknown"
    print("symdesk version \(version)")
    exit(0)
}

SymDeskApp.main()
