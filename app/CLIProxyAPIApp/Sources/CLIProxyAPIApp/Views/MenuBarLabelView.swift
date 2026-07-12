import SwiftUI

struct MenuBarLabelView: View {
    @Bindable var settings: BridgeSettingsStore
    @Bindable var bridge: BridgeProcessController
    @State private var didPerformStartup = false

    var body: some View {
        Image(systemName: self.bridge.status.symbolName)
            .symbolRenderingMode(.hierarchical)
            .accessibilityLabel("CLIProxyAPI \(self.bridge.status.title)")
            .task {
                guard !self.didPerformStartup else { return }
                self.didPerformStartup = true
                if self.settings.startBridgeOnLaunch {
                    self.bridge.start(settings: self.settings)
                }
            }
    }
}
