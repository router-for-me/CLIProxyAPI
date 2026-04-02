# macOS Local Launcher Pattern

This document describes a practical macOS pattern for running CLIProxyAPI as a local background service with a one-click launcher and a separate update workflow.

The goal is not to hardcode one specific app bundle implementation, but to capture the wiring decisions that make the setup stable over time.

## Goals

- Keep the CLIProxyAPI checkout in a fixed writable directory.
- Start the proxy with one click from Finder, Launchpad, Spotlight, or `/Applications`.
- Open the built-in management Web UI automatically after the service becomes ready.
- Allow the launcher to close both the local proxy process and the matching Web UI window.
- Keep code updates separate from runtime state and separate from the app bundle itself.

## Recommended Layout

Use a stable project directory outside the app bundle, for example:

```text
~/CLIProxyAPI/
  bin/
  auths/
  logs/
  temp/
  config.yaml
```

Recommended responsibilities:

- `config.yaml`: local runtime configuration
- `auths/`: OAuth and provider auth files
- `logs/`: runtime logs
- `bin/`: local helper scripts and built binaries
- `temp/`: PID files, browser profile directories, launcher scratch data

This keeps runtime files out of the application bundle and out of git-tracked source files.

## Use `/management.html` for the Built-In Web UI

The built-in management panel is served from:

```text
http://127.0.0.1:8317/management.html
```

Do not treat `/` as the management UI entrypoint. The root path is only a lightweight API status endpoint.

To keep the management panel available:

- keep `remote-management.secret-key` set
- keep `remote-management.disable-control-panel` set to `false`
- keep the server bound to localhost when the proxy is only for local desktop use

## Make the `.app` a Thin Launcher

The macOS `.app` should be a thin launcher, not a second installation of the proxy.

Recommended design:

- the `.app` calls scripts in the real checkout directory
- the real checkout contains the built binary and local scripts
- the launcher never embeds a stale copy of the server binary

Why this matters:

- updates only need to rebuild the real checkout once
- the `.app` automatically uses the latest built binary
- config, logs, auth files, and update state stay in one place

## Start Flow

A robust start flow usually looks like this:

1. Check whether CLIProxyAPI is already running.
2. If not running, start the local binary in a detached session.
3. Wait for the HTTP health endpoint to respond.
4. Wait for `/management.html` to become available.
5. Open the Web UI.

Recommended health checks:

- service health: `http://127.0.0.1:8317/`
- management UI: `http://127.0.0.1:8317/management.html`

## Use a Dedicated Browser Profile for the Web UI

If the launcher also needs a reliable "close Web UI" action, do not open the panel in a random existing browser tab.

Instead, use a dedicated browser profile or app-style window. For example, with Chrome:

```bash
open -na "Google Chrome" --args \
  --user-data-dir="$HOME/CLIProxyAPI/temp/webui-browser-profile" \
  --app="http://127.0.0.1:8317/management.html"
```

Benefits:

- the launcher can close only the matching Web UI browser processes
- normal browser windows and tabs are left untouched
- the Web UI behaves like a lightweight desktop control panel

## Stop Flow

When the launcher is clicked again, a practical toggle flow is:

1. Detect whether the proxy is already running.
2. If running, ask whether to open the Web UI again or stop the local service.
3. On stop:
   - terminate the dedicated Web UI browser profile processes
   - stop the CLIProxyAPI process
   - remove stale PID files if needed

This gives the `.app` a clean "start or control" role without requiring a background daemon manager.

## Keep Updates Outside the `.app`

Do not update the binary inside the app bundle.

A better pattern is:

1. Keep the real git checkout in a fixed directory such as `~/CLIProxyAPI`.
2. Use a local update script that:
   - checks the remote branch or release
   - fast-forwards the checkout
   - rebuilds the binary
   - optionally restarts the local service if it is already running
3. Let the `.app` continue to point at the same checkout.

This works especially well with command aggregators such as `topgrade`, shell aliases like `up`, or any existing personal update workflow.

## Suggested Update Safeguards

- Skip updates if tracked files are locally modified.
- Use request timeouts for `git fetch` / `git ls-remote`.
- Build into a temporary binary first, then replace the old binary only after a successful validation step.
- Keep config, auth, and logs outside tracked source files.

## Summary

The stable macOS pattern is:

- fixed checkout directory
- thin `.app` launcher
- `management.html` as the UI entrypoint
- dedicated browser profile for precise Web UI lifecycle control
- separate update script wired into the user's normal terminal update flow

This approach is simple to reason about, easy to recover, and avoids the usual "stale binary inside app bundle" problem.
