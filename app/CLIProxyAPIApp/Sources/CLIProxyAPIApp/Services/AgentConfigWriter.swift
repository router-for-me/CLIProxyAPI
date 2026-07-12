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
        case "cursor":
            try applyCursor(baseURL: baseURL, apiKey: apiKey)
        case "windsurf":
            try applyWindsurf(baseURL: baseURL, apiKey: apiKey)
        case "claude":
            try applyClaude(baseURL: baseURL, apiKey: apiKey)
        case "devin":
            try applyDevin(baseURL: baseURL, apiKey: apiKey)
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
        case "cursor":
            try resetCursor()
        case "windsurf":
            try resetWindsurf()
        case "claude":
            try resetClaude()
        case "devin":
            try resetDevin()
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

        var lines: [String] = []
        if let content = try? String(contentsOf: configURL, encoding: .utf8) {
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

    // MARK: - Claude

    private func applyClaude(baseURL: String, apiKey: String) throws {
        let envDir = URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent(".claude")
        try? FileManager.default.createDirectory(at: envDir, withIntermediateDirectories: true)
        let envURL = envDir.appendingPathComponent("cli-proxy.env")
        let content = "ANTHROPIC_BASE_URL=\(baseURL)\nANTHROPIC_API_KEY=\(apiKey)\n"
        try content.write(to: envURL, atomically: true, encoding: .utf8)
    }

    private func resetClaude() throws {
        let envURL = URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent(".claude/cli-proxy.env")
        try? FileManager.default.removeItem(at: envURL)
    }

    // MARK: - Devin

    private func applyDevin(baseURL: String, apiKey: String) throws {
        let envDir = URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent(".devin")
        try? FileManager.default.createDirectory(at: envDir, withIntermediateDirectories: true)
        let envURL = envDir.appendingPathComponent("cli-proxy.env")
        let content = "OPENAI_BASE_URL=\(baseURL)\nOPENAI_API_KEY=\(apiKey)\n"
        try content.write(to: envURL, atomically: true, encoding: .utf8)
    }

    private func resetDevin() throws {
        let envURL = URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent(".devin/cli-proxy.env")
        try? FileManager.default.removeItem(at: envURL)
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
