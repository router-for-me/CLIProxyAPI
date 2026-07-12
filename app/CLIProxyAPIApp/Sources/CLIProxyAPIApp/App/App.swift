import AppKit
import SwiftUI

@main
struct CLIProxyAPIApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @State private var settings = BridgeSettingsStore()
    @State private var bridge = BridgeProcessController()
    @State private var codexProvider = CodexProviderController()

    var body: some Scene {
        MenuBarExtra {
            BridgeMenuView(settings: self.settings, bridge: self.bridge, codexProvider: self.codexProvider)
        } label: {
            MenuBarLabelView(settings: self.settings, bridge: self.bridge)
        }
        .menuBarExtraStyle(.menu)

        Settings {
            SettingsView(settings: self.settings, bridge: self.bridge, codexProvider: self.codexProvider)
                .frame(width: 640, height: 560)
        }
        .windowResizability(.contentSize)
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.accessory)
    }
}
