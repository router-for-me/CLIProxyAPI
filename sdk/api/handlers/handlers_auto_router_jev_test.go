package handlers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/autorouter/jevclient"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

func TestEffectiveJevConfigRequiresAllThreeSwitches(t *testing.T) {
	base := autorouter.Config{JevEnabled: true, JevMinConfidence: 0.6, JevTimeoutMs: 250}

	got := effectiveJevConfig(true, true, base)
	if !got.Enabled() {
		t.Fatal("all three switches on must enable the gate")
	}
	if got.MinConfidence != 0.6 {
		t.Errorf("min confidence = %v, want 0.6", got.MinConfidence)
	}
	if got.Timeout != 250*time.Millisecond {
		t.Errorf("timeout = %v, want 250ms", got.Timeout)
	}

	if effectiveJevConfig(false, true, base).Enabled() {
		t.Error("global off must disable the gate")
	}
	if effectiveJevConfig(true, false, base).Enabled() {
		t.Error("missing API key must disable the gate")
	}
	if effectiveJevConfig(true, true, autorouter.Config{JevEnabled: false}).Enabled() {
		t.Error("router off must disable the gate")
	}
}

func TestEffectiveJevConfigAppliesDefaults(t *testing.T) {
	got := effectiveJevConfig(true, true, autorouter.Config{JevEnabled: true})
	if got.MinConfidence != jevDefaultMinConfidence {
		t.Errorf("min confidence = %v, want %v", got.MinConfidence, jevDefaultMinConfidence)
	}
	if got.Timeout != jevDefaultTimeout {
		t.Errorf("timeout = %v, want %v", got.Timeout, jevDefaultTimeout)
	}
	if got.Model == "" {
		t.Error("model must fall back to the global default")
	}
	if got.Model != jevDefaultModel {
		t.Errorf("model = %q, want %q", got.Model, jevDefaultModel)
	}
}

func TestEffectiveJevConfigPrefersRouterModelOverride(t *testing.T) {
	got := effectiveJevConfig(true, true, autorouter.Config{JevEnabled: true, JevModelOverride: "  jev-preview  "})
	if got.Model != "jev-preview" {
		t.Errorf("model = %q, want the trimmed router override", got.Model)
	}
}

// TestJevDisabledIsIdenticalToHeuristic is the regression guard for the
// fail-identical invariant: with the gate off, the routing outcome and the
// persisted decision must be exactly what the heuristic produced, with no jev
// block attached.
func TestJevDisabledIsIdenticalToHeuristic(t *testing.T) {
	router := &autorouter.Config{
		ID: "r1", ModelID: "router:r1", Enabled: true,
		JevEnabled: false,
		Mappings:   []autorouter.TierMapping{{Tier: autorouter.TierSimple, Model: "cheap"}, {Tier: autorouter.TierComplex, Model: "strong"}},
	}
	result := autorouter.ScoreResult{EffectiveTier: autorouter.TierComplex, DecisionCause: autorouter.DecisionCauseComplexityScorer}
	jevCfg := effectiveJevConfig(true, true, *router)
	if jevCfg.Enabled() {
		t.Fatal("precondition: the gate must be off for a router that did not opt in")
	}

	h := &BaseAPIHandler{}
	tier, cause, jevInfo := h.applyJevDecision(nil, jevCfg, []byte(`{"messages":[{"role":"user","content":"hi"}]}`), "openai", "r1", result)
	if tier != result.EffectiveTier {
		t.Errorf("tier = %q, want %q (heuristic must pass through)", tier, result.EffectiveTier)
	}
	if cause != result.DecisionCause {
		t.Errorf("cause = %q, want %q", cause, result.DecisionCause)
	}
	if jevInfo != nil {
		t.Errorf("jevInfo = %+v, want nil when the gate is off", jevInfo)
	}
}

// A router with the gate on but no global master switch must behave exactly
// like a router with the gate off.
func TestJevGlobalOffIsIdenticalToHeuristic(t *testing.T) {
	router := &autorouter.Config{ID: "r1", ModelID: "router:r1", Enabled: true, JevEnabled: true}
	jevCfg := effectiveJevConfig(false, true, *router)
	if jevCfg.Enabled() {
		t.Fatal("precondition: global off must disable the gate")
	}
	result := autorouter.ScoreResult{EffectiveTier: autorouter.TierMedium, DecisionCause: autorouter.DecisionCauseComplexityScorer}

	h := &BaseAPIHandler{}
	tier, cause, jevInfo := h.applyJevDecision(nil, jevCfg, []byte(`{}`), "openai", "r1", result)
	if tier != result.EffectiveTier || cause != result.DecisionCause || jevInfo != nil {
		t.Errorf("got (%q, %q, %v), want the heuristic passthrough", tier, cause, jevInfo)
	}
}

