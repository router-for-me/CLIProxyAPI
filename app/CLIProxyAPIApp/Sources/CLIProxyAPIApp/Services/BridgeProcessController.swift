import AppKit
import Foundation

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
    private var hasReappliedAgentsForCurrentProcess = false

    var hasManagedProcess: Bool {
        self.process != nil
    }

    func start(settings: BridgeSettingsStore) {
        guard self.process == nil else { return }

        let bridgeURL = URL(fileURLWithPath: settings.bridgePath)
        let configURL = Bundle.main.bundleURL.appendingPathComponent("Contents/Resources/config.windsurf.yaml")

        self.status = .starting
        self.consecutiveHealthFailures = 0
        self.hasReappliedAgentsForCurrentProcess = false
        self.lastLogLine = "Starting bridge..."

        let process = Process()
        let pipe = Pipe()
        process.executableURL = bridgeURL
        process.arguments = ["--config", configURL.path, "--no-browser"]
        process.currentDirectoryURL = bridgeURL.deletingLastPathComponent()
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
            Task { @MainActor in
                self?.lastLogLine = line
            }
        }

        process.terminationHandler = { [weak self] terminatedProcess in
            Task { @MainActor in
                guard let self, self.process === terminatedProcess else { return }

                self.outputPipe?.fileHandleForReading.readabilityHandler = nil
                self.outputPipe = nil
                self.process = nil
                self.healthTask?.cancel()
                self.healthTask = nil
                self.consecutiveHealthFailures = 0
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
            self.startHealthPolling(baseURL: settings.baseURL)
        } catch {
            self.status = .failed(error.localizedDescription)
        }
    }

    func stop() {
        self.healthTask?.cancel()
        self.healthTask = nil
        self.consecutiveHealthFailures = 0
        if let process = self.process {
            process.terminate()
        }
        self.process = nil
        self.outputPipe?.fileHandleForReading.readabilityHandler = nil
        self.outputPipe = nil
        self.status = .stopped
        self.lastLogLine = "Bridge stopped."
    }

    func restart(settings: BridgeSettingsStore) {
        self.stop()
        Task { @MainActor in
            try? await Task.sleep(for: .milliseconds(300))
            self.start(settings: settings)
        }
    }

    func openEndpoint(settings: BridgeSettingsStore) {
        NSWorkspace.shared.open(settings.baseURL.appendingPathComponent("v1/models"))
    }

    func openLogs() {
        NSPasteboard.general.clearContents()
        NSPasteboard.general.setString(self.lastLogLine, forType: .string)
    }

    private func startHealthPolling(baseURL: URL) {
        self.healthTask?.cancel()
        self.healthTask = Task { [weak self] in
            while !Task.isCancelled {
                await self?.checkHealth(baseURL: baseURL)
                try? await Task.sleep(for: .seconds(2))
            }
        }
    }

    private func checkHealth(baseURL: URL) async {
        do {
            let url = baseURL.appendingPathComponent("v1/models")
            var request = URLRequest(url: url)
            request.setValue("Bearer devin-test", forHTTPHeaderField: "Authorization")
            let (_, response) = try await URLSession.shared.data(for: request)
            self.lastHealthCheck = Date()
            self.consecutiveHealthFailures = 0
            if let httpResponse = response as? HTTPURLResponse, (200..<300).contains(httpResponse.statusCode) {
                self.status = .running
                if !self.hasReappliedAgentsForCurrentProcess {
                    self.hasReappliedAgentsForCurrentProcess = true
                    Task { await AgentConfigWriter.shared.reapplyEnabledAgents() }
                }
            } else {
                self.status = .unhealthy("Health check returned an unexpected response.")
            }
        } catch {
            guard self.process != nil else { return }
            self.consecutiveHealthFailures += 1

            if self.status == .starting, self.consecutiveHealthFailures < self.maxGracePeriodFailures {
                return
            }

            self.status = .unhealthy(error.localizedDescription)
        }
    }
}
