#!/usr/bin/env bash
set -euo pipefail

usage() {
	printf 'Usage: %s [--upgrade-brew] [--no-restart]\n' "${0##*/}" >&2
}

upgrade_brew=0
restart_service=1
while (($#)); do
	case "$1" in
		--upgrade-brew)
			upgrade_brew=1
			;;
		--no-restart)
			restart_service=0
			;;
		-h|--help)
			usage
			exit 0
			;;
		*)
			usage
			exit 2
			;;
	esac
	shift
done

repo_root="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)"
plugin_dir="${CLIPROXYAPI_PLUGIN_DIR:-$HOME/.cliproxyapi/plugins}"
config_path="${CLIPROXYAPI_CONFIG:-/opt/homebrew/etc/cliproxyapi.conf}"

if ((upgrade_brew == 1)); then
	brew update
	brew upgrade cliproxyapi
fi

make -C "$repo_root/examples/plugin" build-codex-tool-search-shim
make -C "$repo_root/examples/plugin" \
	CLIPROXYAPI_PLUGIN_DIR="$plugin_dir" \
	install-codex-tool-search-shim

ruby -ryaml -e '
  config = YAML.safe_load_file(ARGV[0])
  abort "plugins.enabled is not true" unless config.dig("plugins", "enabled") == true
  abort "codex-tool-search-shim is not enabled" unless config.dig("plugins", "configs", "codex-tool-search-shim", "enabled") == true
' "$config_path"

if ((restart_service == 1)); then
	brew services restart cliproxyapi
	healthy=0
	for _ in {1..20}; do
		if curl -fsS http://127.0.0.1:8317/healthz >/dev/null; then
			healthy=1
			break
		fi
		sleep 1
	done
	if ((healthy == 0)); then
		printf 'CLIProxyAPI health check failed after restart.\n' >&2
		exit 1
	fi
fi

printf 'CLIProxyAPI and codex-tool-search-shim are up to date.\n'
