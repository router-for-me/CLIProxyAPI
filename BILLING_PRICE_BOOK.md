# Billing price book

The built-in billing price book lives in `internal/billing/price_book.go`. It is used only when `billing.price-book.rules` is empty. If an operator supplies any custom `Rules`, that list is a full replacement; the defaults are not merged in.

## Coverage

Default coverage in version `2026-09-21.1`: **88 explicit model ID rules**.

The price book covers standard short-context text-equivalent rates for mainstream API text models from:

- OpenAI
- Anthropic Claude
- Google Gemini Developer API
- xAI Grok
- DeepSeek

All rates are stored as nanos per token:

```text
nanos/token = USD per 1M tokens * 1000
```

For example, `$4.00 / 1M input tokens` is stored as `4000` nanos/token.

## Sources checked

- OpenAI: official markdown fetch from `https://developers.openai.com/api/docs/pricing.md`. The fetched table lists standard short-context prices for `gpt-5.6-sol`, `gpt-5.6-terra`, and `gpt-5.6-luna` as:
  - `gpt-5.6-sol`: input `$4.00`, cached input `$0.40`, cache writes `$5.00`, output `$20.00` per 1M tokens.
  - `gpt-5.6-terra`: input `$2.00`, cached input `$0.20`, cache writes `$2.50`, output `$12.00` per 1M tokens.
  - `gpt-5.6-luna`: input `$0.20`, cached input `$0.02`, cache writes `$0.25`, output `$1.20` per 1M tokens.
- Anthropic: official Claude pricing page `https://docs.anthropic.com/en/docs/about-claude/pricing` and models overview `https://docs.anthropic.com/en/docs/about-claude/models/overview`.
- Google Gemini: official public Gemini Developer API pricing page `https://ai.google.dev/gemini-api/docs/pricing?hl=en`; tool fetch redirected through Google sign-in, so the public HTML was read via command-line retrieval with a browser user agent.
- xAI: official Grok models/pricing page `https://docs.x.ai/developers/models/grok-4.6`.
- DeepSeek: official API pricing page `https://api-docs.deepseek.com/quick_start/pricing`.

## Limitations

This default price book intentionally covers only standard text-equivalent token pricing suitable for request cost accounting.

It does **not** model:

- Long-context price tiers, such as OpenAI or Gemini prompt-size thresholds.
- Batch, fast/priority/flex, provisioned throughput, data residency, regional uplift, or enterprise/private-offer pricing.
- Modality-specific pricing for audio, image generation, video generation, speech, embeddings, moderation, or tool-specific charges.
- Time-varying prices. DeepSeek publishes peak and off-peak rates; the static default uses peak rates so it does not under-price by time of day.
- Cache storage charges priced per hour. The runtime has per-token cache read/write fields only.
- Vendor aliases not explicitly listed. The default price book does not use wildcards or family-prefix matching.

For models with reasoning/thinking tokens, `output_reasoning_nanos_per_token` is set equal to the published output token rate unless the vendor publishes a separate reasoning-token rate.

## Deployment and history

Rates checked 2026-09-21. Existing ledger snapshots are not repriced. Gemini 3.6/3.7/3.8 Flash promotional rates expire after 2026-12-31; review before 2027-01-01. GPT-5.6 Sol promotional pricing is guaranteed at least through 2026-11-21.

Anthropic 1-hour write rates are retained in the price book, but the current usage-to-billing adapter uses 5-minute writes when TTL is unknown, marking cache creation estimated.
