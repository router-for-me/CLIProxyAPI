import AppKit
import Foundation

struct AgentApp: Identifiable, Hashable {
    let id: String
    let name: String
    let bundleID: String?
    let appPath: String?
    let cliPath: String?
    let configPath: String
    let configType: ConfigType
    let defaultBaseURL: String
    let defaultAPIKey: String

    var isInstalled: Bool
    var isEnabled: Bool = false
    var customBaseURL: String = ""
    var customAPIKey: String = ""

    enum ConfigType {
        case json
        case toml
        case yaml
        case vscodeSettings
    }

    var displayURL: String {
        customBaseURL.isEmpty ? defaultBaseURL : customBaseURL
    }

    var displayKey: String {
        customAPIKey.isEmpty ? defaultAPIKey : customAPIKey
    }

    var isRunning: Bool {
        guard let bundleID = bundleID else { return false }
        return !NSRunningApplication.runningApplications(withBundleIdentifier: bundleID).isEmpty
    }
}

extension AgentApp {
    static func discover() -> [AgentApp] {
        return [
            codex,
            cursor,
            claude,
            windsurf,
            devin,
            aider,
            opencode,
        ]
    }

    private static let codex = AgentApp(
        id: "codex",
        name: "Codex",
        bundleID: "com.openai.codex",
        appPath: "/Applications/Codex.app",
        cliPath: nil,
        configPath: NSHomeDirectory() + "/.codex/config.toml",
        configType: .toml,
        defaultBaseURL: "http://localhost:8317/v1",
        defaultAPIKey: "devin-test",
        isInstalled: FileManager.default.fileExists(atPath: "/Applications/Codex.app")
    )

    private static let cursor = AgentApp(
        id: "cursor",
        name: "Cursor",
        bundleID: "com.todesktop.230313mzl4w4u92",
        appPath: "/Applications/Cursor.app",
        cliPath: nil,
        configPath: NSHomeDirectory() + "/Library/Application Support/Cursor/User/settings.json",
        configType: .vscodeSettings,
        defaultBaseURL: "http://localhost:8317/v1",
        defaultAPIKey: "devin-test",
        isInstalled: FileManager.default.fileExists(atPath: "/Applications/Cursor.app")
    )

    private static let claude = AgentApp(
        id: "claude",
        name: "Claude Code",
        bundleID: "com.anthropic.claudefordesktop",
        appPath: "/Applications/Claude.app",
        cliPath: "/usr/local/bin/claude",
        configPath: NSHomeDirectory() + "/.claude/CLIProxyAPI.env",
        configType: .json,
        defaultBaseURL: "http://localhost:8317/v1",
        defaultAPIKey: "devin-test",
        isInstalled: FileManager.default.fileExists(atPath: "/Applications/Claude.app") || FileManager.default.isExecutableFile(atPath: "/usr/local/bin/claude")
    )

    private static let windsurf = AgentApp(
        id: "windsurf",
        name: "Windsurf",
        bundleID: "com.exafunction.windsurf",
        appPath: "/Applications/Windsurf.app",
        cliPath: nil,
        configPath: NSHomeDirectory() + "/.windsurf/config.json",
        configType: .json,
        defaultBaseURL: "http://localhost:8317/v1",
        defaultAPIKey: "devin-test",
        isInstalled: FileManager.default.fileExists(atPath: "/Applications/Windsurf.app")
    )

    private static let devin = AgentApp(
        id: "devin",
        name: "Devin",
        bundleID: nil,
        appPath: nil,
        cliPath: "/usr/local/bin/devin",
        configPath: NSHomeDirectory() + "/.devin/cli-proxy.env",
        configType: .json,
        defaultBaseURL: "http://localhost:8317/v1",
        defaultAPIKey: "devin-test",
        isInstalled: FileManager.default.isExecutableFile(atPath: "/usr/local/bin/devin")
    )

    private static let aider = AgentApp(
        id: "aider",
        name: "Aider",
        bundleID: nil,
        appPath: nil,
        cliPath: "/usr/local/bin/aider",
        configPath: NSHomeDirectory() + "/.aider.conf.yml",
        configType: .yaml,
        defaultBaseURL: "http://localhost:8317/v1",
        defaultAPIKey: "devin-test",
        isInstalled: FileManager.default.isExecutableFile(atPath: "/usr/local/bin/aider")
    )

    private static let opencode = AgentApp(
        id: "opencode",
        name: "OpenCode",
        bundleID: nil,
        appPath: nil,
        cliPath: "/usr/local/bin/opencode",
        configPath: NSHomeDirectory() + "/.opencode/config.json",
        configType: .json,
        defaultBaseURL: "http://localhost:8317/v1",
        defaultAPIKey: "devin-test",
        isInstalled: FileManager.default.isExecutableFile(atPath: "/usr/local/bin/opencode")
    )
}
