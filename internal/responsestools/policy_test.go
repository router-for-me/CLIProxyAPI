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
		Schema:        SchemaPolicy{CompleteSearchRequired: true, LocalRefs: LocalRefsInline},
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
		got := ConventionPolicy(Route{UpstreamFormat: testCase.format})
		if got.ClientSearch != testCase.want {
			t.Errorf("format %q: client-search = %q, want %q", testCase.format, got.ClientSearch, testCase.want)
		}
		if got.CustomTools != CustomToolsInherit {
			t.Errorf("format %q: convention must never infer custom handling, got %q", testCase.format, got.CustomTools)
		}
		if got.CustomGrammar != CustomGrammarReject {
			t.Errorf("format %q: convention must never degrade grammar, got %q", testCase.format, got.CustomGrammar)
		}
		if got.Schema.LocalRefs != LocalRefsPreserve || got.Schema.CompleteSearchRequired {
			t.Errorf("format %q: convention must keep schema handling inert, got %+v", testCase.format, got.Schema)
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