// With the gate enabled but no gate wired (gate == nil), the decision path must
// still pass the heuristic through rather than panicking.
func TestApplyJevDecisionNilGateIsSafe(t *testing.T) {
	router := &autorouter.Config{ID: "r1", ModelID: "router:r1", Enabled: true, JevEnabled: true}
	jevCfg := effectiveJevConfig(true, true, *router)
	result := autorouter.ScoreResult{EffectiveTier: autorouter.TierSimple, DecisionCause: autorouter.DecisionCauseComplexityScorer}

	h := &BaseAPIHandler{}
	tier, cause, jevInfo := h.applyJevDecision(nil, jevCfg, []byte(`{}`), "openai", "r1", result)
	if tier != result.EffectiveTier || cause != result.DecisionCause || jevInfo != nil {
		t.Errorf("unwired gate must be a passthrough, got (%q, %q, %v)", tier, cause, jevInfo)
	}
}

func TestValidRouterTier(t *testing.T) {
	for _, tier := range autorouter.TierOrder {
		if !validRouterTier(tier) {
			t.Errorf("%q must be a valid classifier tier", tier)
		}
	}
	for _, bad := range []autorouter.Tier{"", "Simple", "turbo", "reasoning "} {
		if validRouterTier(bad) {
			t.Errorf("%q must not be a valid classifier tier", bad)
		}
	}
}

// stubJevProvider is a scripted JevSettingsProvider.
type stubJevProvider struct {
	enabled   bool
	apiKeySet bool
	calls     int
}

func (s *stubJevProvider) JevSettings(_ context.Context) (bool, bool) {
	s.calls++
	return s.enabled, s.apiKeySet
}

func TestEffectiveJevConfigForUsesProvider(t *testing.T) {
	p := &stubJevProvider{enabled: true, apiKeySet: true}
	h := &BaseAPIHandler{JevSettingsProvider: p}
	routerCfg := &autorouter.Config{JevEnabled: true}

	got := h.effectiveJevConfigFor(context.Background(), routerCfg)
	if !got.Enabled() {
		t.Fatal("provider switches plus router opt-in must enable the gate")
	}
	if p.calls != 1 {
		t.Errorf("provider calls = %d, want 1", p.calls)
	}
}

func TestEffectiveJevConfigForNilProviderDisables(t *testing.T) {
	h := &BaseAPIHandler{}
	got := h.effectiveJevConfigFor(context.Background(), &autorouter.Config{JevEnabled: true})
	if got.Enabled() {
		t.Error("a nil provider must leave the gate disabled")
	}
}

func TestSetJevSettingsProviderNilSafe(t *testing.T) {
	var h *BaseAPIHandler
	h.SetJevSettingsProvider(&stubJevProvider{}) // must not panic
}

func TestSetJevGateNilDetaches(t *testing.T) {
	SetJevGate(nil)
	if jevGate != nil {
		t.Error("SetJevGate(nil) must detach the gate")
	}
}

// stubJevCaller scripts classifier outcomes for the end-to-end gate tests.
type stubJevCaller struct {
	resp jevclient.Response
	err  error
}

func (s stubJevCaller) Call(_ context.Context, _ string, _ any, _ map[string]jevclient.Question) (jevclient.Response, error) {
	return s.resp, s.err
}

func jevChoiceResponse(choice string, confidence float64) jevclient.Response {
	return jevclient.Response{
		Model: "jev-1.13.0",
		Answers: map[string]jevclient.ChoiceAnswer{
			"tier": {Type: "choice", Choice: choice, Confidence: confidence,
				Probabilities: map[string]float64{choice: confidence}},
		},
		Usage: jevclient.Usage{InputTokens: 120},
	}
}

