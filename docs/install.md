# Install

`cliproxyapi++` can run as a container, standalone binary, or embedded SDK.

## Audience Guidance

- Choose Docker for most production and shared-team use.
- Choose binary for lightweight host installs.
- Choose SDK embedding when you need in-process integration in Go.

## Option A: Docker (Recommended)

```bash
docker pull KooshaPari/cliproxyapi-plusplus:latest
```

Minimal run command:

```bash
docker run -d --name cliproxyapi-plusplus \
  -p 8317:8317 \
  -v "$PWD/config.yaml:/CLIProxyAPI/config.yaml" \
  -v "$PWD/auths:/root/.cli-proxy-api" \
  -v "$PWD/logs:/CLIProxyAPI/logs" \
  KooshaPari/cliproxyapi-plusplus:latest
```

Validate:

```bash
curl -sS http://localhost:8317/health
```

ARM64 note (`#147` scope):

- Prefer Docker image manifests that include `linux/arm64`.
- If your host pulls the wrong image variant, force the platform explicitly:

```bash
docker run --platform linux/arm64 -d --name cliproxyapi-plusplus \
  -p 8317:8317 \
  -v "$PWD/config.yaml:/CLIProxyAPI/config.yaml" \
  -v "$PWD/auths:/root/.cli-proxy-api" \
  -v "$PWD/logs:/CLIProxyAPI/logs" \
  KooshaPari/cliproxyapi-plusplus:latest
```

- Verify architecture inside the running container:

```bash
docker exec cliproxyapi-plusplus uname -m
```

Expected output for ARM hosts: `aarch64`.

## Option B: Standalone Binary

Releases:

- https://github.com/KooshaPari/cliproxyapi-plusplus/releases

Example download and run (adjust artifact name for your OS/arch):

```bash
curl -fL \
  https://github.com/KooshaPari/cliproxyapi-plusplus/releases/latest/download/cliproxyapi++-darwin-amd64 \
  -o cliproxyapi++
chmod +x cliproxyapi++
./cliproxyapi++ --config ./config.yaml
```

## Option C: Build From Source

```bash
git clone https://github.com/KooshaPari/cliproxyapi-plusplus.git
cd cliproxyapi-plusplus
go build ./cmd/cliproxyapi
./cliproxyapi --config ./config.example.yaml
```

## Local Dev Refresh Workflow (process-compose)

Use this for deterministic local startup while keeping config/auth reload handled by the built-in watcher.

```bash
cp config.example.yaml config.yaml
process-compose -f examples/process-compose.dev.yaml up
```

Then edit `config.yaml` or files under `auth-dir`; the running process reloads changes automatically.

For Antigravity quota/routing tuning, this is hot-reload friendly:

- `quota-exceeded.switch-project`
- `quota-exceeded.switch-preview-model`
- `routing.strategy` (`round-robin` / `fill-first`)

Quick verification:

```bash
touch config.yaml
curl -sS http://localhost:8317/health
```

For `gemini-3-pro-preview` tool-use failures, follow the deterministic recovery flow before further edits:

```bash
touch config.yaml
process-compose -f examples/process-compose.dev.yaml down
process-compose -f examples/process-compose.dev.yaml up
curl -sS http://localhost:8317/v1/models \
  -H "Authorization: Bearer <client-key>" | jq '.data[].id' | rg 'gemini-3-pro-preview'
curl -sS -X POST http://localhost:8317/v1/chat/completions \
  -H "Authorization: Bearer <client-key>" \
  -H "Content-Type: application/json" \
  -d '{"model":"gemini-3-pro-preview","messages":[{"role":"user","content":"ping"}],"stream":false}'
```

For binary installs, use this quick update flow instead of full reinstall:

```bash
git fetch --tags
git pull --ff-only
go build ./cmd/cliproxyapi
./cliproxyapi --config ./config.yaml
```

## Option D: System Service (OS parity)

Use service installs to run continuously with restart + lifecycle control.

### Linux (systemd)

Copy and adjust:

```bash
sudo cp examples/systemd/cliproxyapi-plusplus.service /etc/systemd/system/cliproxyapi-plusplus.service
sudo cp examples/systemd/cliproxyapi-plusplus.env /etc/default/cliproxyapi
sudo mkdir -p /var/lib/cliproxyapi /etc/cliproxyapi
sudo touch /etc/cliproxyapi/config.yaml  # replace with your real config
sudo useradd --system --no-create-home --shell /usr/sbin/nologin cliproxyapi || true
sudo chown -R cliproxyapi:cliproxyapi /var/lib/cliproxyapi /etc/cliproxyapi
sudo systemctl daemon-reload
sudo systemctl enable --now cliproxyapi-plusplus
```

Useful operations:

```bash
sudo systemctl status cliproxyapi-plusplus
sudo systemctl restart cliproxyapi-plusplus
sudo systemctl stop cliproxyapi-plusplus
```

Cross-platform helper (optional):

```bash
./scripts/service install
./scripts/service start
./scripts/service status
./scripts/service restart
./scripts/service stop
./scripts/service remove
```

On Linux the script writes the systemd unit to `/etc/systemd/system` and requires root privileges.

### macOS (Homebrew + launchd)

Homebrew installs typically place artifacts under `/opt/homebrew`. If installed elsewhere, keep the same launchd flow and swap the binary/config paths.

```bash
mkdir -p ~/Library/LaunchAgents
cp examples/launchd/com.router-for-me.cliproxyapi-plusplus.plist ~/Library/LaunchAgents/
launchctl bootstrap gui/$(id -u) ~/Library/LaunchAgents/com.router-for-me.cliproxyapi-plusplus.plist
launchctl kickstart -k gui/$(id -u)/com.router-for-me.cliproxyapi-plusplus
```

You can also use a local Homebrew formula with service hooks:

```bash
brew install --HEAD --formula examples/homebrew/cliproxyapi-plusplus.rb
brew services start cliproxyapi-plusplus
brew services restart cliproxyapi-plusplus
```

### Windows (PowerShell service helper)

Run as Administrator:

```powershell
.\examples\windows\cliproxyapi-plusplus-service.ps1 -Action install -BinaryPath "C:\Program Files\cliproxyapi-plusplus\cliproxyapi++.exe" -ConfigPath "C:\ProgramData\cliproxyapi-plusplus\config.yaml"
.\examples\windows\cliproxyapi-plusplus-service.ps1 -Action start
.\examples\windows\cliproxyapi-plusplus-service.ps1 -Action status
```

## Option E: Go SDK / Embedding

```bash
go get github.com/KooshaPari/cliproxyapi-plusplus/sdk/cliproxy
```

Related SDK docs:

- [SDK usage](./sdk-usage.md)
- [SDK advanced](./sdk-advanced.md)
- [SDK watcher](./sdk-watcher.md)

## Install-Time Checklist

- Confirm `config.yaml` is readable by the process/container user.
- Confirm `auth-dir` is writable if tokens refresh at runtime.
- Confirm port `8317` is reachable from intended clients only.
- Confirm at least one provider credential is configured.

## Common Install Failures

- Container starts then exits: invalid config path or parse error.
- `failed to read config file ... is a directory`: pass a file path (for example `/CLIProxyAPI/config.yaml`), not a directory.
- `bind: address already in use`: port conflict; change host port mapping.
- Requests always `401`: missing or incorrect `api-keys` for client auth.
- Management API unavailable: `remote-management.secret-key` unset.
