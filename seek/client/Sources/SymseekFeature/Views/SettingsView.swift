import SwiftUI
import Observation

@MainActor
@Observable
class SettingsModel {
    var ollamaUrl = ""
    var model = ""
    var embeddingDim = 0
    var vectorQuantization = "off"
    var vectorQuantBits = 4
    var vectorQuantizedShortlist = 200
    var vectorExactRerank = true
    var rerankQuery = false
    var rerankModel = ""
    var expandQuery = false
    var expandModel = ""
    
    var isLoading = false
    var saveStatus = ""
    var saveSuccess = true
    
    struct AppConfig: Codable {
        let ollama_url: String
        let model: String
        let embedding_dim: Int
        let vector_quantization: String
        let vector_quant_bits: Int
        let vector_quantized_shortlist: Int
        let vector_exact_rerank: Bool
        let rerank_query: Bool
        let rerank_model: String
        let expand_query: Bool
        let expand_model: String
    }
    
    func readSettings(binaryURL: URL) {
        isLoading = true
        
        let proc = Process()
        proc.executableURL = binaryURL
        proc.arguments = ["config"]
        
        let pipe = Pipe()
        proc.standardOutput = pipe
        
        do {
            try proc.run()
            proc.waitUntilExit()
            
            let data = pipe.fileHandleForReading.readDataToEndOfFile()
            if let text = String(data: data, encoding: .utf8) {
                if let jsonStart = text.range(of: "{") {
                    let jsonText = String(text[jsonStart.lowerBound...])
                    if let jsonData = jsonText.data(using: .utf8) {
                        let config = try JSONDecoder().decode(AppConfig.self, from: jsonData)
                        
                        ollamaUrl = config.ollama_url
                        model = config.model
                        embeddingDim = config.embedding_dim
                        vectorQuantization = config.vector_quantization
                        vectorQuantBits = config.vector_quant_bits
                        vectorQuantizedShortlist = config.vector_quantized_shortlist
                        vectorExactRerank = config.vector_exact_rerank
                        rerankQuery = config.rerank_query
                        rerankModel = config.rerank_model
                        expandQuery = config.expand_query
                        expandModel = config.expand_model
                    }
                }
            }
        } catch {
            print("Failed to read settings: \(error)")
        }
        
        isLoading = false
    }
    
    func saveSettings(binaryURL: URL, isDaemonRunning: Bool) {
        saveStatus = "Applying settings…"
        saveSuccess = true
        
        let changes: [(String, String)] = [
            ("ollama_url", ollamaUrl),
            ("model", model),
            ("embedding_dim", "\(embeddingDim)"),
            ("vector_quantization", vectorQuantization),
            ("vector_quant_bits", "\(vectorQuantBits)"),
            ("vector_quantized_shortlist", "\(vectorQuantizedShortlist)"),
            ("vector_exact_rerank", "\(vectorExactRerank)"),
            ("rerank_query", "\(rerankQuery)"),
            ("rerank_model", rerankModel),
            ("expand_query", "\(expandQuery)"),
            ("expand_model", expandModel)
        ]
        
        Task.detached(priority: .userInitiated) { [weak self] in
            guard let self else { return }
            var success = true
            
            for (key, value) in changes {
                let proc = Process()
                proc.executableURL = binaryURL
                proc.arguments = ["config", "--set-key", key, "--set-value", value]
                
                do {
                    try proc.run()
                    proc.waitUntilExit()
                    if proc.terminationStatus != 0 {
                        success = false
                        break
                    }
                } catch {
                    success = false
                    break
                }
            }
            
            await MainActor.run {
                self.saveSuccess = success
                if success {
                    self.saveStatus = "Settings applied successfully!"
                    if isDaemonRunning {
                        self.saveStatus += " Restart daemon to apply changes."
                    }
                } else {
                    self.saveStatus = "Failed to apply settings."
                }
                
                // Clear after 4 seconds
                Task {
                    try? await Task.sleep(for: .seconds(4))
                    await MainActor.run {
                        self.saveStatus = ""
                    }
                }
            }
        }
    }
}

struct SettingsView: View {
    @Bindable var engineManager: EngineManager
    @State private var model = SettingsModel()
    
