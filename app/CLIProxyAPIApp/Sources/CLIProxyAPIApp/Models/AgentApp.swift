import Foundation

struct AgentApp: Identifiable, Hashable {
    let id: String
    let name: String
    let bundleID: String?
    let appPath: String?
    let configPath: String
    let configType: ConfigType
    let defaultBaseURL: String?
    let defaultAPIKey: String?
    let isInstalled: Bool
    let iconPath: String?

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
        customBaseURL.isEmpty ? (defaultBaseURL ?? "") : customBaseURL
    }

    var displayKey: String {
        customAPIKey.isEmpty ? (defaultAPIKey ?? "") : customAPIKey
    }
}

extension AgentApp {
    static let supportedApps: [AgentApp] = [
        AgentApp(
            id: "codex",
            name: "Codex",
            bundleID: "com.openai.codex",
            appPath: "/Applications/Codex.app",
            configPath: NSHomeDirectory() + "/.codex/config.toml",
            configType: .toml,
            defaultBaseURL: "http://localhost:8317/v1",
            defaultAPIKey: "devin-test",
            isInstalled: FileManager.default.fileExists(atPath: "/Applications/Codex.app"),
            iconPath: "/Applications/Codex.app"
        ),
        AgentApp(
            id: "cursor",
            name: "Cursor",
            bundleID: "com.todesktop.230313mzl4w4u92",
            appPath: "/Applications/Cursor.app",
            configPath: NSHomeDirectory() + "/Library/Application Support/Cursor/User/settings.json",
            configType: .vscodeSettings,
            defaultBaseURL: "http://localhost:8317/v1",
            defaultAPIKey: "devin-test",
            isInstalled: FileManager.default.fileExists(atPath: "/Applications/Cursor.app"),
            iconPath: "/Applications/Cursor.app"
        ),
        AgentApp(
            id: "claude",
            name: "Claude Desktop",
            bundleID: "com.anthropic.claudefordesktop",
            appPath: "/Applications/Claude.app",
            configPath: NSHomeDirectory() + "/Library/Application Support/Claude-3p/configLibrary/preferences.json",
            configType: .json,
            defaultBaseURL: nil,
            defaultAPIKey: nil,
            isInstalled: FileManager.default.fileExists(atPath: "/Applications/Claude.app"),
            iconPath: "/Applications/Claude.app"
        ),
        AgentApp(
            id: "windsurf",
            name: "Windsurf",
            bundleID: "com.exafunction.windsurf",
            appPath: "/Applications/Windsurf.app",
            configPath: NSHomeDirectory() + "/.windsurf/config.json",
            configType: .json,
            defaultBaseURL: "http://localhost:8317/v1",
            defaultAPIKey: "devin-test",
            isInstalled: FileManager.default.fileExists(atPath: "/Applications/Windsurf.app"),
            iconPath: "/Applications/Windsurf.app"
        ),
        AgentApp(
            id: "cline",
            name: "Cline (VS Code)",
            bundleID: nil,
            appPath: nil,
            configPath: NSHomeDirectory() + "/Library/Application Support/Code/User/settings.json",
            configType: .vscodeSettings,
            defaultBaseURL: "http://localhost:8317/v1",
            defaultAPIKey: "devin-test",
            isInstalled: FileManager.default.fileExists(atPath: NSHomeDirectory() + "/Library/Application Support/Code/User/settings.json"),
            iconPath: "/Applications/Visual Studio Code.app"
        ),
        AgentApp(
            id: "continue",
            name: "Continue (VS Code)",
            bundleID: nil,
            appPath: nil,
            configPath: NSHomeDirectory() + "/.continue/config.yaml",
            configType: .yaml,
            defaultBaseURL: "http://localhost:8317/v1",
            defaultAPIKey: "devin-test",
            isInstalled: FileManager.default.fileExists(atPath: NSHomeDirectory() + "/.continue/config.yaml"),
            iconPath: "/Applications/Visual Studio Code.app"
        ),
    ]
}
