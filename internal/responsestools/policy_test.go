package responsestools

import (
	"testing"
)

func validRoute() RoutePolicy {
	return RoutePolicy{
		Match:         RouteMatch{Provider: "codex", AuthKind: "api-key", UpstreamModel: "m-x", UpstreamFormat: "codex", BaseURL: "https://example.invalid/v1/"},
		ClientSearch:  ClientSearchBridge,
		CustomTools:   CustomToolsFunction,
		CustomGrammar: CustomGrammarDescribe,
		Schema:        SchemaPolicy{SearchRequired: SearchRequiredComplete, LocalRefs: LocalRefsInline},
	}
}

func TestCompilePolicyValid(t *testing.T) {
	policy := Policy{Enabled: true, Limits: DefaultLimits(), Routes: []RoutePolicy{validRoute()}}
	compiled, err := CompilePolicy(policy)
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	if len(compiled.Routes) != 1 {
		t.Fatalf("routes lost")
	}
}

func TestConventionPolicyDerivesClientSearchFromUpstreamFormat(t *testing.T) {
	cases := []struct {
		format string
		want   ClientSearchMode
	}{
		{"codex", ClientSearchNative},
		{"openai-response", ClientSearchNative},
		{"OpenAI-Response", ClientSearchNative},
		{" openai-response ", ClientSearchNative},
		{"openai", ClientSearchBridge},
		{"gemini", ClientSearchBridge},
		{"claude", ClientSearchBridge},
		{"", ClientSearchBridge},
	}
	for _, testCase := range cases {
		got := ConventionPolicy(Route{Provider: "codex", UpstreamFormat: testCase.format})
		if got.ClientSearch != testCase.want {
			t.Errorf("format %q: client-search = %q, want %q", testCase.format, got.ClientSearch, testCase.want)
		}
		if got.CustomTools != CustomToolsInherit {
			t.Errorf("format %q: convention must never infer custom handling, got %q", testCase.format, got.CustomTools)
		}
		if got.CustomGrammar != CustomGrammarReject {
			t.Errorf("format %q: convention must never degrade grammar, got %q", testCase.format, got.CustomGrammar)
		}
		if got.Schema.LocalRefs != LocalRefsPreserve || got.Schema.CompletesSearchSchemas() {
			t.Errorf("format %q: convention must keep schema handling inert, got %+v", testCase.format, got.Schema)
		}
	}
}

// The xAI executor deletes the client-executed tool_search built-in before
// sending, so a native Responses wire format is not sufficient evidence that
// native passthrough works. Only providers that forward the built-in keep the
// native convention; everything else falls back to the bridge.
func TestConventionPolicyKeepsSearchBridgeForProvidersThatRewriteToolSearch(t *testing.T) {
	cases := []struct {
		provider string
		format   string
		want     ClientSearchMode
	}{
		{"codex", "codex", ClientSearchNative},
		{"meta", "codex", ClientSearchNative},
		{"xai", "codex", ClientSearchBridge},
		{"XAI", "codex", ClientSearchBridge},
		{"xai", "openai-response", ClientSearchBridge},
		{"", "codex", ClientSearchBridge},
	}
	for _, testCase := range cases {
		got := ConventionPolicy(Route{Provider: testCase.provider, UpstreamFormat: testCase.format})
		if got.ClientSearch != testCase.want {
			t.Errorf("provider %q format %q: client-search = %q, want %q", testCase.provider, testCase.format, got.ClientSearch, testCase.want)
		}
	}
}

func TestEffectivePolicyUsesConventionWhenNoRuleMatches(t *testing.T) {
	policy := Policy{Enabled: true, Limits: DefaultLimits()}
	route := Route{Provider: "gemini", AuthKind: "api-key", UpstreamModel: "m", UpstreamFormat: "gemini", BaseURL: "https://example.invalid/"}
	got, err := EffectivePolicy(policy, route)
	if err != nil {
		t.Fatalf("effective policy: %v", err)
	}
	if got.ClientSearch != ClientSearchBridge {
		t.Fatalf("unmatched route must follow the convention, got %q", got.ClientSearch)
	}
}

func TestEffectivePolicyResolvesInheritToConvention(t *testing.T) {
	route := Route{Provider: "gemini", AuthKind: "api-key", UpstreamModel: "m", UpstreamFormat: "gemini", BaseURL: "https://example.invalid/"}
	rule := RoutePolicy{
		Match:         RouteMatch{Provider: "gemini", AuthKind: "api-key", UpstreamModel: "m", UpstreamFormat: "gemini", BaseURL: "https://example.invalid/"},
		ClientSearch:  ClientSearchInherit,
		CustomTools:   CustomToolsInherit,
		CustomGrammar: CustomGrammarReject,
		Schema:        SchemaPolicy{LocalRefs: LocalRefsPreserve},
	}
	got, err := EffectivePolicy(Policy{Enabled: true, Limits: DefaultLimits(), Routes: []RoutePolicy{rule}}, route)
	if err != nil {
		t.Fatalf("effective policy: %v", err)
	}
	if got.ClientSearch != ClientSearchBridge {
		t.Fatalf("inherit must resolve to the convention, got %q", got.ClientSearch)
	}
}

