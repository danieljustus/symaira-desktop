import Foundation

/// Renders a completed meeting's segments into one of the interoperable
/// ``ExportFormat``s and writes the result to disk with the same
/// overwrite-protected, atomic semantics as the rest of the artifact store.
///
/// Rendering is a pure function of `(manifest, segments, segmentSource,
/// format, options)`: nothing here reads the clock or any other
/// non-deterministic source, so calling ``render(manifest:segments:segmentSource:format:options:)``
/// twice with identical inputs always produces byte-identical output. That
/// determinism is the whole point -- see the "Rules" section of issue #11.
public enum TranscriptRenderer {
  /// Rendering knobs that are not implied by the format itself.
  public struct Options: Sendable {
    /// `txt` only: prefix each line with its timestamp range and speaker
    /// label. Ignored by every other format (markdown, srt, and vtt always
    /// show speaker labels because the format is meaningless without them;
    /// json/jsonl always carry full ``Segment`` data regardless).
    public var withTimestamps: Bool

    public init(withTimestamps: Bool = false) {
      self.withTimestamps = withTimestamps
    }
  }

  public static func render(
    manifest: MeetingManifest,
    segments: [Segment],
    segmentSource: ExportSegmentSource,
    format: ExportFormat,
    options: Options = Options()
  ) throws -> String {
    switch format {
    case .markdown:
      return MarkdownRenderer.render(manifest: manifest, segments: segments)
    case .txt:
      return renderText(segments: segments, options: options)
    case .json:
      return try renderJSON(manifest: manifest, segments: segments, segmentSource: segmentSource)
    case .jsonl:
      return try renderJSONL(segments: segments)
    case .srt:
      return SubtitleRenderer.render(segments: segments, style: .srt)
    case .vtt:
      return SubtitleRenderer.render(segments: segments, style: .vtt)
    }
  }

  /// Writes rendered `content` to `destination`, refusing to clobber an
  /// existing file unless `force` is set. Delegates the actual write to
  /// ``AtomicFileWriter`` -- the same temp-file-plus-rename mechanism every
  /// other artifact write in this codebase uses -- so a crash mid-write can
  /// never leave a half-written export behind.
  public static func write(_ content: String, to destination: URL, force: Bool) throws {
    let directory = destination.deletingLastPathComponent()
    var isDirectory: ObjCBool = false
    guard FileManager.default.fileExists(atPath: directory.path, isDirectory: &isDirectory),
      isDirectory.boolValue
    else {
      throw ExportError.invalidOutputPath(destination.path)
    }
    if !force, FileManager.default.fileExists(atPath: destination.path) {
      throw ExportError.outputExists(destination.path)
    }
    try AtomicFileWriter.write(Data(content.utf8), to: destination)
  }

  // MARK: - txt

  private static func renderText(segments: [Segment], options: Options) -> String {
    let ordered = segments.sorted(by: chronologicalOrder)
    guard !ordered.isEmpty else { return "" }

    let lines: [String]
    if options.withTimestamps {
      lines = ordered.map { segment in
        let start = TimestampFormatter.format(segment.startMS, separator: ".")
        let end = TimestampFormatter.format(segment.endMS, separator: ".")
        return "[\(start) - \(end)] \(segment.speakerID): \(segment.displayText)"
      }
    } else {
      lines = ordered.map(\.displayText)
    }
    return lines.joined(separator: "\n\n") + "\n"
  }

  // MARK: - json / jsonl

  /// The `json` export envelope. Field names are deliberately distinct from
  /// ``MeetingManifest``'s own (`meeting_source` vs. `segment_source`) so a
  /// consumer never has to guess which "source" a given field means.
  private struct ExportEnvelope: Encodable {
    let schemaVersion: Int
    let meetingID: UUID
    let meetingSource: SourceKind
    let language: String?
    let jobState: MeetingJobState?
    let segmentSource: ExportSegmentSource
    let segmentCount: Int
    let segments: [Segment]

    private enum CodingKeys: String, CodingKey {
      case schemaVersion = "schema_version"
      case meetingID = "meeting_id"
      case meetingSource = "meeting_source"
      case language
      case jobState = "job_state"
      case segmentSource = "segment_source"
      case segmentCount = "segment_count"
      case segments
    }
  }

  private static func renderJSON(
    manifest: MeetingManifest, segments: [Segment], segmentSource: ExportSegmentSource
  ) throws -> String {
    let envelope = ExportEnvelope(
      schemaVersion: 1,
      meetingID: manifest.meetingID,
      meetingSource: manifest.source,
      language: manifest.language,
      jobState: manifest.job?.state,
      segmentSource: segmentSource,
      segmentCount: segments.count,
      segments: segments)
    let data = try ContractCodec.encoder(prettyPrinted: true).encode(envelope)
    return String(decoding: data, as: UTF8.self)
  }

  /// One v1 ``Segment`` per line, verbatim -- no reordering, no reshaping.
  /// Segments are emitted in the order ``MeetingStore`` returned them (their
  /// on-disk evidentiary order), matching how `segments.raw.jsonl` itself is
  /// written.
  private static func renderJSONL(segments: [Segment]) throws -> String {
    guard !segments.isEmpty else { return "" }
    let encoder = ContractCodec.encoder()
    let lines = try segments.map { segment in
      String(decoding: try encoder.encode(segment), as: UTF8.self)
    }
    return lines.joined(separator: "\n") + "\n"
  }

  // MARK: - Shared helpers (used by MarkdownRenderer and SubtitleRenderer too)

  /// A total, deterministic chronological order: by start time, then end
  /// time, then speaker, then segment ID. Segments finalized across
  /// concurrent tracks are not guaranteed to land in `segments.raw.jsonl` in
  /// timestamp order, so every human/subtitle-facing format (markdown, txt,
  /// srt, vtt) re-sorts with this before rendering. `json`/`jsonl` deliberately
  /// do not -- see ``renderJSONL(segments:)``.
  static func chronologicalOrder(_ lhs: Segment, _ rhs: Segment) -> Bool {
    if lhs.startMS != rhs.startMS { return lhs.startMS < rhs.startMS }
    if lhs.endMS != rhs.endMS { return lhs.endMS < rhs.endMS }
    if lhs.speakerID != rhs.speakerID { return lhs.speakerID < rhs.speakerID }
    return lhs.segmentID.uuidString < rhs.segmentID.uuidString
  }
}

extension Segment {
  /// The text to show a reader: the user-corrected projection when one
  /// exists, otherwise the immutable engine evidence. This applies uniformly
  /// regardless of which file (`segments.raw.jsonl` or
  /// `segments.edited.jsonl`) the segment was read from, since the `Segment`
  /// contract already models per-segment corrections independently of which
  /// file carries them.
  var displayText: String { editedText ?? engineText }
}

/// Shared `HH:MM:SS<separator>mmm` timestamp formatting for txt, srt, and
/// vtt. Hours are not capped at 24 so hour-plus meetings still render a
/// single, unambiguous timestamp.
enum TimestampFormatter {
  static func format(_ milliseconds: Int, separator: Character) -> String {
    let clamped = max(milliseconds, 0)
    let hours = clamped / 3_600_000
    let minutes = (clamped / 60_000) % 60
    let seconds = (clamped / 1_000) % 60
    let millis = clamped % 1_000
    let hh = String(format: "%02d", hours)
    let mm = String(format: "%02d", minutes)
    let ss = String(format: "%02d", seconds)
    let mmm = String(format: "%03d", millis)
    return "\(hh):\(mm):\(ss)\(separator)\(mmm)"
  }
}
