import Foundation
import Observation

@MainActor
@Observable
final class BridgeSettingsStore {
    var port: Int {
        didSet { self.save() }
    }

    var managementSecret: String {
        didSet { self.save() }
    }

    var startBridgeOnLaunch: Bool {
        didSet { self.save() }
    }

    var bridgePath: String {
        didSet { self.save() }
    }

    private let defaults: UserDefaults

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
        self.port = defaults.object(forKey: Keys.port) as? Int ?? 8317
        self.managementSecret = defaults.string(forKey: Keys.managementSecret) ?? "devin-test"
        self.startBridgeOnLaunch = defaults.object(forKey: Keys.startBridgeOnLaunch) as? Bool ?? true
        self.bridgePath = defaults.string(forKey: Keys.bridgePath) ?? Self.defaultBridgePath()
    }

    var portString: String {
        String(self.port)
    }

    var endpointString: String {
        "http://localhost:\(self.portString)/v1"
    }

    var baseURL: URL {
        URL(string: "http://localhost:\(self.portString)")!
    }

    private func save() {
        self.defaults.set(self.port, forKey: Keys.port)
        self.defaults.set(self.managementSecret, forKey: Keys.managementSecret)
        self.defaults.set(self.startBridgeOnLaunch, forKey: Keys.startBridgeOnLaunch)
        self.defaults.set(self.bridgePath, forKey: Keys.bridgePath)
    }

    private static func defaultBridgePath() -> String {
        let bundleURL = Bundle.main.bundleURL
        let bundled = bundleURL.appendingPathComponent("Contents/MacOS/cli-proxy-api")
        if FileManager.default.isExecutableFile(atPath: bundled.path) {
            return bundled.path
        }

        let candidates = [
            URL(fileURLWithPath: FileManager.default.currentDirectoryPath),
            bundleURL.deletingLastPathComponent().deletingLastPathComponent(),
            URL(fileURLWithPath: NSHomeDirectory()).appendingPathComponent("development/CLIProxyAPI-worktrees/devin-provider"),
        ]

        for candidate in candidates {
            let binary = candidate.appendingPathComponent("cli-proxy-api")
            if FileManager.default.isExecutableFile(atPath: binary.path) {
                return binary.path
            }
        }

        return URL(fileURLWithPath: NSHomeDirectory())
            .appendingPathComponent("development/CLIProxyAPI-worktrees/devin-provider/cli-proxy-api")
            .path
    }
}

private enum Keys {
    static let port = "port"
    static let managementSecret = "managementSecret"
    static let startBridgeOnLaunch = "startBridgeOnLaunch"
    static let bridgePath = "bridgePath"
}
