import AppKit
import SwiftUI

struct BridgeMenuView: View {
    @Environment(\.openSettings) private var openSettings

    @Bindable var settings: BridgeSettingsStore
    @Bindable var bridge: BridgeProcessController

    var body: some View {
        HStack(spacing: 10) {
            Image(systemName: self.bridge.status.symbolName)
                .foregroundStyle(self.bridge.status.isRunning ? .green : .secondary)
            VStack(alignment: .leading, spacing: 2) {
                Text(self.bridge.status.title)
                    .font(.body.weight(.semibold))
                if let date = self.bridge.lastHealthCheck {
                    Text("Last check: \(Formatters.time.string(from: date))")
                        .font(.caption)
                        .foregroundStyle(.secondary)
                }
            }
            Spacer()
        }

        Divider()

        if self.bridge.status.isRunning || self.bridge.status.isStarting {
            Button("Restart Bridge") {
                self.bridge.restart(settings: self.settings)
            }
            Button("Stop Bridge") {
                self.bridge.stop()
            }
        } else {
            Button("Start Bridge") {
                self.bridge.start(settings: self.settings)
            }
            Button("Stop Bridge") {
                self.bridge.stop()
            }
            .disabled(true)
        }

        Divider()

        Button("Settings…") {
            self.openSettings()
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
