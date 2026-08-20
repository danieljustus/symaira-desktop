import ArgumentParser
import SymMeetCore
import SymMeetWhisperKit

extension SymMeet {
  struct Model: AsyncParsableCommand {
    static let configuration = CommandConfiguration(
      abstract: "Manage local transcription models.",
      subcommands: [ModelList.self, ModelInstall.self, ModelRemove.self]
    )
  }

  struct ModelList: AsyncParsableCommand {
    static let configuration = CommandConfiguration(abstract: "List catalog and installed models.")

    @Flag(name: .long, help: "Emit one machine-readable model list.")
    var json = false

    mutating func run() async throws {
      do {
        let records = try await ModelStore().list()
        if json {
          try Output.writeJSON(ModelListOutput(models: records))
        } else {
          for record in records {
            Output.writeLine("\(record.descriptor.id)\t\(record.status.rawValue)")
          }
        }
      } catch {
        throw CLIError.from(error, isJSON: json)
      }
    }
  }

  struct ModelInstall: AsyncParsableCommand {
    static let configuration = CommandConfiguration(abstract: "Download and install one model.")

    @Argument(help: "The catalog model identifier.")
    var modelID: String

    @Flag(name: .long, help: "Emit the installed model as JSON.")
    var json = false

    mutating func run() async throws {
      do {
        let installer = WhisperKitModelInstaller()
        let record = try await installer.install(id: modelID) { fraction in
          Output.writeError("Downloading model (\(Int(fraction * 100))%).")
        }
        if json {
          try Output.writeJSON(record)
        } else {
          Output.writeLine("Installed \(record.descriptor.id).")
        }
      } catch {
        throw CLIError.from(error, isJSON: json)
      }
    }
  }

  struct ModelRemove: AsyncParsableCommand {
    static let configuration = CommandConfiguration(abstract: "Remove one local model.")

    @Argument(help: "The catalog model identifier.")
    var modelID: String

    @Flag(name: .long, help: "Emit one machine-readable result.")
    var json = false

    mutating func run() async throws {
      do {
        let removed = try await ModelStore().remove(id: modelID)
        if json {
          try Output.writeJSON(ModelMutationOutput(modelID: modelID, removed: removed))
        } else {
          Output.writeLine(removed ? "Removed \(modelID)." : "Model \(modelID) was not installed.")
        }
      } catch {
        throw CLIError.from(error, isJSON: json)
      }
    }
  }

  private struct ModelListOutput: Encodable {
    let models: [ModelRecord]
  }

  private struct ModelMutationOutput: Encodable {
    let modelID: String
    let removed: Bool
  }
}