func TestEffectivePolicyKeepsExplicitStrategies(t *testing.T) {
	route := Route{Provider: "codex", AuthKind: "oauth", UpstreamModel: "m", UpstreamFormat: "codex"}
	rule := RoutePolicy{
		Match:         RouteMatch{Provider: "codex", AuthKind: "oauth", UpstreamModel: "m", UpstreamFormat: "codex"},
		ClientSearch:  ClientSearchDisabled,
		CustomTools:   CustomToolsStrip,
		CustomGrammar: CustomGrammarDescribe,
		Schema:        SchemaPolicy{LocalRefs: LocalRefsInline},
	}
	got, err := EffectivePolicy(Policy{Enabled: true, Limits: DefaultLimits(), Routes: []RoutePolicy{rule}}, route)
	if err != nil {
		t.Fatalf("effective policy: %v", err)
	}
	if got.ClientSearch != ClientSearchDisabled || got.CustomTools != CustomToolsStrip ||
		got.CustomGrammar != CustomGrammarDescribe || got.Schema.LocalRefs != LocalRefsInline {
		t.Fatalf("explicit strategies must win over the convention: %+v", got)
	}
}

func TestEffectivePolicySurfacesConflict(t *testing.T) {
	policy := Policy{
		Enabled: true,
		Limits:  DefaultLimits(),
		Routes: []RoutePolicy{
			{Match: RouteMatch{Provider: "codex", AuthKind: "oauth", UpstreamModel: "m", UpstreamFormat: "codex"}, ClientSearch: ClientSearchBridge},
			{Match: RouteMatch{Provider: "codex", AuthKind: "oauth", UpstreamModel: "m", UpstreamFormat: "codex"}, ClientSearch: ClientSearchDisabled},
		},
	}
	if _, err := EffectivePolicy(policy, Route{Provider: "codex", AuthKind: "oauth", UpstreamModel: "m", UpstreamFormat: "codex"}); err == nil {
		t.Fatalf("conflicting rules must surface as an error")
	}
}

func TestCompilePolicyRejectsUnknownEnum(t *testing.T) {
	route := validRoute()
	route.ClientSearch = "sometimes"
	if _, err := CompilePolicy(Policy{Limits: DefaultLimits(), Routes: []RoutePolicy{route}}); err == nil {
		t.Fatalf("expected enum rejection")
	}
}

func TestCompilePolicyRequiresAPIKeyBaseURL(t *testing.T) {
	route := validRoute()
	route.Match.BaseURL = ""
	if _, err := CompilePolicy(Policy{Limits: DefaultLimits(), Routes: []RoutePolicy{route}}); err == nil {
		t.Fatalf("expected base-url requirement")
	}
}

func TestCompilePolicyRejectsOverlappingConflict(t *testing.T) {
	first := validRoute()
	second := validRoute()
	second.CustomTools = CustomToolsStrip
	if _, err := CompilePolicy(Policy{Limits: DefaultLimits(), Routes: []RoutePolicy{first, second}}); err == nil {
		t.Fatalf("expected overlap rejection")
	}
	duplicate := validRoute()
	if _, err := CompilePolicy(Policy{Limits: DefaultLimits(), Routes: []RoutePolicy{first, duplicate}}); err != nil {
		t.Fatalf("identical duplicates must compile: %v", err)
	}
}

func TestResolvePolicyEndpointIsolation(t *testing.T) {
	route := validRoute()
	policy := Policy{Enabled: true, Limits: DefaultLimits(), Routes: []RoutePolicy{route}}
	call := Route{Provider: "codex", AuthKind: "api-key", UpstreamModel: "m-x", UpstreamFormat: "codex", BaseURL: "https://other.invalid/v1"}
	if _, matched, err := ResolvePolicy(policy, call); err != nil || matched {
		t.Fatalf("different endpoint must not match: %v %v", matched, err)
	}
	same := Route{Provider: "codex", AuthKind: "api-key", UpstreamModel: "m-x", UpstreamFormat: "codex", BaseURL: "https://example.invalid/v1/"}
	if _, matched, err := ResolvePolicy(policy, same); err != nil || !matched {
		t.Fatalf("same endpoint must match: %v %v", matched, err)
	}
}

func TestNormalizeBaseURL(t *testing.T) {
	if NormalizeBaseURL("HTTPS://Example.INVALID:443/v1/") != "https://example.invalid/v1" {
		t.Fatalf("normalize wrong")
	}
	if NormalizeBaseURL("https://example.invalid/V1/Path") != "https://example.invalid/V1/Path" {
		t.Fatalf("path case must survive")
	}
}

