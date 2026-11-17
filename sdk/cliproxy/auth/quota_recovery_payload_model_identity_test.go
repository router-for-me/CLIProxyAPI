package auth

import (
	"context"
	"reflect"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestQuotaRecoveryPayloadModelIdentity(t *testing.T) {
	for _, tc := range []struct {
		name                                                                  string
		raw, conditional                                                      bool
		headers                                                               map[string]string
		recover                                                               bool
		otherRoute, otherProtocol, claude, nonString, removeModel, nestedOnly bool
		wildcard, defaultOnly, emptyModel                                     bool
	}{
		{name: "override_bearer", headers: map[string]string{"Authorization": "Bearer token-b"}},
		{name: "override_account", headers: map[string]string{"Chatgpt-Account-Id": "account-b"}},
		{name: "raw_override_bearer", raw: true, headers: map[string]string{"Authorization": "Bearer token-b"}},
		{name: "conditional_override_bearer", conditional: true, headers: map[string]string{"Authorization": "Bearer token-b"}},
		{name: "matching_identity", headers: map[string]string{"Authorization": "Bearer token-a", "Chatgpt-Account-Id": "account-a"}, recover: true},
		{name: "unrelated_headers", headers: map[string]string{"User-Agent": "payload-agent"}, recover: true},
		{name: "conditional_matching_identity", conditional: true, headers: map[string]string{"Authorization": "Bearer token-a"}, recover: true},
		{name: "unaffected_route", otherRoute: true, headers: map[string]string{"Authorization": "Bearer token-b"}, recover: true},
		{name: "unaffected_protocol", otherProtocol: true, headers: map[string]string{"Authorization": "Bearer token-b"}, recover: true},
		{name: "unaffected_provider", claude: true, headers: map[string]string{"Authorization": "Bearer token-b"}, recover: true},
		{name: "non_string_model", nonString: true},
		{name: "removed_model", removeModel: true, headers: map[string]string{"Authorization": "Bearer token-b"}, recover: true},
		{name: "nested_model_field", nestedOnly: true, headers: map[string]string{"Authorization": "Bearer token-b"}, recover: true},
		{name: "wildcard_rewrite_bearer", wildcard: true, headers: map[string]string{"Authorization": "Bearer token-b"}},
		{name: "wildcard_unaffected_route", wildcard: true, otherRoute: true, headers: map[string]string{"Authorization": "Bearer token-b"}, recover: true},
		{name: "default_model_present", defaultOnly: true, headers: map[string]string{"Authorization": "Bearer token-b"}, recover: true},
		{name: "empty_model", emptyModel: true, headers: map[string]string{"Authorization": "Bearer token-b"}, recover: true},
		{name: "raw_matching_identity", raw: true, headers: map[string]string{"Authorization": "Bearer token-a"}, recover: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withQuotaCooldownEnabled(t)
			ctx := context.Background()
			id, route, rewritten := t.Name(), t.Name()+"-route", t.Name()+"-rewritten"
			m := NewManager(nil, nil, nil)
			rule := internalconfig.PayloadRule{Models: []internalconfig.PayloadModelRule{{Name: route, Protocol: "codex"}}, Params: map[string]any{"model": rewritten}}
			if tc.conditional {
				rule.Models[0].Headers = map[string]string{"X-Rewrite": "yes"}
				rule.Models[0].Match = []map[string]any{{"metadata.rewrite": true}}
			}
			if tc.otherRoute {
				rule.Models[0].Name += "-unrelated"
			}
			if tc.wildcard {
				rule.Models[0].Name = "*" + rule.Models[0].Name + "*"
			}
			if tc.emptyModel {
				rule.Params["model"] = ""
			}
			if tc.otherProtocol {
				rule.Models[0].Protocol = "claude"
			}
			if tc.nonString {
				rule.Params["model"] = map[string]any{"name": rewritten}
			}
			if tc.nestedOnly {
				rule.Params = map[string]any{"metadata.model": rewritten}
			}
			cfg := &internalconfig.Config{}
			if tc.raw {
				rule.Params["model"] = `"` + rewritten + `"`
				cfg.Payload.OverrideRaw = []internalconfig.PayloadRule{rule}
			} else {
				cfg.Payload.Override = []internalconfig.PayloadRule{rule}
			}
			if tc.defaultOnly {
				cfg.Payload.Default = cfg.Payload.Override
				cfg.Payload.Override = nil
			}
			if tc.removeModel {
				cfg.Payload.Override = nil
				cfg.Payload.Filter = []internalconfig.PayloadFilterRule{{Models: rule.Models, Params: []string{"model"}}}
			}
			m.SetConfig(cfg)
			reg := registry.GetGlobalRegistry()
			provider := "codex"
			if tc.claude {
				provider = "claude"
			}
			reg.RegisterClient(id, provider, []*registry.ModelInfo{{ID: route}})
			reg.RegisterClient(id+"-rewritten-metadata", "codex", []*registry.ModelInfo{{ID: rewritten, Config: &registry.ModelConfig{OverrideHeader: tc.headers}}})
			t.Cleanup(func() { reg.UnregisterClient(id); reg.UnregisterClient(id + "-rewritten-metadata") })
			if _, err := m.Register(ctx, &Auth{ID: id, Provider: provider, Status: StatusActive, Metadata: map[string]any{"access_token": "token-a", "account_id": "account-a"}}); err != nil {
				t.Fatal(err)
			}
			duration := time.Hour
			m.MarkResult(ctx, Result{AuthID: id, Provider: provider, Model: route, Error: &Error{HTTPStatus: 429, Message: `{"error":{"type":"usage_limit_reached"}}`}, RetryAfter: &duration, CredentialScope: true})
			before, _ := m.GetByID(id)
			if !QuotaRecoveryCandidate(before) {
				t.Fatal("fixture is not an otherwise recoverable quota candidate")
			}
			got, _, err := m.ResetQuotaIfUnchanged(ctx, before)
			if err != nil {
				t.Fatal(err)
			}
			if (got != nil) != tc.recover {
				t.Fatalf("recovered=%v want %v", got != nil, tc.recover)
			}
			after, _ := m.GetByID(id)
			if !tc.recover && !reflect.DeepEqual(after, before) {
				t.Fatal("payload-rewritten identity mismatch changed cooldown")
			}
			if tc.recover && (after.Quota.Exceeded || after.Unavailable || !after.NextRetryAfter.IsZero()) {
				t.Fatal("matching payload identity cooldown was not cleared")
			}
		})
	}
}
