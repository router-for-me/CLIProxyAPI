package billingexpr_test

import (
	"math"
	"os"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/pkg/billingexpr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFixedPriceBranches(t *testing.T) {
	const expression = `(len <= 32000 ? tier("short", fixed(0.01)) : tier("long", p * 2 + c * 8)) * (param("fast") == true ? 2 : 1)`
	for _, tc := range []struct {
		name   string
		params billingexpr.TokenParams
		cost   float64
		tier   string
	}{
		{"zero usage still charges once", billingexpr.TokenParams{}, 20000, "short"},
		{"fixed branch ignores token prices", billingexpr.TokenParams{P: 32000, C: 9000, Len: 32000}, 20000, "short"},
		{"token branch uses actual usage", billingexpr.TokenParams{P: 32001, C: 100, Len: 32001}, 129604, "long"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cost, trace, err := billingexpr.RunExprWithRequest(expression, tc.params, billingexpr.RequestInput{Body: []byte(`{"fast":true}`)})
			require.NoError(t, err)
			assert.Equal(t, tc.cost, cost)
			assert.Equal(t, tc.tier, trace.MatchedTier)
			require.Len(t, trace.RequestRules, 1)
			assert.True(t, trace.RequestRules[0].Matched)
		})
	}
	cost, _, err := billingexpr.RunExpr(`tier("free", fixed(0))`, billingexpr.TokenParams{P: 1000})
	require.NoError(t, err)
	assert.Zero(t, cost)
	for _, count := range []int{-1, 0, 129} {
		_, _, err := billingexpr.RunExprWithRequest(`tier("image", fixed(0.04)) * image_count`, billingexpr.TokenParams{}, billingexpr.RequestInput{ImageCount: &count})
		require.ErrorContains(t, err, "image_count")
	}
}

func TestFixedPriceRejectsInvalidLeavesIncludingUnselectedBranches(t *testing.T) {
	for _, expression := range []string{
		`true ? tier("ok", p) : tier("bad", fixed(-0.01))`,
		`tier("bad", fixed(p))`,
		`tier("bad", fixed(0.01 + 0.02))`,
		`tier("bad", fixed(1e308))`,
		`tier("bad", fixed(0.01) + p * 2)`,
		`tier("bad", p * fixed(0.01))`,
		`tier("a", fixed(0.01)) + tier("b", p * 2)`,
		`fixed(0.01)`,
		`tier("bad", fixed(0.01)) * p`,
		`tier("bad", fixed(0.01)) * (param("fast") == true ? -2 : 1)`,
	} {
		t.Run(expression, func(t *testing.T) {
			_, err := billingexpr.CompileFromCache(expression)
			require.Error(t, err)
		})
	}
}

func TestFixedPriceQuotaBoundariesAndUnsupportedTaskSnapshots(t *testing.T) {
	for _, tc := range []struct {
		name, expression string
		group            float64
		want             int
		clamped          bool
	}{
		{"normal group", `tier("request", fixed(0.01))`, 1.5, 7500, false},
		{"free group", `tier("request", fixed(0.01))`, 0, 0, false},
		{"explicit free price", `tier("free", fixed(0))`, 1, 0, false},
		{"oversized price saturates instead of crediting", `tier("huge", fixed(1e20))`, 1, math.MaxInt32, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snap := &billingexpr.BillingSnapshot{ExprString: tc.expression, ExprHash: billingexpr.ExprHashString(tc.expression), GroupRatio: tc.group, QuotaPerUnit: 500000}
			result, err := billingexpr.ComputeTieredQuota(snap, billingexpr.TokenParams{})
			require.NoError(t, err)
			assert.Equal(t, tc.want, result.ActualQuotaAfterGroup)
			assert.Equal(t, billingexpr.BillingUnitRequest, result.BillingUnit)
			assert.Equal(t, tc.clamped, result.Clamp != nil)
			_, err = billingexpr.QuotaRoundStrict(result.ActualQuotaBeforeGroup * tc.group)
			if tc.clamped {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			snap.TaskUsageBilling = true
			_, err = billingexpr.ComputeTieredQuota(snap, billingexpr.TokenParams{})
			require.ErrorContains(t, err, "task usage")
		})
	}
	const optimized = `true ? tier("tokens", p) : tier("request", fixed(0.01))`
	assert.True(t, billingexpr.UsesFixedPricing(optimized))
	_, trace, err := billingexpr.RunExpr(optimized, billingexpr.TokenParams{P: 10})
	require.NoError(t, err)
	assert.Equal(t, billingexpr.BillingUnitToken, trace.BillingUnit)
	assert.Nil(t, trace.FixedPrice)
}

// ---------------------------------------------------------------------------
// Claude-style: fixed tiers, input > 200K changes both input & output price
// ---------------------------------------------------------------------------

const claudeExpr = `p <= 200000 ? tier("standard", p * 1.5 + c * 7.5) : tier("long_context", p * 3.0 + c * 11.25)`

func TestClaude_StandardTier(t *testing.T) {
	cost, trace, err := billingexpr.RunExpr(claudeExpr, billingexpr.TokenParams{P: 100000, C: 5000})
	if err != nil {
		t.Fatal(err)
	}
	want := 100000*1.5 + 5000*7.5
	if math.Abs(cost-want) > 1e-6 {
		t.Errorf("cost = %f, want %f", cost, want)
	}
	if trace.MatchedTier != "standard" {
		t.Errorf("tier = %q, want %q", trace.MatchedTier, "standard")
	}
}

func TestClaude_LongContextTier(t *testing.T) {
	cost, trace, err := billingexpr.RunExpr(claudeExpr, billingexpr.TokenParams{P: 300000, C: 10000})
	if err != nil {
		t.Fatal(err)
	}
	want := 300000*3.0 + 10000*11.25
	if math.Abs(cost-want) > 1e-6 {
		t.Errorf("cost = %f, want %f", cost, want)
	}
	if trace.MatchedTier != "long_context" {
		t.Errorf("tier = %q, want %q", trace.MatchedTier, "long_context")
	}
}

func TestClaude_BoundaryExact(t *testing.T) {
	cost, trace, err := billingexpr.RunExpr(claudeExpr, billingexpr.TokenParams{P: 200000, C: 1000})
	if err != nil {
		t.Fatal(err)
	}
	want := 200000*1.5 + 1000*7.5
	if math.Abs(cost-want) > 1e-6 {
		t.Errorf("cost = %f, want %f", cost, want)
	}
	if trace.MatchedTier != "standard" {
		t.Errorf("tier = %q, want %q", trace.MatchedTier, "standard")
	}
}

