package usage

import (
	"crypto/sha256"
	"encoding/hex"
	"math"
	"math/big"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

type compiledPrice struct {
	input      *big.Rat
	output     *big.Rat
	cacheRead  *big.Rat
	cacheWrite *big.Rat
}

type compiledRule struct {
	provider    string
	model       string
	serviceTier string
	price       compiledPrice
}

type pricingTable struct {
	currency string
	version  string
	rules    []compiledRule
}

type billableTokens struct {
	input      int64
	output     int64
	cacheRead  int64
	cacheWrite int64
	unknown    int64
}

func compilePricing(input config.UsagePricingConfig) *pricingTable {
	currency := strings.ToUpper(strings.TrimSpace(input.Currency))
	if currency == "" {
		currency = config.DefaultUsagePricingCurrency
	}
	currency = normalizedCurrency(currency)
	version := strings.TrimSpace(input.Version)
	if version == "" {
		version = config.DefaultUsagePricingVersion
	}
	version = sanitizeDisplayValue(version, 96)
	version += "@" + pricingRulesRevision(currency, input.Rules)
	table := &pricingTable{
		currency: currency,
		version:  version,
		rules:    make([]compiledRule, 0, len(input.Rules)),
	}
	for _, rule := range input.Rules {
		cacheInputRate := mergedCacheInputRate(rule)
		table.rules = append(table.rules, compiledRule{
			provider:    normalizePattern(rule.Provider),
			model:       normalizePattern(rule.Model),
			serviceTier: normalizePattern(rule.ServiceTier),
			price: compiledPrice{
				input:      parseNonNegativeDecimal(rule.InputPerMillion),
				output:     parseNonNegativeDecimal(rule.OutputPerMillion),
				cacheRead:  parseNonNegativeDecimal(cacheInputRate),
				cacheWrite: parseNonNegativeDecimal(cacheInputRate),
			},
		})
	}
	return table
}

func normalizePattern(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "*"
	}
	return value
}

func parseNonNegativeDecimal(value string) *big.Rat {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	rate, ok := new(big.Rat).SetString(value)
	if !ok || rate.Sign() < 0 {
		return nil
	}
	return rate
}

func (p *pricingTable) matchingRule(provider, model, serviceTier string) *compiledRule {
	if p == nil {
		return nil
	}
	provider = strings.ToLower(strings.TrimSpace(provider))
	model = strings.ToLower(strings.TrimSpace(model))
	serviceTier = strings.ToLower(strings.TrimSpace(serviceTier))
	for i := range p.rules {
		rule := &p.rules[i]
		if globMatches(rule.provider, provider) && globMatches(rule.model, model) && globMatches(rule.serviceTier, serviceTier) {
			return rule
		}
	}
	return nil
}

func globMatches(pattern, value string) bool {
	// Model identifiers commonly contain '/'. Unlike path.Match, this matcher
	// treats it as an ordinary character. '*' spans any sequence and '?' spans
	// one byte; matching is already case-normalized by the caller.
	patternIndex := 0
	valueIndex := 0
	starIndex := -1
	starValueIndex := 0
	for valueIndex < len(value) {
		if patternIndex < len(pattern) && (pattern[patternIndex] == '?' || pattern[patternIndex] == value[valueIndex]) {
			patternIndex++
			valueIndex++
			continue
		}
		if patternIndex < len(pattern) && pattern[patternIndex] == '*' {
			starIndex = patternIndex
			patternIndex++
			starValueIndex = valueIndex
			continue
		}
		if starIndex >= 0 {
			patternIndex = starIndex + 1
			starValueIndex++
			valueIndex = starValueIndex
			continue
		}
		return false
	}
	for patternIndex < len(pattern) && pattern[patternIndex] == '*' {
		patternIndex++
	}
	return patternIndex == len(pattern)
}

func pricingRulesRevision(currency string, rules []config.UsagePricingRule) string {
	hash := sha256.New()
	writeRevisionPart(hash, strings.ToUpper(strings.TrimSpace(currency)))
	for _, rule := range rules {
		writeRevisionPart(hash, normalizePattern(rule.Provider))
		writeRevisionPart(hash, normalizePattern(rule.Model))
		writeRevisionPart(hash, normalizePattern(rule.ServiceTier))
		writeRevisionPart(hash, normalizedRateForRevision(rule.InputPerMillion))
		writeRevisionPart(hash, normalizedRateForRevision(rule.OutputPerMillion))
		writeRevisionPart(hash, normalizedRateForRevision(mergedCacheInputRate(rule)))
	}
	sum := hash.Sum(nil)
	return hex.EncodeToString(sum[:4])
}

func mergedCacheInputRate(rule config.UsagePricingRule) string {
	if rate := strings.TrimSpace(rule.CacheReadPerMillion); rate != "" {
		return rate
	}
	return strings.TrimSpace(rule.CacheWritePerMillion)
}

