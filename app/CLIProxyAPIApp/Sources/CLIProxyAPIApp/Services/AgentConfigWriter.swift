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
            try applyCodex(baseURL: baseURL, apiKey: apiKey)
        case "continue":
            try applyContinue(baseURL: baseURL, apiKey: apiKey)
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
        case "continue":
            try resetContinue()
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

    private func applyCodex(baseURL: String, apiKey: String) throws {
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

        replaceOrAppend(key: "openai_base_url", value: baseURL)
        replaceOrAppend(key: "OPENAI_API_KEY", value: apiKey)

        try lines.joined(separator: "\n").write(to: configURL, atomically: true, encoding: .utf8)
    }

    private func resetCodex() throws {
        let configURL = URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent(".codex/config.toml")
        guard FileManager.default.fileExists(atPath: configURL.path) else { return }
        var lines = try String(contentsOf: configURL, encoding: .utf8).components(separatedBy: .newlines)
        lines = lines.filter { line in
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            guard !trimmed.isEmpty, !trimmed.hasPrefix("#") else { return true }
            let parts = trimmed.split(separator: "=", maxSplits: 1).map { $0.trimmingCharacters(in: .whitespaces) }
            return parts.count < 2 || (parts[0] != "openai_base_url" && parts[0] != "OPENAI_API_KEY")
        }
        try lines.joined(separator: "\n").write(to: configURL, atomically: true, encoding: .utf8)
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

    // MARK: - Continue

    private func applyContinue(baseURL: String, apiKey: String) throws {
        let configURL = URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent(".continue/config.yaml")
        let configYamlExists = FileManager.default.fileExists(atPath: configURL.path)
        let configURLJson = URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent(".continue/config.json")

        if configYamlExists {
            var content = (try? String(contentsOf: configURL, encoding: .utf8)) ?? ""
            let entry = """
            models:
              - name: CLIProxyAPI
                provider: openai
                model: auto
                apiBase: \(baseURL)
                apiKey: \(apiKey)
            """
            if content.contains("CLIProxyAPI") {
                content = content.replacingOccurrences(of: #"apiBase:.*"#, with: "apiBase: \(baseURL)", options: .regularExpression)
                content = content.replacingOccurrences(of: #"apiKey:.*"#, with: "apiKey: \(apiKey)", options: .regularExpression)
            } else if content.contains("models:") {
                content = content.replacingOccurrences(of: "models:", with: "models:\n  - name: CLIProxyAPI\n    provider: openai\n    model: auto\n    apiBase: \(baseURL)\n    apiKey: \(apiKey)", options: .literal, range: content.range(of: "models:"))
            } else {
                content += "\n" + entry
            }
            try content.write(to: configURL, atomically: true, encoding: .utf8)
        } else {
            var config = try readJSON(configURLJson)
            var models = config["models"] as? [[String: Any]] ?? []
            models.removeAll { ($0["name"] as? String) == "CLIProxyAPI" }
            models.append([
                "name": "CLIProxyAPI",
                "provider": "openai",
                "model": "auto",
                "apiBase": baseURL,
                "apiKey": apiKey,
            ])
            config["models"] = models
            try writeJSON(config, to: configURLJson)
        }
    }

    private func resetContinue() throws {
        let configURL = URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent(".continue/config.yaml")
        if FileManager.default.fileExists(atPath: configURL.path) {
            var content = try String(contentsOf: configURL, encoding: .utf8)
            let pattern = #"\n?  - name: CLIProxyAPI\n(?:    .+\n?)*"#
            if let range = content.range(of: pattern, options: .regularExpression) {
                content.removeSubrange(range)
            }
            try content.write(to: configURL, atomically: true, encoding: .utf8)
        } else {
            let configURLJson = URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent(".continue/config.json")
            var config = try readJSON(configURLJson)
            var models = config["models"] as? [[String: Any]] ?? []
            models.removeAll { ($0["name"] as? String) == "CLIProxyAPI" }
            config["models"] = models
            try writeJSON(config, to: configURLJson)
        }
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
