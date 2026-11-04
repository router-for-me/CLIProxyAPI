# Model listing & client auth

Clients list available models in their native dialect, and every `/v1` and `/v1beta` route rejects requests without a configured API key.

## Sub-features

- `models-auth` no key / wrong key → 401.
- `models-openai` `GET /v1/models` with `Authorization: Bearer` → 200, OpenAI `{"object":"list",...}`.
- `models-claude` same route with `x-api-key` + `anthropic-version` → 200 (Claude-shaped list).
- `models-gemini` `GET /v1beta/models` with `x-goog-api-key` → 200.

## How to get to it (user POV)

- OpenAI SDK/Codex `GET /v1/models`; Claude Code `GET /v1/models`; Gemini CLI `GET /v1beta/models`.

## Driving it with cpa-verify.sh

Preconditions: doctor OK; `-local-model` so the catalog is the embedded one.

- **Reject.** `scripts/cpa-verify.sh drive models` sends `models-noauth` and `models-badkey`; both `401`.
- **Accept.** Same run sends `models`, `models-anthropic`, `gemini-models`; all `200`, `models.body` contains `"object"`.

## Gotchas

- Without `-local-model` the server fetches remote catalogs at startup (network, slower, nondeterministic).
- Model lists are filtered by available credentials; with an empty auth-dir expect few or no provider models — assert shape, not specific IDs.
