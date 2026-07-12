import AppKit
import Foundation

struct AgentApp: Identifiable, Hashable {
    let id: String
    let name: String
    let kind: Kind
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

    enum Kind: String {
        case application = "Application"
        case cli = "CLI"
    }

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
        if let bundleID = bundleID {
            return !NSRunningApplication.runningApplications(withBundleIdentifier: bundleID).isEmpty
        }
        if let cliPath = cliPath, !cliPath.isEmpty {
            return isProcessRunning(named: (cliPath as NSString).lastPathComponent)
        }
        return false
    }

    var effectiveIconPath: String? {
        if let appPath = appPath, FileManager.default.fileExists(atPath: appPath) {
            return appPath
        }
        return nil
    }

    private func isProcessRunning(named: String) -> Bool {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/pgrep")
        process.arguments = ["-x", named]
        let pipe = Pipe()
        process.standardOutput = pipe
        process.standardError = pipe
        try? process.run()
        process.waitUntilExit()
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        let output = String(data: data, encoding: .utf8) ?? ""
        return !output.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
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
            opencode,
        ]
    }

    private static func resolveCLI(_ name: String) -> String? {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/which")
        process.arguments = [name]
        let pipe = Pipe()
        process.standardOutput = pipe
        process.standardError = pipe
        try? process.run()
        process.waitUntilExit()
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        let path = String(data: data, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines)
        return path?.isEmpty == false ? path : nil
    }

    private static func appInstalled(_ path: String) -> Bool {
        FileManager.default.fileExists(atPath: path)
    }

    private static var codex: AgentApp {
        let installed = appInstalled("/Applications/Codex.app")
        return AgentApp(
            id: "codex",
            name: "Codex",
            kind: .application,
            bundleID: "com.openai.codex",
            appPath: "/Applications/Codex.app",
            cliPath: nil,
            configPath: NSHomeDirectory() + "/.codex/config.toml",
            configType: .toml,
            defaultBaseURL: "http://localhost:8317/v1",
            defaultAPIKey: "devin-test",
            isInstalled: installed
        )
    }

    private static var cursor: AgentApp {
        let installed = appInstalled("/Applications/Cursor.app")
        return AgentApp(
            id: "cursor",
            name: "Cursor",
            kind: .application,
            bundleID: "com.todesktop.230313mzl4w4u92",
            appPath: "/Applications/Cursor.app",
            cliPath: nil,
            configPath: NSHomeDirectory() + "/Library/Application Support/Cursor/User/settings.json",
            configType: .vscodeSettings,
            defaultBaseURL: "http://localhost:8317/v1",
            defaultAPIKey: "devin-test",
            isInstalled: installed
        )
    }

    private static var claude: AgentApp {
        let appInstalled = appInstalled("/Applications/Claude.app")
        let cliPath = resolveCLI("claude")
        return AgentApp(
            id: "claude",
            name: "Claude Code",
            kind: cliPath != nil ? .cli : .application,
            bundleID: "com.anthropic.claudefordesktop",
            appPath: "/Applications/Claude.app",
            cliPath: cliPath,
            configPath: NSHomeDirectory() + "/.claude/CLIProxyAPI.env",
            configType: .json,
            defaultBaseURL: "http://localhost:8317/v1",
            defaultAPIKey: "devin-test",
            isInstalled: appInstalled || cliPath != nil
        )
    }

    private static var windsurf: AgentApp {
        let installed = appInstalled("/Applications/Windsurf.app")
        return AgentApp(
            id: "windsurf",
            name: "Windsurf",
            kind: .application,
            bundleID: "com.exafunction.windsurf",
            appPath: "/Applications/Windsurf.app",
            cliPath: nil,
            configPath: NSHomeDirectory() + "/.windsurf/config.json",
            configType: .json,
            defaultBaseURL: "http://localhost:8317/v1",
            defaultAPIKey: "devin-test",
            isInstalled: installed
        )
    }

    private static var devin: AgentApp {
        let path = resolveCLI("devin")
        return AgentApp(
            id: "devin",
            name: "Devin",
            kind: .cli,
            bundleID: nil,
            appPath: nil,
            cliPath: path,
            configPath: NSHomeDirectory() + "/.devin/cli-proxy.env",
            configType: .json,
            defaultBaseURL: "http://localhost:8317/v1",
            defaultAPIKey: "devin-test",
            isInstalled: path != nil
        )
    }

    private static var opencode: AgentApp {
        let path = resolveCLI("opencode")
        return AgentApp(
            id: "opencode",
            name: "OpenCode",
            kind: .cli,
            bundleID: nil,
            appPath: nil,
            cliPath: path,
            configPath: NSHomeDirectory() + "/.opencode/config.json",
            configType: .json,
            defaultBaseURL: "http://localhost:8317/v1",
            defaultAPIKey: "devin-test",
            isInstalled: path != nil
        )
    }
}
