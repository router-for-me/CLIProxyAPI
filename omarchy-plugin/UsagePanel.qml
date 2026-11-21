import QtQuick
import QtQuick.Layouts
import Quickshell
import Quickshell.Io
import qs.Commons as Commons
import qs.Ui

Panel {
    id: root

    ipcTarget: "chicken.proxy-usage"
    implicitWidth: button.implicitWidth
    implicitHeight: button.implicitHeight

    property var usage: ({ proxy: [], local: [], checked: 0 })
    property bool failed: false
    property double now: Date.now()
    readonly property bool autoRefresh: setting("autoRefresh", true)
    readonly property var locale: Qt.locale(Commons.I18n.formatLocale)

    readonly property color secondary: Commons.Util.alpha(Commons.Color.foreground, 0.72)
    readonly property color rule: Commons.Util.alpha(Commons.Color.foreground, 0.18)
    readonly property bool lightSurface: {
        const bg = Commons.Color.background
        return 0.21 * bg.r + 0.72 * bg.g + 0.07 * bg.b > 0.62
    }

    function logo(id) {
        switch (id) {
        case "claude": return Qt.resolvedUrl("assets/claude.svg")
        case "codex": return Qt.resolvedUrl(lightSurface ? "assets/codex-light.svg" : "assets/codex.svg")
        case "agy": return Qt.resolvedUrl("assets/antigravity.svg")
        case "muse": return Qt.resolvedUrl("assets/meta.svg")
        default: return ""
        }
    }

    function tokens(value) {
        if (value >= 1000000) {
            return strings.t("tokenMillions").arg(Commons.I18n.formatNumber(value / 1000000, 1))
        }
        if (value >= 1000) {
            return strings.t("tokenThousands").arg(Commons.I18n.formatNumber(value / 1000, 1))
        }
        return strings.t("tokenCount").arg(Commons.I18n.formatNumber(value))
    }

    function refresh() {
        if (!loader.running) {
            loader.running = true
        }
    }

    function resetLabel(timestamp, relative) {
        if (!timestamp) {
            return ""
        }
        const minutes = Math.ceil((timestamp * 1000 - now) / 60000)
        if (minutes <= 0) {
            return strings.t("resetPassed")
        }
        if (relative) {
            const hours = Commons.I18n.formatNumber(Math.floor(minutes / 60))
            const remainder = minutes % 60
            const part = Commons.I18n.formatNumber(remainder)
            const zero = Commons.I18n.formatNumber(0)
            return strings.t("resetIn").arg(hours).arg(remainder < 10 ? zero + part : part)
        }
        return strings.t("reset").arg(
            new Date(timestamp * 1000).toLocaleString(locale, "MMM d · HH:mm"))
    }

    onOpenedChanged: {
        if (opened) {
            now = Date.now()
            refresh()
        }
    }

    Commons.Translations {
        id: strings
        directory: Qt.resolvedUrl("i18n")
    }

    Process {
        id: loader
        command: ["/usr/bin/python3", Qt.resolvedUrl("usage.py").toString().replace(/^file:\/\//, "")]
        stdout: StdioCollector {
            id: output
            waitForEnd: true
        }
        onExited: function(code) {
            root.failed = true
            if (code === 0 && output.text.length < 65536) {
                try {
                    root.usage = JSON.parse(output.text)
                    root.failed = false
                } catch (error) {
                    // Keep the previous snapshot and show the error.
                }
            }
        }
    }

    Timer {
        interval: 900000
        repeat: true
        running: root.opened && root.autoRefresh
        onTriggered: root.refresh()
    }

    Timer {
        interval: 1000
        repeat: true
        running: root.opened
        onTriggered: root.now = Date.now()
    }

    component Readout: Text {
        color: Commons.Color.foreground
        font.family: Commons.Style.font.family
        font.pixelSize: Commons.Style.font.body
        textFormat: Text.PlainText
        elide: Text.ElideRight
    }

    BarIconButton {
        id: button
        anchors.fill: parent
        bar: root.bar
        text: "󰄧"
        tooltipText: strings.t("tooltip")
        onPressed: function(code) {
            if (code === Qt.LeftButton) {
                root.toggle()
            }
        }
    }

    KeyboardPanel {
        id: panel
        anchorItem: button
        owner: root
        bar: root.bar
        open: root.opened
        focusTarget: panel.loadedContent
        contentWidth: panel.fittedContentWidth(Commons.Style.space(490))
        contentHeight: panel.fittedContentHeight(panel.loadedContentHeight, Commons.Style.space(670))

        content: PanelKeyCatcher {
            anchors.fill: parent
            implicitHeight: body.implicitHeight
            onCloseRequested: root.close()
            onTabRequested: function(direction) { root.switchPanel(direction) }
            onTextKey: function(key) {
                if (key.toLowerCase() === "r") {
                    root.refresh()
                }
            }

            Flickable {
                anchors.fill: parent
                contentWidth: width
                contentHeight: body.implicitHeight
                clip: true
                boundsBehavior: Flickable.StopAtBounds

                ColumnLayout {
                    id: body
                    width: parent.width
                    spacing: Commons.Style.space(12)

                    RowLayout {
                        Layout.fillWidth: true
                        Readout {
                            Layout.fillWidth: true
                            text: strings.t("title")
                            font.pixelSize: Commons.Style.font.display
                            font.bold: true
                        }
                        PanelActionButton {
                            iconText: "󰑓"
                            tooltipText: strings.t("refresh")
                            enabled: !loader.running
                            onClicked: root.refresh()
                        }
                        PanelActionButton {
                            iconText: "󰏌"
                            tooltipText: strings.t("dashboard")
                            onClicked: {
                                Qt.openUrlExternally("http://127.0.0.1:8317/management.html#/quota")
                                root.close()
                            }
                        }
                    }

                    Readout {
                        Layout.fillWidth: true
                        text: loader.running ? strings.t("loading") : root.failed ? strings.t("failed") :
                            root.usage.checked ? strings.t("updated").arg(
                                new Date(root.usage.checked * 1000).toLocaleString(root.locale, "HH:mm")) : ""
                        color: root.failed ? Commons.Color.urgent : root.secondary
                    }

                    Repeater {
                        model: [
                            { title: "proxy", accounts: root.usage.proxy },
                            { title: "local", accounts: root.usage.local }
                        ]

                        delegate: ColumnLayout {
                            required property var modelData
                            Layout.fillWidth: true
                            visible: root.usage.checked > 0
                            spacing: Commons.Style.space(8)

                            RowLayout {
                                Layout.fillWidth: true
                                spacing: Commons.Style.space(8)

                                Image {
                                    visible: modelData.title === "proxy"
                                    Layout.preferredWidth: Commons.Style.space(16)
                                    Layout.preferredHeight: Layout.preferredWidth
                                    source: Qt.resolvedUrl("assets/papa.webp")
                                    fillMode: Image.PreserveAspectFit
                                    mipmap: true
                                }

                                Readout {
                                    text: strings.t(modelData.title)
                                    font.pixelSize: Commons.Style.font.subtitle
                                    font.bold: true
                                    color: root.secondary
                                }

                                Rectangle {
                                    Layout.fillWidth: true
                                    Layout.preferredHeight: 1
                                    color: root.rule
                                }
                            }

                            Readout {
                                Layout.fillWidth: true
                                visible: modelData.title === "proxy" && modelData.accounts.length === 0
                                text: strings.t("proxyEmpty")
                                color: root.secondary
                            }

                            Repeater {
                                model: modelData.accounts
                                delegate: ColumnLayout {
                                    id: account
                                    required property var modelData
                                    Layout.fillWidth: true
                                    spacing: Commons.Style.space(5)

                                    RowLayout {
                                        Layout.fillWidth: true
                                        spacing: Commons.Style.space(7)
                                        Image {
                                            Layout.preferredWidth: Commons.Style.space(17)
                                            Layout.preferredHeight: Layout.preferredWidth
                                            fillMode: Image.PreserveAspectFit
                                            sourceSize.width: 34
                                            sourceSize.height: 34
                                            source: root.logo(account.modelData.id)
                                        }
                                        Readout {
                                            text: account.modelData.name
                                            font.bold: true
                                        }
                                        Readout {
                                            Layout.fillWidth: true
                                            text: account.modelData.detail
                                            color: root.secondary
                                        }
                                        Readout {
                                            visible: account.modelData.status !== "ok"
                                            text: visible ? strings.t(account.modelData.status) : ""
                                            font.pixelSize: Commons.Style.font.caption
                                            color: root.secondary
                                        }
                                    }

                                    Repeater {
                                        model: account.modelData.windows
                                        delegate: RowLayout {
                                            id: meter
                                            required property var modelData
                                            Layout.fillWidth: true
                                            Layout.leftMargin: Commons.Style.space(24)
                                            spacing: Commons.Style.space(9)

                                            Readout {
                                                Layout.preferredWidth: Commons.Style.space(88)
                                                text: meter.modelData.custom ? meter.modelData.label :
                                                    strings.t(meter.modelData.label)
                                                font.pixelSize: Commons.Style.font.bodySmall
                                            }

                                            Rectangle {
                                                Layout.fillWidth: true
                                                Layout.preferredHeight: Commons.Style.space(5)
                                                color: Commons.Util.alpha(Commons.Color.foreground, 0.16)
                                                radius: height / 2

                                                Rectangle {
                                                    width: parent.width * meter.modelData.percent / 100
                                                    height: parent.height
                                                    radius: height / 2
                                                    color: meter.modelData.percent >= 100 ?
                                                        Commons.Color.urgent : Commons.Color.accent
                                                }
                                            }

                                            Readout {
                                                Layout.preferredWidth: Commons.Style.space(75)
                                                horizontalAlignment: Text.AlignRight
                                                text: strings.t("used").arg(Commons.I18n.formatNumber(
                                                    meter.modelData.percent,
                                                    Number.isInteger(meter.modelData.percent) ? 0 : 1))
                                                font.pixelSize: Commons.Style.font.caption
                                            }

                                            Readout {
                                                Layout.preferredWidth: Commons.Style.space(146)
                                                horizontalAlignment: Text.AlignRight
                                                text: root.resetLabel(meter.modelData.reset,
                                                    meter.modelData.label.toLowerCase() === "session")
                                                color: root.secondary
                                                font.pixelSize: Commons.Style.font.caption
                                            }
                                        }
                                    }

                                    Readout {
                                        Layout.fillWidth: true
                                        Layout.leftMargin: Commons.Style.space(24)
                                        visible: !!account.modelData.tokens
                                        text: visible ? strings.t("tokens").arg(
                                            root.tokens(account.modelData.tokens[0])).arg(
                                            root.tokens(account.modelData.tokens[1])) : ""
                                        color: root.secondary
                                        font.pixelSize: Commons.Style.font.bodySmall
                                    }
                                }
                            }
                        }
                    }

                    Readout {
                        Layout.fillWidth: true
                        visible: root.autoRefresh && root.usage.checked > 0
                        text: strings.t("autoRefresh")
                        color: root.secondary
                        font.pixelSize: Commons.Style.font.caption
                    }
                }
            }
        }
    }
}
