import AppKit
import SwiftUI

@main
struct CLIProxyAPIApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate
    @State private var settings = BridgeSettingsStore()
    @State private var bridge = BridgeProcessController()

    var body: some Scene {
        MenuBarExtra {
            BridgeMenuView(settings: self.settings, bridge: self.bridge)
        } label: {
            MenuBarLabelView(settings: self.settings, bridge: self.bridge)
        }
        .menuBarExtraStyle(.menu)

        Settings {
            SettingsView(settings: self.settings, bridge: self.bridge)
                .frame(width: 720, height: 600)
        }
        .windowResizability(.contentSize)
    }
}

final class AppDelegate: NSObject, NSApplicationDelegate {
    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.accessory)
    }
}
