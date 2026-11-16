package apicost

// Usage is one request's token usage in mutually exclusive buckets.
// CacheWrite includes CacheWrite1h and CacheWrite12h.
type Usage struct {
	Input         int64
	CacheRead     int64
	CacheWrite    int64
	CacheWrite1h  int64
	CacheWrite12h int64
	Output        int64
}

// Tier is the service tier a request was billed at.
type Tier int

const (
	TierStandard Tier = iota
	TierFast
	TierFlex
)

const (
	// anthropicTierThreshold is ccusage's default boundary for LiteLLM
	// *_above_200k_tokens rates, applied per token bucket.
	anthropicTierThreshold = 200_000
	// openAILongContextThreshold is the request input size above which OpenAI
	// bills the whole request at *_above_272k_tokens rates.
	openAILongContextThreshold = 272_000
	// cacheWrite1hInputMultiplier matches ccusage: 1h cache writes cost twice
	// the base input rate.
	cacheWrite1hInputMultiplier = 2.0
)

// Cost returns the USD list price of one request.
func Cost(price Price, usage Usage, tier Tier) float64 {
	usage = usage.clamped()
	var cost float64
	if price.Provider == providerOpenAI {
		cost = openAICost(price, usage)
	} else {
		cost = anthropicCost(price, usage)
	}
	switch tier {
	case TierFast:
		cost *= positiveOr(price.Fast, 1)
	case TierFlex:
		cost *= positiveOr(price.Flex, 1)
	}
	return cost
}

// anthropicCost follows ccusage's calculate_cost_from_pricing: every bucket
// is billed at its rate, with LiteLLM above-200k rates applied marginally to
// the part of a bucket beyond 200k tokens.
func anthropicCost(price Price, usage Usage) float64 {
	tier := price.Above200k
	if tier == nil {
		tier = &TierRates{}
	}
	cacheWrite5m := usage.CacheWrite - usage.CacheWrite1h - usage.CacheWrite12h
	cacheWrite1h := price.Input * cacheWrite1hInputMultiplier
	var cacheWrite1hAbove float64
	if tier.Input > 0 {
		cacheWrite1hAbove = tier.Input * cacheWrite1hInputMultiplier
	}
	return tieredCost(usage.Input, price.Input, tier.Input) +
		tieredCost(usage.Output, price.Output, tier.Output) +
		tieredCost(cacheWrite5m+usage.CacheWrite12h, price.CacheWrite, tier.CacheWrite) +
		tieredCost(usage.CacheWrite1h, cacheWrite1h, cacheWrite1hAbove) +
		tieredCost(usage.CacheRead, price.CacheRead, tier.CacheRead)
}

// openAICost follows ccusage's Codex pricing: flat rates, with the whole
// request billed at long-context rates once its input exceeds 272k tokens.
func openAICost(price Price, usage Usage) float64 {
	input, output, cacheWrite, cacheRead := price.Input, price.Output, price.CacheWrite, price.CacheRead
	cacheWrite12h := positiveOr(price.CacheWrite12h, cacheWrite)
	totalInput := usage.Input + usage.CacheRead + usage.CacheWrite
	if tier := price.Above272k; tier != nil && totalInput > openAILongContextThreshold {
		input = positiveOr(tier.Input, input)
		output = positiveOr(tier.Output, output)
		cacheWrite = positiveOr(tier.CacheWrite, cacheWrite)
		cacheRead = positiveOr(tier.CacheRead, cacheRead)
	}
	cacheWriteShort := usage.CacheWrite - usage.CacheWrite12h
	return float64(usage.Input)*input +
		float64(usage.Output)*output +
		float64(cacheWriteShort)*cacheWrite +
		float64(usage.CacheWrite12h)*cacheWrite12h +
		float64(usage.CacheRead)*cacheRead
}

func tieredCost(tokens int64, base, above float64) float64 {
	if tokens <= 0 {
		return 0
	}
	if above > 0 && tokens > anthropicTierThreshold {
		return anthropicTierThreshold*base + float64(tokens-anthropicTierThreshold)*above
	}
	return float64(tokens) * base
}

func (u Usage) clamped() Usage {
	clamp := func(v int64) int64 {
		if v < 0 {
			return 0
		}
		return v
	}
	u.Input, u.CacheRead, u.Output = clamp(u.Input), clamp(u.CacheRead), clamp(u.Output)
	u.CacheWrite, u.CacheWrite1h, u.CacheWrite12h = clamp(u.CacheWrite), clamp(u.CacheWrite1h), clamp(u.CacheWrite12h)
	if u.CacheWrite1h > u.CacheWrite {
		u.CacheWrite1h = u.CacheWrite
	}
	if u.CacheWrite12h > u.CacheWrite-u.CacheWrite1h {
		u.CacheWrite12h = u.CacheWrite - u.CacheWrite1h
	}
	return u
}

func positiveOr(value, fallback float64) float64 {
	if value > 0 {
		return value
	}
	return fallback
}
