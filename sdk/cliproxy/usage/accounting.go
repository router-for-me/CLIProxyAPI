package usage

import "strings"

// TokenAccountingSchemaVersion identifies the canonical token accounting contract.
const TokenAccountingSchemaVersion = 2

// TokenAccountingQuality describes how confidently a token total can be classified.
type TokenAccountingQuality string

const (
	TokenAccountingQualityComplete     TokenAccountingQuality = "complete"
	TokenAccountingQualityInconsistent TokenAccountingQuality = "inconsistent"
	TokenAccountingQualityUnclassified TokenAccountingQuality = "unclassified"
)

type tokenAccountingSemantics uint8

const (
	tokenAccountingSemanticsUnknown tokenAccountingSemantics = iota
	tokenAccountingSemanticsSubset
	tokenAccountingSemanticsIndependent
	tokenAccountingSemanticsSeparateReasoning
	tokenAccountingSemanticsSeparateCache
)

// TokenInputBreakdown contains mutually exclusive input token buckets.
type TokenInputBreakdown struct {
	TotalTokens      int64 `json:"total_tokens"`
	UncachedTokens   int64 `json:"uncached_tokens"`
	CacheReadTokens  int64 `json:"cache_read_tokens"`
	CacheWriteTokens int64 `json:"cache_write_tokens"`
}

// TokenOutputBreakdown contains mutually exclusive output token buckets.
type TokenOutputBreakdown struct {
	TotalTokens        int64 `json:"total_tokens"`
	NonReasoningTokens int64 `json:"non_reasoning_tokens"`
	ReasoningTokens    int64 `json:"reasoning_tokens"`
}

// TokenBreakdown is the canonical, non-overlapping token accounting contract.
type TokenBreakdown struct {
	SchemaVersion      int                    `json:"schema_version"`
	Quality            TokenAccountingQuality `json:"quality"`
	TotalTokens        int64                  `json:"total_tokens"`
	Input              TokenInputBreakdown    `json:"input"`
	Output             TokenOutputBreakdown   `json:"output"`
	UnclassifiedTokens int64                  `json:"unclassified_tokens"`
}

// Valid reports whether the breakdown satisfies the v2 accounting invariants.
func (b TokenBreakdown) Valid() bool {
	if b.SchemaVersion != TokenAccountingSchemaVersion || !validTokenAccountingQuality(b.Quality) {
		return false
	}
	if b.TotalTokens < 0 || b.UnclassifiedTokens < 0 ||
		b.Input.TotalTokens < 0 || b.Input.UncachedTokens < 0 ||
		b.Input.CacheReadTokens < 0 || b.Input.CacheWriteTokens < 0 ||
		b.Output.TotalTokens < 0 || b.Output.NonReasoningTokens < 0 ||
		b.Output.ReasoningTokens < 0 {
		return false
	}
	if b.Input.TotalTokens != b.Input.UncachedTokens+b.Input.CacheReadTokens+b.Input.CacheWriteTokens {
		return false
	}
	if b.Output.TotalTokens != b.Output.NonReasoningTokens+b.Output.ReasoningTokens {
		return false
	}
	if b.TotalTokens != b.Input.TotalTokens+b.Output.TotalTokens+b.UnclassifiedTokens {
		return false
	}
	if b.Quality == TokenAccountingQualityComplete && b.UnclassifiedTokens != 0 {
		return false
	}
	return true
}

func validTokenAccountingQuality(quality TokenAccountingQuality) bool {
	switch quality {
	case TokenAccountingQualityComplete, TokenAccountingQualityInconsistent, TokenAccountingQualityUnclassified:
		return true
	default:
		return false
	}
}

