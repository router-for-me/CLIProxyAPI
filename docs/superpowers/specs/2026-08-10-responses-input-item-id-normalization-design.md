# Responses Input Item ID Normalization

## Context

CLIProxyAPI v7.2.102 currently forwards HTTP `POST /v1/responses` payloads without validating item ID prefixes. Production error logs from 2026-08-10 show that Codex Desktop can replay local history items whose IDs begin with `item_`:

- `type: "function_call"` is rejected because the upstream requires an `fc_` ID.
- `type: "message"` is rejected because the upstream requires a `msg_` ID.

The invalid IDs are already present in the downstream request body and remain unchanged in the upstream request body. The current v7.2.119 source still lacks HTTP Responses input-ID normalization, so upgrading alone does not address this failure.

## Goal

Allow affected Codex Desktop histories to continue through CLIProxyAPI without changing valid Responses items or tool-call relationships.

Success requires:

1. A local `function_call` ID `item_<suffix>` becomes `fc_<suffix>`.
2. A local `message` ID `item_<suffix>` becomes `msg_<suffix>`.
3. Existing valid IDs, `call_id` values, input ordering, item contents, and unrecognized item types remain unchanged.
4. The focused OpenAI handler test package passes.

## Non-goals

- Rewriting every Responses item type without observed evidence.
- Modifying `call_id` values or repairing unrelated tool-call pairing errors.
- Changing websocket request handling in this patch; the observed failures use HTTP downstream and upstream transports.
- Deploying or restarting the production container without a separate deployment decision.

## Considered Approaches

### 1. Targeted stable prefix rewrite - selected

For `input` array items only, replace a leading `item_` according to the two observed types while retaining the suffix. This preserves stable identity and minimizes behavioral change.

### 2. Remove mismatched IDs

Let the upstream allocate IDs by deleting the field. This is smaller mechanically but can discard replay identity and interfere with deduplication or diagnostics.

### 3. Normalize every known Responses type

Maintain a complete type-to-prefix table. This is broader than the evidence supports and increases compatibility risk for item types with different lifecycle rules.

## Design

Add a small deterministic normalizer in the OpenAI Responses handler package. It accepts the raw JSON payload and returns either:

- an updated payload when a recognized local ID is found; or
- the original payload when `input` is absent, is not an array, contains no matching item, or cannot be safely updated.

The mapping is deliberately narrow:

| Item type | Local prefix | Upstream prefix |
| --- | --- | --- |
| `function_call` | `item_` | `fc_` |
| `message` | `item_` | `msg_` |

The handler invokes the normalizer immediately after reading the request body and before multi-agent tool preparation or provider execution. No payload content is logged by the normalizer.

## Error Handling

Normalization is compatibility handling, not request validation. Unexpected JSON shapes or update failures leave the payload unchanged so existing validation and error behavior remain authoritative. The helper does not generate random IDs and never rewrites a non-`item_` ID.

## Test Strategy

Follow red-green-refactor:

1. Add a focused failing test reproducing both production variants in one input transcript and asserting `fc_`/`msg_` rewrites with preserved suffixes and `call_id`.
2. Add preservation cases for valid IDs and an unknown item type carrying an `item_` ID.
3. Implement only enough normalization and handler wiring to make the tests pass.
4. Run the focused `sdk/api/handlers/openai` package, then rerun the wider suite while treating the three already-recorded executor platform-fingerprint failures as baseline failures unless their result changes.
5. Use code-review-graph change detection and impact analysis before completion.

## Deployment Boundary

After source verification, deployment may build a versioned local image from the branch and restart only the `cli-proxy-api` Compose service. That action requires explicit approval because it replaces a running production container.
