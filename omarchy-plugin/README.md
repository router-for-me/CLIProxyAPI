# AI usage for Omarchy

A top bar widget for Claude and Codex quotas from your local Papa Proxy accounts,
plus standalone Antigravity and Meta (Muse) usage.

Run these commands from the Papa Proxy repo root on Omarchy:

```sh
plugin="${XDG_CONFIG_HOME:-$HOME/.config}/omarchy/plugins/chicken.proxy-usage"
install -d "$plugin"
cp -R omarchy-plugin/. "$plugin/"
omarchy-shell shell rescanPlugins
omarchy plugin enable chicken.proxy-usage --section right
```

To replace the built-in Agents widget in its current slot, use
`omarchy plugin enable chicken.proxy-usage --after omarchy.agents` for the
last command, then run `omarchy plugin disable omarchy.agents`.

To update an installed copy, repeat the copy step and run
`omarchy-shell shell reloadPlugins`. If the shell still shows the previous
version, run `omarchy restart shell`.

Click the chart icon to open the panel. Five-hour windows show a live
hours:minutes countdown; longer windows show the local reset date and time.
The panel refreshes on open and every five minutes while visible. You can disable
automatic refresh in the widget settings or press **R** to refresh manually.

The widget expects Papa Proxy at `http://127.0.0.1:8317`, with its Claude and
Codex OAuth files in `~/.cli-proxy-api`. It queries the same read-only quota
endpoints as the dashboard for those accounts. It shows upstream limits; the
proxy's scheduler cooldowns can differ. No management key is required.

Antigravity and Meta use Omarchy's bundled collectors and your existing local
sign-ins. When a provider publishes no quota windows, the panel shows local
token counts when available.

The Claude and Codex icons come from Omarchy's Agents plugin
([license](assets/OMARCHY-LICENSE)). The other icons are based on assets in
Papa Proxy.
