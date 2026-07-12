import Foundation

enum CodexProviderMode: String, CaseIterable, Identifiable {
    case devin
    case openai

    var id: String {
        self.rawValue
    }

    var title: String {
        switch self {
        case .devin:
            "CLIProxyAPI"
        case .openai:
            "OpenAI"
        }
    }
}
