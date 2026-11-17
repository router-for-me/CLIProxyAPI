#!/bin/sh
# proxx installer: downloads the latest proxx release for this machine and (optionally) runs it as a service.
#
#   curl -fsSL https://raw.githubusercontent.com/iamkunal9/proxx/main/install.sh | sh
#
# Options (environment variables, or flags after `sh -s --`):
#   PROXX_VERSION=v8.0.73-proxx.1   --version <tag>    install a specific release (default: latest)
#   PROXX_INSTALL_DIR=/usr/local/bin --install-dir <d> where the binary goes (default: ~/.local/bin, /usr/local/bin as root)
#   PROXX_HOME=~/.proxx              --home <d>        config + panel + logs directory
#   PROXX_NO_SERVICE=1               --no-service      do not install/start a launchd or systemd service
#   PROXX_NO_PLUGIN=1                --no-plugin       Linux: use the portable no-plugin build (musl, old glibc)
#   PROXX_UNINSTALL=1                --uninstall       remove the service and binary (keeps PROXX_HOME)
#   PROXX_SERVICE_LABEL=<label>                        launchd label (default io.github.iamkunal9.proxx; for tests)
#
# A first install copies an existing CLIProxyAPI config (Homebrew's cliproxyapi.conf) when there is one;
# proxx reads the same config format. An existing $PROXX_HOME/config.yaml is never overwritten.

set -eu

REPO="iamkunal9/proxx"
SERVICE_LABEL="${PROXX_SERVICE_LABEL:-io.github.iamkunal9.proxx}"
PANEL_UPSTREAM="https://github.com/router-for-me/Cli-Proxy-API-Management-Center"
PANEL_PROXX="https://github.com/iamkunal9/proxx-panel"

VERSION="${PROXX_VERSION:-latest}"
PROXX_HOME="${PROXX_HOME:-$HOME/.proxx}"
NO_SERVICE="${PROXX_NO_SERVICE:-}"
NO_PLUGIN="${PROXX_NO_PLUGIN:-}"
UNINSTALL="${PROXX_UNINSTALL:-}"
if [ "$(id -u)" = "0" ]; then
	INSTALL_DIR="${PROXX_INSTALL_DIR:-/usr/local/bin}"
else
	INSTALL_DIR="${PROXX_INSTALL_DIR:-$HOME/.local/bin}"
fi

while [ $# -gt 0 ]; do
	case "$1" in
	--version) VERSION="$2"; shift 2 ;;
	--install-dir) INSTALL_DIR="$2"; shift 2 ;;
	--home) PROXX_HOME="$2"; shift 2 ;;
	--no-service) NO_SERVICE=1; shift ;;
	--no-plugin) NO_PLUGIN=1; shift ;;
	--uninstall) UNINSTALL=1; shift ;;
	-h | --help) sed -n '2,18p' "$0" 2>/dev/null || true; exit 0 ;;
	*) echo "unknown option: $1" >&2; exit 2 ;;
	esac
done

info() { printf '\033[1m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[33mwarning:\033[0m %s\n' "$*" >&2; }
die() { printf '\033[31merror:\033[0m %s\n' "$*" >&2; exit 1; }

BIN="$INSTALL_DIR/proxx"
CONFIG="$PROXX_HOME/config.yaml"
LOG="$PROXX_HOME/proxx.log"
PLIST="$HOME/Library/LaunchAgents/$SERVICE_LABEL.plist"
UNIT="$HOME/.config/systemd/user/proxx.service"

fetch() { # fetch <url> <out-file>
	if command -v curl >/dev/null 2>&1; then
		curl -fsSL --retry 3 -o "$2" "$1"
	elif command -v wget >/dev/null 2>&1; then
		wget -q -O "$2" "$1"
	else
		die "curl or wget is required"
	fi
}

has_systemd_user() { command -v systemctl >/dev/null 2>&1 && systemctl --user show-environment >/dev/null 2>&1; }

OS="$(uname -s)"

# The installer only ever stops, replaces or removes a service that runs THIS install's binary ($BIN):
# a service file for the same label that runs another binary (another install dir, a test run) is left
# alone. Never bootout/stop by label alone.
service_file() {
	case "$OS" in
	Darwin) echo "$PLIST" ;;
	Linux) echo "$UNIT" ;;
	esac
}

# owns_service: the service file exists and runs $BIN.
owns_service() {
	f="$(service_file)"
	[ -n "$f" ] && [ -f "$f" ] || return 1
	case "$OS" in
	Darwin) grep -qF "<string>$BIN</string>" "$f" ;;
	Linux) grep -qF "ExecStart=$BIN --config " "$f" ;;
	*) return 1 ;;
	esac
}

# foreign_service: a service file exists for this label but runs another binary.
foreign_service() {
	f="$(service_file)"
	[ -n "$f" ] && [ -f "$f" ] && ! owns_service
}

# own_pids lists processes whose command line starts with exactly "$BIN --config " (no regex, no
# substring: /bin/proxx must not match /usr/local/bin/proxx).
own_pids() {
	ps -axo pid=,args= 2>/dev/null | awk -v want="$BIN --config " '{ pid = $1; sub(/^[ \t]*[0-9]+[ \t]+/, ""); if (index($0, want) == 1) print pid }'
}