// NewSubsetTokenBreakdown normalizes protocols where cache tokens are included
// in input totals and reasoning tokens are included in output totals.
func NewSubsetTokenBreakdown(inputTotal, cacheRead, cacheWrite, outputTotal, reasoning, total int64) TokenBreakdown {
	expectedTotal, okExpected := nonNegativeSum(inputTotal, outputTotal)
	if !okExpected || cacheRead < 0 || cacheWrite < 0 || reasoning < 0 ||
		cacheRead+cacheWrite > inputTotal || reasoning > outputTotal {
		return inconsistentTokenBreakdown(total, expectedTotal)
	}
	resolvedTotal, okTotal := resolveAccountingTotal(total, expectedTotal)
	if !okTotal {
		return inconsistentTokenBreakdown(total, expectedTotal)
	}
	return TokenBreakdown{
		SchemaVersion: TokenAccountingSchemaVersion,
		Quality:       TokenAccountingQualityComplete,
		TotalTokens:   resolvedTotal,
		Input: TokenInputBreakdown{
			TotalTokens:      inputTotal,
			UncachedTokens:   inputTotal - cacheRead - cacheWrite,
			CacheReadTokens:  cacheRead,
			CacheWriteTokens: cacheWrite,
		},
		Output: TokenOutputBreakdown{
			TotalTokens:        outputTotal,
			NonReasoningTokens: outputTotal - reasoning,
			ReasoningTokens:    reasoning,
		},
	}
}

// NewPartialSubsetTokenBreakdown preserves known subset buckets while assigning
// an authoritative remainder to the unclassified bucket.
func NewPartialSubsetTokenBreakdown(inputTotal, cacheRead, cacheWrite, outputTotal, reasoning, total int64) TokenBreakdown {
	cacheTotal, okCache := nonNegativeSum(cacheRead, cacheWrite)
	expectedTotal, okExpected := nonNegativeSum(inputTotal, outputTotal)
	if !okCache || !okExpected || inputTotal < 0 || outputTotal < 0 || reasoning < 0 ||
		cacheTotal > inputTotal || reasoning > outputTotal || total < 0 {
		return inconsistentTokenBreakdown(total, expectedTotal)
	}
	resolvedTotal := total
	if resolvedTotal == 0 {
		resolvedTotal = expectedTotal
	}
	if resolvedTotal < expectedTotal {
		return inconsistentTokenBreakdown(total, expectedTotal)
	}
	unclassified := resolvedTotal - expectedTotal
	quality := TokenAccountingQualityComplete
	if unclassified > 0 {
		quality = TokenAccountingQualityUnclassified
	}
	return TokenBreakdown{
		SchemaVersion: TokenAccountingSchemaVersion,
		Quality:       quality,
		TotalTokens:   resolvedTotal,
		Input: TokenInputBreakdown{
			TotalTokens:      inputTotal,
			UncachedTokens:   inputTotal - cacheTotal,
			CacheReadTokens:  cacheRead,
			CacheWriteTokens: cacheWrite,
		},
		Output: TokenOutputBreakdown{
			TotalTokens:        outputTotal,
			NonReasoningTokens: outputTotal - reasoning,
			ReasoningTokens:    reasoning,
		},
		UnclassifiedTokens: unclassified,
	}
}

// NewIndependentTokenBreakdown normalizes protocols where uncached input,
// cache reads, cache writes, non-reasoning output, and reasoning are separate.
func NewIndependentTokenBreakdown(uncachedInput, cacheRead, cacheWrite, nonReasoningOutput, reasoning, total int64) TokenBreakdown {
	inputTotal, okInput := nonNegativeSum(uncachedInput, cacheRead, cacheWrite)
	outputTotal, okOutput := nonNegativeSum(nonReasoningOutput, reasoning)
	expectedTotal, okExpected := nonNegativeSum(inputTotal, outputTotal)
	if !okInput || !okOutput || !okExpected {
		return inconsistentTokenBreakdown(total, expectedTotal)
	}
	resolvedTotal, okTotal := resolveAccountingTotal(total, expectedTotal)
	if !okTotal {
		return inconsistentTokenBreakdown(total, expectedTotal)
	}
	return TokenBreakdown{
		SchemaVersion: TokenAccountingSchemaVersion,
		Quality:       TokenAccountingQualityComplete,
		TotalTokens:   resolvedTotal,
		Input: TokenInputBreakdown{
			TotalTokens:      inputTotal,
			UncachedTokens:   uncachedInput,
			CacheReadTokens:  cacheRead,
			CacheWriteTokens: cacheWrite,
		},
		Output: TokenOutputBreakdown{
			TotalTokens:        outputTotal,
			NonReasoningTokens: nonReasoningOutput,
			ReasoningTokens:    reasoning,
		},
	}
}