// ---------------------------------------------------------------------------
// GLM-style: multi-condition tiers with both input and output dimensions
// ---------------------------------------------------------------------------

const glmExpr = `
(
	p < 32000 && c < 200 ? tier("tier1_short", (p)*2 + c*8) :
	p < 32000 && c >= 200 ? tier("tier2_long_output", (p)*3 + c*14) :
	tier("tier3_long_input", (p)*4 + c*16)
) / 1000000
`

func TestGLM_Tier1(t *testing.T) {
	cost, trace, err := billingexpr.RunExpr(glmExpr, billingexpr.TokenParams{P: 15000, C: 100})
	if err != nil {
		t.Fatal(err)
	}
	want := (15000.0*2 + 100.0*8) / 1000000
	if math.Abs(cost-want) > 1e-10 {
		t.Errorf("cost = %f, want %f", cost, want)
	}
	if trace.MatchedTier != "tier1_short" {
		t.Errorf("tier = %q, want %q", trace.MatchedTier, "tier1_short")
	}
}

func TestGLM_Tier2(t *testing.T) {
	cost, trace, err := billingexpr.RunExpr(glmExpr, billingexpr.TokenParams{P: 15000, C: 500})
	if err != nil {
		t.Fatal(err)
	}
	want := (15000.0*3 + 500.0*14) / 1000000
	if math.Abs(cost-want) > 1e-10 {
		t.Errorf("cost = %f, want %f", cost, want)
	}
	if trace.MatchedTier != "tier2_long_output" {
		t.Errorf("tier = %q, want %q", trace.MatchedTier, "tier2_long_output")
	}
}

func TestGLM_Tier3(t *testing.T) {
	cost, trace, err := billingexpr.RunExpr(glmExpr, billingexpr.TokenParams{P: 50000, C: 100})
	if err != nil {
		t.Fatal(err)
	}
	want := (50000.0*4 + 100.0*16) / 1000000
	if math.Abs(cost-want) > 1e-10 {
		t.Errorf("cost = %f, want %f", cost, want)
	}
	if trace.MatchedTier != "tier3_long_input" {
		t.Errorf("tier = %q, want %q", trace.MatchedTier, "tier3_long_input")
	}
}

// ---------------------------------------------------------------------------
// Simple flat-rate (no tier() call)
// ---------------------------------------------------------------------------

func TestSimpleExpr_NoTier(t *testing.T) {
	cost, trace, err := billingexpr.RunExpr("p * 0.5 + c * 1.0", billingexpr.TokenParams{P: 1000, C: 500})
	if err != nil {
		t.Fatal(err)
	}
	want := 1000*0.5 + 500*1.0
	if math.Abs(cost-want) > 1e-6 {
		t.Errorf("cost = %f, want %f", cost, want)
	}
	if trace.MatchedTier != "" {
		t.Errorf("tier should be empty, got %q", trace.MatchedTier)
	}
}

// ---------------------------------------------------------------------------
// Math helper functions
// ---------------------------------------------------------------------------

func TestMathHelpers(t *testing.T) {
	cost, _, err := billingexpr.RunExpr("max(p, c) * 0.5 + min(p, c) * 0.1", billingexpr.TokenParams{P: 300, C: 500})
	if err != nil {
		t.Fatal(err)
	}
	want := 500*0.5 + 300*0.1
	if math.Abs(cost-want) > 1e-6 {
		t.Errorf("cost = %f, want %f", cost, want)
	}
}

