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

    func applyCLIProxy(to app: AgentApp, baseURL: String, apiKey: String) throws {
        switch app.id {
        case "codex":
            try applyCodex(baseURL: baseURL, apiKey: apiKey)
        case "cursor":
            try applyCursor(baseURL: baseURL, apiKey: apiKey)
        case "windsurf":
            try applyWindsurf(baseURL: baseURL, apiKey: apiKey)
        case "cline":
            try applyCline(baseURL: baseURL, apiKey: apiKey)
        case "continue":
            try applyContinue(baseURL: baseURL, apiKey: apiKey)
        case "claude":
            try applyClaude(baseURL: baseURL, apiKey: apiKey)
        default:
            throw AgentConfigError.unsupportedConfigType
        }
    }

    func resetToDefault(app: AgentApp) throws {
        switch app.id {
        case "codex":
            try resetCodex()
        case "cursor":
            try resetCursor()
        case "windsurf":
            try resetWindsurf()
        case "cline":
            try resetCline()
        case "continue":
            try resetContinue()
        case "claude":
            try resetClaude()
        default:
            throw AgentConfigError.unsupportedConfigType
        }
    }

    // MARK: - Codex

    private func applyCodex(baseURL: String, apiKey: String) throws {
        let codexDir = URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent(".codex")
        try? FileManager.default.createDirectory(at: codexDir, withIntermediateDirectories: true)
        let configURL = codexDir.appendingPathComponent("config.toml")

        var lines: [String] = []
        if FileManager.default.fileExists(atPath: configURL.path),
           let content = try? String(contentsOf: configURL, encoding: .utf8) {
            lines = content.components(separatedBy: .newlines)
        }

        var dict = [String: String]()
        for line in lines {
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            if trimmed.isEmpty || trimmed.hasPrefix("#") { continue }
            let parts = trimmed.split(separator: "=", maxSplits: 1).map { $0.trimmingCharacters(in: .whitespaces) }
            if parts.count == 2 {
                dict[parts[0]] = parts[1].trimmingCharacters(in: CharacterSet(charactersIn: "\"'"))
            }
        }

        dict["openai_base_url"] = baseURL
        dict["OPENAI_API_KEY"] = apiKey

        var output = "# CLIProxyAPI auto-generated config\n"
        for (key, value) in dict.sorted(by: { $0.key < $1.key }) {
            output += "\(key) = \"\(value)\"\n"
        }

        try output.write(to: configURL, atomically: true, encoding: .utf8)
    }

    private func resetCodex() throws {
        let configURL = URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent(".codex/config.toml")
        guard FileManager.default.fileExists(atPath: configURL.path) else { return }
        var lines = try String(contentsOf: configURL, encoding: .utf8).components(separatedBy: .newlines)
        lines = lines.filter { line in
            let key = line.split(separator: "=", maxSplits: 1).first?.trimmingCharacters(in: .whitespaces) ?? ""
            return key != "openai_base_url" && key != "OPENAI_API_KEY"
        }
        try lines.joined(separator: "\n").write(to: configURL, atomically: true, encoding: .utf8)
    }

    // MARK: - Cursor

    private func applyCursor(baseURL: String, apiKey: String) throws {
        let configURL = URL(fileURLWithPath: NSHomeDirectory())
            .appendingPathComponent("Library/Application Support/Cursor/User/settings.json")
        var config = try readJSON(configURL)
        config["openai.baseUrl"] = baseURL
        config["openai.apiKey"] = apiKey
        try writeJSON(config, to: configURL)
    }

    private func resetCursor() throws {
        let configURL = URL(fileURLWithPath: NSHomeDirectory())
            .appendingPathComponent("Library/Application Support/Cursor/User/settings.json")
        var config = try readJSON(configURL)
        config.removeValue(forKey: "openai.baseUrl")
        config.removeValue(forKey: "openai.apiKey")
        try writeJSON(config, to: configURL)
    }

    // MARK: - Windsurf

    private func applyWindsurf(baseURL: String, apiKey: String) throws {
        let configURL = URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent(".windsurf/config.json")
        var config = try readJSON(configURL)
        config["ai.api_base"] = baseURL
        config["ai.api_key"] = apiKey
        try writeJSON(config, to: configURL)
    }

    private func resetWindsurf() throws {
        let configURL = URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent(".windsurf/config.json")
        var config = try readJSON(configURL)
        config.removeValue(forKey: "ai.api_base")
        config.removeValue(forKey: "ai.api_key")
        try writeJSON(config, to: configURL)
    }

    // MARK: - Cline

    private func applyCline(baseURL: String, apiKey: String) throws {
        let configURL = URL(fileURLWithPath: NSHomeDirectory())
            .appendingPathComponent("Library/Application Support/Code/User/settings.json")
        var config = try readJSON(configURL)
        config["cline.apiProvider"] = "openai"
        config["cline.openAiBaseUrl"] = baseURL
        config["cline.openAiApiKey"] = apiKey
        config["cline.apiModelId"] = "auto"
        try writeJSON(config, to: configURL)
    }

    private func resetCline() throws {
        let configURL = URL(fileURLWithPath: NSHomeDirectory())
            .appendingPathComponent("Library/Application Support/Code/User/settings.json")
        var config = try readJSON(configURL)
        config.removeValue(forKey: "cline.apiProvider")
        config.removeValue(forKey: "cline.openAiBaseUrl")
        config.removeValue(forKey: "cline.openAiApiKey")
        config.removeValue(forKey: "cline.apiModelId")
        try writeJSON(config, to: configURL)
    }

    // MARK: - Continue

    private func applyContinue(baseURL: String, apiKey: String) throws {
        let configURL = URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent(".continue/config.yaml")
        var content = (try? String(contentsOf: configURL, encoding: .utf8)) ?? ""
        let entry = """
        models:
          - name: CLIProxyAPI
            provider: openai
            model: auto
            apiBase: \(baseURL)
            apiKey: \(apiKey)
        """
        if content.isEmpty {
            content = entry
        } else if content.contains("CLIProxyAPI") {
            content = content.replacingOccurrences(of: #"apiBase:.*"#, with: "apiBase: \(baseURL)", options: .regularExpression)
            content = content.replacingOccurrences(of: #"apiKey:.*"#, with: "apiKey: \(apiKey)", options: .regularExpression)
        } else {
            content += "\n" + entry
        }
        try content.write(to: configURL, atomically: true, encoding: .utf8)
    }

    private func resetContinue() throws {
        let configURL = URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent(".continue/config.yaml")
        guard FileManager.default.fileExists(atPath: configURL.path) else { return }
        var content = try String(contentsOf: configURL, encoding: .utf8)
        if let range = content.range(of: "models:") {
            let tail = content[range.upperBound...]
            if let nextSection = tail.range(of: "\n[a-zA-Z]", options: .regularExpression) {
                content = String(content[..<range.lowerBound]) + String(tail[nextSection.upperBound...])
            } else {
                content = String(content[..<range.lowerBound])
            }
        }
        try content.write(to: configURL, atomically: true, encoding: .utf8)
    }

    // MARK: - Claude

    private func applyClaude(baseURL: String, apiKey: String) throws {
        // Claude Desktop third-party inference requires plist/MDM or env vars.
        // We write a .env file for local use and show a hint in the UI.
        let envURL = URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent(".claude/cli-proxy.env")
        try? FileManager.default.createDirectory(at: envURL.deletingLastPathComponent(), withIntermediateDirectories: true)
        let content = "ANTHROPIC_BASE_URL=\(baseURL)\nANTHROPIC_API_KEY=\(apiKey)\n"
        try content.write(to: envURL, atomically: true, encoding: .utf8)
    }

    private func resetClaude() throws {
        let envURL = URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent(".claude/cli-proxy.env")
        try? FileManager.default.removeItem(at: envURL)
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
}