stop_service() {
	if foreign_service; then
		warn "leaving $(service_file) alone: it does not run $BIN"
		return 0
	fi
	if owns_service; then
		case "$OS" in
		Darwin) launchctl bootout "gui/$(id -u)/$SERVICE_LABEL" >/dev/null 2>&1 || true ;;
		Linux) has_systemd_user && systemctl --user stop proxx.service >/dev/null 2>&1 || true ;;
		esac
	fi
	# Wait for this install's proxx to exit so its port is free. proxx drains in-flight requests for up
	# to 120s (server.shutdown-timeout) before it exits, so allow ~135s.
	i=0
	while [ "$i" -lt 270 ] && [ -n "$(own_pids)" ]; do
		i=$((i + 1))
		sleep 0.5
	done
}

if [ -n "$UNINSTALL" ]; then
	stop_service
	if owns_service; then
		case "$OS" in
		Darwin) rm -f "$PLIST" ;;
		Linux)
			if has_systemd_user; then systemctl --user disable proxx.service >/dev/null 2>&1 || true; fi
			rm -f "$UNIT"
			has_systemd_user && systemctl --user daemon-reload || true
			;;
		esac
	fi
	rm -f "$BIN"
	info "proxx removed (kept $PROXX_HOME)"
	exit 0
fi

if [ -z "$NO_SERVICE" ] && foreign_service; then
	die "$(service_file) runs another proxx binary, not $BIN. Uninstall that one first (sh install.sh --uninstall --install-dir <its dir>), or pick another label with PROXX_SERVICE_LABEL."
fi

# --- pick the release asset for this machine ---------------------------------
case "$OS" in
Darwin) os=darwin ;;
Linux) os=linux ;;
FreeBSD) os=freebsd ;;
*) die "unsupported OS: $OS (download a release manually from https://github.com/$REPO/releases)" ;;
esac
case "$(uname -m)" in
x86_64 | amd64) arch=amd64 ;;
arm64 | aarch64) arch=aarch64 ;;
*) die "unsupported CPU: $(uname -m)" ;;
esac
suffix=""
if [ "$os" = linux ]; then
	# The default Linux build needs glibc; musl systems (Alpine, OpenWrt) get the portable build.
	if [ -n "$NO_PLUGIN" ] || ! (ldd --version 2>&1 | grep -qi -e glibc -e "gnu libc"); then suffix="_no-plugin"; fi
fi
[ "$os" = freebsd ] && [ "$arch" = aarch64 ] && suffix="_no-plugin"

TMP="$(mktemp -d 2>/dev/null || mktemp -d -t proxx)"
trap 'rm -rf "$TMP"' EXIT INT TERM

if [ "$VERSION" = latest ]; then
	api="https://api.github.com/repos/$REPO/releases/latest"
else
	api="https://api.github.com/repos/$REPO/releases/tags/$VERSION"
fi
fetch "$api" "$TMP/release.json" || die "could not read release info from $api"
tag="$(sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' "$TMP/release.json" | head -n 1)"
asset_re="proxx_[^\"/]*_${os}_${arch}${suffix}\.tar\.gz"
url="$(grep -o "\"browser_download_url\": *\"[^\"]*/${asset_re}\"" "$TMP/release.json" | sed 's/.*"\(https[^"]*\)"$/\1/' | head -n 1)"
[ -n "$url" ] || die "release $tag has no ${os}/${arch}${suffix} build (yet?): https://github.com/$REPO/releases/tag/$tag"
archive="${url##*/}"

info "Downloading proxx $tag ($archive)"
fetch "$url" "$TMP/$archive"

sums_url="$(grep -o '"browser_download_url": *"[^"]*/checksums.txt"' "$TMP/release.json" | sed 's/.*"\(https[^"]*\)"$/\1/' | head -n 1)"
if [ -n "$sums_url" ] && fetch "$sums_url" "$TMP/checksums.txt"; then
	want="$(grep " $archive\$" "$TMP/checksums.txt" | awk '{print $1}')"
	if command -v sha256sum >/dev/null 2>&1; then got="$(sha256sum "$TMP/$archive" | awk '{print $1}')"; else got="$(shasum -a 256 "$TMP/$archive" | awk '{print $1}')"; fi
	if [ -n "$want" ]; then
		[ "$want" = "$got" ] || die "checksum mismatch for $archive (expected $want, got $got)"
		info "Checksum verified"
	else
		warn "checksums.txt has no entry for $archive; skipping verification"
	fi
else
	warn "release has no checksums.txt; skipping verification"
fi

mkdir -p "$TMP/x"
tar -xzf "$TMP/$archive" -C "$TMP/x"
[ -f "$TMP/x/proxx" ] || die "archive does not contain a proxx binary"

# --- install the binary --------------------------------------------------------
if [ -z "$NO_SERVICE" ]; then
	stop_service
elif owns_service; then
	# --no-service never stops or restarts a service; the binary is replaced by rename, so a running one
	# keeps working on the old version.
	warn "the service $SERVICE_LABEL keeps running the previous version until it is restarted"