    var body: some View {
        @Bindable var model = model
        ScrollView {
            VStack(alignment: .leading, spacing: 24) {
                // Header
                VStack(alignment: .leading, spacing: 4) {
                    Text("SETTINGS & CONFIGURATION")
                        .font(.system(.caption, design: .monospaced))
                        .foregroundStyle(Color.symairaGold)
                        .tracking(2)
                    
                    Text("Configure Search Engine")
                        .font(.title2.bold())
                        .foregroundStyle(Color.symairaText)
                }
                
                if model.isLoading {
                    HStack(spacing: 8) {
                        ProgressView()
                            .controlSize(.small)
                        Text("Reading configuration…")
                            .foregroundStyle(Color.symairaTextSecondary)
                    }
                    .padding()
                } else {
                    // Ollama section
                    VStack(alignment: .leading, spacing: 16) {
                        HStack {
                            Image(systemName: "brain.head.profile")
                                .font(.title3)
                                .foregroundStyle(Color.symairaGold)
                            Text("Ollama Integration")
                                .font(.headline)
                                .foregroundStyle(Color.symairaText)
                        }
                        
                        VStack(alignment: .leading, spacing: 6) {
                            Text("Ollama Server URL")
                                .font(.caption.bold())
                                .foregroundStyle(Color.symairaGold)
                            TextField("URL", text: $model.ollamaUrl)
                                .textFieldStyle(.plain)
                                .padding(8)
                                .background(Color.symairaBgDarker)
                                .cornerRadius(6)
                                .overlay(RoundedRectangle(cornerRadius: 6).stroke(Color.symairaBorder, lineWidth: 1))
                        }
                        
                        VStack(alignment: .leading, spacing: 6) {
                            Text("Embedding Model Name")
                                .font(.caption.bold())
                                .foregroundStyle(Color.symairaGold)
                            TextField("e.g. nomic-embed-text", text: $model.model)
                                .textFieldStyle(.plain)
                                .padding(8)
                                .background(Color.symairaBgDarker)
                                .cornerRadius(6)
                                .overlay(RoundedRectangle(cornerRadius: 6).stroke(Color.symairaBorder, lineWidth: 1))
                        }
                        
                        VStack(alignment: .leading, spacing: 6) {
                            Text("Embedding Dimension (0 = auto-detect)")
                                .font(.caption.bold())
                                .foregroundStyle(Color.symairaGold)
                            TextField("Dimensions", value: $model.embeddingDim, formatter: NumberFormatter())
                                .textFieldStyle(.plain)
                                .padding(8)
                                .background(Color.symairaBgDarker)
                                .cornerRadius(6)
                                .overlay(RoundedRectangle(cornerRadius: 6).stroke(Color.symairaBorder, lineWidth: 1))
                        }
                    }
                    .symairaCard()
                    
                    // TurboQuant section
                    VStack(alignment: .leading, spacing: 16) {
                        HStack {
                            Image(systemName: "bolt.batteryblock.fill")
                                .font(.title3)
                                .foregroundStyle(Color.symairaGold)
                            Text("TurboQuant (Quantized Vector Search)")
                                .font(.headline)
                                .foregroundStyle(Color.symairaText)
                        }
                        
                        Picker("Quantization Mode", selection: $model.vectorQuantization) {
                            Text("Disabled").tag("off")
                            Text("Turbo Production").tag("turbo-prod")
                        }
                        .pickerStyle(.radioGroup)
                        .padding(.vertical, 2)
                        
                        if model.vectorQuantization == "turbo-prod" {
                            VStack(alignment: .leading, spacing: 12) {
                                Picker("Quantization Bits", selection: $model.vectorQuantBits) {
                                    Text("2-bit").tag(2)
                                    Text("3-bit").tag(3)
                                    Text("4-bit").tag(4)
                                }
                                .pickerStyle(.segmented)
                                
                                VStack(alignment: .leading, spacing: 6) {
                                    Text("Approximate Shortlist Size")
                                        .font(.caption.bold())
                                        .foregroundStyle(Color.symairaGold)
                                    TextField("e.g. 200", value: $model.vectorQuantizedShortlist, formatter: NumberFormatter())
                                        .textFieldStyle(.plain)
                                        .padding(8)
                                        .background(Color.symairaBgDarker)
                                        .cornerRadius(6)
                                        .overlay(RoundedRectangle(cornerRadius: 6).stroke(Color.symairaBorder, lineWidth: 1))
                                }
                                
                                Toggle("Exact Cosine Rerank on Shortlist (Recommended)", isOn: $model.vectorExactRerank)
                                    .toggleStyle(.checkbox)
                            }
                            .padding(.leading, 8)
                            .transition(.opacity)
                        }
                    }
                    .symairaCard()
                    
                    // Advanced search features
                    VStack(alignment: .leading, spacing: 16) {
                        HStack {
                            Image(systemName: "slider.horizontal.3")
                                .font(.title3)
                                .foregroundStyle(Color.symairaGold)
                            Text("Advanced Search Features")
                                .font(.headline)
                                .foregroundStyle(Color.symairaText)
                        }
                        
                        // HyDE
                        VStack(alignment: .leading, spacing: 10) {
                            Toggle("Enable HyDE Query Expansion", isOn: $model.expandQuery)
                                .toggleStyle(.checkbox)
                                .bold()
                            
                            if model.expandQuery {
                                VStack(alignment: .leading, spacing: 6) {
                                    Text("HyDE Chat Model (leave empty to reuse embedding model)")
                                        .font(.caption)
                                        .foregroundStyle(Color.symairaTextSecondary)
                                    TextField("e.g. llama3", text: $model.expandModel)
                                        .textFieldStyle(.plain)
                                        .padding(8)
                                        .background(Color.symairaBgDarker)
                                        .cornerRadius(6)
                                        .overlay(RoundedRectangle(cornerRadius: 6).stroke(Color.symairaBorder, lineWidth: 1))
                                }
                                .padding(.leading, 18)
                            }
                        }
                        
                        Divider()
                            .background(Color.symairaBorder)
                        
                        // Reranker
                        VStack(alignment: .leading, spacing: 10) {
                            Toggle("Enable LLM Re-ranking of Results", isOn: $model.rerankQuery)
                                .toggleStyle(.checkbox)
                                .bold()
                            
                            if model.rerankQuery {
                                VStack(alignment: .leading, spacing: 6) {
                                    Text("Rerank Chat Model (leave empty to reuse embedding model)")
                                        .font(.caption)
                                        .foregroundStyle(Color.symairaTextSecondary)
                                    TextField("e.g. llama3", text: $model.rerankModel)
                                        .textFieldStyle(.plain)
                                        .padding(8)
                                        .background(Color.symairaBgDarker)
                                        .cornerRadius(6)
                                        .overlay(RoundedRectangle(cornerRadius: 6).stroke(Color.symairaBorder, lineWidth: 1))
                                }
                                .padding(.leading, 18)
                            }
                        }
                    }
                    .symairaCard()
                    
                    // Save and status row
                    HStack(spacing: 16) {
                        Button("Apply Settings") {
                            save()
                        }
                        .buttonStyle(.borderedProminent)
                        .tint(Color.symairaGold)
                        .foregroundStyle(Color.symairaBg)
                        .accessibilityLabel("Apply settings")
                        
                        if !model.saveStatus.isEmpty {
                            Text(model.saveStatus)
                                .font(.caption.bold())
                                .foregroundStyle(model.saveSuccess ? Color.green : Color.red)
                                .transition(.opacity)
                        }
                        
                        Spacer()
                    }
                    .padding(.bottom, 24)
                }
            }
            .padding()
        }
        .background(Color.symairaBg)
        .onAppear {
            read()
        }
    }
    
