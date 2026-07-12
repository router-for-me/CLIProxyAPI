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

        // Save the original config once so we can fully restore it later.
        ensureCodexBackup(at: configURL)

        let catalogPath = codexDir.appendingPathComponent("cli-proxy-model-catalog.json")
        do {
            try await buildCodexModelCatalog(at: catalogPath, baseURL: baseURL, apiKey: apiKey)
            NSLog("applyCodex: built catalog at %@", catalogPath.path)
        } catch {
            NSLog("applyCodex: failed to build catalog: %@", error.localizedDescription)
            throw error
        }

        var editor = try CodexConfigEditor(url: configURL)

        let exposed = UserDefaults.standard.array(forKey: "exposedModelsCache") as? [String] ?? []
        let firstModel = exposed.first ?? "devin/glm-5.2"

        editor.setTopLevelString(key: "model_provider", value: "openai")
        editor.setTopLevelString(key: "model", value: firstModel)
        editor.setTopLevelString(key: "openai_base_url", value: baseURL)
        editor.setTopLevelString(key: "OPENAI_API_KEY", value: apiKey)
        editor.setTopLevelString(key: "model_catalog_json", value: catalogPath.path)

        editor.setFeatureBool(key: "remote_compaction_v2", value: true)

        editor.setProviderBlock(name: "devin-subscription", baseURL: baseURL, apiKey: apiKey)

        try editor.write(to: configURL)
        NSLog("applyCodex: wrote config to %@", configURL.path)
    }

    private func resetCodex() throws {
        let configURL = URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent(".codex/config.toml")
        guard FileManager.default.fileExists(atPath: configURL.path) else { return }

        let backupURL = configURL.appendingPathExtension("cliproxy-backup")
        if FileManager.default.fileExists(atPath: backupURL.path) {
            try FileManager.default.removeItem(at: configURL)
            try FileManager.default.copyItem(at: backupURL, to: configURL)
        }

        let catalogPath = URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent(".codex/cli-proxy-model-catalog.json")
        try? FileManager.default.removeItem(at: catalogPath)

        NSLog("resetCodex: restored config from %@", backupURL.path)
    }

    private func buildCodexModelCatalog(at catalogURL: URL, baseURL: String, apiKey: String) async throws {
        let openaiURL = URL(string: baseURL + "/models") ?? catalogURL.deletingLastPathComponent().appendingPathComponent("models")
        let codexURL = URL(string: baseURL + "/models?client_version=1") ?? openaiURL

        let openaiData = try await fetchJSON(url: openaiURL, apiKey: apiKey)
        let codexData = try await fetchJSON(url: codexURL, apiKey: apiKey)

        let openaiModels = openaiData["data"] as? [[String: Any]] ?? []
        let codexModels = codexData["models"] as? [[String: Any]] ?? []

        let catalog: [String: Any] = [
            "object": "list",
            "data": openaiModels,
            "models": codexModels
        ]

        let data = try JSONSerialization.data(withJSONObject: catalog, options: [.prettyPrinted, .sortedKeys])
        try data.write(to: catalogURL, options: [.atomic])
    }

    private func fetchJSON(url: URL, apiKey: String) async throws -> [String: Any] {
        var request = URLRequest(url: url)
        request.setValue("Bearer \(apiKey)", forHTTPHeaderField: "Authorization")
        let (data, response) = try await URLSession.shared.data(for: request)
        if let http = response as? HTTPURLResponse, !(200..<300).contains(http.statusCode) {
            throw AgentConfigError.fileReadError
        }
        guard let json = try JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            throw AgentConfigError.invalidFormat
        }
        return json
    }

    private func ensureCodexBackup(at configURL: URL) {
        let backupURL = configURL.appendingPathExtension("cliproxy-backup")
        guard !FileManager.default.fileExists(atPath: backupURL.path) else { return }
        guard FileManager.default.fileExists(atPath: configURL.path) else { return }
        try? FileManager.default.copyItem(at: configURL, to: backupURL)
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

// MARK: - Codex TOML Editor

private struct CodexConfigEditor {
    private var preamble: [String]
    private var sections: [String: [String]]
    private var sectionOrder: [String]

    init(url: URL) throws {
        let content = (try? String(contentsOf: url, encoding: .utf8)) ?? ""
        let lines = content.components(separatedBy: .newlines)

        var preamble: [String] = []
        var sections: [String: [String]] = [:]
        var sectionOrder: [String] = []
        var currentSection: String? = nil

        for line in lines {
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            if trimmed.hasPrefix("[") && trimmed.hasSuffix("]") {
                let header = trimmed
                currentSection = header
                if sections[header] == nil {
                    sections[header] = []
                    sectionOrder.append(header)
                }
                sections[header]?.append(line)
            } else if let section = currentSection {
                sections[section]?.append(line)
            } else {
                preamble.append(line)
            }
        }

        self.preamble = preamble
        self.sections = sections
        self.sectionOrder = sectionOrder
    }

    mutating func setTopLevelString(key: String, value: String) {
        var found = false
        for (index, line) in preamble.enumerated() {
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            guard !trimmed.isEmpty, !trimmed.hasPrefix("#") else { continue }
            if trimmed.hasPrefix("[") { continue }
            let parts = trimmed.split(separator: "=", maxSplits: 1).map { $0.trimmingCharacters(in: .whitespaces) }
            if parts.count == 2, parts[0] == key {
                preamble[index] = "\(key) = \"\(value)\""
                found = true
                break
            }
        }
        if !found {
            preamble.append("\(key) = \"\(value)\"")
        }
    }

    mutating func removeTopLevelKey(key: String) {
        preamble.removeAll { line in
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            guard !trimmed.isEmpty, !trimmed.hasPrefix("#") else { return false }
            let parts = trimmed.split(separator: "=", maxSplits: 1).map { $0.trimmingCharacters(in: .whitespaces) }
            return parts.count == 2 && parts[0] == key
        }
    }

    mutating func setFeatureBool(key: String, value: Bool) {
        let header = "[features]"
        var body = sections[header] ?? [header]
        if sections[header] == nil {
            sections[header] = body
            sectionOrder.append(header)
        }

        let valueText = value ? "true" : "false"
        var found = false
        for (index, line) in body.enumerated() {
            let trimmed = line.trimmingCharacters(in: .whitespaces)
            guard !trimmed.isEmpty, !trimmed.hasPrefix("#"), !trimmed.hasPrefix("[") else { continue }
            let parts = trimmed.split(separator: "=", maxSplits: 1).map { $0.trimmingCharacters(in: .whitespaces) }
            if parts.count == 2, parts[0] == key {
                body[index] = "\(key) = \(valueText)"
                found = true
                break
            }
        }
        if !found {
            body.append("\(key) = \(valueText)")
        }
        sections[header] = body
    }

    mutating func setProviderBlock(name: String, baseURL: String, apiKey: String) {
        let header = "[model_providers.\(name)]"
        let body = [
            header,
            "name = \"\(name)\"",
            "base_url = \"\(baseURL)\"",
            "api_key = \"\(apiKey)\"",
            "wire_api = \"responses\"",
        ]

        if sections[header] != nil {
            sections[header] = body
            return
        }

        sections[header] = body
        // Place the provider block before the first [plugins] or [mcp_servers] section
        // or at the end if neither exists.
        if let firstPlugins = sectionOrder.firstIndex(where: { $0.hasPrefix("[plugins.") || $0 == "[mcp_servers]" || $0.hasPrefix("[mcp_servers.") }) {
            sectionOrder.insert(header, at: firstPlugins)
        } else {
            sectionOrder.append(header)
        }
    }

    func write(to url: URL) throws {
        var output = preamble.joined(separator: "\n")
        for header in sectionOrder {
            if let body = sections[header] {
                output += "\n" + body.joined(separator: "\n")
            }
        }
        try output.write(to: url, atomically: true, encoding: .utf8)
    }
}
