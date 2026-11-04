# Chat completions

OpenAI-compatible `POST /v1/chat/completions` routes a request to a provider account chosen by model name.

## Sub-features

- `chat-auth` no API key → 401.
- `chat-no-provider` valid key, no credential for the model → 4xx JSON `{"error":{...}}` (observed: 400 `model_not_found`, "unknown provider for model gpt-5").
- `chat-real` actual completion — SKIPPED without OAuth credentials in auth-dir.

## How to get to it (user POV)

- Any OpenAI SDK with `base_url=http://host:port/v1`.

## Driving it with cpa-verify.sh

Preconditions: doctor OK; empty auth-dir.

- **Reject.** `scripts/cpa-verify.sh drive chat-completions` → `chat-noauth.status` `401`.
- **Error shape.** `chat-nocreds.status` ≥ 400 and body has `"error"`.

## Gotchas

- To exercise `chat-real`, copy a disposable credential JSON into `$CPA_RUN/auths/` after launch (hot-reloaded); never copy the user's `~/.cli-proxy-api` wholesale and never commit it.
- `/v1/messages` (Claude) and `/v1/responses` follow the same auth middleware; add steps here when covering them.
