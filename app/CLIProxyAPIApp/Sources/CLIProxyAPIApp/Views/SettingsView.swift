import SwiftUI

struct SettingsView: View {
    @Bindable var settings: BridgeSettingsStore
    @Bindable var bridge: BridgeProcessController

    var body: some View {
        TabView {
            ModelsSettingsPane(settings: self.settings, bridge: self.bridge)
                .tabItem {
                    Label("Models", systemImage: "list.bullet")
                }

            UseInAgentPane(settings: self.settings, bridge: self.bridge)
                .tabItem {
                    Label("Use in Agent", systemImage: "externaldrive.connected.to.line.below")
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
