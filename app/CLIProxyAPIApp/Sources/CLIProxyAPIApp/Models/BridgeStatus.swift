import Foundation

enum BridgeStatus: Equatable {
    case stopped
    case starting
    case running
    case unhealthy(String)
    case failed(String)

    var title: String {
        switch self {
        case .stopped:
            "Stopped"
        case .starting:
            "Starting"
        case .running:
            "Running"
        case .unhealthy:
            "Unhealthy"
        case .failed:
            "Failed"
        }
    }

    var symbolName: String {
        switch self {
        case .running:
            "bolt.circle.fill"
        case .starting:
            "circle.dotted"
        case .stopped:
            "pause.circle"
        case .unhealthy:
            "exclamationmark.triangle.fill"
        case .failed:
            "xmark.circle.fill"
        }
    }

    var isRunning: Bool {
        if case .running = self { return true }
        return false
    }

    var isStarting: Bool {
        if case .starting = self { return true }
        return false
    }

    var isActive: Bool {
        self.isRunning || self.isStarting
    }

    var detail: String? {
        switch self {
        case let .unhealthy(message), let .failed(message):
            message
        default:
            nil
        }
    }
}