// NewSeparateCacheTokenBreakdown normalizes protocols where cache tokens are
// separate from uncached input while reasoning remains included in output.
func NewSeparateCacheTokenBreakdown(uncachedInput, cacheRead, cacheWrite, outputTotal, reasoning, total int64) TokenBreakdown {
	inputTotal, okInput := nonNegativeSum(uncachedInput, cacheRead, cacheWrite)
	expectedTotal, okExpected := nonNegativeSum(inputTotal, outputTotal)
	if !okInput || !okExpected || reasoning < 0 || reasoning > outputTotal {
		return inconsistentTokenBreakdown(total, expectedTotal)
	}
	resolvedTotal, okTotal := resolveAccountingTotal(total, expectedTotal)
	if !okTotal {
		return inconsistentTokenBreakdown(total, expectedTotal)
	}
	return TokenBreakdown{
		SchemaVersion: TokenAccountingSchemaVersion,
		Quality:       TokenAccountingQualityComplete,
		TotalTokens:   resolvedTotal,
		Input: TokenInputBreakdown{
			TotalTokens:      inputTotal,
			UncachedTokens:   uncachedInput,
			CacheReadTokens:  cacheRead,
			CacheWriteTokens: cacheWrite,
		},
		Output: TokenOutputBreakdown{
			TotalTokens:        outputTotal,
			NonReasoningTokens: outputTotal - reasoning,
			ReasoningTokens:    reasoning,
		},
	}
}

// NewSeparateReasoningTokenBreakdown normalizes protocols where cache tokens
// are included in input totals while reasoning is separate from ordinary output.
func NewSeparateReasoningTokenBreakdown(inputTotal, cacheRead, cacheWrite, nonReasoningOutput, reasoning, total int64) TokenBreakdown {
	if inputTotal < 0 || cacheRead < 0 || cacheWrite < 0 || cacheRead+cacheWrite > inputTotal {
		return inconsistentTokenBreakdown(total, 0)
	}
	outputTotal, okOutput := nonNegativeSum(nonReasoningOutput, reasoning)
	expectedTotal, okExpected := nonNegativeSum(inputTotal, outputTotal)
	if !okOutput || !okExpected {
		return inconsistentTokenBreakdown(total, expectedTotal)
	}
	resolvedTotal, okTotal := resolveAccountingTotal(total, expectedTotal)
	if !okTotal {
		return inconsistentTokenBreakdown(total, expectedTotal)
	}
	return TokenBreakdown{
		SchemaVersion: TokenAccountingSchemaVersion,
		Quality:       TokenAccountingQualityComplete,
		TotalTokens:   resolvedTotal,
		Input: TokenInputBreakdown{
			TotalTokens:      inputTotal,
			UncachedTokens:   inputTotal - cacheRead - cacheWrite,
			CacheReadTokens:  cacheRead,
			CacheWriteTokens: cacheWrite,
		},
		Output: TokenOutputBreakdown{
			TotalTokens:        outputTotal,
			NonReasoningTokens: nonReasoningOutput,
			ReasoningTokens:    reasoning,
		},
	}
}

// NewUnclassifiedTokenBreakdown preserves an authoritative total without
// guessing how an unknown protocol partitions it.
func NewUnclassifiedTokenBreakdown(total int64) TokenBreakdown {
	if total <= 0 {
		quality := TokenAccountingQualityComplete
		if total < 0 {
			quality = TokenAccountingQualityInconsistent
		}
		return TokenBreakdown{SchemaVersion: TokenAccountingSchemaVersion, Quality: quality}
	}
	return TokenBreakdown{
		SchemaVersion:      TokenAccountingSchemaVersion,
		Quality:            TokenAccountingQualityUnclassified,
		TotalTokens:        total,
		UnclassifiedTokens: total,
	}
}

// EnsureTokenBreakdown attaches a valid v2 breakdown to legacy or direct SDK
// usage details without guessing whether reasoning is already inside output.
func EnsureTokenBreakdown(detail Detail) Detail {
	return EnsureTokenBreakdownForProvider(detail, "", "")
}