// jevTestRouter maps every tier to a distinct model so an override is visible
// in the resolved target.
func jevTestRouter() *store.AutoRouter {
	return &store.AutoRouter{
		ID: "pk-jev", ModelID: "router:jev", Enabled: true, JevEnabled: true,
		Mappings: []store.TierMapping{
			{Tier: "simple", Model: "cheap-model"},
			{Tier: "medium", Model: "mid-model"},
			{Tier: "complex", Model: "strong-model"},
			{Tier: "reasoning", Model: "reason-model"},
		},
	}
}

// jevRouterHandler builds a handler on the production (compiled-profile) code
// path, which is the only path the gate runs on.
func jevRouterHandler(router *store.AutoRouter, provider JevSettingsProvider) *BaseAPIHandler {
	resolver := stubCachedConfigResolver{router: router}
	return &BaseAPIHandler{
		AutoRouterResolver:        resolver,
		AutoRouterProfileResolver: resolver,
		JevSettingsProvider:       provider,
	}
}

const jevProbeBody = `{"model":"router:jev","messages":[{"role":"user","content":"hi"}]}`

func TestJevGateOverridesHeuristicTier(t *testing.T) {
	SetJevGate(stubJevCaller{resp: jevChoiceResponse("reasoning", 0.93)})
	defer SetJevGate(nil)

	h := jevRouterHandler(jevTestRouter(), &stubJevProvider{enabled: true, apiKeySet: true})
	got := h.resolveAutoRouterModel(context.Background(), "openai", "router:jev", []byte(jevProbeBody))

	if got.tier != "reasoning" {
		t.Errorf("tier = %q, want reasoning (classifier must override)", got.tier)
	}
	if got.targetModel != "reason-model" {
		t.Errorf("target = %q, want reason-model", got.targetModel)
	}
	if got.decision.DecisionCause != autorouter.DecisionCauseJevClassifier {
		t.Errorf("cause = %q, want %q", got.decision.DecisionCause, autorouter.DecisionCauseJevClassifier)
	}
	if got.decision.Jev == nil {
		t.Fatal("jev block must be persisted")
	}
	if got.decision.Jev.Confidence != 0.93 || got.decision.Jev.Choice != "reasoning" {
		t.Errorf("jev block = %+v", got.decision.Jev)
	}
	if got.decision.Jev.InputTokens != 120 {
		t.Errorf("input tokens = %d, want 120", got.decision.Jev.InputTokens)
	}
	// The heuristic tier must survive for comparison against the override.
	if got.decision.ScoredTier == "" {
		t.Error("scored tier must be preserved alongside the override")
	}
	if got.decision.EffectiveTier != "reasoning" {
		t.Errorf("effective tier = %q, want the classifier's pick", got.decision.EffectiveTier)
	}
}

func TestJevGateLowConfidenceKeepsHeuristicTier(t *testing.T) {
	SetJevGate(stubJevCaller{resp: jevChoiceResponse("reasoning", 0.20)})
	defer SetJevGate(nil)

	h := jevRouterHandler(jevTestRouter(), &stubJevProvider{enabled: true, apiKeySet: true})
	got := h.resolveAutoRouterModel(context.Background(), "openai", "router:jev", []byte(jevProbeBody))

	if got.decision.DecisionCause != autorouter.DecisionCauseJevLowConfidence {
		t.Errorf("cause = %q, want %q", got.decision.DecisionCause, autorouter.DecisionCauseJevLowConfidence)
	}
	if got.decision.Jev == nil {
		t.Fatal("a low-confidence verdict must still be recorded for tuning")
	}
	if got.decision.Jev.Confidence != 0.20 {
		t.Errorf("recorded confidence = %v, want 0.20", got.decision.Jev.Confidence)
	}
	// The heuristic tier must be the one that routes.
	if got.tier == "reasoning" {
		t.Error("a sub-threshold verdict must not override the tier")
	}
	if got.decision.EffectiveTier != got.decision.ScoredTier {
		t.Errorf("effective tier = %q, want the scored tier %q", got.decision.EffectiveTier, got.decision.ScoredTier)
	}
}

