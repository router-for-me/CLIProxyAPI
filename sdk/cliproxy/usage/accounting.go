package usage

// TokenAccountingSchemaVersion identifies the canonical token accounting contract.
const TokenAccountingSchemaVersion = 2

// TokenAccountingQuality describes how confidently a token total can be classified.
type TokenAccountingQuality string

const (
	TokenAccountingQualityComplete     TokenAccountingQuality = "complete"
	TokenAccountingQualityInconsistent TokenAccountingQuality = "inconsistent"
	TokenAccountingQualityUnclassified TokenAccountingQuality = "unclassified"
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
	if !detail.TokenBreakdown.Valid() {
		total := detail.TotalTokens
		if total == 0 {
			total = detail.InputTokens + detail.OutputTokens
		}
		detail.TokenBreakdown = NewUnclassifiedTokenBreakdown(total)
	}
	if detail.TotalTokens == 0 {
		detail.TotalTokens = detail.TokenBreakdown.TotalTokens
	}
	return detail
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