func normalizedRateForRevision(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	rate := parseNonNegativeDecimal(value)
	if rate == nil {
		return "invalid:" + value
	}
	return rate.RatString()
}

type revisionWriter interface {
	Write([]byte) (int, error)
}

func writeRevisionPart(writer revisionWriter, value string) {
	_, _ = writer.Write([]byte{0})
	_, _ = writer.Write([]byte(value))
}

// calculate returns a rounded integer number of one-millionth currency units.
// Because rates are expressed per one million tokens, multiplying a decimal
// rate by a token count directly yields micro-currency units.
func (p *pricingTable) calculate(provider, model, serviceTier string, tokens billableTokens) (costMicros, unpricedTokens int64, unpricedAttempt bool) {
	rule := p.matchingRule(provider, model, serviceTier)
	if rule == nil {
		return 0, tokens.total(), true
	}

	total := new(big.Rat)
	addPricedCategory(total, rule.price.input, tokens.input, &unpricedTokens)
	addPricedCategory(total, rule.price.output, tokens.output, &unpricedTokens)
	addPricedCategory(total, rule.price.cacheRead, tokens.cacheRead, &unpricedTokens)
	addPricedCategory(total, rule.price.cacheWrite, tokens.cacheWrite, &unpricedTokens)
	unpricedTokens = saturatingAdd(unpricedTokens, nonNegative(tokens.unknown))
	return roundNonNegativeRat(total), unpricedTokens, unpricedTokens > 0
}

func addPricedCategory(total, rate *big.Rat, tokens int64, unpriced *int64) {
	tokens = nonNegative(tokens)
	if tokens == 0 {
		return
	}
	if rate == nil {
		*unpriced = saturatingAdd(*unpriced, tokens)
		return
	}
	term := new(big.Rat).Mul(rate, new(big.Rat).SetInt64(tokens))
	total.Add(total, term)
}

func roundNonNegativeRat(value *big.Rat) int64 {
	if value == nil || value.Sign() <= 0 {
		return 0
	}
	numerator := new(big.Int).Set(value.Num())
	denominator := new(big.Int).Set(value.Denom())
	quotient, remainder := new(big.Int).QuoRem(numerator, denominator, new(big.Int))
	if new(big.Int).Lsh(remainder, 1).Cmp(denominator) >= 0 {
		quotient.Add(quotient, big.NewInt(1))
	}
	if !quotient.IsInt64() {
		return math.MaxInt64
	}
	return quotient.Int64()
}

func (t billableTokens) total() int64 {
	return saturatingAdd(t.categorizedTotal(), nonNegative(t.unknown))
}

func (t billableTokens) categorizedTotal() int64 {
	total := saturatingAdd(nonNegative(t.input), nonNegative(t.output))
	total = saturatingAdd(total, nonNegative(t.cacheRead))
	return saturatingAdd(total, nonNegative(t.cacheWrite))
}

func normalizeBillableTokens(provider string, detail coreusage.Detail) billableTokens {
	provider = strings.ToLower(strings.TrimSpace(provider))
	input := nonNegative(detail.InputTokens)
	output := nonNegative(detail.OutputTokens)
	cacheRead := nonNegative(detail.CacheReadTokens)
	cacheWrite := nonNegative(detail.CacheCreationTokens)
	reasoning := nonNegative(detail.ReasoningTokens)

	var tokens billableTokens
	switch provider {
	case "claude":
		// Anthropic reports uncached input, cache reads, and cache writes as
		// disjoint counters. CachedTokens mirrors one of those counters.
		tokens = billableTokens{input: input, output: output, cacheRead: cacheRead, cacheWrite: cacheWrite}
	case "gemini", "aistudio", "antigravity", "vertex":
		// Gemini-family prompt counts include cached content, while thinking
		// tokens are separate from candidate output tokens.
		if cacheRead == 0 {
			cacheRead = nonNegative(detail.CachedTokens)
		}
		tokens = billableTokens{
			input:      subtractFloorZero(input, cacheRead),
			output:     saturatingAdd(output, reasoning),
			cacheRead:  cacheRead,
			cacheWrite: cacheWrite,
		}
	default:
		// OpenAI-compatible protocols include cached input in prompt/input
		// tokens and reasoning in completion/output tokens.
		if cacheRead == 0 {
			cacheRead = nonNegative(detail.CachedTokens)
		}
		tokens = billableTokens{
			input:      subtractFloorZero(input, cacheRead),
			output:     output,
			cacheRead:  cacheRead,
			cacheWrite: cacheWrite,
		}
	}
	tokens.unknown = subtractFloorZero(nonNegative(detail.TotalTokens), tokens.categorizedTotal())
	return tokens
}

func subtractFloorZero(total, part int64) int64 {
	if part >= total {
		return 0
	}
	return total - part
}
