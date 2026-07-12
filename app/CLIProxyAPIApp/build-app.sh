#!/bin/bash
set -e

APP_NAME="CLIProxyAPIApp.app"
BUILD_DIR=".build/debug"
APP_BUNDLE=".build/${APP_NAME}"
BINARY="${BUILD_DIR}/CLIProxyAPIApp"

echo "Building release binary..."
swift build -c release

rm -rf "${APP_BUNDLE}"
mkdir -p "${APP_BUNDLE}/Contents/MacOS"
mkdir -p "${APP_BUNDLE}/Contents/Resources"

cp ".build/release/CLIProxyAPIApp" "${APP_BUNDLE}/Contents/MacOS/"

# Bundle the cli-proxy-api binary if it exists next to the app source.
if [ -f "../../cli-proxy-api" ]; then
    cp "../../cli-proxy-api" "${APP_BUNDLE}/Contents/MacOS/"
    chmod +x "${APP_BUNDLE}/Contents/MacOS/cli-proxy-api"
fi

if [ -f "../../config.windsurf.yaml" ]; then
    cp "../../config.windsurf.yaml" "${APP_BUNDLE}/Contents/Resources/"
fi

if [ -d "Resources/AgentIcons" ]; then
    cp -R "Resources/AgentIcons" "${APP_BUNDLE}/Contents/Resources/"
fi

cat > "${APP_BUNDLE}/Contents/Info.plist" <<'PLIST'
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>CFBundleDevelopmentRegion</key>
    <string>en</string>
    <key>CFBundleExecutable</key>
    <string>CLIProxyAPIApp</string>
    <key>CFBundleIdentifier</key>
    <string>ai.devin.cli-proxy-api-app</string>
    <key>CFBundleInfoDictionaryVersion</key>
    <string>6.0</string>
    <key>CFBundleName</key>
    <string>CLIProxyAPI</string>
    <key>CFBundlePackageType</key>
    <string>APPL</string>
    <key>CFBundleShortVersionString</key>
    <string>1.0</string>
    <key>CFBundleVersion</key>
    <string>1</string>
    <key>LSApplicationCategoryType</key>
    <string>public.app-category.developer-tools</string>
    <key>LSUIElement</key>
    <true/>
    <key>NSHighResolutionCapable</key>
    <true/>
</dict>
</plist>
PLIST

echo "Created ${APP_BUNDLE}"
