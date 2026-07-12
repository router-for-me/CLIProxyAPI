import AppKit
import SwiftUI

@main
struct CLIProxyAPIApp: App {
    @NSApplicationDelegateAdaptor(AppDelegate.self) private var appDelegate

    var body: some Scene {
        MenuBarExtra {
            BridgeMenuView(settings: appDelegate.settings, bridge: appDelegate.bridge, apiClient: appDelegate.apiClient)
        } label: {
            MenuBarLabelView(bridge: appDelegate.bridge)
        }
        .menuBarExtraStyle(.menu)

        Settings {
            SettingsView(settings: appDelegate.settings, bridge: appDelegate.bridge, apiClient: appDelegate.apiClient)
                .frame(width: 640, height: 600)
        }
        .windowResizability(.contentSize)
    }
}

@MainActor
@Observable
final class AppDelegate: NSObject, NSApplicationDelegate {
    var settings = SettingsStore()
    var bridge = BridgeProcessController()
    var apiClient = APIClient()

    func applicationDidFinishLaunching(_ notification: Notification) {
        NSApp.setActivationPolicy(.accessory)

        if settings.startBridgeOnLaunch {
            bridge.start(settings: settings)
        }

        Task {
            await apiClient.fetchSubscriptions(baseURL: settings.baseURL, secret: settings.managementSecret)
            await apiClient.fetchExposedModels(baseURL: settings.baseURL, secret: settings.managementSecret)
        }
    }
}