func TestConventionPolicyFoldsToolsForProvidersWithoutPortableSurface(t *testing.T) {
	portable := ConventionPolicy(Route{Provider: "meta", UpstreamFormat: "codex"})
	if portable.ClientSearch != ClientSearchNative {
		t.Errorf("meta client-search = %q, want %q", portable.ClientSearch, ClientSearchNative)
	}
	if portable.CustomTools != CustomToolsFunction || portable.CustomGrammar != CustomGrammarDescribe {
		t.Errorf("meta tools = %q/%q, want %q/%q", portable.CustomTools, portable.CustomGrammar, CustomToolsFunction, CustomGrammarDescribe)
	}
	if !portable.Schema.CompletesSearchSchemas() {
		t.Error("meta schema completion not enabled")
	}
	if portable.Schema.LocalRefs != LocalRefsFlatten {
		t.Errorf("meta local-refs = %q, want %q", portable.Schema.LocalRefs, LocalRefsFlatten)
	}
	native := ConventionPolicy(Route{Provider: "codex", UpstreamFormat: "codex"})
	if native.CustomTools != CustomToolsInherit || native.CustomGrammar != CustomGrammarReject || native.Schema.CompletesSearchSchemas() ||
		native.Schema.LocalRefs != LocalRefsPreserve {
		t.Errorf("codex tools = %q/%q complete=%v local-refs=%q, want the native surface",
			native.CustomTools, native.CustomGrammar, native.Schema.CompletesSearchSchemas(), native.Schema.LocalRefs)
	}
}

// Each schema strategy inherits on its own, so stating one no longer opts the
// route out of the convention for the other, while an explicit opt-out is kept.
func TestApplyConventionDefaultsResolvesEachSchemaStrategy(t *testing.T) {
	route := Route{Provider: "meta", UpstreamFormat: "codex"}
	inherited := applyConventionDefaults(RoutePolicy{}, route)
	if !inherited.Schema.CompletesSearchSchemas() {
		t.Error("empty rule did not inherit the convention schema")
	}
	partial := applyConventionDefaults(RoutePolicy{Schema: SchemaPolicy{LocalRefs: LocalRefsInline}}, route)
	if !partial.Schema.CompletesSearchSchemas() {
		t.Error("stating local-refs alone dropped the convention's search-schema strategy")
	}
	if partial.Schema.LocalRefs != LocalRefsInline {
		t.Errorf("local refs = %q, want %q", partial.Schema.LocalRefs, LocalRefsInline)
	}
	optedOut := applyConventionDefaults(RoutePolicy{Schema: SchemaPolicy{SearchRequired: SearchRequiredPreserve}}, route)
	if optedOut.Schema.CompletesSearchSchemas() {
		t.Error("an explicit opt-out was overridden by the convention")
	}
}

func musePortableRoute() (Route, RouteMatch) {
	match := RouteMatch{
		Provider:       "meta",
		AuthKind:       "api-key",
		UpstreamModel:  "muse-spark-1.3-contributor",
		UpstreamFormat: "codex",
		BaseURL:        "https://example.invalid/v1",
	}
	return Route{
		Provider:       match.Provider,
		AuthKind:       match.AuthKind,
		UpstreamModel:  match.UpstreamModel,
		UpstreamFormat: match.UpstreamFormat,
		BaseURL:        match.BaseURL,
	}, match
}

// A rule that only overrides one strategy must not have to restate the rest.
// Rejecting the omitted ones made such a rule fail compilation, which silenced
// the whole feature for the route instead of applying the convention.
func TestValidateRoutePolicyAcceptsOmittedStrategies(t *testing.T) {
	_, match := musePortableRoute()
	if err := ValidateRoutePolicy(RoutePolicy{Match: match, ClientSearch: ClientSearchBridge}); err != nil {
		t.Fatalf("route with omitted strategies rejected: %v", err)
	}
	for _, invalid := range []RoutePolicy{
		{Match: match, ClientSearch: "nope"},
		{Match: match, CustomTools: "nope"},
		{Match: match, CustomGrammar: "nope"},
		{Match: match, Schema: SchemaPolicy{LocalRefs: "nope"}},
	} {
		if err := ValidateRoutePolicy(invalid); err == nil {
			t.Errorf("invalid strategy accepted: %+v", invalid)
		}
	}
}

func TestEffectivePolicyInheritsOmittedStrategiesFromConvention(t *testing.T) {
	route, match := musePortableRoute()
	compiled, err := CompilePolicy(Policy{
		Limits: DefaultLimits(),
		Routes: []RoutePolicy{{Match: match, ClientSearch: ClientSearchBridge}},
	})
	if err != nil {
		t.Fatalf("compile: %v", err)
	}
	effective, err := EffectivePolicy(compiled, route)
	if err != nil {
		t.Fatalf("effective policy: %v", err)
	}
	if effective.ClientSearch != ClientSearchBridge {
		t.Errorf("client-search = %q, want the rule's %q", effective.ClientSearch, ClientSearchBridge)
	}
	if effective.CustomTools != CustomToolsFunction || effective.CustomGrammar != CustomGrammarDescribe {
		t.Errorf("custom handling = %q/%q, want the convention's %q/%q",
			effective.CustomTools, effective.CustomGrammar, CustomToolsFunction, CustomGrammarDescribe)
	}
	if !effective.Schema.CompletesSearchSchemas() {
		t.Error("schema completion was not inherited from the convention")
	}
}