// EnsureTokenBreakdownForProvider attaches a valid v2 breakdown to legacy or
// direct SDK usage details using the known provider's token semantics. Unknown
// providers remain unclassified instead of guessing how their buckets overlap.
func EnsureTokenBreakdownForProvider(detail Detail, provider, executorType string) Detail {
	if detail.CacheInputMode == "" && detail.TokenBreakdown == (TokenBreakdown{}) &&
		detail.InputTokens == 0 && detail.OutputTokens == 0 && detail.ReasoningTokens == 0 &&
		detail.CachedTokens == 0 && detail.CacheReadTokens == 0 && detail.CacheCreationTokens == 0 &&
		detail.TotalTokens == 0 {
		return detail
	}
	semantics := tokenAccountingSemanticsForDetail(detail, provider, executorType)
	hadCanonicalBreakdown := detail.TokenBreakdown.Valid()
	if !hadCanonicalBreakdown {
		if detail.CacheReadTokens == 0 && detail.CacheCreationTokens == 0 && detail.CachedTokens > 0 {
			switch semantics {
			case tokenAccountingSemanticsIndependent, tokenAccountingSemanticsSeparateCache:
				detail.CacheReadTokens = detail.CachedTokens
			case tokenAccountingSemanticsSubset, tokenAccountingSemanticsSeparateReasoning:
				legacyCacheFitsInput := detail.CachedTokens <= detail.InputTokens
				legacyCacheIsOnlyUsage := detail.InputTokens == 0 && detail.OutputTokens == 0 &&
					detail.ReasoningTokens == 0 && detail.TotalTokens == 0
				if legacyCacheFitsInput || legacyCacheIsOnlyUsage {
					detail.CacheReadTokens = detail.CachedTokens
				}
			}
		}
		detail.TokenBreakdown = tokenBreakdownForSemantics(detail, semantics)
	}
	if detail.CacheInputMode != "" && !cacheInputModeMatchesCanonicalBreakdown(detail) {
		detail.CacheInputMode = ""
	}
	if detail.CacheInputMode == "" && !hadCanonicalBreakdown && hasTokenAccountingSignal(detail) &&
		detail.TokenBreakdown.Quality != TokenAccountingQualityInconsistent {
		detail.CacheInputMode = cacheInputModeForDetail(detail, semantics)
	}
	if detail.TotalTokens == 0 {
		detail.TotalTokens = detail.TokenBreakdown.TotalTokens
	}
	return detail
}

func cacheInputModeMatchesCanonicalBreakdown(detail Detail) bool {
	breakdown := detail.TokenBreakdown
	if !breakdown.Valid() || breakdown.Quality == TokenAccountingQualityInconsistent ||
		!cacheInputModeFitsDetail(detail) {
		return false
	}
	if breakdown.Quality == TokenAccountingQualityUnclassified && breakdown.Input == (TokenInputBreakdown{}) {
		lowerBound, okLowerBound := unclassifiedTokenLowerBound(detail)
		return okLowerBound && breakdown.TotalTokens >= lowerBound
	}
	if detail.CacheReadTokens != 0 && detail.CacheReadTokens != breakdown.Input.CacheReadTokens {
		return false
	}
	if detail.CacheCreationTokens != 0 && detail.CacheCreationTokens != breakdown.Input.CacheWriteTokens {
		return false
	}
	canonicalCached := breakdown.Input.CacheReadTokens
	if canonicalCached == 0 {
		canonicalCached = breakdown.Input.CacheWriteTokens
	}
	if detail.CachedTokens != 0 && detail.CachedTokens != canonicalCached {
		return false
	}
	cacheTokens, okCache := nonNegativeSum(breakdown.Input.CacheReadTokens, breakdown.Input.CacheWriteTokens)
	if !okCache {
		return false
	}
	modeMatches := false
	switch detail.CacheInputMode {
	case CacheInputModeIncluded:
		modeMatches = breakdown.Input.TotalTokens == detail.InputTokens
	case CacheInputModeSeparate:
		expectedInput, okInput := nonNegativeSum(detail.InputTokens, cacheTokens)
		modeMatches = okInput && breakdown.Input.UncachedTokens == detail.InputTokens && breakdown.Input.TotalTokens == expectedInput
	default:
		return false
	}
	if !modeMatches {
		return false
	}
	if breakdown.Quality == TokenAccountingQualityUnclassified {
		lowerBound, okLowerBound := unclassifiedTokenLowerBound(detail)
		return okLowerBound && breakdown.TotalTokens >= lowerBound
	}
	return true
}

