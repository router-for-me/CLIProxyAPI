package handlers

import (
	"fmt"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/usage"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v6/sdk/config"
)

type apiKeyQuotaEvaluation struct {
	Blocked bool
	Limit   int64
	Current int64
}

func evaluateAPIKeyQuota(cfg *sdkconfig.SDKConfig, snapshot usage.StatisticsSnapshot, apiKey, model string, now time.Time) apiKeyQuotaEvaluation {
	if cfg == nil {
		return apiKeyQuotaEvaluation{}
	}
	quotas := cfg.APIKeyQuotas
	if !quotas.Enabled {
		return apiKeyQuotaEvaluation{}
	}
	apiKey = strings.TrimSpace(apiKey)
	model = strings.TrimSpace(model)
	if apiKey == "" || model == "" {
		return apiKeyQuotaEvaluation{}
	}

	baseModel := model
	if idx := strings.Index(baseModel, "("); idx > 0 {
		baseModel = strings.TrimSpace(baseModel[:idx])
	}
	if baseModel == "" {
		baseModel = model
	}

	if modelMatchesAnyPattern(baseModel, quotas.ExcludeModelPatterns) {
		return apiKeyQuotaEvaluation{}
	}

	limit, hasLimit := modelLimitForModel(quotas.MonthlyTokenLimits, apiKey, baseModel)
	if !hasLimit || limit <= 0 {
		return apiKeyQuotaEvaluation{}
	}

	apiSnapshot, ok := snapshot.APIs[apiKey]
	if !ok {
		return apiKeyQuotaEvaluation{Limit: limit, Current: 0}
	}

	month := monthBucket(now)
	current := monthlyTokensForModel(apiSnapshot, baseModel, month)
	if current >= limit {
		return apiKeyQuotaEvaluation{Blocked: true, Limit: limit, Current: current}
	}
	return apiKeyQuotaEvaluation{Limit: limit, Current: current}
}

func buildAPIKeyQuotaError(model string, evaluation apiKeyQuotaEvaluation, now time.Time) error {
	return fmt.Errorf(
		"monthly token quota exceeded for model %s: %d/%d tokens used in %s",
		model,
		evaluation.Current,
		evaluation.Limit,
		monthBucket(now),
	)
}

func modelLimitForModel(entries []sdkconfig.APIKeyMonthlyModelTokenLimit, apiKey, model string) (int64, bool) {
	for _, entry := range entries {
		apiKeyPattern := strings.TrimSpace(entry.APIKey)
		if apiKeyPattern == "" {
			apiKeyPattern = "*"
		}
		if !matchQuotaModelPattern(apiKeyPattern, apiKey) {
			continue
		}
		modelPattern := strings.TrimSpace(entry.Model)
		if modelPattern == "" {
			continue
		}
		if !matchQuotaModelPattern(modelPattern, model) {
			continue
		}
		if entry.Limit <= 0 {
			continue
		}
		return entry.Limit, true
	}
	return 0, false
}

func modelMatchesAnyPattern(model string, patterns []string) bool {
	for _, pattern := range patterns {
		trimmed := strings.TrimSpace(pattern)
		if trimmed == "" {
			continue
		}
		if matchQuotaModelPattern(trimmed, model) {
			return true
		}
	}
	return false
}

func monthBucket(t time.Time) string {
	if t.IsZero() {
		t = time.Now()
	}
	return t.UTC().Format("2006-01")
}

func monthlyTokensForModel(apiSnapshot usage.APISnapshot, model, month string) int64 {
	var total int64
	for modelName, modelSnapshot := range apiSnapshot.Models {
		if !sameModelName(modelName, model) {
			continue
		}
		for _, detail := range modelSnapshot.Details {
			if monthBucket(detail.Timestamp) != month {
				continue
			}
			tokens := detail.Tokens.TotalTokens
			if tokens <= 0 {
				tokens = detail.Tokens.InputTokens + detail.Tokens.OutputTokens + detail.Tokens.ReasoningTokens + detail.Tokens.CachedTokens
			}
			if tokens > 0 {
				total += tokens
			}
		}
	}
	return total
}

func sameModelName(a, b string) bool {
	a = strings.TrimSpace(a)
	b = strings.TrimSpace(b)
	if a == b {
		return true
	}
	if idx := strings.Index(a, "("); idx > 0 {
		a = strings.TrimSpace(a[:idx])
	}
	if idx := strings.Index(b, "("); idx > 0 {
		b = strings.TrimSpace(b[:idx])
	}
	return a == b
}

// matchQuotaModelPattern performs simple wildcard matching where '*' matches zero or more characters.
func matchQuotaModelPattern(pattern, model string) bool {
	pattern = strings.TrimSpace(pattern)
	model = strings.TrimSpace(model)
	if pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	pi, si := 0, 0
	starIdx := -1
	matchIdx := 0
	for si < len(model) {
		if pi < len(pattern) && pattern[pi] == model[si] {
			pi++
			si++
			continue
		}
		if pi < len(pattern) && pattern[pi] == '*' {
			starIdx = pi
			matchIdx = si
			pi++
			continue
		}
		if starIdx != -1 {
			pi = starIdx + 1
			matchIdx++
			si = matchIdx
			continue
		}
		return false
	}
	for pi < len(pattern) && pattern[pi] == '*' {
		pi++
	}
	return pi == len(pattern)
}
