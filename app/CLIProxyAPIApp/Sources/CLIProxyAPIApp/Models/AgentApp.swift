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
        // Only list agents where a full OpenAI-compatible base URL + model swap is supported.
        return [
            codex,
            `continue`,
            cline,
            opencode,
        ]
    }

    private static func resolveCLI(_ name: String) -> String? {
        for shell in ["/bin/zsh", "/bin/bash", "/bin/sh"] {
            let process = Process()
            process.executableURL = URL(fileURLWithPath: shell)
            process.arguments = ["-lc", "which \(name)"]
            let pipe = Pipe()
            process.standardOutput = pipe
            process.standardError = pipe
            try? process.run()
            process.waitUntilExit()
            let data = pipe.fileHandleForReading.readDataToEndOfFile()
            let path = String(data: data, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines)
            if let path = path, !path.isEmpty, !path.contains("not found") {
                return path
            }
        }
        return nil
    }

    private static func appInstalled(_ path: String) -> Bool {
        FileManager.default.fileExists(atPath: path)
    }

    private static func findApp(named: String) -> String? {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/mdfind")
        process.arguments = ["kMDItemDisplayName == '\(named).app'c"]
        let pipe = Pipe()
        process.standardOutput = pipe
        process.standardError = pipe
        try? process.run()
        process.waitUntilExit()
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        let output = String(data: data, encoding: .utf8) ?? ""
        return output.components(separatedBy: .newlines).first { !$0.isEmpty }
    }

    private static func findAppByBundleID(_ bundleID: String) -> String? {
        let process = Process()
        process.executableURL = URL(fileURLWithPath: "/usr/bin/mdfind")
        process.arguments = ["kMDItemCFBundleIdentifier == '\(bundleID)'"]
        let pipe = Pipe()
        process.standardOutput = pipe
        process.standardError = pipe
        try? process.run()
        process.waitUntilExit()
        let data = pipe.fileHandleForReading.readDataToEndOfFile()
        let output = String(data: data, encoding: .utf8) ?? ""
        return output.components(separatedBy: .newlines).first { !$0.isEmpty }
    }

    private static var codex: AgentApp {
        let path = findAppByBundleID("com.openai.codex") ?? findApp(named: "Codex") ?? "/Applications/Codex.app"
        let running = !NSRunningApplication.runningApplications(withBundleIdentifier: "com.openai.codex").isEmpty
        let installed = appInstalled(path) || running
        return AgentApp(
            id: "codex",
            name: "Codex",
            kind: .application,
            bundleID: "com.openai.codex",
            appPath: path,
            cliPath: nil,
            configPath: NSHomeDirectory() + "/.codex/config.toml",
            configType: .toml,
            defaultBaseURL: "http://localhost:8317/v1",
            defaultAPIKey: "devin-test",
            isInstalled: installed
        )
    }

    private static var `continue`: AgentApp {
        let path = resolveCLI("continue")
        let configYaml = NSHomeDirectory() + "/.continue/config.yaml"
        let configJson = NSHomeDirectory() + "/.continue/config.json"
        let configPath = FileManager.default.fileExists(atPath: configYaml) ? configYaml : configJson
        return AgentApp(
            id: "continue",
            name: "Continue",
            kind: .cli,
            bundleID: nil,
            appPath: nil,
            cliPath: path,
            configPath: configPath,
            configType: configPath.hasSuffix(".yaml") ? .yaml : .json,
            defaultBaseURL: "http://localhost:8317/v1",
            defaultAPIKey: "devin-test",
            isInstalled: path != nil || FileManager.default.fileExists(atPath: configPath)
        )
    }

    private static var cline: AgentApp {
        let vscodePath = findAppByBundleID("com.microsoft.VSCode") ?? findApp(named: "Visual Studio Code") ?? "/Applications/Visual Studio Code.app"
        let configURL = NSHomeDirectory() + "/Library/Application Support/Code/User/settings.json"
        return AgentApp(
            id: "cline",
            name: "Cline",
            kind: .application,
            bundleID: "com.microsoft.VSCode",
            appPath: vscodePath,
            cliPath: nil,
            configPath: configURL,
            configType: .vscodeSettings,
            defaultBaseURL: "http://localhost:8317/v1",
            defaultAPIKey: "devin-test",
            isInstalled: appInstalled(vscodePath) || FileManager.default.fileExists(atPath: configURL)
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
