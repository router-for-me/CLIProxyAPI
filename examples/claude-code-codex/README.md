# Claude Code with a Codex backend

Claude Code can remain the client while CLIProxyAPI routes its Messages requests
to a Codex credential. Client model names are routing aliases: they do not change
the upstream model or make the connection an Anthropic subscription session.

## Public progress during tool use

Codex distinguishes public `commentary` messages from `final_answer` messages.
By default, the Claude translator carries commentary as a `thinking` block. This
keeps existing text-only and speech consumers from receiving both a preamble and
a final answer. In an interactive coding session, however, progress and answers
to questions asked during a tool loop can consequently appear hidden.

To show that public text:

```yaml
client:
  claude:
    show-codex-commentary: true
```

The default is `false`. `CPA_VISIBLE_CODEX_COMMENTARY=1` also enables the option
for deployments and embedded translation callers using environment settings.
The display policy is captured for each request; reloading the configuration
does not change the representation halfway through an existing stream.

This works with streaming and non-streaming Codex-to-Claude responses. It does
not turn genuine reasoning summaries into public text. A commentary block keeps
its existing `cpa:commentary:v1` replay marker and gains a visible text copy when
the public block finishes. Final-answer text continues to stream normally.
Both forms are stored in the client's history. With the option enabled, request
translation consumes the exact adjacent visible copy, preserving one upstream
`commentary` item. Other final-answer text, tool calls, and user messages remain.
This display operation does not issue an additional generation or increase the
upstream-reported output token count.

Existing history is not rewritten. Use the setting consistently within a
conversation: disabling it leaves previously recorded copies in history and
restores the original replay behavior rather than modifying stored messages.

## The Auto Mode classifier has its own model

Claude Code may use a different model for tool authorization than it uses for
the main conversation. A working main-model alias therefore does not prove the
classifier route is available. For example, a client may request
`claude-sonnet-5` even when its conversation and default Sonnet alias use a
different version. Check the actual model named by the client and the request
logs. Configure a separate alias for that model when needed:

```yaml
oauth:
  model-alias:
    codex:
      - name: "your-codex-model"
        alias: "claude-sonnet-5"
        fork: true
```

Replace `your-codex-model` with a model available to the selected credential.
Tool authorization still requires a real classifier verdict. Permission rules,
workspace trust, and client approvals continue to be owned by Claude Code.
See the client's [permission-mode documentation](https://code.claude.com/docs/en/permission-modes)
for its current classifier-selection rules.

## Image tools and coding latency

CLIProxyAPI can add the native OpenAI `image_generation` tool automatically.
That is separate from any external MCP image tool declared by a client.
Operators who want the client to own its tool list can use the existing mode:

```yaml
multimedia:
  disable-image-generation: "passthrough"
```

On non-image endpoints this mode neither injects nor strips the image tool.
Explicitly requested image tools remain available, and `/v1/images/generations`
and `/v1/images/edits` remain enabled. This is an operator choice; the default
image-generation behavior is unchanged by the commentary feature.

Match tool definitions when comparing latency. A direct request without an
image tool and a proxied request with an automatically injected one are different
model workloads. Also distinguish response headers, the first SSE event, the
first generated content, the first visible text, and completion. A
`response.created` event is not generated answer text.

For large OAuth requests, existing upstream request compression can reduce
network bytes:

```yaml
upstream:
  codex:
    upstream-request-compression: true
```

This is lossless zstd transport compression, not conversation summarization or
token reduction. The executor retries a rejected compressed body without
compression. Model reasoning, context processing, classifier requests, and
upstream queueing can still dominate latency.

## Validate with isolated client state

Use a temporary `CLAUDE_CONFIG_DIR` and a separate test session for live checks.
Check a simple answer, a harmless tool call, and a resume of that same session.
If testing a Telegram setup, use an empty strict MCP configuration instead of
starting a second poller with a live bot token. Native client history and a
gateway's upstream credential store are distinct and should remain separate.