func TestRequestProbeHelpers(t *testing.T) {
	cost, _, err := billingexpr.RunExprWithRequest(
		`p * 0.5 + c * 1.0 * (param("service_tier") == "fast" ? 2 : 1)`,
		billingexpr.TokenParams{P: 1000, C: 500},
		billingexpr.RequestInput{
			Body: []byte(`{"service_tier":"fast"}`),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := 1000*0.5 + 500*1.0*2
	if math.Abs(cost-want) > 1e-6 {
		t.Errorf("cost = %f, want %f", cost, want)
	}
}

func TestHeaderProbeHelper(t *testing.T) {
	cost, _, err := billingexpr.RunExprWithRequest(
		`p * 0.5 + c * 1.0 * (has(header("anthropic-beta"), "fast-mode") ? 2 : 1)`,
		billingexpr.TokenParams{P: 1000, C: 500},
		billingexpr.RequestInput{
			Headers: map[string]string{
				"Anthropic-Beta": "fast-mode-2026-02-01",
			},
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := 1000*0.5 + 500*1.0*2
	if math.Abs(cost-want) > 1e-6 {
		t.Errorf("cost = %f, want %f", cost, want)
	}
}

func TestParamProbeNestedBool(t *testing.T) {
	cost, _, err := billingexpr.RunExprWithRequest(
		`p * (param("stream_options.fast_mode") == true ? 1.5 : 1.0)`,
		billingexpr.TokenParams{P: 100},
		billingexpr.RequestInput{
			Body: []byte(`{"stream_options":{"fast_mode":true}}`),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := 150.0
	if math.Abs(cost-want) > 1e-6 {
		t.Errorf("cost = %f, want %f", cost, want)
	}
}

func TestParamProbeArrayLength(t *testing.T) {
	cost, _, err := billingexpr.RunExprWithRequest(
		`p * (param("messages.#") > 20 ? 1.2 : 1.0)`,
		billingexpr.TokenParams{P: 100},
		billingexpr.RequestInput{
			Body: []byte(`{"messages":[1,2,3,4,5,6,7,8,9,10,11,12,13,14,15,16,17,18,19,20,21]}`),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	want := 120.0
	if math.Abs(cost-want) > 1e-6 {
		t.Errorf("cost = %f, want %f", cost, want)
	}
}

func TestRequestProbeMissingFieldReturnsNil(t *testing.T) {
	cost, _, err := billingexpr.RunExprWithRequest(
		`param("missing.value") == nil ? 2 : 1`,
		billingexpr.TokenParams{},
		billingexpr.RequestInput{
			Body: []byte(`{"service_tier":"standard"}`),
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if cost != 2 {
		t.Errorf("cost = %f, want 2", cost)
	}
}

func TestRequestProbeMultipleRulesTraceAllFactors(t *testing.T) {
	exprStr := `(tier("base", p * 2)) * (param("service_tier") == "fast" ? 2 : 1) * (has(header("anthropic-beta"), "fast-mode-2026-02-01") ? 2.5 : 1)`
	cost, trace, err := billingexpr.RunExprWithRequest(
		exprStr,
		billingexpr.TokenParams{P: 10},
		billingexpr.RequestInput{
			Headers: map[string]string{
				"Anthropic-Beta": "fast-mode-2026-02-01",
			},
			Body: []byte(`{"service_tier":"fast"}`),
		},
	)

	require.NoError(t, err)
	assert.InDelta(t, 100, cost, 1e-6)
	assert.Equal(t, "base", trace.MatchedTier)
	assert.Equal(t, []billingexpr.RequestRuleTrace{
		{Cond: `param("service_tier") == "fast"`, Multiplier: 2, Matched: true},
		{Cond: `has(header("anthropic-beta"), "fast-mode-2026-02-01")`, Multiplier: 2.5, Matched: true},
	}, trace.RequestRules)
}

func TestRequestProbeTraceIncludesUnmatchedFactors(t *testing.T) {
	exprStr := `(tier("base", p * 2)) * (param("service_tier") == "fast" ? 2 : 1) * (has(header("anthropic-beta"), "fast-mode") ? 2.5 : 1)`
	cost, trace, err := billingexpr.RunExprWithRequest(
		exprStr,
		billingexpr.TokenParams{P: 10},
		billingexpr.RequestInput{Body: []byte(`{"service_tier":"fast"}`)},
	)

	require.NoError(t, err)
	assert.InDelta(t, 40, cost, 1e-6)
	assert.Equal(t, []billingexpr.RequestRuleTrace{
		{Cond: `param("service_tier") == "fast"`, Multiplier: 2, Matched: true},
		{Cond: `has(header("anthropic-beta"), "fast-mode")`, Multiplier: 2.5, Matched: false},
	}, trace.RequestRules)
}

func TestRequestProbeTracePreservesIntegerConditionalType(t *testing.T) {
	cost, trace, err := billingexpr.RunExprWithRequest(
		`5 % (param("service_tier") == "fast" ? 2 : 1)`,
		billingexpr.TokenParams{},
		billingexpr.RequestInput{Body: []byte(`{"service_tier":"fast"}`)},
	)

	require.NoError(t, err)
	assert.Equal(t, float64(1), cost)
	assert.Equal(t, []billingexpr.RequestRuleTrace{
		{Cond: `param("service_tier") == "fast"`, Multiplier: 2, Matched: true},
	}, trace.RequestRules)
}

func TestRequestProbeNonUnitFallbackIsNotTraced(t *testing.T) {
	cost, trace, err := billingexpr.RunExprWithRequest(
		`10 * (param("service_tier") == "fast" ? 2 : 1.5)`,
		billingexpr.TokenParams{},
		billingexpr.RequestInput{Body: []byte(`{"service_tier":"standard"}`)},
	)

	require.NoError(t, err)
	assert.InDelta(t, 15, cost, 1e-6)
	assert.Empty(t, trace.RequestRules)
}

func TestRequestProbeInternalTraceFunctionIsReserved(t *testing.T) {
	_, err := billingexpr.CompileFromCache(`_trace(0, true, 5.0)`)

	require.ErrorContains(t, err, `identifier "_trace" is reserved for internal use`)
}

func TestCeilFloor(t *testing.T) {
	cost, _, err := billingexpr.RunExpr("ceil(p / 1000) * 0.5", billingexpr.TokenParams{P: 1500})
	if err != nil {
		t.Fatal(err)
	}
	want := math.Ceil(1500.0/1000) * 0.5
	if math.Abs(cost-want) > 1e-6 {
		t.Errorf("cost = %f, want %f", cost, want)
	}
}

// ---------------------------------------------------------------------------
// Zero tokens
// ---------------------------------------------------------------------------

func TestZeroTokens(t *testing.T) {
	cost, _, err := billingexpr.RunExpr(claudeExpr, billingexpr.TokenParams{})
	if err != nil {
		t.Fatal(err)
	}
	if cost != 0 {
		t.Errorf("cost should be 0 for zero tokens, got %f", cost)
	}
}

// ---------------------------------------------------------------------------
// Rounding
// ---------------------------------------------------------------------------

func TestQuotaRound(t *testing.T) {
	tests := []struct {
		in   float64
		want int
	}{
		{0, 0},
		{0.4, 0},
		{0.5, 1},
		{0.6, 1},
		{1.5, 2},
		{-0.5, -1},
		{-0.6, -1},
		{999.4999, 999},
		{999.5, 1000},
		{1e9 + 0.5, 1e9 + 1},
		// Oversized expression results saturate at the single-request limit (delegated to
		// common.QuotaRound); full saturation coverage lives in common.
		{3.6893488147419103e19, common.MaxQuota},
	}
	for _, tt := range tests {
		got := billingexpr.QuotaRound(tt.in)
		if got != tt.want {
			t.Errorf("QuotaRound(%f) = %d, want %d", tt.in, got, tt.want)
		}
	}
}

// ---------------------------------------------------------------------------
// Settlement
// ---------------------------------------------------------------------------

func TestComputeTieredQuota_Basic(t *testing.T) {
	snap := &billingexpr.BillingSnapshot{
		BillingMode:               "tiered_expr",
		ExprString:                claudeExpr,
		ExprHash:                  billingexpr.ExprHashString(claudeExpr),
		GroupRatio:                1.0,
		EstimatedPromptTokens:     100000,
		EstimatedCompletionTokens: 5000,
		EstimatedQuotaBeforeGroup: (100000*1.5 + 5000*7.5) / 1_000_000 * 500_000,
		EstimatedQuotaAfterGroup:  billingexpr.QuotaRound((100000*1.5 + 5000*7.5) / 1_000_000 * 500_000),
		EstimatedTier:             "standard",
		QuotaPerUnit:              500_000,
	}

	result, err := billingexpr.ComputeTieredQuota(snap, billingexpr.TokenParams{P: 300000, C: 10000})
	if err != nil {
		t.Fatal(err)
	}

	wantBefore := (300000*3.0 + 10000*11.25) / 1_000_000 * 500_000
	if math.Abs(result.ActualQuotaBeforeGroup-wantBefore) > 1e-6 {
		t.Errorf("before group: got %f, want %f", result.ActualQuotaBeforeGroup, wantBefore)
	}
	if result.MatchedTier != "long_context" {
		t.Errorf("tier = %q, want %q", result.MatchedTier, "long_context")
	}
	if !result.CrossedTier {
		t.Error("expected crossed_tier=true (estimated standard, actual long_context)")
	}
}

func TestComputeTieredQuota_SameTier(t *testing.T) {
	snap := &billingexpr.BillingSnapshot{
		BillingMode:               "tiered_expr",
		ExprString:                claudeExpr,
		ExprHash:                  billingexpr.ExprHashString(claudeExpr),
		GroupRatio:                1.5,
		EstimatedPromptTokens:     50000,
		EstimatedCompletionTokens: 1000,
		EstimatedQuotaBeforeGroup: (50000*1.5 + 1000*7.5) / 1_000_000 * 500_000,
		EstimatedQuotaAfterGroup:  billingexpr.QuotaRound((50000*1.5 + 1000*7.5) / 1_000_000 * 500_000 * 1.5),
		EstimatedTier:             "standard",
		QuotaPerUnit:              500_000,
	}

	result, err := billingexpr.ComputeTieredQuota(snap, billingexpr.TokenParams{P: 80000, C: 2000})
	if err != nil {
		t.Fatal(err)
	}

	wantBefore := (80000*1.5 + 2000*7.5) / 1_000_000 * 500_000
	wantAfter := billingexpr.QuotaRound(wantBefore * 1.5)
	if result.ActualQuotaAfterGroup != wantAfter {
		t.Errorf("after group: got %d, want %d", result.ActualQuotaAfterGroup, wantAfter)
	}
	if result.CrossedTier {
		t.Error("expected crossed_tier=false (both standard)")
	}
}

// ---------------------------------------------------------------------------
// Compile errors
// ---------------------------------------------------------------------------

func TestCompileError(t *testing.T) {
	_, _, err := billingexpr.RunExpr("invalid +-+ syntax", billingexpr.TokenParams{})
	if err == nil {
		t.Error("expected compile error")
	}
}

// ---------------------------------------------------------------------------
// Compile Cache
// ---------------------------------------------------------------------------

func TestCompileCache_SameResult(t *testing.T) {
	r1, _, err := billingexpr.RunExpr("p * 0.5", billingexpr.TokenParams{P: 100})
	if err != nil {
		t.Fatal(err)
	}
	r2, _, err := billingexpr.RunExpr("p * 0.5", billingexpr.TokenParams{P: 100})
	if err != nil {
		t.Fatal(err)
	}
	if r1 != r2 {
		t.Errorf("cached and uncached results differ: %f != %f", r1, r2)
	}
}

func TestInvalidateCache(t *testing.T) {
	billingexpr.InvalidateCache()
	r1, _, _ := billingexpr.RunExpr("p * 0.5", billingexpr.TokenParams{P: 100})
	billingexpr.InvalidateCache()
	r2, _, _ := billingexpr.RunExpr("p * 0.5", billingexpr.TokenParams{P: 100})
	if r1 != r2 {
		t.Errorf("post-invalidate results differ: %f != %f", r1, r2)
	}
}

// ---------------------------------------------------------------------------
// Hash
// ---------------------------------------------------------------------------

func TestExprHashString_Deterministic(t *testing.T) {
	h1 := billingexpr.ExprHashString("p * 0.5")
	h2 := billingexpr.ExprHashString("p * 0.5")
	if h1 != h2 {
		t.Error("hash should be deterministic")
	}
	h3 := billingexpr.ExprHashString("p * 0.6")
	if h1 == h3 {
		t.Error("different expressions should have different hashes")
	}
}

// ---------------------------------------------------------------------------
// Cache variables: present
// ---------------------------------------------------------------------------

const claudeWithCacheExpr = `p <= 200000 ? tier("standard", p * 1.5 + c * 7.5 + cr * 0.15 + cc * 1.875) : tier("long_context", p * 3.0 + c * 11.25 + cr * 0.3 + cc * 3.75)`

func TestCachePresent_StandardTier(t *testing.T) {
	params := billingexpr.TokenParams{P: 100000, C: 5000, CR: 50000, CC: 10000}
	cost, trace, err := billingexpr.RunExpr(claudeWithCacheExpr, params)
	if err != nil {
		t.Fatal(err)
	}
	want := 100000*1.5 + 5000*7.5 + 50000*0.15 + 10000*1.875
	if math.Abs(cost-want) > 1e-6 {
		t.Errorf("cost = %f, want %f", cost, want)
	}
	if trace.MatchedTier != "standard" {
		t.Errorf("tier = %q, want %q", trace.MatchedTier, "standard")
	}
}

func TestCachePresent_LongContextTier(t *testing.T) {
	params := billingexpr.TokenParams{P: 300000, C: 10000, CR: 100000, CC: 20000}
	cost, trace, err := billingexpr.RunExpr(claudeWithCacheExpr, params)
	if err != nil {
		t.Fatal(err)
	}
	want := 300000*3.0 + 10000*11.25 + 100000*0.3 + 20000*3.75
	if math.Abs(cost-want) > 1e-6 {
		t.Errorf("cost = %f, want %f", cost, want)
	}
	if trace.MatchedTier != "long_context" {
		t.Errorf("tier = %q, want %q", trace.MatchedTier, "long_context")
	}
}

// ---------------------------------------------------------------------------
// Cache variables: absent (all zero) — same expression still works
// ---------------------------------------------------------------------------

func TestCacheAbsent_ZeroCacheTokens(t *testing.T) {
	params := billingexpr.TokenParams{P: 100000, C: 5000}
	cost, trace, err := billingexpr.RunExpr(claudeWithCacheExpr, params)
	if err != nil {
		t.Fatal(err)
	}
	want := 100000*1.5 + 5000*7.5
	if math.Abs(cost-want) > 1e-6 {
		t.Errorf("cost = %f, want %f (cache terms should be 0)", cost, want)
	}
	if trace.MatchedTier != "standard" {
		t.Errorf("tier = %q, want %q", trace.MatchedTier, "standard")
	}
}

// ---------------------------------------------------------------------------
// Mixed cache fields: cc and cc1h non-zero
// ---------------------------------------------------------------------------

const claudeCacheSplitExpr = `tier("default", p * 1.5 + c * 7.5 + cr * 0.15 + cc * 2.0 + cc1h * 3.0)`

func TestMixedCacheFields(t *testing.T) {
	params := billingexpr.TokenParams{P: 100000, C: 5000, CR: 10000, CC: 5000, CC1h: 2000}
	cost, _, err := billingexpr.RunExpr(claudeCacheSplitExpr, params)
	if err != nil {
		t.Fatal(err)
	}
	want := 100000*1.5 + 5000*7.5 + 10000*0.15 + 5000*2.0 + 2000*3.0
	if math.Abs(cost-want) > 1e-6 {
		t.Errorf("cost = %f, want %f", cost, want)
	}
}

func TestMixedCacheFields_AllCacheZero(t *testing.T) {
	params := billingexpr.TokenParams{P: 100000, C: 5000}
	cost, _, err := billingexpr.RunExpr(claudeCacheSplitExpr, params)
	if err != nil {
		t.Fatal(err)
	}
	want := 100000*1.5 + 5000*7.5
	if math.Abs(cost-want) > 1e-6 {
		t.Errorf("cost = %f, want %f (all cache zero)", cost, want)
	}
}

// ---------------------------------------------------------------------------
// Backward compatibility: p+c only expressions still work with TokenParams
// ---------------------------------------------------------------------------

func TestBackwardCompat_OldExprWithTokenParams(t *testing.T) {
	params := billingexpr.TokenParams{P: 100000, C: 5000, CR: 99999, CC: 88888}
	cost, trace, err := billingexpr.RunExpr(claudeExpr, params)
	if err != nil {
		t.Fatal(err)
	}
	want := 100000*1.5 + 5000*7.5
	if math.Abs(cost-want) > 1e-6 {
		t.Errorf("cost = %f, want %f (old expr ignores cache fields)", cost, want)
	}
	if trace.MatchedTier != "standard" {
		t.Errorf("tier = %q, want %q", trace.MatchedTier, "standard")
	}
}

// ---------------------------------------------------------------------------
// Settlement with cache tokens
// ---------------------------------------------------------------------------

func TestComputeTieredQuota_WithCache(t *testing.T) {
	snap := &billingexpr.BillingSnapshot{
		BillingMode:               "tiered_expr",
		ExprString:                claudeWithCacheExpr,
		ExprHash:                  billingexpr.ExprHashString(claudeWithCacheExpr),
		GroupRatio:                1.0,
		EstimatedPromptTokens:     100000,
		EstimatedCompletionTokens: 5000,
		EstimatedQuotaBeforeGroup: (100000*1.5 + 5000*7.5) / 1_000_000 * 500_000,
		EstimatedQuotaAfterGroup:  billingexpr.QuotaRound((100000*1.5 + 5000*7.5) / 1_000_000 * 500_000),
		EstimatedTier:             "standard",
		QuotaPerUnit:              500_000,
	}

	params := billingexpr.TokenParams{P: 100000, C: 5000, CR: 50000, CC: 10000}
	result, err := billingexpr.ComputeTieredQuota(snap, params)
	if err != nil {
		t.Fatal(err)
	}

	wantBefore := (100000*1.5 + 5000*7.5 + 50000*0.15 + 10000*1.875) / 1_000_000 * 500_000
	if math.Abs(result.ActualQuotaBeforeGroup-wantBefore) > 1e-6 {
		t.Errorf("before group: got %f, want %f", result.ActualQuotaBeforeGroup, wantBefore)
	}
	if result.MatchedTier != "standard" {
		t.Errorf("tier = %q, want %q", result.MatchedTier, "standard")
	}
	if result.CrossedTier {
		t.Error("expected crossed_tier=false (same tier)")
	}
}

func TestComputeTieredQuota_WithCacheCrossTier(t *testing.T) {
	snap := &billingexpr.BillingSnapshot{
		BillingMode:               "tiered_expr",
		ExprString:                claudeWithCacheExpr,
		ExprHash:                  billingexpr.ExprHashString(claudeWithCacheExpr),
		GroupRatio:                2.0,
		EstimatedPromptTokens:     100000,
		EstimatedCompletionTokens: 5000,
		EstimatedQuotaBeforeGroup: (100000*1.5 + 5000*7.5) / 1_000_000 * 500_000,
		EstimatedQuotaAfterGroup:  billingexpr.QuotaRound((100000*1.5 + 5000*7.5) / 1_000_000 * 500_000 * 2.0),
		EstimatedTier:             "standard",
		QuotaPerUnit:              500_000,
	}

	params := billingexpr.TokenParams{P: 300000, C: 10000, CR: 50000, CC: 10000}
	result, err := billingexpr.ComputeTieredQuota(snap, params)
	if err != nil {
		t.Fatal(err)
	}

	wantBefore := (300000*3.0 + 10000*11.25 + 50000*0.3 + 10000*3.75) / 1_000_000 * 500_000
	wantAfter := billingexpr.QuotaRound(wantBefore * 2.0)
	if math.Abs(result.ActualQuotaBeforeGroup-wantBefore) > 1e-6 {
		t.Errorf("before group: got %f, want %f", result.ActualQuotaBeforeGroup, wantBefore)
	}
	if result.ActualQuotaAfterGroup != wantAfter {
		t.Errorf("after group: got %d, want %d", result.ActualQuotaAfterGroup, wantAfter)
	}
	if !result.CrossedTier {
		t.Error("expected crossed_tier=true (estimated standard, actual long_context)")
	}
}

// ---------------------------------------------------------------------------
// Settlement-level tests for ComputeTieredQuota
// ---------------------------------------------------------------------------

func TestComputeTieredQuota_BasicSettlement(t *testing.T) {
	exprStr := `tier("default", p + c)`
	snap := &billingexpr.BillingSnapshot{
		BillingMode:  "tiered_expr",
		ExprString:   exprStr,
		ExprHash:     billingexpr.ExprHashString(exprStr),
		GroupRatio:   1.0,
		QuotaPerUnit: 500_000,
	}

	result, err := billingexpr.ComputeTieredQuota(snap, billingexpr.TokenParams{P: 3000, C: 2000})
	if err != nil {
		t.Fatal(err)
	}
	// exprOutput = 5000; quota = 5000 / 1M * 500K = 2500
	if math.Abs(result.ActualQuotaBeforeGroup-2500) > 1e-6 {
		t.Errorf("before group = %f, want 2500", result.ActualQuotaBeforeGroup)
	}
	if result.ActualQuotaAfterGroup != 2500 {
		t.Errorf("after group = %d, want 2500", result.ActualQuotaAfterGroup)
	}
	if result.MatchedTier != "default" {
		t.Errorf("tier = %q, want default", result.MatchedTier)
	}
}

func TestComputeTieredQuota_WithGroupRatio(t *testing.T) {
	exprStr := `tier("default", p + c)`
	snap := &billingexpr.BillingSnapshot{
		BillingMode:  "tiered_expr",
		ExprString:   exprStr,
		ExprHash:     billingexpr.ExprHashString(exprStr),
		GroupRatio:   2.0,
		QuotaPerUnit: 500_000,
	}

	result, err := billingexpr.ComputeTieredQuota(snap, billingexpr.TokenParams{P: 1000, C: 500})
	if err != nil {
		t.Fatal(err)
	}
	// exprOutput = 1500; quotaBeforeGroup = 750; afterGroup = round(750 * 2.0) = 1500
	if result.ActualQuotaAfterGroup != 1500 {
		t.Errorf("after group = %d, want 1500", result.ActualQuotaAfterGroup)
	}
}

func TestComputeTieredQuota_ZeroTokens(t *testing.T) {
	exprStr := `tier("default", p * 2 + c * 10)`
	snap := &billingexpr.BillingSnapshot{
		BillingMode:  "tiered_expr",
		ExprString:   exprStr,
		ExprHash:     billingexpr.ExprHashString(exprStr),
		GroupRatio:   1.0,
		QuotaPerUnit: 500_000,
	}

	result, err := billingexpr.ComputeTieredQuota(snap, billingexpr.TokenParams{})
	if err != nil {
		t.Fatal(err)
	}
	if result.ActualQuotaAfterGroup != 0 {
		t.Errorf("after group = %d, want 0", result.ActualQuotaAfterGroup)
	}
}

func TestComputeTieredQuota_RoundingEdge(t *testing.T) {
	exprStr := `tier("default", p * 0.5)` // 3 * 0.5 = 1.5 (expr); 1.5 / 1M * 500K = 0.75; round(0.75) = 1
	snap := &billingexpr.BillingSnapshot{
		BillingMode:  "tiered_expr",
		ExprString:   exprStr,
		ExprHash:     billingexpr.ExprHashString(exprStr),
		GroupRatio:   1.0,
		QuotaPerUnit: 500_000,
	}

	result, err := billingexpr.ComputeTieredQuota(snap, billingexpr.TokenParams{P: 3})
	if err != nil {
		t.Fatal(err)
	}
	// 3 * 0.5 = 1.5 (expr); quota = 1.5 / 1M * 500K = 0.75; round(0.75) = 1
	if result.ActualQuotaAfterGroup != 1 {
		t.Errorf("after group = %d, want 1 (round 0.75 up)", result.ActualQuotaAfterGroup)
	}
}

func TestComputeTieredQuota_RoundingEdgeDown(t *testing.T) {
	exprStr := `tier("default", p * 0.4)` // 3 * 0.4 = 1.2 (expr); 1.2 / 1M * 500K = 0.6; round(0.6) = 1
	snap := &billingexpr.BillingSnapshot{
		BillingMode:  "tiered_expr",
		ExprString:   exprStr,
		ExprHash:     billingexpr.ExprHashString(exprStr),
		GroupRatio:   1.0,
		QuotaPerUnit: 500_000,
	}

	result, err := billingexpr.ComputeTieredQuota(snap, billingexpr.TokenParams{P: 3})
	if err != nil {
		t.Fatal(err)
	}
	// 3 * 0.4 = 1.2 (expr); quota = 1.2 / 1M * 500K = 0.6; round(0.6) = 1
	if result.ActualQuotaAfterGroup != 1 {
		t.Errorf("after group = %d, want 1 (round 0.6 up)", result.ActualQuotaAfterGroup)
	}
}

func TestComputeTieredQuotaWithRequest_ProbeAffectsQuota(t *testing.T) {
	exprStr := `param("fast") == true ? tier("fast", p * 4) : tier("normal", p * 2)`
	snap := &billingexpr.BillingSnapshot{
		BillingMode:   "tiered_expr",
		ExprString:    exprStr,
		ExprHash:      billingexpr.ExprHashString(exprStr),
		GroupRatio:    1.0,
		EstimatedTier: "normal",
		QuotaPerUnit:  500_000,
	}

	// Without request: normal tier
	r1, err := billingexpr.ComputeTieredQuota(snap, billingexpr.TokenParams{P: 1000})
	if err != nil {
		t.Fatal(err)
	}
	// normal: p*2 = 2000; quota = 2000 / 1M * 500K = 1000
	if r1.ActualQuotaAfterGroup != 1000 {
		t.Errorf("normal = %d, want 1000", r1.ActualQuotaAfterGroup)
	}

	// With request: fast tier
	r2, err := billingexpr.ComputeTieredQuotaWithRequest(snap, billingexpr.TokenParams{P: 1000}, billingexpr.RequestInput{
		Body: []byte(`{"fast":true}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	// fast: p*4 = 4000; quota = 4000 / 1M * 500K = 2000
	if r2.ActualQuotaAfterGroup != 2000 {
		t.Errorf("fast = %d, want 2000", r2.ActualQuotaAfterGroup)
	}
	if !r2.CrossedTier {
		t.Error("expected CrossedTier = true when probe changes tier")
	}
}

func TestComputeTieredQuota_BoundaryTierCrossing(t *testing.T) {
	exprStr := `p <= 100000 ? tier("small", p * 1) : tier("large", p * 2)`
	snap := &billingexpr.BillingSnapshot{
		BillingMode:   "tiered_expr",
		ExprString:    exprStr,
		ExprHash:      billingexpr.ExprHashString(exprStr),
		GroupRatio:    1.0,
		EstimatedTier: "small",
		QuotaPerUnit:  500_000,
	}

	// At boundary: small, p*1 = 100000; quota = 100000 / 1M * 500K = 50000
	r1, err := billingexpr.ComputeTieredQuota(snap, billingexpr.TokenParams{P: 100000})
	if err != nil {
		t.Fatal(err)
	}
	if r1.MatchedTier != "small" {
		t.Errorf("at boundary: tier = %s, want small", r1.MatchedTier)
	}
	if r1.ActualQuotaAfterGroup != 50000 {
		t.Errorf("at boundary: quota = %d, want 50000", r1.ActualQuotaAfterGroup)
	}

	// Past boundary: large, p*2 = 200002; quota = 200002 / 1M * 500K = 100001
	r2, err := billingexpr.ComputeTieredQuota(snap, billingexpr.TokenParams{P: 100001})
	if err != nil {
		t.Fatal(err)
	}
	if r2.MatchedTier != "large" {
		t.Errorf("past boundary: tier = %s, want large", r2.MatchedTier)
	}
	if r2.ActualQuotaAfterGroup != 100001 {
		t.Errorf("past boundary: quota = %d, want 100001", r2.ActualQuotaAfterGroup)
	}
	if !r2.CrossedTier {
		t.Error("expected CrossedTier = true")
	}
}

// ---------------------------------------------------------------------------
// Time function tests
// ---------------------------------------------------------------------------

func TestTimeFunctions_ValidTimezone(t *testing.T) {
	exprStr := `tier("default", p) * (hour("UTC") >= 0 ? 1 : 1)`
	cost, _, err := billingexpr.RunExpr(exprStr, billingexpr.TokenParams{P: 100})
	if err != nil {
		t.Fatal(err)
	}
	if cost != 100 {
		t.Errorf("cost = %f, want 100", cost)
	}
}

func TestTimeFunctions_AllFunctionsCompile(t *testing.T) {
	exprStr := `tier("default", p) * (hour("Asia/Shanghai") >= 0 ? 1 : 1) * (minute("UTC") >= 0 ? 1 : 1) * (weekday("UTC") >= 0 ? 1 : 1) * (month("UTC") >= 1 ? 1 : 1) * (day("UTC") >= 1 ? 1 : 1)`
	cost, _, err := billingexpr.RunExpr(exprStr, billingexpr.TokenParams{P: 500})
	if err != nil {
		t.Fatal(err)
	}
	if cost != 500 {
		t.Errorf("cost = %f, want 500", cost)
	}
}

func TestTimeFunctions_InvalidTimezone(t *testing.T) {
	exprStr := `tier("default", p) * (hour("Invalid/Zone") >= 0 ? 1 : 2)`
	cost, _, err := billingexpr.RunExpr(exprStr, billingexpr.TokenParams{P: 100})
	if err != nil {
		t.Fatal(err)
	}
	// Invalid timezone falls back to UTC; hour is 0-23, so condition is always true
	if cost != 100 {
		t.Errorf("cost = %f, want 100 (fallback to UTC)", cost)
	}
}

func TestTimeFunctions_EmptyTimezone(t *testing.T) {
	exprStr := `tier("default", p) * (hour("") >= 0 ? 1 : 2)`
	cost, _, err := billingexpr.RunExpr(exprStr, billingexpr.TokenParams{P: 100})
	if err != nil {
		t.Fatal(err)
	}
	if cost != 100 {
		t.Errorf("cost = %f, want 100 (empty tz -> UTC)", cost)
	}
}

func TestTimeFunctions_NightDiscountPattern(t *testing.T) {
	exprStr := `tier("default", p * 2 + c * 10) * (hour("UTC") >= 21 || hour("UTC") < 6 ? 0.5 : 1)`
	cost, _, err := billingexpr.RunExpr(exprStr, billingexpr.TokenParams{P: 1000, C: 500})
	if err != nil {
		t.Fatal(err)
	}
	// Base = 1000*2 + 500*10 = 7000; multiplier is either 0.5 or 1 depending on current UTC hour
	if cost != 7000 && cost != 3500 {
		t.Errorf("cost = %f, want 7000 or 3500", cost)
	}
}

func TestTimeFunctions_WeekdayRange(t *testing.T) {
	exprStr := `tier("default", p) * (weekday("UTC") >= 0 && weekday("UTC") <= 6 ? 1 : 999)`
	cost, _, err := billingexpr.RunExpr(exprStr, billingexpr.TokenParams{P: 100})
	if err != nil {
		t.Fatal(err)
	}
	// weekday is always 0-6, so multiplier is always 1
	if cost != 100 {
		t.Errorf("cost = %f, want 100", cost)
	}
}

func TestTimeFunctions_MonthDayPattern(t *testing.T) {
	exprStr := `tier("default", p) * (month("Asia/Shanghai") == 1 && day("Asia/Shanghai") == 1 ? 0.5 : 1)`
	cost, _, err := billingexpr.RunExpr(exprStr, billingexpr.TokenParams{P: 1000})
	if err != nil {
		t.Fatal(err)
	}
	// Either 1000 (not Jan 1) or 500 (Jan 1) — both are valid
	if cost != 1000 && cost != 500 {
		t.Errorf("cost = %f, want 1000 or 500", cost)
	}
}

// ---------------------------------------------------------------------------
// Image and audio token tests
// ---------------------------------------------------------------------------

func TestImageTokenVariable(t *testing.T) {
	exprStr := `tier("base", p * 2 + c * 10 + img * 5)`
	cost, _, err := billingexpr.RunExpr(exprStr, billingexpr.TokenParams{P: 1000, C: 500, Img: 200})
	if err != nil {
		t.Fatal(err)
	}
	// 1000*2 + 500*10 + 200*5 = 2000 + 5000 + 1000 = 8000
	if math.Abs(cost-8000) > 1e-6 {
		t.Errorf("cost = %f, want 8000", cost)
	}
}

func TestAudioTokenVariables(t *testing.T) {
	exprStr := `tier("base", p * 2 + c * 10 + ai * 50 + ao * 100)`
	cost, _, err := billingexpr.RunExpr(exprStr, billingexpr.TokenParams{P: 1000, C: 500, AI: 100, AO: 50})
	if err != nil {
		t.Fatal(err)
	}
	// 1000*2 + 500*10 + 100*50 + 50*100 = 2000 + 5000 + 5000 + 5000 = 17000
	if math.Abs(cost-17000) > 1e-6 {
		t.Errorf("cost = %f, want 17000", cost)
	}
}

func TestImageAudioVariables(t *testing.T) {
	exprStr := `tier("base", p * 1 + img * 3 + ai * 5 + ao * 10)`
	cost, _, err := billingexpr.RunExpr(exprStr, billingexpr.TokenParams{P: 100, Img: 50, AI: 20, AO: 10})
	if err != nil {
		t.Fatal(err)
	}
	// 100*1 + 50*3 + 20*5 + 10*10 = 100 + 150 + 100 + 100 = 450
	if math.Abs(cost-450) > 1e-6 {
		t.Errorf("cost = %f, want 450", cost)
	}
}

func TestImageAudioZero(t *testing.T) {
	exprStr := `tier("base", p * 2 + img * 5 + ai * 50 + ao * 100)`
	cost, _, err := billingexpr.RunExpr(exprStr, billingexpr.TokenParams{P: 1000})
	if err != nil {
		t.Fatal(err)
	}
	// img, ai, ao default to 0
	if math.Abs(cost-2000) > 1e-6 {
		t.Errorf("cost = %f, want 2000", cost)
	}
}

// ---------------------------------------------------------------------------
// len variable tests — tier conditions based on context length
// ---------------------------------------------------------------------------

const lenTieredExpr = `len <= 200000 ? tier("standard", p * 3 + c * 15 + cr * 0.3) : tier("long_context", p * 6 + c * 22.5 + cr * 0.6)`

func TestLen_StandardTier(t *testing.T) {
	params := billingexpr.TokenParams{P: 80000, C: 5000, Len: 100000, CR: 20000}
	cost, trace, err := billingexpr.RunExpr(lenTieredExpr, params)
	if err != nil {
		t.Fatal(err)
	}
	want := 80000*3 + 5000*15 + 20000*0.3
	if math.Abs(cost-want) > 1e-6 {
		t.Errorf("cost = %f, want %f", cost, want)
	}
	if trace.MatchedTier != "standard" {
		t.Errorf("tier = %q, want standard", trace.MatchedTier)
	}
}

func TestLen_LongContextTier(t *testing.T) {
	// p is low (cache subtracted), but len is high (full context)
	params := billingexpr.TokenParams{P: 50000, C: 5000, Len: 300000, CR: 250000}
	cost, trace, err := billingexpr.RunExpr(lenTieredExpr, params)
	if err != nil {
		t.Fatal(err)
	}
	want := 50000*6 + 5000*22.5 + 250000*0.6
	if math.Abs(cost-want) > 1e-6 {
		t.Errorf("cost = %f, want %f", cost, want)
	}
	if trace.MatchedTier != "long_context" {
		t.Errorf("tier = %q, want long_context (len=300000 > 200000)", trace.MatchedTier)
	}
}

func TestLen_BoundaryExact(t *testing.T) {
	params := billingexpr.TokenParams{P: 100000, C: 1000, Len: 200000, CR: 100000}
	_, trace, err := billingexpr.RunExpr(lenTieredExpr, params)
	if err != nil {
		t.Fatal(err)
	}
	if trace.MatchedTier != "standard" {
		t.Errorf("tier = %q, want standard (len=200000 <= 200000)", trace.MatchedTier)
	}
}

func TestLen_BoundaryPlusOne(t *testing.T) {
	params := billingexpr.TokenParams{P: 100000, C: 1000, Len: 200001, CR: 100001}
	_, trace, err := billingexpr.RunExpr(lenTieredExpr, params)
	if err != nil {
		t.Fatal(err)
	}
	if trace.MatchedTier != "long_context" {
		t.Errorf("tier = %q, want long_context (len=200001 > 200000)", trace.MatchedTier)
	}
}

func TestLen_ZeroDefaultsToZero(t *testing.T) {
	// len defaults to 0 when not set
	params := billingexpr.TokenParams{P: 1000, C: 500}
	_, trace, err := billingexpr.RunExpr(lenTieredExpr, params)
	if err != nil {
		t.Fatal(err)
	}
	if trace.MatchedTier != "standard" {
		t.Errorf("tier = %q, want standard (len=0 <= 200000)", trace.MatchedTier)
	}
}

// ---------------------------------------------------------------------------
// Benchmarks: compile vs cached execution
// ---------------------------------------------------------------------------

const benchComplexExpr = `len <= 200000 ? tier("standard", p * 3 + c * 15 + cr * 0.3 + cc * 3.75 + cc1h * 6 + img * 3 + img_o * 30 + ai * 10 + ao * 40) : tier("long_context", p * 6 + c * 22.5 + cr * 0.6 + cc * 7.5 + cc1h * 12 + img * 6 + img_o * 60 + ai * 20 + ao * 80)`

func BenchmarkExprCompile(b *testing.B) {
	for i := 0; i < b.N; i++ {
		billingexpr.InvalidateCache()
		billingexpr.CompileFromCache(benchComplexExpr)
	}
}

func BenchmarkExprRunCached(b *testing.B) {
	billingexpr.CompileFromCache(benchComplexExpr)
	params := billingexpr.TokenParams{P: 150000, C: 10000, Len: 188000, CR: 30000, CC: 5000, Img: 2000, AI: 1000, AO: 500}
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		billingexpr.RunExpr(benchComplexExpr, params)
	}
}

// Shared fixtures protect the frontend simulator against drift from the real engine.
func TestFrontendSimulationContract(t *testing.T) {
	data, err := os.ReadFile("testdata/frontend_simulation.json")
	require.NoError(t, err)
	var cases []struct {
		Name        string
		Expression  string
		Tokens      billingexpr.TokenParams
		Body        map[string]any
		Headers     map[string]string
		Usage       map[string]any
		Cost        float64
		Tier        string
		Multipliers []float64
		Matched     []bool
		BillingUnit billingexpr.BillingUnit
		FixedPrice  *float64
		ImageCount  *int
	}
	require.NoError(t, common.Unmarshal(data, &cases))
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			body, err := common.Marshal(tc.Body)
			require.NoError(t, err)
			cost, trace, err := billingexpr.RunExprWithRequest(tc.Expression, tc.Tokens, billingexpr.RequestInput{Body: body, Headers: tc.Headers, Usage: tc.Usage, ImageCount: tc.ImageCount})
			require.NoError(t, err)
			assert.InDelta(t, tc.Cost, cost, 1e-9)
			assert.Equal(t, tc.Tier, trace.MatchedTier)
			if tc.ImageCount != nil {
				assert.Equal(t, tc.ImageCount, trace.ImageCount)
			}
			if tc.BillingUnit != "" {
				assert.Equal(t, tc.BillingUnit, trace.BillingUnit)
				assert.Equal(t, tc.FixedPrice, trace.FixedPrice)
			}
			require.Len(t, trace.RequestRules, len(tc.Multipliers))
			for i, rule := range trace.RequestRules {
				assert.Equal(t, tc.Multipliers[i], rule.Multiplier)
				assert.Equal(t, tc.Matched[i], rule.Matched)
			}
		})
	}
}
