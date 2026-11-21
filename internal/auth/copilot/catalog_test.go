package copilot

import (
	"slices"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
)

func TestResolveAccountEndpoint(t *testing.T) {
	for _, tc := range []struct {
		name, entry, want string
	}{
		{"known model account overrides preference", `{"id":"claude-fable-5.1","supported_endpoints":["/responses"]}`, Responses},
		{"local preference allowed", `{"id":"claude-fable-5.1","supported_endpoints":["/responses","/v1/messages"]}`, Messages},
		{"known model field absent", `{"id":"claude-fable-5.1"}`, Messages},
		{"explicit empty", `{"id":"claude-fable-5.1","supported_endpoints":[]}`, ""},
		{"unknown model with supported endpoints", `{"id":"new","supported_endpoints":["/chat/completions","/v1/messages","/responses"]}`, Responses},
		{"unknown model without endpoints", `{"id":"new"}`, ""},
		{"unsupported protocol", `{"id":"claude-fable-5.1","supported_endpoints":["ws:/responses","/interactions"]}`, ""},
		{"not authorized", `{"id":"claude-fable-5.1","policy":{"state":"disabled"}}`, ""},
		{"hidden despite enabled policy", `{"id":"claude-opus-5","model_picker_enabled":false,"policy":{"state":"enabled"}}`, ""},
		{"automatic selection is not a concrete model", `{"id":"auto","supported_endpoints":["/responses"]}`, ""},
		{"unconfigured", `{"id":"claude-fable-5.1","policy":{"state":"unconfigured"}}`, ""},
		{"MAI Code 2 prefers Responses", `{"id":"mai-code-2","supported_endpoints":["/chat/completions","/responses"],"policy":{"state":"enabled"}}`, Responses},
		{"MAI Code 2 Flash falls back to local profile", `{"id":"mai-code-2-flash","policy":{"state":"enabled"}}`, Responses},
		{"MAI Code 2 Flash obeys advertised Chat endpoint", `{"id":"mai-code-2-flash","supported_endpoints":["/chat/completions"]}`, Chat},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var model Model
			if err := json.Unmarshal([]byte(tc.entry), &model); err != nil {
				t.Fatal(err)
			}
			now := time.Now()
			encoded, err := json.Marshal(Catalog{FetchedAt: now, Models: []Model{model}})
			if err != nil {
				t.Fatal(err)
			}
			route, ok := ReadCatalog(string(encoded), now).Route(model.ID)
			if got := route.Endpoint; got != tc.want || ok != (tc.want != "") {
				t.Fatalf("route = %q, %v, want %q", got, ok, tc.want)
			}
			if _, okFuture := ReadCatalog(string(encoded), now.Add(-time.Second)).Route(model.ID); okFuture {
				t.Fatal("future catalog produced a route")
			}
			if _, okExpired := ReadCatalog(string(encoded), now.Add(CatalogMaxAge)).Route(model.ID); okExpired {
				t.Fatal("expired catalog produced a route")
			}
		})
	}
}

func TestOfficialCLIEffortFallbacks(t *testing.T) {
	for _, tc := range []struct {
		id, reasoning string
		want          []string
	}{
		{"gemini-4-argon", "", []string{"low", "medium", "high"}},
		{"kimi-k3", "", []string{"low", "high", "max"}},
		{"gpt-5.6-luna", "", []string{"none", "low", "medium", "high", "xhigh", "max"}},
		{"gpt-5.6-sol", "", []string{"none", "low", "medium", "high", "xhigh", "max"}},
		{"mai-code-2-flash", `["medium"]`, []string{"medium"}},
	} {
		t.Run(tc.id, func(t *testing.T) {
			model := Model{ID: tc.id}
			model.Capabilities.Supports.Reasoning = json.RawMessage(tc.reasoning)
			route, ok := model.route()
			if !ok || route.Info.Thinking == nil || !slices.Equal(route.Info.Thinking.Levels, tc.want) {
				t.Fatalf("unexpected route capabilities: %+v", route.Info)
			}
		})
	}
}

func TestCopilotCLIDefaultsRespectAccountCapabilities(t *testing.T) {
	m := Model{ID: "grok-4.7", DefaultEffort: "low"}
	m.Capabilities.Supports.Reasoning = json.RawMessage(`["low","high"]`)
	if r, _ := m.route(); r.DefaultEffort != "low" {
		t.Errorf("account default lost: %q", r.DefaultEffort)
	}
	m.DefaultEffort = ""
	m.Capabilities.Supports.Reasoning = json.RawMessage(`["low"]`)
	if r, _ := m.route(); r.DefaultEffort != "" {
		t.Errorf("unsupported CLI effort seeded: %q", r.DefaultEffort)
	}
}

func TestTokenTrustedEndpoint(t *testing.T) {
	var token Token
	token.Value = "proxy-ep=proxy.individual.githubcopilot.com"
	if base, err := token.API(); err != nil || base != defaultAPI {
		t.Fatalf("API = %q, %v", base, err)
	}
	token.Endpoints.API = "https://api.business.githubcopilot.com/"
	if base, err := token.API(); err != nil || base != "https://api.business.githubcopilot.com" {
		t.Fatalf("API = %q, %v", base, err)
	}
	for _, bad := range []string{"http://api.githubcopilot.com", "https://api.githubcopilot.com.evil.test", "https://api.githubcopilot.com@evil.test", "https://api.githubcopilot.com:443", "https://api.githubcopilot.com/path"} {
		token.Endpoints.API = bad
		if _, err := token.API(); err == nil {
			t.Fatalf("accepted endpoint %q", bad)
		}
	}
}
