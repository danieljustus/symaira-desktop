import ArgumentParser
import Foundation
import SymMeetCore

extension SymMeet {
  struct Export: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      commandName: "export",
      abstract: "Render a completed meeting into a portable transcript format."
    )

    @Argument(help: "The meeting UUID.")
    var meetingID: String

    @Option(name: .long, help: "Export format: markdown, txt, json, jsonl, srt, or vtt.")
    var format: String

    @Option(name: .long, help: "Output file path, or '-' to write to stdout.")
    var output: String

    @Option(
      name: .long,
      help:
        "Segment source: raw or edited. Defaults to edited when an edited version exists on disk, otherwise raw."
    )
    var segments: String?

    @Flag(
      name: .long,
      help: "txt only: prefix each line with its timestamp range and speaker label.")
    var withTimestamps = false

    @Flag(name: .long, help: "Overwrite an existing output file.")
    var force = false

    @Flag(
      name: .long,
      help: "Allow exporting a meeting whose transcription job has not completed.")
    var allowIncomplete = false

    @Flag(name: .long, help: "Allow exporting a meeting currently in local trash.")
    var includeTrashed = false

    @Flag(
      name: .long, help: "Emit one machine-readable result document. Not valid with --output -.")
    var json = false

    mutating func run() async throws {
      guard let exportFormat = ExportFormat(rawValue: format) else {
        throw CLIError(
          exitCode: CLIExit.usage.rawValue,
          code: "unknown_format",
          message:
            "Unknown format '\(format)'. Supported: "
            + ExportFormat.allCases.map(\.rawValue).joined(separator: ", ") + ".",
          isJSON: json)
      }

      var requestedSource: ExportSegmentSource?
      if let segments {
        guard let parsed = ExportSegmentSource(rawValue: segments) else {
          throw CLIError(
            exitCode: CLIExit.usage.rawValue,
            code: "invalid_meeting_id",
            message: "Unknown segment source '\(segments)'. Supported: raw, edited.",
            isJSON: json)
        }
        requestedSource = parsed
      }

      let writesToStdout = output == "-"
      if writesToStdout, json {
        throw CLIError(
          exitCode: CLIExit.usage.rawValue,
          code: "export_failed",
          message: "--json cannot be combined with --output -.",
          isJSON: json)
      }

      do {
        let store = MeetingStore()
        let resolution = try await resolveMeeting(
          store: store, meetingID: meetingID, allowIncomplete: allowIncomplete,
          includeTrashed: includeTrashed)
        let resolvedSegments = try await resolveSegments(
          store: store, meetingID: meetingID, trashed: resolution.wasTrashed,
          requested: requestedSource)

        let content = try TranscriptRenderer.render(
          manifest: resolution.manifest,
          segments: resolvedSegments.segments,
          segmentSource: resolvedSegments.source,
          format: exportFormat,
          options: TranscriptRenderer.Options(withTimestamps: withTimestamps))

        if writesToStdout {
          Output.writeRaw(content)
        } else {
          let url = URL(fileURLWithPath: output)
          try TranscriptRenderer.write(content, to: url, force: force)
          if json {
            try Output.writeJSON(
              ExportResultOutput(
                meetingID: meetingID.lowercased(),
                format: exportFormat.rawValue,
                segmentSource: resolvedSegments.source.rawValue,
                outputPath: url.path,
                segmentCount: resolvedSegments.segments.count))
          } else {
            Output.writeLine(
              "Exported \(meetingID.lowercased()) (\(exportFormat.rawValue)) to \(url.path).")
          }
        }
      } catch {
        throw CLIError.from(error, isJSON: json)
      }
    }
  }
}

// MARK: - Meeting resolution

private struct MeetingResolution {
  let manifest: MeetingManifest
  let wasTrashed: Bool
}

/// Loads the manifest for `meetingID`, recognizing a trashed meeting rather
/// than surfacing the same generic "missing" error a never-existed meeting
/// would produce, then enforces the trashed/incomplete override flags.
private func resolveMeeting(
  store: MeetingStore, meetingID: String, allowIncomplete: Bool, includeTrashed: Bool
) async throws -> MeetingResolution {
  let manifest: MeetingManifest
  let wasTrashed: Bool
  do {
    manifest = try await store.load(meetingID: meetingID)
    wasTrashed = false
  } catch StoreError.missing {
    manifest = try await store.loadTrashed(meetingID: meetingID)
    wasTrashed = true
  }

  if wasTrashed, !includeTrashed {
    throw ExportError.meetingTrashed
  }
  if manifest.job?.state != .completed, !allowIncomplete {
    throw ExportError.meetingIncomplete(jobState: manifest.job?.state.rawValue ?? "not_started")
  }
  return MeetingResolution(manifest: manifest, wasTrashed: wasTrashed)
}

// MARK: - Segment source resolution

private struct SegmentResolution {
  let segments: [Segment]
  let source: ExportSegmentSource
}

/// Resolves which segment file to export from. An explicit `--segments raw`
/// or `--segments edited` is honored exactly (an explicit `edited` request
/// against a meeting with no edited segments is an error, not a silent
/// fallback). With no explicit request, edited segments are preferred when
/// they exist, otherwise raw -- see issue #11's segment-source rule.
private func resolveSegments(
  store: MeetingStore, meetingID: String, trashed: Bool, requested: ExportSegmentSource?
) async throws -> SegmentResolution {
  func rawSegments() async throws -> [Segment] {
    trashed
      ? try await store.rawSegments(trashedMeetingID: meetingID)
      : try await store.rawSegments(meetingID: meetingID)
  }
  func editedSegments() async throws -> [Segment] {
    trashed
      ? try await store.editedSegments(trashedMeetingID: meetingID)
      : try await store.editedSegments(meetingID: meetingID)
  }

  switch requested {
  case .raw:
    return SegmentResolution(segments: try await rawSegments(), source: .raw)
  case .edited:
    let edited = try await editedSegments()
    guard !edited.isEmpty else { throw ExportError.editedSegmentsUnavailable }
    return SegmentResolution(segments: edited, source: .edited)
  case nil:
    let edited = try await editedSegments()
    if !edited.isEmpty {
      return SegmentResolution(segments: edited, source: .edited)
    }
    return SegmentResolution(segments: try await rawSegments(), source: .raw)
  }
}

// MARK: - Output types

private struct ExportResultOutput: Encodable {
  let meetingID: String
  let format: String
  let segmentSource: String
  let outputPath: String
  let segmentCount: Int
}
