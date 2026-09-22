package billing

const defaultPriceBookVersion = "2026-09-21.1"

// defaultPriceRules returns the built-in standard short-context text price book
// unless an operator supplies any custom rules. A non-empty custom rule list is
// a full replacement, not an overlay, so operators can pin their own billing
// semantics without inherited defaults.
func defaultPriceRules(pb PriceBookConfig) []PriceRule {
	if len(pb.Rules) > 0 {
		return pb.Rules
	}
	return append([]PriceRule(nil), defaultPriceBookRules...)
}

// defaultPriceBookRules contains explicit model IDs only. Rates are expressed
// in nanos/token, which is USD-per-1M-tokens multiplied by 1000.
var defaultPriceBookRules = []PriceRule{
	// OpenAI standard short-context text pricing.
	priceRule("openai-gpt-6-astra-standard-20260921", "openai", "gpt-6-astra", 10000, 1000, 12500, 50000),
	priceRule("openai-gpt-5.6-sol-standard-20260921", "openai", "gpt-5.6-sol", 4000, 400, 5000, 20000),
	priceRule("openai-gpt-5.6-terra-standard-20260921", "openai", "gpt-5.6-terra", 2000, 200, 2500, 12000),
	priceRule("openai-gpt-5.6-luna-standard-20260921", "openai", "gpt-5.6-luna", 200, 20, 250, 1200),
	priceRuleNoWrite("openai-gpt-5.5-standard-20260921", "openai", "gpt-5.5", 5000, 500, 30000),
	priceRuleNoWrite("openai-gpt-5.5-pro-standard-20260921", "openai", "gpt-5.5-pro", 30000, 0, 180000),
	priceRuleNoWrite("openai-gpt-5.4-standard-20260921", "openai", "gpt-5.4", 2500, 250, 15000),
	priceRuleNoWrite("openai-gpt-5.4-mini-standard-20260921", "openai", "gpt-5.4-mini", 750, 75, 4500),
	priceRuleNoWrite("openai-gpt-5.4-nano-standard-20260921", "openai", "gpt-5.4-nano", 200, 20, 1250),
	priceRuleNoWrite("openai-gpt-5.4-pro-standard-20260921", "openai", "gpt-5.4-pro", 30000, 0, 180000),
	priceRuleNoWrite("openai-gpt-5.2-standard-20260921", "openai", "gpt-5.2", 1750, 175, 14000),
	priceRuleNoWrite("openai-gpt-5.2-pro-standard-20260921", "openai", "gpt-5.2-pro", 21000, 0, 168000),
	priceRuleNoWrite("openai-gpt-5.1-standard-20260921", "openai", "gpt-5.1", 1250, 125, 10000),
	priceRuleNoWrite("openai-gpt-5-standard-20260921", "openai", "gpt-5", 1250, 125, 10000),
	priceRuleNoWrite("openai-gpt-5-mini-standard-20260921", "openai", "gpt-5-mini", 250, 25, 2000),
	priceRuleNoWrite("openai-gpt-5-nano-standard-20260921", "openai", "gpt-5-nano", 50, 5, 400),
	priceRuleNoWrite("openai-gpt-5-pro-standard-20260921", "openai", "gpt-5-pro", 15000, 0, 120000),
	priceRuleNoWrite("openai-gpt-4.1-standard-20260921", "openai", "gpt-4.1", 2000, 500, 8000),
	priceRuleNoWrite("openai-gpt-4.1-mini-standard-20260921", "openai", "gpt-4.1-mini", 400, 100, 1600),
	priceRuleNoWrite("openai-gpt-4.1-nano-standard-20260921", "openai", "gpt-4.1-nano", 100, 25, 400),
	priceRuleNoWrite("openai-gpt-4o-standard-20260921", "openai", "gpt-4o", 2500, 1250, 10000),
	priceRuleNoWrite("openai-gpt-4o-2024-05-13-standard-20260921", "openai", "gpt-4o-2024-05-13", 5000, 0, 15000),
	priceRuleNoWrite("openai-gpt-4o-mini-standard-20260921", "openai", "gpt-4o-mini", 150, 75, 600),
	priceRuleNoWrite("openai-o1-standard-20260921", "openai", "o1", 15000, 7500, 60000),
	priceRuleNoWrite("openai-o1-pro-standard-20260921", "openai", "o1-pro", 150000, 0, 600000),
	priceRuleNoWrite("openai-o3-pro-standard-20260921", "openai", "o3-pro", 20000, 0, 80000),
	priceRuleNoWrite("openai-o3-standard-20260921", "openai", "o3", 2000, 500, 8000),
	priceRuleNoWrite("openai-o4-mini-standard-20260921", "openai", "o4-mini", 1100, 275, 4400),
	priceRuleNoWrite("openai-o3-mini-standard-20260921", "openai", "o3-mini", 1100, 550, 4400),
	priceRuleNoWrite("openai-gpt-4-turbo-2024-04-09-standard-20260921", "openai", "gpt-4-turbo-2024-04-09", 10000, 0, 30000),
	priceRuleNoWrite("openai-gpt-4-0613-standard-20260921", "openai", "gpt-4-0613", 30000, 0, 60000),
	priceRuleNoWrite("openai-gpt-3.5-turbo-standard-20260921", "openai", "gpt-3.5-turbo", 500, 0, 1500),
	priceRuleNoWrite("openai-gpt-3.5-turbo-0125-standard-20260921", "openai", "gpt-3.5-turbo-0125", 500, 0, 1500),
	priceRuleNoWrite("openai-gpt-3.5-turbo-1106-standard-20260921", "openai", "gpt-3.5-turbo-1106", 1000, 0, 2000),
	priceRuleNoWrite("openai-gpt-3.5-turbo-instruct-standard-20260921", "openai", "gpt-3.5-turbo-instruct", 1500, 0, 2000),
	priceRuleNoWrite("openai-davinci-002-standard-20260921", "openai", "davinci-002", 2000, 0, 2000),
	priceRuleNoWrite("openai-babbage-002-standard-20260921", "openai", "babbage-002", 400, 0, 400),
	priceRuleNoWrite("openai-gpt-5.3-codex-standard-20260921", "openai", "gpt-5.3-codex", 1750, 175, 14000),
	priceRuleNoWrite("openai-gpt-5-search-api-standard-20260921", "openai", "gpt-5-search-api", 1250, 125, 10000),
	priceRuleNoWrite("openai-chat-latest-standard-20260921", "openai", "chat-latest", 5000, 500, 30000),

	// Anthropic Claude standard API pricing. Both exact dated IDs known from
	// current model docs and user-facing dot aliases are listed explicitly.
	priceRule("claude-fable-5.1-standard-20260921", "claude", "claude-fable-5.1", 10000, 250, 12500, 50000),
	priceRule("claude-fable-5-1-standard-20260921", "claude", "claude-fable-5-1", 10000, 250, 12500, 50000),
	priceRule("claude-mythos-5.1-standard-20260921", "claude", "claude-mythos-5.1", 10000, 250, 12500, 50000),
	priceRule("claude-mythos-5-1-standard-20260921", "claude", "claude-mythos-5-1", 10000, 250, 12500, 50000),
	priceRule("claude-fable-5-standard-20260921", "claude", "claude-fable-5", 10000, 1000, 12500, 50000),
	priceRule("claude-mythos-5-standard-20260921", "claude", "claude-mythos-5", 10000, 1000, 12500, 50000),
	priceRule("claude-opus-5-standard-20260921", "claude", "claude-opus-5", 5000, 500, 6250, 25000),
	priceRule("claude-opus-4.8-standard-20260921", "claude", "claude-opus-4.8", 5000, 500, 6250, 25000),
	priceRule("claude-opus-4-8-standard-20260921", "claude", "claude-opus-4-8", 5000, 500, 6250, 25000),
	priceRule("claude-opus-4.7-standard-20260921", "claude", "claude-opus-4.7", 5000, 500, 6250, 25000),
	priceRule("claude-opus-4-7-standard-20260921", "claude", "claude-opus-4-7", 5000, 500, 6250, 25000),
	priceRule("claude-opus-4.6-standard-20260921", "claude", "claude-opus-4.6", 5000, 500, 6250, 25000),
	priceRule("claude-opus-4-6-standard-20260921", "claude", "claude-opus-4-6", 5000, 500, 6250, 25000),
	priceRule("claude-opus-4.5-standard-20260921", "claude", "claude-opus-4.5", 5000, 500, 6250, 25000),
	priceRule("claude-opus-4-5-standard-20260921", "claude", "claude-opus-4-5", 5000, 500, 6250, 25000),
	priceRule("claude-opus-4.1-standard-20260921", "claude", "claude-opus-4.1", 15000, 1500, 18750, 75000),
	priceRule("claude-opus-4-1-20250805-standard-20260921", "claude", "claude-opus-4-1-20250805", 15000, 1500, 18750, 75000),
	priceRule("claude-opus-4-standard-20260921", "claude", "claude-opus-4", 15000, 1500, 18750, 75000),
	priceRule("claude-opus-4-20250514-standard-20260921", "claude", "claude-opus-4-20250514", 15000, 1500, 18750, 75000),
	priceRule("claude-sonnet-5-standard-20260921", "claude", "claude-sonnet-5", 2000, 200, 2500, 10000),
	priceRule("claude-sonnet-4.6-standard-20260921", "claude", "claude-sonnet-4.6", 3000, 300, 3750, 15000),
	priceRule("claude-sonnet-4-6-standard-20260921", "claude", "claude-sonnet-4-6", 3000, 300, 3750, 15000),
	priceRule("claude-sonnet-4.5-standard-20260921", "claude", "claude-sonnet-4.5", 3000, 300, 3750, 15000),
	priceRule("claude-sonnet-4-5-20250929-standard-20260921", "claude", "claude-sonnet-4-5-20250929", 3000, 300, 3750, 15000),
	priceRule("claude-sonnet-4-standard-20260921", "claude", "claude-sonnet-4", 3000, 300, 3750, 15000),
	priceRule("claude-sonnet-4-20250514-standard-20260921", "claude", "claude-sonnet-4-20250514", 3000, 300, 3750, 15000),
	priceRule("claude-haiku-4.5-standard-20260921", "claude", "claude-haiku-4.5", 1000, 100, 1250, 5000),
	priceRule("claude-haiku-4-5-20251001-standard-20260921", "claude", "claude-haiku-4-5-20251001", 1000, 100, 1250, 5000),
	priceRule("claude-haiku-3.5-standard-20260921", "claude", "claude-haiku-3.5", 800, 80, 1000, 4000),
	priceRule("claude-3-5-haiku-20241022-standard-20260921", "claude", "claude-3-5-haiku-20241022", 800, 80, 1000, 4000),

	priceRule("claude-opus-4-5-20251101-standard-20260921", "claude", "claude-opus-4-5-20251101", 5000, 500, 6250, 25000),
	priceRule("claude-haiku-4-5-standard-20260921", "claude", "claude-haiku-4-5", 1000, 100, 1250, 5000),
	priceRule("claude-sonnet-4-5-standard-20260921", "claude", "claude-sonnet-4-5", 3000, 300, 3750, 15000),

	// Google Gemini Developer API standard text-equivalent pricing.
	// 3.6/3.7/3.8 Flash promotional prices expire after 2026-12-31.
	priceRuleNoWrite("gemini-3.7-flash-standard-20260921", "gemini", "gemini-3.7-flash", 750, 75, 3750),
	priceRuleNoWrite("gemini-3.6-flash-standard-20260921", "gemini", "gemini-3.6-flash", 750, 75, 3750),
	priceRuleNoWrite("gemini-3.5-flash-standard-20260921", "gemini", "gemini-3.5-flash", 1500, 150, 9000),
	priceRuleNoWrite("gemini-3.5-flash-lite-standard-20260921", "gemini", "gemini-3.5-flash-lite", 300, 30, 2500),
	priceRuleNoWrite("gemini-3.1-flash-lite-standard-20260921", "gemini", "gemini-3.1-flash-lite", 250, 25, 1500),
	priceRuleNoWrite("gemini-3.8-flash-standard-20260921", "gemini", "gemini-3.8-flash", 750, 75, 3750),
	priceRuleNoWrite("gemini-3.1-pro-preview-standard-20260921", "gemini", "gemini-3.1-pro-preview", 2000, 200, 12000),
	priceRuleNoWrite("gemini-3.1-pro-preview-customtools-standard-20260921", "gemini", "gemini-3.1-pro-preview-customtools", 2000, 200, 12000),
	priceRuleNoWrite("gemini-3-flash-preview-standard-20260921", "gemini", "gemini-3-flash-preview", 500, 50, 3000),
	priceRuleNoWrite("gemini-2.5-pro-standard-20260921", "gemini", "gemini-2.5-pro", 1250, 125, 10000),
	priceRuleNoWrite("gemini-2.5-flash-standard-20260921", "gemini", "gemini-2.5-flash", 300, 30, 2500),
	priceRuleNoWrite("gemini-2.5-flash-lite-standard-20260921", "gemini", "gemini-2.5-flash-lite", 100, 10, 400),

	// xAI text pricing.
	priceRuleNoWrite("xai-grok-4.6-standard-20260921", "xai", "grok-4.6", 2000, 500, 6000),

	// DeepSeek peak text pricing. The price book is static and cannot switch by
	// UTC peak window at usage time, so it uses the published peak price.
	priceRuleNoWrite("deepseek-flash-peak-standard-20260921", "deepseek", "deepseek-flash", 300, 6, 1200),
	priceRuleNoWrite("deepseek-v4-pro-peak-standard-20260921", "deepseek", "deepseek-v4-pro", 1320, 44, 3960),
}

func priceRuleNoWrite(id, provider, model string, input, cacheRead, output int64) PriceRule {
	return priceRule(id, provider, model, input, cacheRead, 0, output)
}

func priceRule(id, provider, model string, input, cacheRead, cacheWrite5m, output int64) PriceRule {
	// Anthropic publishes 1-hour cache writes at twice the base input rate.
	var cacheWrite1h int64
	if provider == "claude" {
		cacheWrite1h = 2 * input
	}
	return PriceRule{
		InputCacheWrite1hNanosPerToken: cacheWrite1h,
		ID:                             id,
		BillingProvider:                provider,
		Model:                          model,
		ServiceTier:                    "standard",
		InputUncachedNanosPerToken:     input,
		InputCacheReadNanosPerToken:    cacheRead,
		InputCacheWrite5mNanosPerToken: cacheWrite5m,
		OutputTextNanosPerToken:        output,
		OutputReasoningNanosPerToken:   output,
	}
}
