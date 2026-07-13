import AppKit
import Foundation

@MainActor
@Observable
final class CodexProviderController {
    private(set) var selectedMode: CodexProviderMode = .openai
    private(set) var isSwitching = false

    func refreshStatus(settings: BridgeSettingsStore) {
        Task {
            do {
                let output = try await self.readCodexConfig()
                self.selectedMode = Self.mode(fromStatusOutput: output, settings: settings)
            } catch {}
        }
    }

    func switchProvider(to mode: CodexProviderMode, settings: BridgeSettingsStore) {
        guard !self.isSwitching else { return }
        self.isSwitching = true

        Task {
            defer { self.isSwitching = false }

            do {
                try await self.writeCodexConfig(mode: mode, settings: settings)
                self.selectedMode = mode
                await self.restartCodex()
            } catch {}
        }
    }

    func restartCodex() async {
        let bundleIdentifier = "com.openai.codex"
        let workspace = NSWorkspace.shared
        let codexURL = workspace.urlForApplication(withBundleIdentifier: bundleIdentifier)

        let runningApps = NSRunningApplication.runningApplications(withBundleIdentifier: bundleIdentifier)
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

        if let codexURL {
            let configuration = NSWorkspace.OpenConfiguration()
            _ = try? await workspace.openApplication(at: codexURL, configuration: configuration)
        }
    }

    private func readCodexConfig() async throws -> String {
        let configURL = URL(fileURLWithPath: NSHomeDirectory())
            .appendingPathComponent(".codex/config.toml")

        guard FileManager.default.fileExists(atPath: configURL.path) else {
            return ""
        }
        return (try? String(contentsOf: configURL, encoding: .utf8)) ?? ""
    }

    private func writeCodexConfig(mode: CodexProviderMode, settings: BridgeSettingsStore) async throws {
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

        switch mode {
        case .devin:
            dict["openai_base_url"] = settings.endpointString
            dict["OPENAI_API_KEY"] = "devin-test"
        case .openai:
            dict.removeValue(forKey: "openai_base_url")
            dict.removeValue(forKey: "OPENAI_API_KEY")
        }

        var output = "# CLIProxyAPI auto-generated config\n"
        for (key, value) in dict.sorted(by: { $0.key < $1.key }) {
            output += "\(key) = \"\(value)\"\n"
        }

        try output.write(to: configURL, atomically: true, encoding: .utf8)
    }

    private static func mode(fromStatusOutput output: String, settings: BridgeSettingsStore) -> CodexProviderMode {
        if output.contains(settings.endpointString) {
            return .devin
        }
        return .openai
    }
}
