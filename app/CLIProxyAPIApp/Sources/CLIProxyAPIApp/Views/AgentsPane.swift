import AppKit
import SwiftUI

@MainActor
@Observable
final class AgentsPaneStore {
    var apps: [AgentApp] = []
    private let agentStore = AgentAppStore()
    private let writer = AgentConfigWriter.shared
    private(set) var isApplying = false
    private(set) var lastError: String?

    init() {
        refresh()
    }

    func refresh() {
        agentStore.refresh()
        apps = agentStore.apps
    }

    func toggle(_ app: AgentApp) {
        guard let index = apps.firstIndex(where: { $0.id == app.id }) else { return }
        let newValue = !apps[index].isEnabled
        apps[index].isEnabled = newValue
        agentStore.update(id: app.id, isEnabled: newValue)

        Task {
            if newValue {
                await apply(app: apps[index])
            } else {
                await reset(app: apps[index])
            }
        }
    }

    func updateURL(_ url: String, for app: AgentApp) {
        guard let index = apps.firstIndex(where: { $0.id == app.id }) else { return }
        apps[index].customBaseURL = url
        agentStore.update(id: app.id, customBaseURL: url)
    }

    func updateKey(_ key: String, for app: AgentApp) {
        guard let index = apps.firstIndex(where: { $0.id == app.id }) else { return }
        apps[index].customAPIKey = key
        agentStore.update(id: app.id, customAPIKey: key)
    }

    func reset(app: AgentApp) async {
        guard let index = apps.firstIndex(where: { $0.id == app.id }) else { return }
        isApplying = true
        lastError = nil
        defer { isApplying = false }

        do {
            try await writer.resetToDefault(app: app)
            apps[index].isEnabled = false
            apps[index].customBaseURL = ""
            apps[index].customAPIKey = ""
            agentStore.resetToDefault(id: app.id)
        } catch {
            lastError = error.localizedDescription
        }
    }

    func apply(app: AgentApp) async {
        isApplying = true
        lastError = nil
        defer { isApplying = false }

        do {
            try await writer.applyCLIProxy(to: app, baseURL: app.displayURL, apiKey: app.displayKey)
        } catch {
            lastError = error.localizedDescription
        }
    }
}

struct AgentsPane: View {
    @Bindable var settings: BridgeSettingsStore
    @Bindable var bridge: BridgeProcessController
    @State private var store = AgentsPaneStore()
    @State private var editingAppID: String? = nil

    var body: some View {
        ScrollView(.vertical, showsIndicators: true) {
            VStack(alignment: .leading, spacing: 16) {
                SettingsSection(title: "Agents", caption: "Select the agent apps or CLIs you want to route through CLIProxyAPI. When enabled, their config is updated and the app is restarted if it is currently running.") {
                    if store.isApplying {
                        HStack {
                            ProgressView()
                                .controlSize(.small)
                            Text("Applying configuration...")
                                .font(.caption)
                                .foregroundStyle(.secondary)
                        }
                    }

                    if let err = store.lastError {
                        Text(err)
                            .font(.caption)
                            .foregroundStyle(.red)
                            .lineLimit(3)
                    }

                    ForEach(store.apps) { app in
                        AgentAppRow(
                            app: app,
                            isEditing: editingAppID == app.id,
                            onToggle: { store.toggle(app) },
                            onEdit: { editingAppID = editingAppID == app.id ? nil : app.id },
                            onURLChange: { store.updateURL($0, for: app) },
                            onKeyChange: { store.updateKey($0, for: app) },
                            onReset: { Task { await store.reset(app: app) } }
                        )
                    }
                }

                if bridge.status.isRunning {
                    SettingsSection(title: "Selected model catalog", caption: "These models are currently exposed at /v1/models for all enabled agents.") {
                        ExposedModelSummary(settings: settings)
                    }
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.vertical, 4)
        }
        .task {
            store.refresh()
        }
    }
}

struct AgentAppRow: View {
    let app: AgentApp
    let isEditing: Bool
    let onToggle: () -> Void
    let onEdit: () -> Void
    let onURLChange: (String) -> Void
    let onKeyChange: (String) -> Void
    let onReset: () -> Void