func TestJevGateClassifierErrorFallsBackToHeuristic(t *testing.T) {
	SetJevGate(stubJevCaller{err: errors.New("classifier unavailable")})
	defer SetJevGate(nil)

	h := jevRouterHandler(jevTestRouter(), &stubJevProvider{enabled: true, apiKeySet: true})
	got := h.resolveAutoRouterModel(context.Background(), "openai", "router:jev", []byte(jevProbeBody))

	if !got.matched {
		t.Fatal("an upstream classifier failure must never fail the request")
	}
	if got.decision.DecisionCause != autorouter.DecisionCauseJevFallback {
		t.Errorf("cause = %q, want %q", got.decision.DecisionCause, autorouter.DecisionCauseJevFallback)
	}
	if got.decision.EffectiveTier != got.decision.ScoredTier {
		t.Errorf("effective tier = %q, want the scored tier %q", got.decision.EffectiveTier, got.decision.ScoredTier)
	}
	if got.targetModel == "" {
		t.Error("the request must still resolve to a target model")
	}
}

// A classifier that answers with a choice outside the four tier keys is a
// malformed response and must fall back rather than reach the resolver.
func TestJevGateUnknownChoiceFallsBack(t *testing.T) {
	SetJevGate(stubJevCaller{resp: jevChoiceResponse("turbo", 0.99)})
	defer SetJevGate(nil)

	h := jevRouterHandler(jevTestRouter(), &stubJevProvider{enabled: true, apiKeySet: true})
	got := h.resolveAutoRouterModel(context.Background(), "openai", "router:jev", []byte(jevProbeBody))

	if !got.matched || got.resolveFailed {
		t.Fatalf("an unknown choice must fall back, got %+v", got)
	}
	if got.decision.DecisionCause != autorouter.DecisionCauseJevFallback {
		t.Errorf("cause = %q, want %q", got.decision.DecisionCause, autorouter.DecisionCauseJevFallback)
	}
	if autorouter.Tier(got.tier) != got.decision.ScoredTier {
		t.Errorf("tier = %q, want the scored tier %q", got.tier, got.decision.ScoredTier)
	}
}

// The gate must be consulted even when the score cache answers, so a warm cache
// cannot silently skip classification.
func TestJevGateRunsOnScoreCacheHit(t *testing.T) {
	SetJevGate(stubJevCaller{resp: jevChoiceResponse("complex", 0.88)})
	defer SetJevGate(nil)

	router := jevTestRouter()
	body := []byte(`{"model":"router:jev","messages":[{"role":"user","content":"cache this unique prompt"}]}`)
	h := jevRouterHandler(router, &stubJevProvider{enabled: true, apiKeySet: true})

	first := h.resolveAutoRouterModel(context.Background(), "openai", "router:jev", body)
	second := h.resolveAutoRouterModel(context.Background(), "openai", "router:jev", body)

	for label, res := range map[string]autoRouterResolved{"first": first, "second": second} {
		if res.tier != "complex" {
			t.Errorf("%s: tier = %q, want complex", label, res.tier)
		}
		if res.decision.DecisionCause != autorouter.DecisionCauseJevClassifier {
			t.Errorf("%s: cause = %q, want the classifier cause", label, res.decision.DecisionCause)
		}
	}
}

// With the global master switch off, a router that opted in must behave exactly
// like one that did not: heuristic tier, no jev block.
func TestJevGateGlobalOffKeepsHeuristic(t *testing.T) {
	calls := &countingJevCaller{resp: jevChoiceResponse("reasoning", 0.99)}
	SetJevGate(calls)
	defer SetJevGate(nil)

	h := jevRouterHandler(jevTestRouter(), &stubJevProvider{enabled: false, apiKeySet: true})
	got := h.resolveAutoRouterModel(context.Background(), "openai", "router:jev", []byte(jevProbeBody))

	if got.decision.Jev != nil {
		t.Error("the master switch off must leave no jev block")
	}
	if got.decision.DecisionCause == autorouter.DecisionCauseJevClassifier {
		t.Error("the master switch off must not use the classifier cause")
	}
	if calls.calls != 0 {
		t.Errorf("classifier calls = %d, want 0 when globally disabled", calls.calls)
	}
	if got.decision.EffectiveTier != got.decision.ScoredTier {
		t.Errorf("effective tier = %q, want the scored tier", got.decision.EffectiveTier)
	}
}

// countingJevCaller records how many times the classifier was invoked.
type countingJevCaller struct {
	resp  jevclient.Response
	calls int
}

func (c *countingJevCaller) Call(_ context.Context, _ string, _ any, _ map[string]jevclient.Question) (jevclient.Response, error) {
	c.calls++
	return c.resp, nil
}