    private func read() {
        if let binaryURL = locateBinary() {
            model.readSettings(binaryURL: binaryURL)
        }
    }
    
    private func save() {
        if let binaryURL = locateBinary() {
            model.saveSettings(binaryURL: binaryURL, isDaemonRunning: engineManager.isRunning)
        }
    }
    
    private func locateBinary() -> URL? {
        if let bundleURL = Bundle.main.url(forResource: "symseek", withExtension: nil) {
            return bundleURL
        }
        let bundleDir = Bundle.main.bundleURL.deletingLastPathComponent()
        let devBinary = bundleDir.appendingPathComponent("symseek")
        if FileManager.default.fileExists(atPath: devBinary.path) {
            return devBinary
        }
        let projectRoot = URL(fileURLWithPath: #filePath)
            .deletingLastPathComponent()
            .deletingLastPathComponent()
            .deletingLastPathComponent()
        let projectBinary = projectRoot.appendingPathComponent("symseek")
        if FileManager.default.fileExists(atPath: projectBinary.path) {
            return projectBinary
        }
        let fallbackPath = "/usr/local/bin/symseek"
        if FileManager.default.fileExists(atPath: fallbackPath) {
            return URL(fileURLWithPath: fallbackPath)
        }
        let armFallbackPath = "/opt/homebrew/bin/symseek"
        if FileManager.default.fileExists(atPath: armFallbackPath) {
            return URL(fileURLWithPath: armFallbackPath)
        }
        return nil
    }
}
