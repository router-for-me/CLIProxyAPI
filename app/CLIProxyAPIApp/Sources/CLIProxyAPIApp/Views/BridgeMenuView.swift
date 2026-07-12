import AppKit
import SwiftUI

struct BridgeMenuView: View {
    @Environment(\.openSettings) private var openSettings

    @Bindable var settings: SettingsStore
    @Bindable var bridge: BridgeProcessController
    @Bindable var apiClient: APIClient

    var body: some View {
        VStack(alignment: .leading, spacing: 6) {
            HStack(spacing: 8) {
                Image(systemName: bridge.status.symbolName)
                    .foregroundStyle(Color(bridge.status.color))
                Text(bridge.status.title)
                Spacer()
            }

            if apiClient.isLoading {
                Text("Loading…")
                    .font(.caption)
                    .foregroundStyle(.secondary)
            }

            if !apiClient.subscriptions.isEmpty {
                Text("Detected subscriptions:")
                    .font(.caption)
                    .foregroundStyle(.secondary)
                    .padding(.top, 2)

                ForEach(apiClient.subscriptions) { subscription in
                    HStack {
                        Image(systemName: "checkmark.circle.fill")
                            .foregroundStyle(.green)
                        Text(subscription.provider.capitalized)
                        Spacer()
                        Text("\(subscription.models.count) models")
                            .font(.caption)
                            .foregroundStyle(.secondary)
                    }
                }
            }
        }
        .padding(.vertical, 4)

        Divider()

        if bridge.status.isActive {
            Button("Restart Bridge") {
                bridge.restart(settings: settings)
            }
            Button("Stop Bridge") {
                bridge.stop()
            }
        } else {
            Button("Start Bridge") {
                bridge.start(settings: settings)
            }
            Button("Stop Bridge") {
                bridge.stop()
            }
            .disabled(true)
        }

        Button("Detect Subscriptions") {
            Task {
                await apiClient.extractAuth(baseURL: settings.baseURL, secret: settings.managementSecret)
            }
        }

        Divider()

        Button("Settings…") {
            openSettings()
            Self.activateSettingsWindow()
        }

        Button("Quit") {
            NSApp.terminate(nil)
        }
        .keyboardShortcut("q")
    }

    private static func activateSettingsWindow() {
        NSApp.activate(ignoringOtherApps: true)
        DispatchQueue.main.async {
            NSApp.activate(ignoringOtherApps: true)
            NSApp.windows
                .filter(\.isVisible)
                .first?
                .makeKeyAndOrderFront(nil)
        }
    }
}
