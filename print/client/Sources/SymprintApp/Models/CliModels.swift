import Foundation

// MARK: - Engine Info (Doctor)
struct EngineInfo: Codable, Identifiable, Hashable {
    var id: String { name }
    let name: String
    let available: Bool
    let path: String?
    let version: String?
    let hint: String?
}

struct DoctorResult: Codable {
    let typst: EngineInfo
    let pandoc: EngineInfo
    let verapdf: EngineInfo
}

// MARK: - Profile Details
struct CliProfile: Codable, Identifiable, Hashable {
    var id: String { name }
    let name: String
    let title: String
    let description: String
    let template: String
    let engine: String
    let form: String?
    let pdfStandard: [String]?
    let reproducible: Bool
    let requiredFields: [String]?
    let stability: String
    
    enum CodingKeys: String, CodingKey {
        case name, title, description, template, engine, form
        case pdfStandard = "pdf_standard"
        case reproducible
        case requiredFields = "required_fields"
        case stability
    }
}

// MARK: - Validate Result (frontmatter contract check)
struct ValidateResult: Codable {
    let profile: String
    let ok: Bool
}

// MARK: - Render Result
struct RenderResult: Codable, Hashable {
    let outputPath: String
    let profile: String
    let engine: String
    let engineVersion: String?
    let pdfStandard: [String]?
    let reproducible: Bool
    let bytes: Int64
    let durationMs: Int64
    
    enum CodingKeys: String, CodingKey {
        case outputPath = "output_path"
        case profile, engine
        case engineVersion = "engine_version"
        case pdfStandard = "pdf_standard"
        case reproducible, bytes
        case durationMs = "duration_ms"
    }
}
