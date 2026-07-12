import AppKit
import SwiftUI

struct BridgeMenuView: View {
    @Environment(\.openSettings) private var openSettings

    @Bindable var settings: BridgeSettingsStore
    @Bindable var bridge: BridgeProcessController
    @Bindable var codexProvider: CodexProviderController

    @State private var selectedProvider: CodexProviderMode = .openai

    var body: some View {
        Picker("Backend: " + self.selectedProvider.title, selection: self.$selectedProvider) {
            ForEach(CodexProviderMode.allCases) { mode in
                Text(mode.title).tag(mode)
            }
        }
        .onChange(of: self.selectedProvider) { _, newValue in
            if newValue == .devin, !self.bridge.status.isActive {
                self.bridge.start(settings: self.settings)
            }
            self.codexProvider.switchProvider(to: newValue, settings: self.settings)
        }

        if self.codexProvider.isSwitching {
            Text("Switching…")
        }

        Divider()

        Button("Restart Codex") {
            Task { await self.codexProvider.restartCodex() }
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
        .task {
            self.codexProvider.refreshStatus(settings: self.settings)
            self.selectedProvider = self.codexProvider.selectedMode
        }
        .onChange(of: self.codexProvider.selectedMode) { _, newValue in
            self.selectedProvider = newValue
        }
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
