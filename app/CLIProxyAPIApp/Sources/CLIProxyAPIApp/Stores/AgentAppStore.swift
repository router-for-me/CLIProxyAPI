import Foundation

@MainActor
@Observable
final class AgentAppStore {
    var apps: [AgentApp] = []
    private let defaults = UserDefaults.standard
    private let key = "agentApps"

    init() {
        load()
    }

    func refresh() {
        var refreshed: [AgentApp] = []
        for var app in AgentApp.supportedApps {
            let saved = apps.first { $0.id == app.id }
            app.isEnabled = saved?.isEnabled ?? false
            app.customBaseURL = saved?.customBaseURL ?? ""
            app.customAPIKey = saved?.customAPIKey ?? ""
            refreshed.append(app)
        }
        apps = refreshed
        save()
    }

    func update(id: String, isEnabled: Bool? = nil, customBaseURL: String? = nil, customAPIKey: String? = nil) {
        guard let index = apps.firstIndex(where: { $0.id == id }) else { return }
        if let isEnabled = isEnabled {
            apps[index].isEnabled = isEnabled
        }
        if let customBaseURL = customBaseURL {
            apps[index].customBaseURL = customBaseURL
        }
        if let customAPIKey = customAPIKey {
            apps[index].customAPIKey = customAPIKey
        }
        save()
    }

    func resetToDefault(id: String) {
        guard let index = apps.firstIndex(where: { $0.id == id }) else { return }
        apps[index].isEnabled = false
        apps[index].customBaseURL = ""
        apps[index].customAPIKey = ""
        save()
    }

    private func save() {
        let data = apps.map { [
            "id": $0.id,
            "isEnabled": $0.isEnabled,
            "customBaseURL": $0.customBaseURL,
            "customAPIKey": $0.customAPIKey,
        ] }
        defaults.set(data, forKey: key)
    }

    private func load() {
        guard let data = defaults.array(forKey: key) as? [[String: Any]] else { return }
        apps = data.compactMap { dict in
            guard let id = dict["id"] as? String else { return nil }
            let isEnabled = dict["isEnabled"] as? Bool ?? false
            let customBaseURL = dict["customBaseURL"] as? String ?? ""
            let customAPIKey = dict["customAPIKey"] as? String ?? ""
            var app = AgentApp.supportedApps.first { $0.id == id }
            app?.isEnabled = isEnabled
            app?.customBaseURL = customBaseURL
            app?.customAPIKey = customAPIKey
            return app
        }
    }
}
