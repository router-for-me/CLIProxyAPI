package clawmaster

import (
	"regexp"
	"strings"
)

// apiPrice is what one model's tokens would cost on the Claude API at standard (non-batch) rates,
// in US dollars per million tokens. The pool serves subscriptions, which are not billed per token;
// this prices what it served as if it had gone through the API, so people and models can be
// compared in dollars.
type apiPrice struct {
	input, write5m, write1h, cacheRead, output float64
	// fastInput and fastOutput are fast mode's rates, when the model has a fast mode. Fast mode
	// keeps the cache multipliers, so its cache rates scale with its input rate.
	fastInput, fastOutput float64
	// longInput..longOutput replace the rates above for a request whose prompt (input, cache reads
	// and cache writes together) is over longPrompt tokens; zero when the model has one rate.
	longPrompt                                                     int64
	longInput, longWrite5m, longWrite1h, longCacheRead, longOutput float64
}

// apiPrices is Anthropic's price list (https://platform.claude.com/docs/en/about-claude/pricing),
// read 2026-11-24, by model alias. Keep it to public model names.
var apiPrices = map[string]apiPrice{
	"claude-fable-5-2":  {input: 8, write5m: 10, write1h: 16, cacheRead: 0.20, output: 40, fastInput: 16, fastOutput: 80},
	"claude-mythos-5-2": {input: 8, write5m: 10, write1h: 16, cacheRead: 0.20, output: 40, fastInput: 16, fastOutput: 80},
	"claude-fable-5-1":  {input: 10, write5m: 12.50, write1h: 20, cacheRead: 0.25, output: 50},
	"claude-mythos-5-1": {input: 10, write5m: 12.50, write1h: 20, cacheRead: 0.25, output: 50},
	"claude-fable-5":    {input: 10, write5m: 12.50, write1h: 20, cacheRead: 1, output: 50},
	"claude-mythos-5":   {input: 10, write5m: 12.50, write1h: 20, cacheRead: 1, output: 50},
	"claude-opus-5-5":   {input: 4, write5m: 5, write1h: 8, cacheRead: 0.20, output: 20, fastInput: 8, fastOutput: 40},
	"claude-opus-5":     {input: 5, write5m: 6.25, write1h: 10, cacheRead: 0.50, output: 25, fastInput: 10, fastOutput: 50},
	"claude-opus-4-8":   {input: 5, write5m: 6.25, write1h: 10, cacheRead: 0.50, output: 25, fastInput: 10, fastOutput: 50},
	"claude-opus-4-7":   {input: 5, write5m: 6.25, write1h: 10, cacheRead: 0.50, output: 25},
	"claude-opus-4-6":   {input: 5, write5m: 6.25, write1h: 10, cacheRead: 0.50, output: 25},
	"claude-opus-4-5":   {input: 5, write5m: 6.25, write1h: 10, cacheRead: 0.50, output: 25},
	"claude-sonnet-5-5": {input: 2, write5m: 2.50, write1h: 4, cacheRead: 0.10, output: 10},
	"claude-sonnet-5":   {input: 2, write5m: 2.50, write1h: 4, cacheRead: 0.20, output: 10},
	"claude-sonnet-4-6": {input: 3, write5m: 3.75, write1h: 6, cacheRead: 0.30, output: 15},
	"claude-sonnet-4-5": {input: 3, write5m: 3.75, write1h: 6, cacheRead: 0.30, output: 15},
	"claude-haiku-5-5": {
		input: 0.10, write5m: 0.125, write1h: 0.20, cacheRead: 0.01, output: 0.50,
		longPrompt: 100_000, longInput: 0.50, longWrite5m: 0.625, longWrite1h: 1, longCacheRead: 0.05, longOutput: 2.50,
	},
	"claude-haiku-4-5": {input: 1, write5m: 1.25, write1h: 2, cacheRead: 0.10, output: 5},
}

// A model id's dated form ("-20251001") and a platform's form ("us.anthropic.…-v1:0") name the
// same model as its alias.
var (
	modelVersionSuffix  = regexp.MustCompile(`-v\d+(?::\d+)?$`)
	modelDateSuffix     = regexp.MustCompile(`-\d{8}$`)
	modelPlatformPrefix = regexp.MustCompile(`^(?:[a-z]{2}\.|global\.)?anthropic\.`)
)

func apiPriceOf(model string) (apiPrice, bool) {
	model = modelPlatformPrefix.ReplaceAllString(strings.ToLower(strings.TrimSpace(model)), "")
	model = modelDateSuffix.ReplaceAllString(modelVersionSuffix.ReplaceAllString(model, ""), "")
	price, ok := apiPrices[model]
	return price, ok
}

// apiCost is one response's tokens priced at the model's API rates, in US dollars; false for a
// model the price list does not name. Cache writes the response does not split by duration are
// priced as 5-minute writes, the API's default.
func apiCost(model string, counts tokenCounts, fast bool) (float64, bool) {
	p, ok := apiPriceOf(model)
	if !ok {
		return 0, false
	}
	input, write5m, write1h, cacheRead, output := p.input, p.write5m, p.write1h, p.cacheRead, p.output
	if prompt := counts.input + counts.cacheRead + counts.cacheCreation; p.longPrompt > 0 && prompt > p.longPrompt {
		input, write5m, write1h, cacheRead, output = p.longInput, p.longWrite5m, p.longWrite1h, p.longCacheRead, p.longOutput
	}
	if fast && p.fastInput > 0 {
		scale := p.fastInput / input
		input, write5m, write1h, cacheRead, output = p.fastInput, write5m*scale, write1h*scale, cacheRead*scale, p.fastOutput
	}
	oneHour := min(max(counts.cacheCreation1h, 0), counts.cacheCreation)
	dollars := float64(counts.input)*input +
		float64(counts.cacheCreation-oneHour)*write5m +
		float64(oneHour)*write1h +
		float64(counts.cacheRead)*cacheRead +
		float64(counts.output)*output
	return dollars / 1e6, true
}