    var body: some View {
        HStack(spacing: 12) {
            AgentAppIcon(app: app)
                .frame(width: 32, height: 32)

            VStack(alignment: .leading, spacing: 2) {
                Text(app.name)
                    .font(.body.weight(.semibold))
                if !app.isInstalled {
                    Text("Not installed")
                        .font(.caption)
                        .foregroundStyle(.red)
                } else if app.isRunning {
                    Text("Running")
                        .font(.caption)
                        .foregroundStyle(.green)
                } else if app.isEnabled {
                    Text("Routed to CLIProxyAPI")
                        .font(.caption)
                        .foregroundStyle(.blue)
                } else {
                    Text("Default provider")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }

            Spacer()

            if isEditing {
                Button("Done") {
                    onEdit()
                }
            } else {
                Button("Reset to Default") {
                    onReset()
                }
                .buttonStyle(.borderless)
                .font(.caption)

                Button("Edit") {
                    onEdit()
                }
                .buttonStyle(.borderless)
                .font(.caption)

                Toggle("", isOn: Binding(
                    get: { app.isEnabled },
                    set: { _ in onToggle() }
                ))
                .toggleStyle(.switch)
                .labelsHidden()
                .disabled(!app.isInstalled)
            }
        }

        if isEditing {
            VStack(alignment: .leading, spacing: 8) {
                HStack {
                    Text("Base URL")
                    Spacer()
                    TextField("Base URL", text: Binding(
                        get: { app.customBaseURL.isEmpty ? app.defaultBaseURL : app.customBaseURL },
                        set: { onURLChange($0) }
                    ))
                    .textFieldStyle(.roundedBorder)
                    .frame(width: 260)
                }

                HStack {
                    Text("API Key")
                    Spacer()
                    TextField("API Key", text: Binding(
                        get: { app.customAPIKey.isEmpty ? app.defaultAPIKey : app.customAPIKey },
                        set: { onKeyChange($0) }
                    ))
                    .textFieldStyle(.roundedBorder)
                    .frame(width: 260)
                }
            }
            .padding(.leading, 44)
        }

        Divider()
    }
}

struct AgentAppIcon: View {
    let app: AgentApp

    var body: some View {
        Group {
            if let path = app.appPath {
                Image(nsImage: NSWorkspace.shared.icon(forFile: path))
                    .resizable()
                    .aspectRatio(contentMode: .fit)
            } else {
                Image(systemName: "terminal.fill")
                    .resizable()
                    .aspectRatio(contentMode: .fit)
                    .foregroundStyle(.secondary)
            }
        }
    }
}

struct ExposedModelSummary: View {
    @Bindable var settings: BridgeSettingsStore
    @State private var models: [String] = []
    @State private var isLoading = false

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            if isLoading {
                ProgressView()
                    .controlSize(.small)
            } else if models.isEmpty {
                Text("No models exposed. Select models on the Models tab.")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            } else {
                ForEach(models, id: \.self) { model in
                    HStack {
                        Image(systemName: "checkmark")
                            .font(.caption)
                            .foregroundStyle(.green)
                        Text(model)
                            .font(.system(.body, design: .monospaced))
                    }
                }
            }
        }
        .task {
            await load()
        }
    }

    private func load() async {
        isLoading = true
        defer { isLoading = false }
        let url = settings.baseURL.appendingPathComponent("v0/management/exposed-models")
        var request = URLRequest(url: url)
        request.setValue("devin-test", forHTTPHeaderField: "X-Management-Key")
        do {
            let (data, _) = try await URLSession.shared.data(for: request)
            if let decoded = try? JSONDecoder().decode([String: [String]].self, from: data),
               let models = decoded["models"] {
                self.models = models
            }
        } catch {
            self.models = []
        }
    }
}
