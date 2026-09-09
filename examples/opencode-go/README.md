# OpenCode Go through CLIProxyAPI

OpenCode Go asks coding clients to identify themselves and send a stable
conversation ID for routing and prompt caching. Its [client requirements](https://opencode.ai/docs/go/#where-can-i-use-it)
also document support for Claude Code's and Codex's native session headers.

CLIProxyAPI can forward those headers with its existing dynamic custom-header
configuration. A `$Header-Name` value copies that incoming request header; when
the client omits it, CLIProxyAPI omits the corresponding custom header.

## Chat Completions models

Add the following provider to `config.yaml`, replacing the API key with your
OpenCode Go key. Choose a model whose [documented endpoint](https://opencode.ai/docs/go/#endpoints)
is `/chat/completions`:

```yaml
openai-compatibility:
  - name: "opencode-go"
    base-url: "https://opencode.ai/zen/go/v1"
    headers:
      User-Agent: "$User-Agent"
      X-Opencode-Session: "$X-Opencode-Session"
      X-Claude-Code-Session-Id: "$X-Claude-Code-Session-Id"
      Session_id: "$Session_id"
      Session-Id: "$Session-Id"
    api-key-entries:
      - api-key: "YOUR_OPENCODE_GO_API_KEY"
    models:
      - name: "deepseek-v4-flash"
        alias: "go-deepseek-v4-flash"
```

Select `go-deepseek-v4-flash` in the coding client connected to CLIProxyAPI.
The header names are case-insensitive; `Session_id` and `Session-Id` remain
distinct names, so both Codex spellings are included. The user agent is the
real caller's value. This configuration does not impersonate another client.

This provider example uses the Chat Completions upstream path. Do not copy a
Responses-only or Messages-only model into it: select the matching upstream
protocol separately. Session headers do not change the model's wire protocol.

## Clients with another conversation header

For a client that sends its conversation ID as `X-Session-ID`, replace the
`X-Opencode-Session` mapping above with:

```yaml
headers:
  User-Agent: "$User-Agent"
  X-Opencode-Session: "$X-Session-ID"
```

Use the actual header sent by your client. `$Header-Name` reads an HTTP header;
it does not interpolate task metadata or expressions such as `${task-id}`.
Keep the ID stable across turns in one conversation and change it for a new
conversation. Do not configure a single static ID shared by all conversations.

For a custom client or SDK caller, send your own user agent and a stable
`X-Opencode-Session` header on every model request, including auxiliary requests
that belong to the same conversation. A proxy cannot recover a missing explicit
conversation ID merely by copying headers.

## Checking the configuration

If Go still reports a missing session, inspect a redacted request log and check
both the incoming and outgoing header sets. At least one session header
recognized by Go must survive every proxy hop. In particular, forwarding
`$X-Opencode-Session` alone does nothing when the client only sends its native
Claude Code or Codex header.

The provider API key belongs in `api-key-entries`. Header forwarding supplies
client and conversation metadata; it does not replace upstream authentication.
