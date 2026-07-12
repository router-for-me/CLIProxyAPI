import AppKit
import SwiftUI

struct BridgeMenuView: View {
    @Environment(\.openWindow) private var openWindow

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
            NSApp.setActivationPolicy(.regular)
            openWindow(id: "settings")
            Self.activateSettingsWindow()
            NSLog("openWindow settings triggered")
        }

        Button("Quit") {
            NSApp.terminate(nil)
        }
        .keyboardShortcut("q")
    }

    private static func activateSettingsWindow() {
        NSApp.activate(ignoringOtherApps: true)
        for delay in [0.05, 0.1, 0.2, 0.5] {
            DispatchQueue.main.asyncAfter(deadline: .now() + delay) {
                NSApp.activate(ignoringOtherApps: true)
                NSApp.windows
                    .filter { $0.isVisible && $0.title != "" }
                    .first?
                    .makeKeyAndOrderFront(nil)
            }
        }
    }
}
