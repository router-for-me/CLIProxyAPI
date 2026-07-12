import SwiftUI

struct MenuBarLabelView: View {
    let bridge: BridgeProcessController

    var body: some View {
        Image(systemName: bridge.status.symbolName)
            .foregroundStyle(Color(bridge.status.color))
            .symbolRenderingMode(.multicolor)
    }
}
