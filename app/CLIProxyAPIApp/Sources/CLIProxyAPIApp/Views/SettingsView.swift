import SwiftUI

struct SettingsView: View {
    @Bindable var settings: SettingsStore
    @Bindable var bridge: BridgeProcessController
    @Bindable var apiClient: APIClient

    var body: some View {
        TabView {
            GeneralSettingsPane(settings: settings, bridge: bridge)
                .tabItem {
                    Label("General", systemImage: "gearshape")
                }

            ModelsSettingsPane(settings: settings, apiClient: apiClient)
                .tabItem {
                    Label("Models", systemImage: "list.bullet")
                }

            AboutSettingsPane()
                .tabItem {
                    Label("About", systemImage: "info.circle")
                }
        }
        .padding(.horizontal, 24)
        .padding(.vertical, 18)
    }
}

struct GeneralSettingsPane: View {
    @Bindable var settings: SettingsStore
    @Bindable var bridge: BridgeProcessController

    var body: some View {
        ScrollView(.vertical, showsIndicators: true) {
            VStack(alignment: .leading, spacing: 16) {
                SettingsSection(contentSpacing: 12) {
                    Text("BRIDGE")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .textCase(.uppercase)

                    HStack(spacing: 10) {
                        Image(systemName: bridge.status.symbolName)
                            .foregroundStyle(Color(bridge.status.color))
                        VStack(alignment: .leading, spacing: 2) {
                            Text(bridge.status.title)
                                .font(.body)
                            Text(statusSubtitle)
                                .font(.footnote)
                                .foregroundStyle(.tertiary)
                        }
                        Spacer()
                        if bridge.status.isActive {
                            Button("Restart") {
                                bridge.restart(settings: settings)
                            }
                            Button("Stop") {
                                bridge.stop()
                            }
                        } else {
                            Button("Start") {
                                bridge.start(settings: settings)
                            }
                            Button("Stop") {
                                bridge.stop()
                            }
                            .disabled(true)
                        }
                    }

                    PreferenceToggleRow(
                        title: "Start bridge when app opens",
                        subtitle: "Launches the local OpenAI-compatible endpoint with the menu bar app.",
                        binding: $settings.startBridgeOnLaunch)
                }

                SettingsSection(contentSpacing: 12) {
                    Text("ENDPOINT")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .textCase(.uppercase)

                    SettingsRow(title: "Local URL", subtitle: "Point Codex or any OpenAI-compatible client at this URL.") {
                        Text(settings.endpointString)
                            .font(.system(.body, design: .monospaced))
                            .textSelection(.enabled)
                    }
                }

                SettingsSection(contentSpacing: 12) {
                    Text("AUTH")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .textCase(.uppercase)

                    SettingsRow(title: "Management secret", subtitle: "Secret key for the management endpoints.") {
                        SecureField("Secret", text: $settings.managementSecret)
                            .textFieldStyle(.roundedBorder)
                            .frame(width: 200)
                            .onChange(of: settings.managementSecret) { settings.save() }
                    }
                }

                SettingsSection(contentSpacing: 12) {
                    Text("SERVER")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                        .textCase(.uppercase)

                    SettingsRow(title: "Port", subtitle: "Change only if the default conflicts with another service.") {
                        TextField("Port", value: $settings.port, format: .number.grouping(.never))
                            .textFieldStyle(.roundedBorder)
                            .frame(width: 84)
                            .onChange(of: settings.port) { settings.save() }
                    }

                    SettingsRow(title: "Bridge path", subtitle: "Path to the cli-proxy-api binary.") {
                        TextField("Bridge path", text: $settings.bridgePath)
                            .textFieldStyle(.roundedBorder)
                            .frame(width: 280)
                            .onChange(of: settings.bridgePath) { settings.save() }
                    }

                    Text(bridge.lastLogLine)
                        .font(.system(.footnote, design: .monospaced))
                        .foregroundStyle(.secondary)
                        .lineLimit(4)
                        .textSelection(.enabled)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.vertical, 4)
        }
    }

    private var statusSubtitle: String {
        if let date = bridge.lastHealthCheck {
            return "Last health check at \(Formatters.time.string(from: date))."
        }
        return bridge.status.isActive ? "No health check yet." : ""
    }
}

struct AboutSettingsPane: View {
    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            SettingsSection(contentSpacing: 8) {
                Text("ABOUT")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .textCase(.uppercase)
                Text("CLIProxyAPI")
                    .font(.title3.weight(.semibold))
                Text("A local menu bar controller for the OpenAI-compatible CLIProxyAPI endpoint.")
                    .font(.footnote)
                    .foregroundStyle(.tertiary)
                    .fixedSize(horizontal: false, vertical: true)
            }

            Spacer()
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.vertical, 4)
    }
}

struct Formatters {
    static let time: DateFormatter = {
        let formatter = DateFormatter()
        formatter.timeStyle = .medium
        return formatter
    }()
}
