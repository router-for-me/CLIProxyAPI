import Foundation

@MainActor
@Observable
final class SettingsStore {
    var port: Int
    var startBridgeOnLaunch: Bool
    var bridgePath: String
    var managementSecret: String

    private let defaults: UserDefaults
    private enum Keys {
        static let port = "port"
        static let startBridgeOnLaunch = "startBridgeOnLaunch"
        static let bridgePath = "bridgePath"
        static let managementSecret = "managementSecret"
    }

    init(defaults: UserDefaults = .standard) {
        self.defaults = defaults
        self.port = defaults.object(forKey: Keys.port) as? Int ?? 8317
        self.startBridgeOnLaunch = defaults.object(forKey: Keys.startBridgeOnLaunch) as? Bool ?? true
        self.bridgePath = defaults.string(forKey: Keys.bridgePath) ?? defaultBridgePath()
        self.managementSecret = defaults.string(forKey: Keys.managementSecret) ?? "devin-test"
    }

    var baseURL: URL {
        URL(string: "http://localhost:\(port)")!
    }

    var endpointString: String {
        "http://localhost:\(port)/v1"
    }

    func save() {
        defaults.set(port, forKey: Keys.port)
        defaults.set(startBridgeOnLaunch, forKey: Keys.startBridgeOnLaunch)
        defaults.set(bridgePath, forKey: Keys.bridgePath)
        defaults.set(managementSecret, forKey: Keys.managementSecret)
    }
}

private func defaultBridgePath() -> String {
    let bundleBinary = Bundle.main.bundleURL.appendingPathComponent("Contents/MacOS/cli-proxy-api").path
    if FileManager.default.fileExists(atPath: bundleBinary) {
        return bundleBinary
    }
    let candidates = [
        FileManager.default.currentDirectoryPath,
        NSHomeDirectory() + "/development/CLIProxyAPI-worktrees/devin-provider",
    ]
    for candidate in candidates {
        let path = (candidate as NSString).appendingPathComponent("cli-proxy-api")
        if FileManager.default.fileExists(atPath: path) {
            return path
        }
    }
    return "cli-proxy-api"
}
