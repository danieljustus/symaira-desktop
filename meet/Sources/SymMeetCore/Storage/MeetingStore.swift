import Foundation

/// An actor-backed filesystem store for portable meeting artifacts.
public actor MeetingStore {
  public let layout: ArtifactLayout

  public init(dataRoot: URL = SymMeetPaths().dataDirectory) {
    layout = ArtifactLayout(dataRoot: dataRoot)
  }

  public func create(_ manifest: MeetingManifest) throws {
    try validateManifestPaths(manifest)
    let meetingID = manifest.meetingID.uuidString.lowercased()
    try validateMeetingID(meetingID)
    try prepareDirectory(layout.meetingsDirectory)

    let directory = layout.meetingDirectory(meetingID)
    try requireSafePath(directory)
    guard !FileManager.default.fileExists(atPath: directory.path) else {
      throw StoreError.alreadyExists
    }

    do {
      try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: false)
      try FileManager.default.setAttributes(
        [.posixPermissions: 0o700], ofItemAtPath: directory.path)
      try writeInitialFiles(for: manifest, in: directory)
    } catch {
      try? FileManager.default.removeItem(at: directory)
      throw error is StoreError ? error : StoreError.operationFailed
    }
  }

  public func load(meetingID: String) throws -> MeetingManifest {
    let normalizedID = try normalizedMeetingID(meetingID)
    return try loadManifest(
      normalizedID: normalizedID, directory: layout.meetingDirectory(normalizedID))
  }

  /// Loads a manifest for a meeting currently sitting in local trash. Mirrors
  /// ``load(meetingID:)`` but reads from the trash directory instead of the
  /// active meetings directory -- used by callers (like `symmeet export`)
  /// that need to recognize and clearly report a trashed meeting rather than
  /// surfacing the same generic "missing" error a never-existed meeting would.
  public func loadTrashed(meetingID: String) throws -> MeetingManifest {
    let normalizedID = try normalizedMeetingID(meetingID)
    return try loadManifest(
      normalizedID: normalizedID, directory: layout.trashedMeetingDirectory(normalizedID))
  }

  private func loadManifest(normalizedID: String, directory: URL) throws -> MeetingManifest {
    try requireExistingSafeDirectory(directory)

    let manifestURL = layout.manifestURL(in: directory)
    try requireSafePath(manifestURL)
    guard FileManager.default.fileExists(atPath: manifestURL.path) else {
      throw StoreError.malformedArtifact
    }

    do {
      let manifest = try ContractCodec.decoder().decode(
        MeetingManifest.self, from: Data(contentsOf: manifestURL))
      guard manifest.meetingID.uuidString.lowercased() == normalizedID else {
        throw StoreError.malformedArtifact
      }
      try validateManifestPaths(manifest)
      return manifest
    } catch {
      throw error is StoreError ? error : StoreError.malformedArtifact
    }
  }

  public func update(_ manifest: MeetingManifest) throws {
    try validateManifestPaths(manifest)
    let meetingID = manifest.meetingID.uuidString.lowercased()
    let directory = layout.meetingDirectory(meetingID)
    try requireExistingSafeDirectory(directory)

    do {
      let data = try ContractCodec.encoder(prettyPrinted: true).encode(manifest)
      try AtomicFileWriter.write(data, to: layout.manifestURL(in: directory))
    } catch {
      throw error is StoreError ? error : StoreError.operationFailed
    }
  }

  public func list() throws -> MeetingList {
    try prepareDirectory(layout.meetingsDirectory)
    let entries: [URL]

    do {
      entries = try FileManager.default.contentsOfDirectory(
        at: layout.meetingsDirectory,
        includingPropertiesForKeys: [.isDirectoryKey, .isSymbolicLinkKey],
        options: [.skipsHiddenFiles]
      )
    } catch {
      throw StoreError.operationFailed
    }

    var meetings: [MeetingManifest] = []
    var diagnostics: [StoreDiagnostic] = []

    for entry in entries.sorted(by: { $0.lastPathComponent < $1.lastPathComponent }) {
      let meetingID = entry.lastPathComponent
      guard UUID(uuidString: meetingID) != nil else {
        diagnostics.append(StoreDiagnostic(meetingID: meetingID, code: .invalidMeetingDirectory))
        continue
      }

      do {
        try requireExistingSafeDirectory(entry)
        meetings.append(try load(meetingID: meetingID))
      } catch StoreError.unsafePath {
        diagnostics.append(StoreDiagnostic(meetingID: meetingID, code: .unsafePath))
      } catch {
        diagnostics.append(StoreDiagnostic(meetingID: meetingID, code: .malformedManifest))
      }
    }

    return MeetingList(meetings: meetings, diagnostics: diagnostics)
  }

  public func append(_ event: EventEnvelope) throws {
    let meetingID = event.meetingID.uuidString.lowercased()
    let directory = layout.meetingDirectory(meetingID)
    try requireExistingSafeDirectory(directory)

    let eventsURL = layout.eventsURL(in: directory)
    try requireSafePath(eventsURL)
    do {
      let eventData = try ContractCodec.encoder().encode(event)
      var line = eventData
      line.append(0x0A)
      try AtomicFileWriter.append(line, to: eventsURL)
    } catch {
      throw error is StoreError ? error : StoreError.operationFailed
    }
  }

  /// Appends one finalized segment to the meeting's `segments.raw.jsonl`
  /// evidence file. Engine output is immutable evidence, so this file is
  /// append-only -- callers that need to avoid persisting the same segment
  /// twice across a retry must dedupe before calling this (see
  /// ``SegmentAccumulator``).
  public func appendRawSegment(_ segment: Segment, meetingID: String) throws {
    let normalizedID = try normalizedMeetingID(meetingID)
    let directory = layout.meetingDirectory(normalizedID)
    try requireExistingSafeDirectory(directory)

    let segmentsURL = layout.rawSegmentsURL(in: directory)
    try requireSafePath(segmentsURL)
    do {
      let segmentData = try ContractCodec.encoder().encode(segment)
      var line = segmentData
      line.append(0x0A)
      try AtomicFileWriter.append(line, to: segmentsURL)
    } catch {
      throw error is StoreError ? error : StoreError.operationFailed
    }
  }

  /// Reads back every segment already finalized in `segments.raw.jsonl`, in
  /// the order they were written. Used to seed ``SegmentAccumulator`` before
  /// a retry so already-finalized time ranges are never persisted twice.
  public func rawSegments(meetingID: String) throws -> [Segment] {
    let normalizedID = try normalizedMeetingID(meetingID)
    let directory = layout.meetingDirectory(normalizedID)
    return try segmentsJSONL(at: layout.rawSegmentsURL(in: directory), in: directory).segments
  }

  /// Reads at most `limit` raw segments while still counting every segment
  /// record in the evidence file. The returned segments preserve write order.
  public func rawSegments(meetingID: String, limit: Int) throws -> (
    segments: [Segment], totalCount: Int
  ) {
    let normalizedID = try normalizedMeetingID(meetingID)
    let directory = layout.meetingDirectory(normalizedID)
    return try segmentsJSONL(
      at: layout.rawSegmentsURL(in: directory), in: directory, limit: max(0, limit))
  }

  /// Reads back every segment recorded in `segments.edited.jsonl` -- the
  /// user-authored overlay a caller (like `symmeet export`) prefers over raw
  /// engine evidence when one exists. Returns an empty array both when the
  /// file is absent and when it exists but has never been written to, since
  /// there is currently no writer for this file anywhere in the codebase.
  public func editedSegments(meetingID: String) throws -> [Segment] {
    let normalizedID = try normalizedMeetingID(meetingID)
    let directory = layout.meetingDirectory(normalizedID)
    return try segmentsJSONL(at: layout.editedSegmentsURL(in: directory), in: directory).segments
  }

  /// Mirrors ``rawSegments(meetingID:)`` for a meeting currently in local
  /// trash.
  public func rawSegments(trashedMeetingID meetingID: String) throws -> [Segment] {
    let normalizedID = try normalizedMeetingID(meetingID)
    let directory = layout.trashedMeetingDirectory(normalizedID)
    return try segmentsJSONL(at: layout.rawSegmentsURL(in: directory), in: directory).segments
  }

  /// Mirrors ``editedSegments(meetingID:)`` for a meeting currently in local
  /// trash.
  public func editedSegments(trashedMeetingID meetingID: String) throws -> [Segment] {
    let normalizedID = try normalizedMeetingID(meetingID)
    let directory = layout.trashedMeetingDirectory(normalizedID)
    return try segmentsJSONL(at: layout.editedSegmentsURL(in: directory), in: directory).segments
  }

  private func segmentsJSONL(
    at url: URL, in directory: URL, limit: Int? = nil
  ) throws -> (segments: [Segment], totalCount: Int) {
    try requireExistingSafeDirectory(directory)
    try requireSafePath(url)
    guard let data = try? Data(contentsOf: url), !data.isEmpty else {
      return (segments: [], totalCount: 0)
    }

    do {
      let decoder = ContractCodec.decoder()
      var segments: [Segment] = []
      var totalCount = 0

      for line in data.split(separator: 0x0A) {
        totalCount += 1
        if let limit, segments.count >= limit {
          continue
        }
        segments.append(try decoder.decode(Segment.self, from: Data(line)))
      }
      return (segments: segments, totalCount: totalCount)
    } catch {
      throw StoreError.malformedArtifact
    }
  }

  // MARK: - Diarization turns

  /// Appends raw diarization turns to the meeting's `turns.raw.jsonl`
  /// evidence file. Engine output is immutable, so this file is append-only.
  public func appendRawTurns(_ turns: [SpeakerTurn], meetingID: String) throws {
    let normalizedID = try normalizedMeetingID(meetingID)
    let directory = layout.meetingDirectory(normalizedID)
    try requireExistingSafeDirectory(directory)
    let url = layout.turnsRawURL(in: directory)
    try requireSafePath(url)
    guard !turns.isEmpty else { return }

    do {
      var data = Data()
      for turn in turns {
        data.append(try ContractCodec.encoder().encode(turn))
        data.append(0x0A)
      }
      try AtomicFileWriter.append(data, to: url)
    } catch {
      throw error is StoreError ? error : StoreError.operationFailed
    }
  }

  /// Reads back every turn in `turns.raw.jsonl`, in write order.
  public func rawTurns(meetingID: String) throws -> [SpeakerTurn] {
    let normalizedID = try normalizedMeetingID(meetingID)
    let directory = layout.meetingDirectory(normalizedID)
    try requireExistingSafeDirectory(directory)
    let url = layout.turnsRawURL(in: directory)
    try requireSafePath(url)
    guard let data = try? Data(contentsOf: url), !data.isEmpty else { return [] }
    do {
      return try data.split(separator: 0x0A).map {
        try ContractCodec.decoder().decode(SpeakerTurn.self, from: Data($0))
      }
    } catch {
      throw StoreError.malformedArtifact
    }
  }

  /// Overwrites the edited turns file. Unlike raw turns, edited turns are
  /// a derived projection that may be regenerated from the raw turns +
  /// speaker map at any time.
  public func writeEditedTurns(_ turns: [SpeakerTurn], meetingID: String) throws {
    let normalizedID = try normalizedMeetingID(meetingID)
    let directory = layout.meetingDirectory(normalizedID)
    try requireExistingSafeDirectory(directory)
    let url = layout.turnsEditedURL(in: directory)
    try requireSafePath(url)
    do {
      var data = Data()
      for turn in turns {
        let line = try ContractCodec.encoder().encode(turn)
        data.append(line)
        data.append(0x0A)
      }
      try AtomicFileWriter.write(data, to: url)
    } catch {
      throw error is StoreError ? error : StoreError.operationFailed
    }
  }

  /// Reads the edited turns, if any.
  public func editedTurns(meetingID: String) throws -> [SpeakerTurn] {
    let normalizedID = try normalizedMeetingID(meetingID)
    let directory = layout.meetingDirectory(normalizedID)
    try requireExistingSafeDirectory(directory)
    let url = layout.turnsEditedURL(in: directory)
    try requireSafePath(url)
    guard let data = try? Data(contentsOf: url), !data.isEmpty else { return [] }
    do {
      return try data.split(separator: 0x0A).map {
        try ContractCodec.decoder().decode(SpeakerTurn.self, from: Data($0))
      }
    } catch {
      throw StoreError.malformedArtifact
    }
  }

  // MARK: - Speaker alignment

  /// Persists the alignment for a meeting (overwrites -- alignment is a
  /// derived artifact that may be regenerated).
  public func writeAlignment(_ alignments: [SpeakerAlignment], meetingID: String) throws {
    let normalizedID = try normalizedMeetingID(meetingID)
    let directory = layout.meetingDirectory(normalizedID)
    try requireExistingSafeDirectory(directory)
    let url = layout.alignmentURL(in: directory)
    try requireSafePath(url)
    do {
      let data = try ContractCodec.encoder(prettyPrinted: true).encode(alignments)
      try AtomicFileWriter.write(data, to: url)
    } catch {
      throw error is StoreError ? error : StoreError.operationFailed
    }
  }

  /// Reads the alignment data, if any.
  public func alignment(meetingID: String) throws -> [SpeakerAlignment] {
    let normalizedID = try normalizedMeetingID(meetingID)
    let directory = layout.meetingDirectory(normalizedID)
    try requireExistingSafeDirectory(directory)
    let url = layout.alignmentURL(in: directory)
    try requireSafePath(url)
    guard let data = try? Data(contentsOf: url), !data.isEmpty else { return [] }
    do {
      return try ContractCodec.decoder().decode([SpeakerAlignment].self, from: data)
    } catch {
      throw StoreError.malformedArtifact
    }
  }

  // MARK: - Speaker edits

  /// Appends one speaker edit event to `speaker_edits.jsonl`.
  public func appendSpeakerEdit(_ event: SpeakerEditEvent, meetingID: String) throws {
    let normalizedID = try normalizedMeetingID(meetingID)
    let directory = layout.meetingDirectory(normalizedID)
    try requireExistingSafeDirectory(directory)
    let url = layout.speakerEditsURL(in: directory)
    try requireSafePath(url)
    let existing = (try? Data(contentsOf: url)) ?? Data()
    do {
      let data = try ContractCodec.encoder().encode(event)
      var updated = existing
      updated.append(data)
      updated.append(0x0A)
      try AtomicFileWriter.write(updated, to: url)
    } catch {
      throw error is StoreError ? error : StoreError.operationFailed
    }
  }

  /// Reads all speaker edit events, in write order.
  public func speakerEdits(meetingID: String) throws -> [SpeakerEditEvent] {
    let normalizedID = try normalizedMeetingID(meetingID)
    let directory = layout.meetingDirectory(normalizedID)
    try requireExistingSafeDirectory(directory)
    let url = layout.speakerEditsURL(in: directory)
    try requireSafePath(url)
    guard let data = try? Data(contentsOf: url), !data.isEmpty else { return [] }
    do {
      return try data.split(separator: 0x0A).map {
        try ContractCodec.decoder().decode(SpeakerEditEvent.self, from: Data($0))
      }
    } catch {
      throw StoreError.malformedArtifact
    }
  }

  // MARK: - Speaker map

  /// Persists the derived speaker map.
  public func writeSpeakerMap(_ map: SpeakerMap, meetingID: String) throws {
    let normalizedID = try normalizedMeetingID(meetingID)
    let directory = layout.meetingDirectory(normalizedID)
    try requireExistingSafeDirectory(directory)
    let url = layout.speakerMapURL(in: directory)
    try requireSafePath(url)
    do {
      let data = try ContractCodec.encoder(prettyPrinted: true).encode(map)
      try AtomicFileWriter.write(data, to: url)
    } catch {
      throw error is StoreError ? error : StoreError.operationFailed
    }
  }

  /// Reads the derived speaker map, if any.
  public func speakerMap(meetingID: String) throws -> SpeakerMap? {
    let normalizedID = try normalizedMeetingID(meetingID)
    let directory = layout.meetingDirectory(normalizedID)
    try requireExistingSafeDirectory(directory)
    let url = layout.speakerMapURL(in: directory)
    try requireSafePath(url)
    guard let data = try? Data(contentsOf: url), !data.isEmpty else { return nil }
    do {
      return try ContractCodec.decoder().decode(SpeakerMap.self, from: data)
    } catch {
      throw StoreError.malformedArtifact
    }
  }

  // MARK: - Pipeline state

  /// Persists the pipeline state for a meeting.
  public func writePipelineState(_ state: PipelineState, meetingID: String) throws {
    let normalizedID = try normalizedMeetingID(meetingID)
    let directory = layout.meetingDirectory(normalizedID)
    try requireExistingSafeDirectory(directory)
    let url = layout.pipelineStateURL(in: directory)
    try requireSafePath(url)
    do {
      let data = try ContractCodec.encoder(prettyPrinted: true).encode(state)
      try AtomicFileWriter.write(data, to: url)
    } catch {
      throw error is StoreError ? error : StoreError.operationFailed
    }
  }

  /// Reads the pipeline state, if any.
  public func pipelineState(meetingID: String) throws -> PipelineState? {
    let normalizedID = try normalizedMeetingID(meetingID)
    let directory = layout.meetingDirectory(normalizedID)
    try requireExistingSafeDirectory(directory)
    let url = layout.pipelineStateURL(in: directory)
    try requireSafePath(url)
    guard let data = try? Data(contentsOf: url), !data.isEmpty else { return nil }
    do {
      return try ContractCodec.decoder().decode(PipelineState.self, from: data)
    } catch {
      throw StoreError.malformedArtifact
    }
  }

  /// Returns the derived files eligible for a retention cleanup after validating
  /// that every path remains under the configured data root.
  public func derivedArtifactURLs(meetingID: String) throws -> [URL] {
    let normalizedID = try normalizedMeetingID(meetingID)
    let directory = layout.meetingDirectory(normalizedID)
    try requireExistingSafeDirectory(directory)

    let audioDirectory = directory.appending(path: "audio", directoryHint: .isDirectory)
    let candidates = [
      layout.rawSegmentsURL(in: directory),
      layout.editedSegmentsURL(in: directory),
      layout.transcriptURL(in: directory),
      audioDirectory.appending(path: "normalized.caf", directoryHint: .notDirectory),
      audioDirectory.appending(path: "normalized", directoryHint: .isDirectory),
    ]
    for candidate in candidates {
      try requireSafePath(candidate)
    }
    return candidates
  }

  public func trash(meetingID: String) throws {
    let normalizedID = try normalizedMeetingID(meetingID)
    let source = layout.meetingDirectory(normalizedID)
    let destination = layout.trashedMeetingDirectory(normalizedID)
    try requireExistingSafeDirectory(source)
    try prepareDirectory(layout.trashDirectory)
    try requireSafePath(destination)

    guard !FileManager.default.fileExists(atPath: destination.path) else {
      throw StoreError.operationFailed
    }
    try move(source, to: destination)
  }

  public func restore(meetingID: String) throws {
    let normalizedID = try normalizedMeetingID(meetingID)
    let source = layout.trashedMeetingDirectory(normalizedID)
    let destination = layout.meetingDirectory(normalizedID)
    try requireExistingSafeDirectory(source)
    try prepareDirectory(layout.meetingsDirectory)
    try requireSafePath(destination)

    guard !FileManager.default.fileExists(atPath: destination.path) else {
      throw StoreError.alreadyExists
    }
    try move(source, to: destination)
  }

  /// Permanently deletes an already-trashed meeting. The operation is idempotent.
  @discardableResult
  public func permanentlyDelete(meetingID: String) throws -> Bool {
    let normalizedID = try normalizedMeetingID(meetingID)
    let directory = layout.trashedMeetingDirectory(normalizedID)
    try requireSafePath(directory)

    guard FileManager.default.fileExists(atPath: directory.path) else { return false }
    try requireExistingSafeDirectory(directory)

    do {
      try FileManager.default.removeItem(at: directory)
      return true
    } catch {
      throw StoreError.operationFailed
    }
  }

  private func writeInitialFiles(for manifest: MeetingManifest, in directory: URL) throws {
    let manifestData = try ContractCodec.encoder(prettyPrinted: true).encode(manifest)
    try AtomicFileWriter.write(manifestData, to: layout.manifestURL(in: directory))
    try AtomicFileWriter.write(Data(), to: layout.eventsURL(in: directory))
    try AtomicFileWriter.write(Data(), to: layout.rawSegmentsURL(in: directory))
    try AtomicFileWriter.write(Data(), to: layout.editedSegmentsURL(in: directory))
    try AtomicFileWriter.write(Data(), to: layout.turnsRawURL(in: directory))
    try AtomicFileWriter.write(
      Data(
        "## Summary\n\n## Decisions\n\n## Action Items\n\n## Participants\n\n## Transcript\n".utf8),
      to: layout.transcriptURL(in: directory)
    )
  }

  private func move(_ source: URL, to destination: URL) throws {
    do {
      try FileManager.default.moveItem(at: source, to: destination)
    } catch {
      throw StoreError.operationFailed
    }
  }

  private func normalizedMeetingID(_ value: String) throws -> String {
    guard let uuid = UUID(uuidString: value) else { throw StoreError.invalidMeetingID }
    return uuid.uuidString.lowercased()
  }

  private func validateMeetingID(_ value: String) throws {
    guard UUID(uuidString: value) != nil else { throw StoreError.invalidMeetingID }
  }

  private func validateManifestPaths(_ manifest: MeetingManifest) throws {
    if let originalAsset = manifest.originalAsset {
      try validateRelativePath(originalAsset)
    }
    for track in manifest.audioTracks {
      try validateRelativePath(track.relativePath)
    }
  }

  private func validateRelativePath(_ value: String) throws {
    let components = value.split(separator: "/", omittingEmptySubsequences: false)
    guard
      !value.isEmpty,
      !value.hasPrefix("/"),
      !value.hasPrefix("~"),
      !components.contains(""),
      !components.contains("..")
    else {
      throw StoreError.invalidRelativePath
    }
  }

  private func prepareDirectory(_ directory: URL) throws {
    try requireSafePath(directory)
    do {
      try FileManager.default.createDirectory(at: directory, withIntermediateDirectories: true)
      try FileManager.default.setAttributes(
        [.posixPermissions: 0o700], ofItemAtPath: directory.path)
      try requireSafePath(directory)
    } catch {
      throw error is StoreError ? error : StoreError.operationFailed
    }
  }

  private func requireExistingSafeDirectory(_ directory: URL) throws {
    try requireSafePath(directory)
    var isDirectory: ObjCBool = false
    guard FileManager.default.fileExists(atPath: directory.path, isDirectory: &isDirectory),
      isDirectory.boolValue
    else {
      throw StoreError.missing
    }
  }

  private func requireSafePath(_ candidate: URL) throws {
    let root = layout.dataRoot.standardizedFileURL
    let path = candidate.standardizedFileURL
    let rootPath = root.path
    guard path.path == rootPath || path.path.hasPrefix(rootPath + "/") else {
      throw StoreError.unsafePath
    }

    var current = root
    if FileManager.default.fileExists(atPath: current.path), try isSymbolicLink(current) {
      throw StoreError.unsafePath
    }

    let relative = path.path.dropFirst(rootPath.count).split(separator: "/")
    for component in relative {
      current.append(path: String(component), directoryHint: .isDirectory)
      if FileManager.default.fileExists(atPath: current.path), try isSymbolicLink(current) {
        throw StoreError.unsafePath
      }
    }
  }

  private func isSymbolicLink(_ url: URL) throws -> Bool {
    do {
      return try url.resourceValues(forKeys: [.isSymbolicLinkKey]).isSymbolicLink == true
    } catch {
      throw StoreError.operationFailed
    }
  }
}