fi
mkdir -p "$INSTALL_DIR"
cp "$TMP/x/proxx" "$BIN.tmp"
chmod 755 "$BIN.tmp"
mv -f "$BIN.tmp" "$BIN"
info "Installed $BIN"
case ":$PATH:" in *":$INSTALL_DIR:"*) ;; *) warn "$INSTALL_DIR is not on your PATH; add: export PATH=\"$INSTALL_DIR:\$PATH\"" ;; esac

# --- config ---------------------------------------------------------------------
mkdir -p "$PROXX_HOME"
if [ ! -f "$CONFIG" ]; then
	src=""
	for c in /opt/homebrew/etc/cliproxyapi.conf /usr/local/etc/cliproxyapi.conf /home/linuxbrew/.linuxbrew/etc/cliproxyapi.conf; do
		if [ -f "$c" ]; then src="$c"; break; fi
	done
	if [ -n "$src" ]; then
		cp "$src" "$CONFIG"
		info "Copied your CLIProxyAPI config $src -> $CONFIG (the original is untouched)"
	else
		cp "$TMP/x/config.example.yaml" "$CONFIG"
		info "Wrote starter config $CONFIG (edit it: add API keys, set a management secret)"
	fi
	chmod 600 "$CONFIG"
	# Use the proxx panel instead of the upstream one when the copied config pins upstream's.
	if grep -q "$PANEL_UPSTREAM" "$CONFIG"; then
		sed "s#$PANEL_UPSTREAM#$PANEL_PROXX#g" "$CONFIG" >"$CONFIG.tmp" && mv -f "$CONFIG.tmp" "$CONFIG"
	fi
else
	info "Keeping existing config $CONFIG"
fi

# Tells proxx (and support) how this copy was installed.
printf 'script\n' >"$PROXX_HOME/.install-method"

update_hint() {
	info "Updates: proxx can now update itself in place from the panel (or set proxx.update.auto-apply: true); re-running this installer also works."
}

# --- service --------------------------------------------------------------------
# proxx drains in-flight requests for up to 120s on stop and bounds its own shutdown (~150s), so the
# supervisor must not kill it earlier: launchd ExitTimeOut 0 (launchd caps agent values at 60s) and
# systemd TimeoutStopSec=130.
if [ -n "$NO_SERVICE" ]; then
	update_hint
	info "Done. Run: $BIN --config $CONFIG"
	exit 0
fi

port="$(sed -n 's/^ *port: *\([0-9][0-9]*\).*/\1/p' "$CONFIG" | head -n 1)"
port="${port:-8317}"
if command -v lsof >/dev/null 2>&1 && lsof -nP -iTCP:"$port" -sTCP:LISTEN >/dev/null 2>&1; then
	warn "port $port is already in use (another CLIProxyAPI? stop it with: brew services stop cliproxyapi)"
fi

case "$OS" in
Darwin)
	mkdir -p "$(dirname "$PLIST")"
	cat >"$PLIST" <<EOF
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key><string>$SERVICE_LABEL</string>
	<key>ProgramArguments</key>
	<array><string>$BIN</string><string>--config</string><string>$CONFIG</string></array>
	<key>WorkingDirectory</key><string>$PROXX_HOME</string>
	<key>RunAtLoad</key><true/>
	<key>KeepAlive</key><true/>
	<key>ExitTimeOut</key><integer>0</integer>
	<key>StandardOutPath</key><string>$LOG</string>
	<key>StandardErrorPath</key><string>$LOG</string>
</dict>
</plist>
EOF
	# A just-booted-out job can take a moment to unload; retry briefly.
	n=0
	until launchctl bootstrap "gui/$(id -u)" "$PLIST" 2>/dev/null; do
		n=$((n + 1))
		[ "$n" -ge 5 ] && die "launchctl bootstrap failed; try: launchctl bootstrap gui/$(id -u) $PLIST"
		sleep 1
	done
	info "Started launchd service $SERVICE_LABEL (logs: $LOG)"
	info "Stop: launchctl bootout gui/$(id -u)/$SERVICE_LABEL   Start: launchctl bootstrap gui/$(id -u) $PLIST"
	;;
Linux)
	if has_systemd_user; then
		mkdir -p "$(dirname "$UNIT")"
		cat >"$UNIT" <<EOF
[Unit]
Description=proxx LLM proxy
After=network-online.target

[Service]
ExecStart=$BIN --config $CONFIG
WorkingDirectory=$PROXX_HOME
Restart=on-failure
TimeoutStopSec=130

[Install]
WantedBy=default.target
EOF
		systemctl --user daemon-reload
		systemctl --user enable --now proxx.service
		info "Started systemd user service proxx (logs: journalctl --user -u proxx -f)"
		info "Keep it running after logout: sudo loginctl enable-linger $(id -un)"
	else
		info "No systemd user session; run it yourself: $BIN --config $CONFIG"
	fi
	;;
*)
	info "Run: $BIN --config $CONFIG"
	;;
esac
info "Panel: http://127.0.0.1:$port/management.html"
update_hint
