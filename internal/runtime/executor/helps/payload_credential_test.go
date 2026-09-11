package helps

import (
	"net/http"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	"github.com/tidwall/gjson"
)

func TestPayloadCredentialSelectorsAcrossRuleKinds(t *testing.T) {
	auth := &cliproxyauth.Auth{ID: "selected", Index: "index-a", Prefix: "gateway-a"}
	for _, kind := range []string{"default", "default-raw", "override", "override-raw", "filter"} {
		for _, test := range []struct {
			name      string
			index     string
			prefix    string
			auth      *cliproxyauth.Auth
			from      string
			wantMatch bool
		}{
			{name: "index matches", index: "index-a", auth: auth, from: "openai", wantMatch: true},
			{name: "prefix matches", prefix: "gateway-a", auth: auth, from: "openai", wantMatch: true},
			{name: "selectors combine", index: "index-a", prefix: "gateway-a", auth: auth, from: "openai", wantMatch: true},
			{name: "both selectors required", index: "index-a", prefix: "gateway-b", auth: auth, from: "openai"},
			{name: "different credential", index: "index-b", auth: auth, from: "openai"},
			{name: "exact prefix", prefix: "gateway-*", auth: auth, from: "openai"},
			{name: "missing auth", index: "index-a", from: "openai"},
			{name: "source gate still applies", index: "index-a", auth: auth, from: "claude"},
			{name: "unscoped with auth", auth: auth, from: "openai", wantMatch: true},
			{name: "unscoped without auth", from: "openai", wantMatch: true},
		} {
			t.Run(kind+"/"+test.name, func(t *testing.T) {
				entry := config.PayloadModelRule{Name: "gpt-*", Protocol: "codex", FromProtocol: "openai", AuthIndex: test.index, Prefix: test.prefix, Headers: map[string]string{"X-Client-Tier": "premium"}}
				rule := config.PayloadRule{Models: []config.PayloadModelRule{entry}, Params: map[string]any{"target": map[string]any{"value": true}}}
				cfg := &config.Config{}
				switch kind {
				case "default":
					cfg.Payload.Default = []config.PayloadRule{rule}
				case "default-raw":
					rule.Params["target"] = `{"value":true}`
					cfg.Payload.DefaultRaw = []config.PayloadRule{rule}
				case "override":
					cfg.Payload.Override = []config.PayloadRule{rule}
				case "override-raw":
					rule.Params["target"] = `{"value":true}`
					cfg.Payload.OverrideRaw = []config.PayloadRule{rule}
				case "filter":
					cfg.Payload.Filter = []config.PayloadFilterRule{{Models: rule.Models, Params: []string{`tools.#(type=="tool_search")`}}}
				}
				payload := []byte(`{"tools":[{"type":"tool_search"},{"type":"function","name":"lookup"}],"keep":42}`)
				original := string(payload)
				headers := http.Header{"X-Client-Tier": {"premium"}, "X-Auth-Index": {"index-a"}}
				out, touched := ApplyPayloadConfigForAuthWithTrackedPaths(cfg, test.auth, "gpt-test", "codex", test.from, "", payload, payload, "", "", headers, "target", "tools")
				if kind == "filter" {
					wantCount := int64(2)
					if test.wantMatch {
						wantCount = 1
					}
					if got := gjson.GetBytes(out, "tools.#").Int(); got != wantCount {
						t.Errorf("tool count = %d, want %d; body=%s", got, wantCount, out)
					}
					if touched["tools"] != test.wantMatch {
						t.Errorf("tools touched = %t, want %t", touched["tools"], test.wantMatch)
					}
				} else {
					if got := gjson.GetBytes(out, "target.value").Bool(); got != test.wantMatch {
						t.Errorf("rule applied = %t, want %t; body=%s", got, test.wantMatch, out)
					}
					if touched["target"] != test.wantMatch {
						t.Errorf("target touched = %t, want %t", touched["target"], test.wantMatch)
					}
				}
				if string(payload) != original || gjson.GetBytes(out, "keep").Int() != 42 {
					t.Error("modified the original request or an unrelated field")
				}
			})
		}
	}
}

func TestPayloadCredentialSelectorsDoNotMutateAuthOrMatchWithoutIt(t *testing.T) {
	auth := &cliproxyauth.Auth{ID: "unregistered-sdk-auth"}
	indexed := *auth
	index := indexed.EnsureIndex()
	cfg := &config.Config{Payload: config.PayloadConfig{Override: []config.PayloadRule{{
		Models: []config.PayloadModelRule{{Name: "model", AuthIndex: index}},
		Params: map[string]any{"service_tier": "priority"},
	}}}}
	payload := []byte(`{"input":[]}`)
	out := ApplyPayloadConfigForAuth(cfg, auth, "model", "codex", "openai", "", payload, payload, "", "", nil)
	if gjson.GetBytes(out, "service_tier").String() != "priority" {
		t.Fatalf("SDK credential index was not resolved: %s", out)
	}
	if auth.Index != "" {
		t.Fatal("matching mutated the SDK caller's auth")
	}
	out = ApplyPayloadConfigWithRequest(cfg, "model", "codex", "openai", "", payload, payload, "", "", nil)
	if gjson.GetBytes(out, "service_tier").Exists() {
		t.Fatalf("legacy API without selected auth applied a credential-scoped rule: %s", out)
	}
}

func TestPayloadCredentialSelectorDoesNotConsumeDefaultForAnotherAuth(t *testing.T) {
	cfg := &config.Config{Payload: config.PayloadConfig{Default: []config.PayloadRule{
		{Models: []config.PayloadModelRule{{Name: "model", AuthIndex: "index-a"}}, Params: map[string]any{"service_tier": "priority"}},
		{Models: []config.PayloadModelRule{{Name: "model"}}, Params: map[string]any{"service_tier": "default"}},
	}}}
	payload := []byte(`{"input":[]}`)
	out := ApplyPayloadConfigForAuth(cfg, &cliproxyauth.Auth{Index: "index-b"}, "model", "codex", "openai", "", payload, payload, "", "", nil)
	if gjson.GetBytes(out, "service_tier").String() != "default" {
		t.Fatalf("nonmatching credential rule consumed the default: %s", out)
	}
}
