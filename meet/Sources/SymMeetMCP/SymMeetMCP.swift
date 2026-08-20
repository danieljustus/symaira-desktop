import SymMeetCore

/// A placeholder for the future stdio MCP transport.
///
/// The target deliberately has no capture-framework dependency so it remains a
/// narrow protocol adapter over `SymMeetCore`.
public enum SymMeetMCP {
  public static let protocolSchemaVersion = SymMeetCore.schemaVersion
}
