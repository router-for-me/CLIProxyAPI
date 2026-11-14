# Local Responses input-token estimates

The Codex executor counts locally; its `/v1/messages/count_tokens` result is an
estimate, not an authoritative server count. Do not use it as an exact billing
measurement or a guarantee that a request will fit an upstream context window.

The estimator includes instructions, message roles and text, whitespace,
function/custom-tool calls and textual results, nested tool definitions, JSON
output formats, reasoning summaries and image estimates. It accepts Responses
message shorthand as well as explicit message/content blocks. Transport metadata,
request IDs, generation limits and opaque encrypted reasoning are not tokenized.
Provider framing, server-added instructions, encrypted state, file documents,
audio and video can make the actual input count differ. Image bytes and URLs are
never tokenized as text.

For an inline PNG, JPEG or GIF with readable headers, documented model-specific
patch/tile formulas are used for the image component. The formulas follow
[OpenAI's image guide](https://developers.openai.com/api/docs/guides/images-vision),
including detail-specific resizing, patch budgets, multipliers and tile costs.
The model policy table is intentionally limited to documented families; an
unlisted variant does not inherit a formula just because its name shares a
prefix. Billing rounding can differ by a token even for documented examples.

For unknown families or unreadable dimensions, use the same **coarse fallback**
as the [native Codex context estimator](https://github.com/openai/codex/blob/main/codex-rs/core/src/context_manager/history.rs):
1,844 tokens per image. With `detail: original`, a readable inline image uses its
32×32 patch count, capped at 10,000; an opaque file reference uses 10,000.
This fallback can overestimate small images and underestimate large images. It
is not a model's official image charge and must not be described as exact or a
safe upper bound. In particular, the current Sol, Terra and Luna IDs use this
fallback until their preprocessing rules are documented. Astra has a documented
policy. WebP and other unrecognized formats also use the fallback.

Counting never fetches an image URL or opens a file reference. Only image headers
are decoded, with a 1 MiB read limit, so counting does not add image pixel
allocations, arbitrary URL requests or access to local files. Invalid or unusual
images fall back without preventing an otherwise valid generation request.

Preliminary counts must not be added to final provider usage. For context usage,
count each cached input token once; cache hits still occupy context. Actual
Codex response usage remains authoritative for the completed request. After
switching models, an earlier model's actual usage is not a fresh preflight count
for the new model. These local estimates improve coverage but cannot establish
an exact auto-compaction boundary.
