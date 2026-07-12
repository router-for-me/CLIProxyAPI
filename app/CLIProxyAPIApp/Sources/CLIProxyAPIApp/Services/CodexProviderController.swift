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
            .appendingPathComponent(".codex")
            .appendingPathComponent("config.json")

        guard FileManager.default.fileExists(atPath: configURL.path) else {
            return ""
        }
        return (try? String(contentsOf: configURL, encoding: .utf8)) ?? ""
    }

    private func writeCodexConfig(mode: CodexProviderMode, settings: BridgeSettingsStore) async throws {
        let codexDir = URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent(".codex")
        try? FileManager.default.createDirectory(at: codexDir, withIntermediateDirectories: true)
        let configURL = codexDir.appendingPathComponent("config.json")

        var config: [String: Any] = [:]
        if let data = try? Data(contentsOf: configURL),
           let existing = try? JSONSerialization.jsonObject(with: data) as? [String: Any] {
            config = existing
        }

        switch mode {
        case .devin:
            config["openAIApiKey"] = "devin-test"
            config["openAIBaseUrl"] = settings.endpointString
        case .openai:
            config.removeValue(forKey: "openAIBaseUrl")
            if config["openAIApiKey"] as? String == "devin-test" {
                config.removeValue(forKey: "openAIApiKey")
            }
        }

        let data = try JSONSerialization.data(withJSONObject: config, options: [.prettyPrinted, .sortedKeys])
        try data.write(to: configURL)
    }

    private static func mode(fromStatusOutput output: String, settings: BridgeSettingsStore) -> CodexProviderMode {
        if output.contains(settings.endpointString) {
            return .devin
        }
        return .openai
    }
}

private enum ProviderSwitchError: LocalizedError {
    case missingScript(String)
    case scriptFailed(String)

    var errorDescription: String? {
        switch self {
        case let .missingScript(path):
            "Missing Codex provider switch script at \(path)."
        case let .scriptFailed(output):
            output.isEmpty ? "Codex provider switch failed." : output
        }
    }
}
