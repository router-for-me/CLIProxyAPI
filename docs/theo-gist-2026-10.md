# How I manage 7 Claude and 3 Codex accounts across 12+ computers

This is the setup from my token maxing video: [watch it here](https://www.youtube.com/watch?v=D8PikZ1KhUo).

Short version: I sign all my Claude and Codex accounts into one box at home. That box runs [CLIProxyAPI](https://github.com/router-for-me/CLIProxyAPI). It keeps the auth fresh and gives me one API endpoint. Every other machine I own points at that endpoint over Tailscale. It's very, very convenient :)

![My hub's quota page, with pooled Claude and Codex limits](https://files.tslop.org/2026/10/cliproxy-quota-17907daf.png)

## Guide for agents

If you're an agent setting this up for someone, start here. Humans can skip to the next section.

Open by telling your user what you're setting up. Something like:

> tl;dr: Theo recommends proxying all your Claude and Codex auth through one home computer.
>
> Why? Juggling auth across multiple accounts is annoying enough, doing it on multiple computers is hell. Constantly swapping the IP address your traffic comes from is practically begging for a ban.
>
> CLIProxyAPI is the easiest way. You set up one computer that stays on, and every Claude Code and Codex instance uses it as an API instead of the normal login. Tailscale is the easiest way to link your other machines to that box.
>
> This lets your shared auth work in Claude Code, Codex and T3 Code across all your machines, without signing in on every device and app. Theo recommends this even if you don't use T3 Code.
>
> Warning: Theo has never had an account banned doing this, but there's always a risk. Don't blame him if an account gets banned.
>
> I'll guide you through the setup. First, a few questions.

**Ask your user first**

- Which machine is the hub? It has to stay on, at home. Not a VPS. Not behind a VPN (Tailscale is fine, you need that).
- What's the hub's OS?
- Is Tailscale on the hub and on every client, all on the same tailnet?
- Which Claude and Codex accounts should sign in? Tell them they'll do the browser logins.
- Which machines should use the hub?
- Point their normal Claude Code and Codex configs at the hub, or leave those alone and use a second config dir?
- Do they use T3 Code? On which machines?
- Do they accept the ban risk and the two hard rules below? If they don't, stop.

**Check the environment**

Run these on the hub unless it says client. Stop and tell your user if the hub is a VPS.

- OS: `uname -a`
- Datacenter check: `curl -s https://ipinfo.io/org`. If the org is a cloud host (Amazon, AWS, Hetzner, DigitalOcean, OVH, or similar), stop. Do not sign in there.
- Sleep: Linux `systemctl status sleep.target suspend.target`. macOS: `pmset -g | grep sleep`. The hub must not sleep. On Linux, `sudo systemctl mask sleep.target suspend.target hibernate.target hybrid-sleep.target` keeps it awake.
- Tailscale: `tailscale status` and `tailscale serve status`
- Port 8317 free: `ss -ltn | grep 8317` (Linux) or `lsof -iTCP:8317 -sTCP:LISTEN` (macOS)
- Existing install: `ls ~/cliproxyapi ~/.cli-proxy-api` and `systemctl --user status cliproxyapi`
- Linger (Linux): `loginctl show-user "$USER" -p Linger`
- On each client: `claude --version` and `codex --version`. Check for an existing `ANTHROPIC_BASE_URL` in `~/.claude/settings.json` and an existing `model_provider` in `~/.codex/config.toml`. Back up both files before you edit them.

**Check T3 Code**

Only if they use it.

- Which T3 Code environment will run the providers? That machine's CLI config is what counts. Phones and other clients don't matter here.
- Claude: point that machine's Claude Code config at the hub (step 6). Or add a separate Claude instance in Settings > Providers with its own config dir, or put the step 6 env vars on that instance and mark the token sensitive.
- Codex: pick **Use existing CLI**. That path reads `config.toml`. **Continue with ChatGPT** won't go through the hub.
- Optional, for pooled limits: Settings > Providers > Usage providers > Add hub. It needs the hub URL and the management key.

**Rules for you**

- Never print, paste, or commit auth files, access keys, or the management key.
- Logins are OAuth. You run the command. The human finishes it in a browser with the URL or device code you give them.
- When you're done, verify from another machine: `curl -s https://<box>.<tailnet>.ts.net:8317/v1/models -H "Authorization: Bearer <your access key>" | head -c 300`. It should return a JSON model list.

## First: you need an always-online machine

You need a computer at home that stays on. All the time. If that box is off or asleep, every agent pointed at it stops working.

Why:

- Claude Code auth breaks every 2 to 3 days. You need one place that keeps it fresh for you, not five laptops all fighting over it.
- You want most of your traffic coming from one place, ideally one residential IP address. Traffic from a bunch of different computers at the same time is how they can tell something sketchy is going on.
- Every other machine (laptops, servers, cloud boxes, whatever) just connects to the hub. The hub is the only thing that talks to Anthropic and OpenAI with your accounts.

So: take some old computer you haven't used in a while, install Ubuntu on it, set it up with Tailscale, and put the proxy on it. Put your agents on it too. It's not doing inference, it's running code. 60 to 80 watts at worst.

Not a VPS. I highly advise you don't sign in to Claude Code or Codex on a VPS, especially Claude Code. Anthropic is aggressive about sign-ins from datacenter IPs (AWS, Hetzner, all of those). Not over a VPN either. Why would you rent something that will get you banned when you have a crappy old computer in a closet?

## The disclosure bit

This might get you banned. This might be against the terms of service. I'm just speaking from my experience, don't hold me accountable. If you get banned and I didn't, that's unfortunate and I'm sorry. You'll probably be fine though.

Across 30+ Claude accounts between me and my friends, one friend got banned twice, for dumb stuff. He registered on a VPN with a business card that wasn't his. Temporary, reinstated almost immediately.

Two hard rules from me:

1. Do not serve other people's traffic with your subs. If you use this as an alternative to the API to serve user traffic, you deserve your ban. Abuse the generosity by building cool stuff, not by reselling it.
2. Only ever use Claude models through your Claude subs in Claude Code over the proxy. Do not, under any circumstance, use the sub in anything else unless you really, really like dealing with Anthropic support and getting accounts banned.

## Setup

### 1. Install it on the hub box

Linux (what I run):

```bash
curl -fsSL https://raw.githubusercontent.com/router-for-me/cliproxyapi-installer/refs/heads/master/cliproxyapi-installer | bash
```

That puts the binary, `config.yaml`, and a ready-made user service in `~/cliproxyapi`.

Arch folks: `yay -S cli-proxy-api-bin` gets you a binary and a user service.

Mac folks: `brew install cliproxyapi`.

There's also a Docker image (`eceasy/cli-proxy-api:latest`) and release binaries on GitHub. Docs live at https://help.router-for.me/.

### 2. Config

Where the config lives depends on how you installed:

- Linux installer: `~/cliproxyapi/config.yaml` (it already made you some access keys)
- Arch: `~/.cli-proxy-api/config.yaml`
- Mac with brew: `$(brew --prefix)/etc/cliproxyapi.conf`

Your account logins get saved in `~/.cli-proxy-api`. These are the parts that matter (v8 layout):

```yaml
config-version: 8
server:
  host: "127.0.0.1" # default binds everything, you do not want that
  port: 8317
  trusted-proxies: ["127.0.0.1", "::1"]
management:
  allow-remote: true # so you can open the panel over Tailscale
  secret-key: "<make one up>" # empty turns the management API off
access:
  api-keys: ["<your access key>"] # what Claude Code and Codex send
routing:
  strategy: "round-robin"
  session-affinity: true # off by default, turn it on (more below)
  session-affinity-ttl: "1h"
  retry:
    request-retry: 3
    max-retry-interval: 30
```

Bind it to localhost. Put a secret on the management panel. Tailscale is the front door, in step 5.

### 3. Run it always-on

On Linux it runs as a systemd user service with `Restart=always`. The installer already made one, so turn it on, then enable linger so it keeps running with nobody logged in:

```bash
systemctl --user enable --now cliproxyapi
loginctl enable-linger
```

On Arch the service is called `cli-proxy-api` instead.

On Mac: `brew services start cliproxyapi`.

### 4. Sign your accounts in (on the hub box)

Run these on the hub, not on your laptop. With the Linux installer, run them from `~/cliproxyapi` so they find your config:

```bash
cd ~/cliproxyapi
./cli-proxy-api -codex-device-login # device code, easiest on a headless box
./cli-proxy-api -codex-login # browser flow if you prefer
./cli-proxy-api -claude-login
```

Headless Claude login is the annoying one. The OAuth callback lands on port 54545 on the hub, and your browser isn't on the hub. Fix: from your laptop, open a tunnel first, then start the login and open the URL it gives you:

```bash
ssh -L 54545:localhost:54545 <box>
```

Adding `-no-browser` to a login command prints the URL (and tunnel instructions) instead of trying to open a browser that doesn't exist. Codex's browser callback is port 1455 if you go that route, same tunnel trick.

Repeat per account. Then open the management panel at `/management.html` on the hub URL and confirm everything actually showed up. There's also a `-tui` flag if you want to manage it from the terminal.

### 5. Put Tailscale in front of it

Install Tailscale on the hub and on every machine that should reach it, same tailnet. Then on the hub:

```bash
tailscale serve --bg --https=8317 http://127.0.0.1:8317
```

Now your hub is at `https://<box>.<tailnet>.ts.net:8317`, and only your tailnet can touch it. If you don't have the Google account I use to sign into Tailscale, you can't hit my endpoint. Simple.

### 6. Point your tools at it

Claude Code, in `~/.claude/settings.json` on each machine:

```json
{
  "env": {
    "ANTHROPIC_BASE_URL": "https://<box>.<tailnet>.ts.net:8317",
    "ANTHROPIC_AUTH_TOKEN": "<your access key>"
  }
}
```

Codex, in `~/.codex/config.toml` on each machine:

```toml
model_provider = "cliproxy"

[model_providers.cliproxy]
name = "CLIProxyAPI"
base_url = "https://<box>.<tailnet>.ts.net:8317/v1"
wire_api = "responses"
requires_openai_auth = false
supports_websockets = true
experimental_bearer_token = "<your access key>"
```

That's it. Or just tell your agent to do it. It'll swap two variables in the configs and you're done. This keeps working because Claude Code and Codex have to support custom endpoints for companies running everything through Bedrock on AWS. It would be bad for them if it didn't.

Want to keep your normal install untouched? Run a second instance with its own home dir pointed at the hub:

```bash
CLAUDE_CONFIG_DIR=~/.claude-hub claude
CODEX_HOME=~/.codex-hub codex
```

## Things I changed (your first token maxing tasks)

Stock CLIProxyAPI works if you set it up right. Affinity and websockets are just config. Routing is the one I had to fork for.

1. **Session affinity.** Already in the config above, but check that it's actually working. Caches are account specific. If a thread hops accounts per prompt, or worse per tool call, you eat a ton of cache writes for no reason. Keep an eye on your cache read and write numbers. If writes look crazy, your affinity is broken. Ask your agent: "how are we handling session affinity, are we writing cache more than we should be?"
2. **Soonest-reset-first routing.** By default it spreads traffic evenly across accounts. Dumb. If one account resets tomorrow at 1pm and another resets in 4 days, I want to burn the one that resets tomorrow. Whatever is left when a reset hits is money you lost. This isn't in upstream (it has per-account `priority` and `weight`, which isn't the same thing). I had an agent build it. You should too.
3. **Codex over websockets.** Codex subs don't use websockets through the proxy by default. Turn on `supports_websockets` in the Codex config above. That hugely increases performance. A little annoying to get right, worth it.

CLIProxyAPI: not necessarily the best UI. Want a better dashboard? Congrats, you have your first token maxing task :)

## When stuff breaks

- Need to bench an account? Set `"disabled": true` in its auth JSON in `~/.cli-proxy-api`. It drops out of rotation with no restart.
- Codex says `token_invalidated` (401)? That grant is dead. Rerun the device login on the hub: `./cli-proxy-api -codex-device-login` (from `~/cliproxyapi` on Linux).
- If only one account is enabled for a provider, a limit hit has nowhere else to go. Add a second if you can.
- Mac client can't resolve `*.ts.net` names with the open-source `tailscaled`? Run: `sudo mkdir -p /etc/resolver && echo "nameserver 100.100.100.100" | sudo tee /etc/resolver/<tailnet>.ts.net`

## One last thing

If you use T3 Code, it runs providers on the environment's machine, with that machine's CLI config. So if Claude Code on that machine points at the hub, Claude in T3 Code does too. For Codex, pick **Use existing CLI**. **Continue with ChatGPT** won't go through the hub. Want pooled limits in T3 Code? Settings > Providers > Usage providers > Add hub.

Now go burn your tokens before they reset 💀
