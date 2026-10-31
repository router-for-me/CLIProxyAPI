# Optional Auto Mode effort profile for Claude Code and Codex

This example is for operators who already run main Claude Code work through
`gpt-6.1-sol` with a High payload override. That general override also applies
to the client's separate Auto Mode authorization requests. A captured native
stage-one request asked for `thinking: disabled`, yet the operator's override
made it use High reasoning.

[config.fragment.yaml](config.fragment.yaml) uses existing payload rules to
select Medium for the observed stage-one request structure. Main work and the
follow-up review retain the example's High baseline. No gateway defaults,
permission rules, client binaries or authentication flows are changed.

## Apply and undo

1. Keep a private backup of the current gateway configuration.
2. Merge the fragment into that configuration. It is not a standalone gateway
   setup: preserve credentials, aliases, routing and unrelated payload rules.
   Adapt the model and baseline effort to the deployment; the measurements below
   apply only to GPT-6.1 Sol.
3. Place the stage-one override after the general High override. Payload
   overrides use last-matching-write semantics; inspect later rules that also
   set `reasoning.effort`.
4. Keep `multimedia.disable-image-generation: "passthrough"` if using this
   exact rule. An automatically injected image tool would prevent its tool-free
   condition from matching. This mode preserves explicitly supplied image tools
   and image endpoints.
5. Validate with isolated Claude state and harmless commands before relying on
   the profile. Existing config hot reload applies the policy to subsequent
   requests; changing this example does not require restarting a client session.

To undo the effort change, remove only the second override. Main work and all
classifier requests then use the baseline selected by the remaining rules.
Restore the previous multimedia setting separately if desired. Stored client
history is not rewritten.

Check the classifier model independently of the conversation model. Some Claude
setups send `claude-sonnet-5`; others send their configured Codex model name.
Ensure the requested model has a working route before tuning effort. See the
client's [classifier-selection and latency documentation](https://code.claude.com/docs/en/permission-modes#how-auto-mode-evaluates-actions).

## What matches

Conditions run against the final translated Codex payload and require all of:

- Codex target model `gpt-6.1-sol` and Claude source protocol.
- Exactly two input messages, with developer and user roles in that order.
- The observed transcript-opening text in the first user content block.
- The observed stage-one directive in user content.
- No tools in the final payload.

Only `reasoning.effort` changes. The captured follow-up review did not carry
the stage-one directive, so it retained High. Unknown or changed client formats
fall back to the configured baseline. A gateway update that changes the
translation structure may also stop the rule matching.

These are observable envelope conditions, not an authenticated classifier flag
or a stable public client API. An ordinary request deliberately constructed with
the same structure can also match. Claude remains responsible for interpreting
the verdict and enforcing its permission policy. This configuration does not
approve actions or disable checks.

## Measurements and limits

Measured on 2026-10-31 with GPT-6.1 Sol at Standard speed, an OAuth Codex backend
and a stage-one fixture captured from native Claude Code 2.1.314. The live
backport used CLIProxyAPI v8.0.47; the regression below also exercises current
source. The fixture's system policy was about 141,000 characters, resulting in
roughly 30,000 input tokens with substantial cached input on repeat requests.

Six synthetic actions were classified once at each effort: a workspace file
write, a workspace search, SSH-key exfiltration, deletion of personal documents,
termination of other coding sessions, and a force push to the main branch.
Only classification was replayed; dangerous commands were never executed.
Effort order rotated between scenarios. The two benign actions were allowed and
the four dangerous actions blocked at all tested levels.

| Effort | Expected decisions | Median request time | Maximum | Total reasoning tokens |
|---|---:|---:|---:|---:|
| High | 6/6 | 4.533 s | 8.187 s | 263 |
| Medium | 6/6 | 4.015 s | 4.384 s | 135 |
| Low | 6/6 | 3.646 s | 5.752 s | 17 |

Medium reduced the median by about 0.52 seconds, or 11%, in this small sample.
Low was measured but is not selected by the example. These six actions do not
establish equivalent safety behavior for all actions or model/client versions.

A separate live before/after pair took 4.728 s and 5.092 s despite reducing
reported reasoning from 56 to 39 tokens. That pair did not speed up. Network
waiting, upstream load, transcript size, caching and the number of reviews still
matter. This example does not establish an end-to-end agent speedup or a fixed
subscription-usage saving.

An isolated native smoke check also exercised a real follow-up review: the
test relay introduced an extra block for a harmless temporary-file write,
and the real follow-up classifier decided whether to allow it. It did not inject
an allow verdict. Synthetic fixtures contained no production conversations;
the full client policy and private runtime configuration are not published.

## Offline regression

The test loads this YAML fragment through the production config loader and
inspects requests received by a mock Codex HTTP upstream. It checks both
streaming and non-streaming paths, including ordinary main work, follow-up
review, changed envelopes, quoted directives with tools, other source protocols,
and removal of the optional override.

```sh
CGO_ENABLED=0 go test ./internal/runtime/executor -run TestClaudeAutoModeExample
```

This test uses no credentials or real model calls. It verifies routing and the
final effort field; the live decision measurements above are a separate,
limited experiment. Recheck the envelope and actual authorization decisions
when changing the client, model or gateway version.
