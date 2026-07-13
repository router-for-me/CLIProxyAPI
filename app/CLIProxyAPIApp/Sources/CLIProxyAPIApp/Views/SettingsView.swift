import SwiftUI

struct SettingsView: View {
    @Bindable var settings: BridgeSettingsStore
    @Bindable var bridge: BridgeProcessController

    var body: some View {
        TabView {
            GeneralSettingsPane(settings: self.settings, bridge: self.bridge)
                .tabItem {
                    Label("General", systemImage: "gearshape")
                }

            AgentsPane(settings: self.settings, bridge: self.bridge)
                .tabItem {
                    Label("Agents", systemImage: "person.2.fill")
                }

            ModelsSettingsPane(settings: self.settings, bridge: self.bridge)
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
    @Bindable var settings: BridgeSettingsStore
    @Bindable var bridge: BridgeProcessController
    @State private var showAdvanced = false

    var body: some View {
        ScrollView(.vertical, showsIndicators: true) {
            VStack(alignment: .leading, spacing: 16) {
                // Bridge status
                SettingsSection(title: "Bridge Status") {
                    HStack(spacing: 10) {
                        Image(systemName: self.bridge.status.symbolName)
                            .foregroundStyle(self.bridge.status.isRunning ? .green : .secondary)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(self.bridge.status.title)
                                .font(.body)
                            Text(self.statusSubtitle)
                                .font(.footnote)
                                .foregroundStyle(.tertiary)
                        }
                        Spacer()
                        if self.bridge.status.isActive {
                            Button("Restart") {
                                self.bridge.restart(settings: self.settings)
                            }
                            Button("Stop") {
                                self.bridge.stop()
                            }
                        } else {
                            Button("Start") {
                                self.bridge.start(settings: self.settings)
                            }
                            Button("Stop") {
                                self.bridge.stop()
                            }
                            .disabled(true)
                        }
                    }

                    PreferenceToggleRow(
                        title: "Start bridge when app opens",
                        subtitle: "Launches the local OpenAI-compatible endpoint with the menu bar app.",
                        binding: self.$settings.startBridgeOnLaunch)
                }

                // Endpoint
                SettingsSection(title: "Endpoint") {
                    SettingsRow(title: "Local URL", subtitle: "Point Codex or any OpenAI-compatible client at this URL.") {
                        Text(self.settings.endpointString)
                            .font(.system(.body, design: .monospaced))
                            .textSelection(.enabled)
                    }

                    SettingsRow(title: "API Key", subtitle: "Use this key in the client.") {
                        Text("devin-test")
                            .font(.system(.body, design: .monospaced))
                            .textSelection(.enabled)
                    }
                }

                // Advanced
                DisclosureGroup("Advanced", isExpanded: self.$showAdvanced) {
                    VStack(alignment: .leading, spacing: 16) {
                        SettingsSection(title: "Server") {
                            SettingsRow(title: "Port", subtitle: "Change only if the default conflicts with another service.") {
                                TextField("Port", value: self.$settings.port, format: .number.grouping(.never))
                                    .textFieldStyle(.roundedBorder)
                                    .frame(width: 84)
                            }
                        }

                        SettingsSection(title: "Bridge Path") {
                            SettingsRow(title: "Bridge path", subtitle: "Installed builds use the bundled bridge automatically.") {
                                TextField("Bridge path", text: self.$settings.bridgePath)
                                    .textFieldStyle(.roundedBorder)
                                    .frame(width: 280)
                            }

                            Button {
                                self.bridge.openEndpoint(settings: self.settings)
                            } label: {
                                Label("Open Health Check", systemImage: "safari")
                            }
                        }

                        SettingsSection(title: "Last Log") {
                            Text(self.bridge.lastLogLine)
                                .font(.system(.footnote, design: .monospaced))
                                .foregroundStyle(.secondary)
                                .lineLimit(4)
                                .textSelection(.enabled)
                        }
                    }
                    .padding(.top, 8)
                }
            }
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(.vertical, 4)
        }
    }

    private var statusSubtitle: String {
        if let date = self.bridge.lastHealthCheck {
            return "Last health check at \(Formatters.time.string(from: date))."
        }
        return self.bridge.status.detail ?? "No health check yet."
    }
}

struct AboutSettingsPane: View {
    var body: some View {
        VStack(alignment: .leading, spacing: 16) {
            SettingsSection(title: "About") {
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