func cacheInputModeFitsDetail(detail Detail) bool {
	cacheTokens, okCache := nonNegativeSum(detail.CacheReadTokens, detail.CacheCreationTokens)
	if !okCache || detail.InputTokens < 0 || detail.CachedTokens < 0 {
		return false
	}
	if cacheTokens == 0 && detail.CachedTokens > 0 {
		cacheTokens = detail.CachedTokens
	}
	switch detail.CacheInputMode {
	case CacheInputModeIncluded:
		return cacheTokens <= detail.InputTokens && detail.CachedTokens <= detail.InputTokens
	case CacheInputModeSeparate:
		return true
	default:
		return false
	}
}

func hasTokenAccountingSignal(detail Detail) bool {
	return detail.InputTokens != 0 || detail.OutputTokens != 0 || detail.ReasoningTokens != 0 ||
		detail.CachedTokens != 0 || detail.CacheReadTokens != 0 || detail.CacheCreationTokens != 0 ||
		detail.TotalTokens != 0 || detail.TokenBreakdown.TotalTokens != 0
}

func tokenBreakdownForSemantics(detail Detail, semantics tokenAccountingSemantics) TokenBreakdown {
	if detail.TotalTokens == 0 && detail.InputTokens == 0 && detail.OutputTokens == 0 {
		if total, okTotal := unclassifiedTokenLowerBound(detail); !okTotal {
			return inconsistentTokenBreakdown(detail.TotalTokens, 0)
		} else if total > 0 && (semantics == tokenAccountingSemanticsUnknown ||
			semantics == tokenAccountingSemanticsSubset ||
			(semantics == tokenAccountingSemanticsSeparateReasoning &&
				(detail.CacheReadTokens > 0 || detail.CacheCreationTokens > 0 || detail.CachedTokens > 0))) {
			return NewUnclassifiedTokenBreakdown(total)
		}
	}
	switch semantics {
	case tokenAccountingSemanticsSubset:
		return NewSubsetTokenBreakdown(
			detail.InputTokens,
			detail.CacheReadTokens,
			detail.CacheCreationTokens,
			detail.OutputTokens,
			detail.ReasoningTokens,
			detail.TotalTokens,
		)
	case tokenAccountingSemanticsIndependent:
		return NewIndependentTokenBreakdown(
			detail.InputTokens,
			detail.CacheReadTokens,
			detail.CacheCreationTokens,
			detail.OutputTokens,
			detail.ReasoningTokens,
			detail.TotalTokens,
		)
	case tokenAccountingSemanticsSeparateReasoning:
		return NewSeparateReasoningTokenBreakdown(
			detail.InputTokens,
			detail.CacheReadTokens,
			detail.CacheCreationTokens,
			detail.OutputTokens,
			detail.ReasoningTokens,
			detail.TotalTokens,
		)
	case tokenAccountingSemanticsSeparateCache:
		return NewSeparateCacheTokenBreakdown(
			detail.InputTokens,
			detail.CacheReadTokens,
			detail.CacheCreationTokens,
			detail.OutputTokens,
			detail.ReasoningTokens,
			detail.TotalTokens,
		)
	default:
		total := detail.TotalTokens
		if total == 0 {
			var okTotal bool
			total, okTotal = unclassifiedTokenLowerBound(detail)
			if !okTotal {
				return inconsistentTokenBreakdown(detail.TotalTokens, 0)
			}
		}
		return NewUnclassifiedTokenBreakdown(total)
	}
}

func unclassifiedTokenLowerBound(detail Detail) (int64, bool) {
	cacheTokens, okCache := nonNegativeSum(detail.CacheReadTokens, detail.CacheCreationTokens)
	if !okCache || detail.InputTokens < 0 || detail.OutputTokens < 0 || detail.ReasoningTokens < 0 || detail.CachedTokens < 0 {
		return 0, false
	}
	inputTotal := detail.InputTokens
	if detail.CacheInputMode == CacheInputModeSeparate {
		if detail.CachedTokens > cacheTokens {
			cacheTokens = detail.CachedTokens
		}
		var okInput bool
		inputTotal, okInput = nonNegativeSum(inputTotal, cacheTokens)
		if !okInput {
			return 0, false
		}
	} else {
		if cacheTokens > inputTotal {
			inputTotal = cacheTokens
		}
		if detail.CachedTokens > inputTotal {
			inputTotal = detail.CachedTokens
		}
	}
	outputTotal := detail.OutputTokens
	if detail.ReasoningTokens > outputTotal {
		outputTotal = detail.ReasoningTokens
	}
	return nonNegativeSum(inputTotal, outputTotal)
}

