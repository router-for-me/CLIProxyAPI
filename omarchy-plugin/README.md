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
The panel checks on open and every 15 minutes while visible. **R** checks
manually; it still reuses readings less than 15 minutes old. You can disable
automatic checks in the widget settings.

The widget saves readings in your user cache so reopening the panel or
restarting Omarchy does not repeat quota requests. When Claude or Codex throttles
a lookup, the widget honors the provider's retry time and labels any previous
readings as last known. Standalone retry behavior comes from Omarchy's collectors.

The widget expects Papa Proxy at `http://127.0.0.1:8317`, with its Claude and
Codex OAuth files in `~/.cli-proxy-api`. If you configured a different auth
directory, select it in the widget settings. The widget scans subdirectories
and shows up to 16 accounts. It queries the same read-only quota endpoints
as the dashboard for those accounts. It shows upstream limits; the
proxy's scheduler cooldowns can differ. No management key is required.

Antigravity and Meta use Omarchy's bundled collectors and your existing local
sign-ins. Omarchy's Antigravity collector only reads the token `agy` keeps,
which lapses an hour after `agy` last ran, so `agy.py` renews a lapsed token
in memory before running it. The keyring is never written. It also counts
the sessions T3 Code runs from its own Antigravity data directory, and on
plans like Ultra, where Google meters only Claude and GPT models, labels
the meters as that pool. Gemini usage has no published quota, so it shows
only in the token counts. When a provider publishes no quota windows, the
panel shows local token counts when available.

The Claude and Codex icons come from Omarchy's Agents plugin
([license](assets/OMARCHY-LICENSE)). The other icons are based on assets in
Papa Proxy.
