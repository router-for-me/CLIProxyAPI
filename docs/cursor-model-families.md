# Cursor model families

Cursor can advertise separate model IDs for reasoning effort, thinking, and
speed, such as `claude-opus-5-thinking-high-fast`. The model list returns one
family ID for each of those groups, with the reasoning levels and fast speed
that account actually offers. Clients select the family and send its options
separately.

For example, when the corresponding variant is available:

```json
{
  "model": "cursor/claude-opus-5",
  "reasoning": { "effort": "high" },
  "service_tier": "priority",
  "input": "Hello"
}
```

This Responses request selects `claude-opus-5-thinking-high-fast`. The
`cursor/` prefix in this example is an auth configuration prefix; use the
prefix configured for your account, or omit it when none is configured.

| API | Effort | Thinking | Fast speed |
| --- | --- | --- | --- |
| Responses | `reasoning.effort` | Named effort selects thinking variants when present; `none` selects non-thinking | `service_tier: "priority"` |
| Chat Completions | `reasoning_effort` | Same as Responses | `service_tier: "priority"` |
| Claude Messages | `output_config.effort`, or normalized `thinking.budget_tokens` | `thinking.type`: `enabled`, `adaptive`, or `disabled` | `speed: "fast"` |

Without explicit options, a family uses its exact base ID when available.
Otherwise, it prefers a thinking variant when that dimension exists, medium
effort followed by high, and standard speed. Only advertised variants can be
selected. Unsupported combinations return HTTP 400 rather than silently
selecting a different effort or speed.

Families with neither effort nor thinking variants ignore effort/thinking
values sent by harness defaults. For example, Composer 2.5 accepts a request
containing `reasoning.effort: "medium"` but only its speed setting affects
variant selection. This does not add reasoning controls to Composer.

Canonical model suffixes such as `claude-opus-5(high)` also select family
options and take precedence over body effort and thinking settings.
Thinking-only families advertise thinking support even without effort levels.

Explicit variant IDs still resolve when a client sends them, but they are
not listed separately. A family advertises its reasoning levels, and a fast
service tier when any advertised variant is fast. Model exclusions are applied
before deriving families and remain effective during variant resolution.
Available options can differ between accounts; advertised family effort levels
do not imply that every effort/thinking/speed combination exists.

If a selected account lacks a requested combination, the proxy tries another
eligible account within the configured credential-attempt limit. These misses
do not cool down the account or family. If the available attempts are exhausted,
the request returns HTTP 400; invalid options stop immediately without rotation.
Canceled model refreshes retain both the previous registration and its filtered
routing catalog.

This routing feature does not change tool translation or streaming behavior,
and does not establish compatibility of every Cursor model with every harness.
