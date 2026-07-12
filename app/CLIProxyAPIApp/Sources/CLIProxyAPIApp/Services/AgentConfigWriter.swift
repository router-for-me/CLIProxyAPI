import AppKit
import Foundation

enum AgentConfigError: LocalizedError {
    case unsupportedConfigType
    case fileReadError
    case fileWriteError
    case invalidFormat

    var errorDescription: String? {
        switch self {
        case .unsupportedConfigType: return "Unsupported config type"
        case .fileReadError: return "Failed to read config file"
        case .fileWriteError: return "Failed to write config file"
        case .invalidFormat: return "Invalid config format"
        }
    }
}

@MainActor
final class AgentConfigWriter {
    static let shared = AgentConfigWriter()

    func applyCLIProxy(to app: AgentApp, baseURL: String, apiKey: String) async throws {
        switch app.id {
        case "codex":
            try await applyCodex(baseURL: baseURL, apiKey: apiKey)
        case "cline":
            try applyCline(baseURL: baseURL, apiKey: apiKey)
        case "opencode":
            try applyOpencode(baseURL: baseURL, apiKey: apiKey)
        default:
            throw AgentConfigError.unsupportedConfigType
        }

        if app.isRunning, let bundleID = app.bundleID {
            await restartApp(bundleID: bundleID)
        }
    }

    func resetToDefault(app: AgentApp) async throws {
        switch app.id {
        case "codex":
            try resetCodex()
        case "cline":
            try resetCline()
        case "opencode":
            try resetOpencode()
        default:
            throw AgentConfigError.unsupportedConfigType
        }

        if app.isRunning, let bundleID = app.bundleID {
            await restartApp(bundleID: bundleID)
        }
    }

    // MARK: - Codex

    private func applyCodex(baseURL: String, apiKey: String) async throws {
        let codexDir = URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent(".codex")
        try? FileManager.default.createDirectory(at: codexDir, withIntermediateDirectories: true)
        let configURL = codexDir.appendingPathComponent("config.toml")

        var lines = (try? String(contentsOf: configURL, encoding: .utf8))?.components(separatedBy: .newlines) ?? []

        func replaceOrAppend(key: String, value: String) {
            var found = false
            for (index, line) in lines.enumerated() {
                let trimmed = line.trimmingCharacters(in: .whitespaces)
                guard !trimmed.isEmpty, !trimmed.hasPrefix("#") else { continue }
                let parts = trimmed.split(separator: "=", maxSplits: 1).map { $0.trimmingCharacters(in: .whitespaces) }
                if parts.count == 2, parts[0] == key {
                    lines[index] = "\(key) = \"\(value)\""
                    found = true
                    break
                }
            }
            if !found {
                lines.append("\(key) = \"\(value)\"")
            }
        }

        // Save the current model so we can restore it on reset.
        let originalModel = lines.first { line in
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            guard !trimmed.isEmpty, !trimmed.hasPrefix("#") else { return false }
            let parts = trimmed.split(separator: "=", maxSplits: 1).map { $0.trimmingCharacters(in: .whitespaces) }
            return parts.count == 2 && parts[0] == "model"
        }
        if let originalModel = originalModel, let value = originalModel.split(separator: "=", maxSplits: 1).last {
            let cleaned = value.trimmingCharacters(in: .whitespaces).trimmingCharacters(in: CharacterSet(charactersIn: "\"'"))
            UserDefaults.standard.set(cleaned, forKey: "codexOriginalModel")
        }

        // Build a model catalog JSON containing only the models exposed in CLIProxy.
        let catalogPath = codexDir.appendingPathComponent("cli-proxy-model-catalog.json")
        let slugs = try await buildCodexModelCatalog(at: catalogPath, baseURL: baseURL, apiKey: apiKey)

        // Set a fixed model so Codex Desktop has something to use. The model
        // picker will show it as "Custom" but requests will go to CLIProxy.
        if let firstModel = slugs.first {
            replaceOrAppend(key: "model", value: firstModel)
        }

        replaceOrAppend(key: "model_catalog_json", value: catalogPath.path)
        replaceOrAppend(key: "openai_base_url", value: baseURL)
        replaceOrAppend(key: "OPENAI_API_KEY", value: apiKey)

        try lines.joined(separator: "\n").write(to: configURL, atomically: true, encoding: .utf8)
    }

    private func resetCodex() throws {
        let configURL = URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent(".codex/config.toml")
        guard FileManager.default.fileExists(atPath: configURL.path) else { return }
        var lines = try String(contentsOf: configURL, encoding: .utf8).components(separatedBy: .newlines)

        let originalModel = UserDefaults.standard.string(forKey: "codexOriginalModel")
        if let originalModel = originalModel, !originalModel.isEmpty {
            let hasModel = lines.contains { line in
                let trimmed = line.trimmingCharacters(in: .whitespaces)
                guard !trimmed.isEmpty, !trimmed.hasPrefix("#") else { return false }
                let parts = trimmed.split(separator: "=", maxSplits: 1).map { $0.trimmingCharacters(in: .whitespaces) }
                return parts.count == 2 && parts[0] == "model"
            }
            if !hasModel {
                lines.append("model = \"\(originalModel)\"")
            }
            UserDefaults.standard.removeObject(forKey: "codexOriginalModel")
        }

        lines = lines.filter { line in
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            guard !trimmed.isEmpty, !trimmed.hasPrefix("#") else { return true }
            let parts = trimmed.split(separator: "=", maxSplits: 1).map { $0.trimmingCharacters(in: .whitespaces) }
            return parts.count < 2 || (parts[0] != "openai_base_url" && parts[0] != "OPENAI_API_KEY" && parts[0] != "model_catalog_json")
        }

        let catalogPath = URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent(".codex/cli-proxy-model-catalog.json")
        try? FileManager.default.removeItem(at: catalogPath)

        try lines.joined(separator: "\n").write(to: configURL, atomically: true, encoding: .utf8)
    }

