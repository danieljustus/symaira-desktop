import Foundation

@testable import SymMeetMCP

/// SymMeetMCP's own MCP server type.
///
/// Aliased so test files can `import SymairaMCP` (which exports its own
/// `MCPServer`) without an ambiguous unqualified lookup, and without
/// qualifying through the module name — the module and the `SymMeetMCP`
/// enum share a name, which would otherwise resolve to the enum.
typealias MeetMCPServer = MCPServer
