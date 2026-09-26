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