func cacheInputModeForDetail(detail Detail, semantics tokenAccountingSemantics) CacheInputMode {
	cacheTokens, okCache := nonNegativeSum(detail.CacheReadTokens, detail.CacheCreationTokens)
	if !okCache || detail.InputTokens < 0 || detail.CachedTokens < 0 {
		return ""
	}
	if cacheTokens == 0 && detail.CachedTokens > 0 {
		cacheTokens = detail.CachedTokens
	} else if cacheTokens > 0 && detail.CachedTokens > 0 {
		canonicalCached := detail.CacheReadTokens
		if canonicalCached == 0 {
			canonicalCached = detail.CacheCreationTokens
		}
		if detail.CachedTokens != canonicalCached {
			return ""
		}
	}
	switch semantics {
	case tokenAccountingSemanticsSubset, tokenAccountingSemanticsSeparateReasoning:
		if cacheTokens > detail.InputTokens || detail.CachedTokens > detail.InputTokens {
			return ""
		}
		return CacheInputModeIncluded
	case tokenAccountingSemanticsIndependent:
		return CacheInputModeSeparate
	default:
		return ""
	}
}

func tokenAccountingSemanticsForDetail(detail Detail, provider, executorType string) tokenAccountingSemantics {
	semantics := tokenAccountingSemanticsFor(provider, executorType)
	switch detail.CacheInputMode {
	case CacheInputModeIncluded:
		switch semantics {
		case tokenAccountingSemanticsIndependent:
			return tokenAccountingSemanticsSeparateReasoning
		case tokenAccountingSemanticsUnknown:
			if detail.ReasoningTokens == 0 {
				return tokenAccountingSemanticsSubset
			}
		}
	case CacheInputModeSeparate:
		switch semantics {
		case tokenAccountingSemanticsSubset:
			return tokenAccountingSemanticsSeparateCache
		case tokenAccountingSemanticsSeparateReasoning:
			return tokenAccountingSemanticsIndependent
		case tokenAccountingSemanticsUnknown:
			if detail.ReasoningTokens == 0 {
				return tokenAccountingSemanticsSeparateCache
			}
		}
	}
	return semantics
}

func tokenAccountingSemanticsFor(provider, executorType string) tokenAccountingSemantics {
	normalizedProvider := strings.ToLower(strings.TrimSpace(provider))
	normalizedExecutor := strings.ToLower(strings.TrimSpace(executorType))
	value := strings.TrimSpace(normalizedProvider + " " + normalizedExecutor)
	if value == "" || value == "unknown" || value == "unknown unknown" {
		return tokenAccountingSemanticsUnknown
	}
	if normalizedExecutor == "openaicompatexecutor" || normalizedProvider == "openai-compatibility" || strings.HasPrefix(normalizedProvider, "openai-compatible-") {
		return tokenAccountingSemanticsSubset
	}
	if strings.Contains(value, "claude") || strings.Contains(value, "anthropic") {
		return tokenAccountingSemanticsIndependent
	}
	for _, marker := range []string{"gemini", "aistudio", "antigravity", "vertex", "interaction"} {
		if strings.Contains(value, marker) {
			return tokenAccountingSemanticsSeparateReasoning
		}
	}
	for _, marker := range []string{"openai", "codex", "xai", "grok", "kimi", "qwen", "deepseek", "openrouter"} {
		if strings.Contains(value, marker) {
			return tokenAccountingSemanticsSubset
		}
	}
	return tokenAccountingSemanticsUnknown
}

func inconsistentTokenBreakdown(total, fallback int64) TokenBreakdown {
	resolved := total
	if resolved <= 0 {
		resolved = fallback
	}
	if resolved < 0 {
		resolved = 0
	}
	return TokenBreakdown{
		SchemaVersion:      TokenAccountingSchemaVersion,
		Quality:            TokenAccountingQualityInconsistent,
		TotalTokens:        resolved,
		UnclassifiedTokens: resolved,
	}
}

func resolveAccountingTotal(total, expected int64) (int64, bool) {
	if total < 0 || expected < 0 {
		return 0, false
	}
	if total == 0 {
		return expected, true
	}
	return total, total == expected
}

func nonNegativeSum(values ...int64) (int64, bool) {
	var total int64
	for _, value := range values {
		if value < 0 || total > int64(^uint64(0)>>1)-value {
			return 0, false
		}
		total += value
	}
	return total, true
}
