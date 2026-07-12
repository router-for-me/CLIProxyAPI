import AppKit
import Foundation

enum BridgeStatus: Equatable, Sendable {
    case stopped
    case starting
    case running
    case unhealthy(String)
    case failed(String)

    var isRunning: Bool {
        if case .running = self { return true }
        return false
    }

    var isStarting: Bool {
        if case .starting = self { return true }
        return false
    }

    var isActive: Bool {
        isRunning || isStarting
    }

    var title: String {
        switch self {
        case .stopped: return "Stopped"
        case .starting: return "Starting…"
        case .running: return "Running"
        case .unhealthy(let msg): return "Unhealthy: \(msg)"
        case .failed(let msg): return "Failed: \(msg)"
        }
    }

    var symbolName: String {
        switch self {
        case .running: return "circle.fill"
        case .starting: return "circle.dashed"
        case .unhealthy, .failed: return "exclamationmark.triangle.fill"
        case .stopped: return "circle"
        }
    }

    var color: NSColor {
        switch self {
        case .running: return .systemGreen
        case .starting: return .systemYellow
        case .unhealthy, .failed: return .systemRed
        case .stopped: return .systemGray
        }
    }
}

@MainActor
@Observable
final class BridgeProcessController {
    private(set) var status: BridgeStatus = .stopped
    private(set) var lastHealthCheck: Date?
    private(set) var lastLogLine = "No logs yet."

    private var process: Process?
    private var outputPipe: Pipe?
    private var healthTask: Task<Void, Never>?
    private var consecutiveHealthFailures = 0
    private let maxGracePeriodFailures = 5

    func start(settings: SettingsStore) {
        guard process == nil else {
            NSLog("start called but process already exists")
            return
        }

        status = .starting
        NSLog("start bridge requested at path: \(settings.bridgePath)")
        consecutiveHealthFailures = 0

        let process = Process()
        let pipe = Pipe()
        var arguments = ["--no-browser"]
        var workingDirectory = URL(fileURLWithPath: (settings.bridgePath as NSString).deletingLastPathComponent)

        let bundledConfig = Bundle.main.bundleURL.appendingPathComponent("Contents/Resources/config.windsurf.yaml")
        if FileManager.default.fileExists(atPath: bundledConfig.path) {
            arguments = ["--config", bundledConfig.path, "--no-browser"]
            workingDirectory = URL(fileURLWithPath: (settings.bridgePath as NSString).deletingLastPathComponent)
        }

        process.executableURL = URL(fileURLWithPath: settings.bridgePath)
        process.arguments = arguments
        process.currentDirectoryURL = workingDirectory
        process.standardOutput = pipe
        process.standardError = pipe

        var environment = ProcessInfo.processInfo.environment
        environment["PORT"] = String(settings.port)
        process.environment = environment

        pipe.fileHandleForReading.readabilityHandler = { [weak self] handle in
            let data = handle.availableData
            guard !data.isEmpty,
                  let line = String(data: data, encoding: .utf8)?.trimmingCharacters(in: .whitespacesAndNewlines),
                  !line.isEmpty else {
                return
            }
            Task { @MainActor [weak self] in
                self?.lastLogLine = line
            }
        }

        process.terminationHandler = { [weak self] terminatedProcess in
            Task { @MainActor [weak self] in
                guard let self, self.process === terminatedProcess else { return }
                self.process = nil
                self.healthTask?.cancel()
                if terminatedProcess.terminationStatus == 0 {
                    self.status = .stopped
                } else {
                    self.status = .failed("Process exited with code \(terminatedProcess.terminationStatus).")
                }
            }
        }

        do {
            try process.run()
            self.process = process
            self.outputPipe = pipe
            startHealthPolling(baseURL: settings.baseURL)
            NSLog("Started bridge process at \(settings.bridgePath) with arguments \(arguments)")
        } catch {
            status = .failed("Failed to start: \(error.localizedDescription)")
            NSLog("Failed to start bridge: \(error)")
        }
    }

    func stop() {
        guard let process else { return }
        process.terminate()
        self.process = nil
        healthTask?.cancel()
        status = .stopped
    }

    func restart(settings: SettingsStore) {
        stop()
        start(settings: settings)
    }

    private func startHealthPolling(baseURL: URL) {
        healthTask = Task { [weak self] in
            while !Task.isCancelled {
                await self?.checkHealth(baseURL: baseURL)
                try? await Task.sleep(for: .seconds(2))
            }
        }
    }

    private func checkHealth(baseURL: URL) async {
        do {
            let (data, response) = try await URLSession.shared.data(from: baseURL.appendingPathComponent("health"))
            lastHealthCheck = Date()
            consecutiveHealthFailures = 0
            if let httpResponse = response as? HTTPURLResponse, (200..<300).contains(httpResponse.statusCode) {
                status = .running
            } else {
                status = .unhealthy("HTTP \(response.url?.absoluteString ?? "")")
            }
            if let body = String(data: data, encoding: .utf8), body.count > 0 {
                lastLogLine = body
            }
        } catch {
            consecutiveHealthFailures += 1
            if status == .starting, consecutiveHealthFailures < maxGracePeriodFailures {
                return
            }
            status = .unhealthy(error.localizedDescription)
        }
    }
}