    private func buildCodexModelCatalog(at catalogURL: URL, baseURL: String, apiKey: String) async throws -> [String] {
        let exposed = UserDefaults.standard.array(forKey: "exposedModelsCache") as? [String] ?? []

        // If no models are exposed, query the runtime /v1/models endpoint.
        let slugs: [String]
        if exposed.isEmpty {
            let url = URL(string: baseURL + "/models") ?? catalogURL.deletingLastPathComponent().appendingPathComponent("models")
            var request = URLRequest(url: url)
            request.setValue("Bearer \(apiKey)", forHTTPHeaderField: "Authorization")
            let (data, _) = try await URLSession.shared.data(for: request)
            struct ModelsResponse: Codable {
                let data: [ModelEntry]
                struct ModelEntry: Codable {
                    let id: String
                }
            }
            let decoded = try JSONDecoder().decode(ModelsResponse.self, from: data)
            slugs = decoded.data.map { $0.id }
        } else {
            slugs = exposed
        }

        var models: [[String: Any]] = []
        for slug in slugs {
            let displayName = slug.split(separator: "/").last.map(String.init) ?? slug
            models.append([
                "slug": slug,
                "display_name": displayName,
                "description": "CLIProxy model \(slug)",
                "shell_type": "shell_command",
                "visibility": "list",
                "supported_in_api": true,
                "priority": 0,
                "context_window": 128000,
                "max_context_window": 128000,
                "effective_context_window_percent": 90,
                "input_modalities": ["text"],
                "supports_parallel_tool_calls": true,
                "supports_search_tool": true,
                "supports_image_detail_original": false,
                "supports_reasoning_summaries": false,
                "support_verbosity": true,
                "apply_patch_tool_type": "freeform",
                "web_search_tool_type": "text",
                "supported_reasoning_levels": [
                    ["effort": "low", "description": "Fast"],
                    ["effort": "medium", "description": "Balanced"],
                    ["effort": "high", "description": "Deep"],
                ],
                "service_tiers": [
                    ["id": "default", "name": "Standard", "description": "Standard"],
                ],
                "truncation_policy": [
                    "mode": "tokens",
                    "limit": 10000,
                ],
            ])
        }

        let catalog: [String: Any] = ["models": models]
        let data = try JSONSerialization.data(withJSONObject: catalog, options: [.prettyPrinted, .sortedKeys])
        try data.write(to: catalogURL, options: [.atomic])

        return slugs
    }

    // MARK: - Cline

    private func applyCline(baseURL: String, apiKey: String) throws {
        let configURL = URL(fileURLWithPath: NSHomeDirectory())
            .appendingPathComponent("Library/Application Support/Code/User/settings.json")
        var config = try readJSON(configURL)
        config["cline.apiProvider"] = "openai-compatible"
        config["cline.openAiCompatible.baseUrl"] = baseURL
        config["cline.openAiCompatible.apiKey"] = apiKey
        config["cline.openAiCompatible.modelId"] = "auto"
        try writeJSON(config, to: configURL)
    }

    private func resetCline() throws {
        let configURL = URL(fileURLWithPath: NSHomeDirectory())
            .appendingPathComponent("Library/Application Support/Code/User/settings.json")
        var config = try readJSON(configURL)
        config.removeValue(forKey: "cline.apiProvider")
        config.removeValue(forKey: "cline.openAiCompatible.baseUrl")
        config.removeValue(forKey: "cline.openAiCompatible.apiKey")
        config.removeValue(forKey: "cline.openAiCompatible.modelId")
        try writeJSON(config, to: configURL)
    }

    // MARK: - OpenCode

    private func applyOpencode(baseURL: String, apiKey: String) throws {
        let configURL = URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent(".opencode/config.json")
        var config = try readJSON(configURL)
        config["api_base"] = baseURL
        config["api_key"] = apiKey
        try writeJSON(config, to: configURL)
    }

    private func resetOpencode() throws {
        let configURL = URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent(".opencode/config.json")
        var config = try readJSON(configURL)
        config.removeValue(forKey: "api_base")
        config.removeValue(forKey: "api_key")
        try writeJSON(config, to: configURL)
    }

    // MARK: - Helpers

    private func readJSON(_ url: URL) throws -> [String: Any] {
        if FileManager.default.fileExists(atPath: url.path),
           let data = try? Data(contentsOf: url),
           let json = try JSONSerialization.jsonObject(with: data) as? [String: Any] {
            return json
        }
        return [:]
    }

    private func writeJSON(_ value: [String: Any], to url: URL) throws {
        try? FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
        let data = try JSONSerialization.data(withJSONObject: value, options: [.prettyPrinted, .sortedKeys])
        try data.write(to: url)
    }

    private func restartApp(bundleID: String) async {
        let workspace = NSWorkspace.shared
        let url = workspace.urlForApplication(withBundleIdentifier: bundleID)

        let runningApps = NSRunningApplication.runningApplications(withBundleIdentifier: bundleID)
        for app in runningApps {
            app.terminate()
        }

        for _ in 0..<20 {
            if runningApps.allSatisfy(\.isTerminated) { break }
            try? await Task.sleep(for: .milliseconds(150))
        }

        for app in runningApps where !app.isTerminated {
            app.forceTerminate()
        }

        if let url {
            let configuration = NSWorkspace.OpenConfiguration()
            _ = try? await workspace.openApplication(at: url, configuration: configuration)
        }
    }
}
