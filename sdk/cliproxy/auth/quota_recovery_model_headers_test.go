package auth

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/MoeForge/moego/base64"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
)

func TestQuotaRecoveryModelHeaderIdentity(t *testing.T) {
	for _, tc := range []struct {
		name                                                                     string
		headers                                                                  map[string]string
		alias, prefix, suffix, otherSupported, unsupported, claude, authWideOnly bool
		siwc, invalidSIWC, disabledSIWC                                          bool
		apiAlias, pool, storedOnly                                               bool
		recover                                                                  bool
	}{
		{name: "siwc_matching_nested", headers: map[string]string{"Authorization": "Bearer nested-token"}, siwc: true, recover: true},
		{name: "siwc_matching_outer", headers: map[string]string{"Authorization": "Bearer token-a"}, siwc: true, recover: true},
		{name: "siwc_different_account", headers: map[string]string{"Authorization": "Bearer nested-token"}, siwc: true, invalidSIWC: true},
		{name: "siwc_disabled_nested", headers: map[string]string{"Authorization": "Bearer nested-token"}, siwc: true, disabledSIWC: true},
		{name: "stored_model_restriction", headers: map[string]string{"Authorization": "Bearer token-b"}, storedOnly: true},
		{name: "api_alias", headers: map[string]string{"Authorization": "Bearer token-b"}, apiAlias: true},
		{name: "matching_api_alias", headers: map[string]string{"Authorization": "Bearer token-a"}, apiAlias: true, recover: true},
		{name: "pool_second_candidate", headers: map[string]string{"Authorization": "Bearer token-b"}, pool: true},
		{name: "matching_pool", headers: map[string]string{"Authorization": "Bearer token-a"}, pool: true, recover: true},
		{name: "different_account", headers: map[string]string{"Chatgpt-Account-Id": "account-b"}},
		{name: "different_bearer", headers: map[string]string{"Authorization": "Bearer token-b"}},
		{name: "blank_account", headers: map[string]string{"Chatgpt-Account-Id": ""}},
		{name: "blank_bearer", headers: map[string]string{"Authorization": ""}},
		{name: "alias_upstream", headers: map[string]string{"Authorization": "Bearer token-b"}, alias: true},
		{name: "prefix_suffix_alias", headers: map[string]string{"Chatgpt-Account-Id": "account-b"}, alias: true, prefix: true, suffix: true},
		{name: "other_supported_model", headers: map[string]string{"Authorization": "Bearer token-b"}, otherSupported: true},
		{name: "auth_wide_only", headers: map[string]string{"Chatgpt-Account-Id": "account-b"}, authWideOnly: true},
		{name: "matching_identity", headers: map[string]string{"authorization": "Bearer token-a", "chatgpt-account-id": "account-a"}, recover: true},
		{name: "unrelated_headers", headers: map[string]string{"User-Agent": "model-agent", "X-Test": "value"}, recover: true},
		{name: "unsupported_model", headers: map[string]string{"Authorization": "Bearer token-b"}, unsupported: true, recover: true},
		{name: "claude_not_overridden", headers: map[string]string{"Authorization": "Bearer token-b"}, claude: true, recover: true},
		{name: "matching_alias", headers: map[string]string{"Authorization": "Bearer token-a"}, alias: true, prefix: true, suffix: true, recover: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withQuotaCooldownEnabled(t)
			phases := []bool{false}
			if tc.alias || tc.apiAlias || tc.pool {
				phases = append(phases, true)
			}
			for _, authWide := range phases {
				func() {
					ctx := context.Background()
					m := NewManager(nil, nil, nil)
					fixtureID := t.Name()
					if authWide {
						fixtureID += "-auth-wide"
					}
					id, route, upstream, other := fixtureID, fixtureID+"-route", fixtureID+"-upstream", fixtureID+"-other"
					provider := "codex"
					if tc.claude {
						provider = "claude"
					}
					a := &Auth{ID: id, Provider: provider, Status: StatusActive, Metadata: map[string]any{"access_token": "token-a", "account_id": "account-a"}}
					if tc.prefix {
						a.Prefix = "pref"
					}
					if tc.siwc {
						claim := `{"https://api.openai.com/auth":{"chatgpt_account_id":"account-a"}}`
						if tc.invalidSIWC {
							claim = `{"https://api.openai.com/auth":{"chatgpt_account_id":"account-b"}}`
						}
						a.Metadata["use_siwc"] = !tc.disabledSIWC
						a.Metadata["siwc"] = map[string]any{"access": "nested-token", "id_token": "e30." + base64.RawURLEncoding.EncodeToString([]byte(claim)) + ".sig"}
					}
					configured := route
					if tc.alias {
						m.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{provider: {{Name: upstream, Alias: route, Fork: true}}})
						configured = upstream
					}
					if tc.apiAlias {
						a.Attributes = map[string]string{"api_key": "config-key"}
						m.SetConfig(&internalconfig.Config{CodexKey: []internalconfig.CodexKey{{APIKey: "config-key", Models: []internalconfig.CodexModel{{Name: upstream, Alias: route}}}}})
						configured = upstream
					}
					if tc.pool {
						a.Attributes = map[string]string{"api_key": "config-key", "compat_name": "model-header-pool"}
						m.SetConfig(&internalconfig.Config{OpenAICompatibility: []internalconfig.OpenAICompatibility{{Name: "model-header-pool", Models: []internalconfig.OpenAICompatibilityModel{{Name: upstream, Alias: route}, {Name: other, Alias: route}}}}})
						configured = other
					}
					if tc.prefix {
						route = "pref/" + route
					}
					if tc.suffix {
						route += "(high)"
					}
					reg := registry.GetGlobalRegistry()
					models := []*registry.ModelInfo{{ID: route}}
					overrideID := configured
					if tc.otherSupported {
						overrideID = other
						models = append(models, &registry.ModelInfo{ID: other})
					}
					if tc.unsupported {
						overrideID = other
					}
					reg.RegisterClient(id, provider, models)
					t.Cleanup(func() { reg.UnregisterClient(id) })
					overrideClient := id + "-metadata"
					reg.RegisterClient(overrideClient, provider, []*registry.ModelInfo{{ID: overrideID, Config: &registry.ModelConfig{OverrideHeader: tc.headers}}})
					t.Cleanup(func() { reg.UnregisterClient(overrideClient) })
					if _, err := m.Register(ctx, a); err != nil {
						t.Fatal(err)
					}
					duration := time.Hour
					model := route
					if tc.authWideOnly || authWide {
						model = ""
					}
					m.MarkResult(ctx, Result{AuthID: id, Provider: provider, Model: model, Error: &Error{HTTPStatus: 429, Message: `{"error":{"type":"usage_limit_reached"}}`}, RetryAfter: &duration, CredentialScope: true})
					if tc.storedOnly {
						reg.UnregisterClient(id)
					}
					poolOffsetsBefore := make(map[string]int)
					for key, value := range m.modelPoolOffsets {
						poolOffsetsBefore[key] = value
					}
					before, _ := m.GetByID(id)
					if authWide && (len(before.ModelStates) != 0 || !before.Quota.Exceeded || !before.Unavailable || before.NextRetryAfter.IsZero()) {
						t.Fatal("alias phase must have only an auth-wide quota restriction")
					}
					if QuotaRecoveryCandidate(before) == tc.invalidSIWC {
						t.Fatal("fixture is not an otherwise recoverable quota candidate")
					}
					got, _, err := m.ResetQuotaIfUnchanged(ctx, before)
					if !reflect.DeepEqual(m.modelPoolOffsets, poolOffsetsBefore) && len(m.modelPoolOffsets) != 0 {
						t.Fatal("recovery advanced model pool rotation")
					}
					if err != nil {
						t.Fatal(err)
					}
					if (got != nil) != tc.recover {
						t.Fatalf("recovered=%v want %v", got != nil, tc.recover)
					}
					after, _ := m.GetByID(id)
					if !tc.recover {
						if !reflect.DeepEqual(after, before) {
							t.Fatal("identity mismatch changed cooldown")
						}
						return
					}
					if after.Quota.Exceeded || after.Unavailable || !after.NextRetryAfter.IsZero() {
						t.Fatal("matching identity cooldown was not cleared")
					}
					for _, state := range after.ModelStates {
						if state != nil && (state.Quota.Exceeded || state.Unavailable || !state.NextRetryAfter.IsZero()) {
							t.Fatal("model cooldown was not cleared")
						}
					}
				}()
			}
		})
	}
}

type quotaModelHeaderChangingStore struct {
	Store
	change func()
}

func (s *quotaModelHeaderChangingStore) Save(context.Context, *Auth) (string, error) {
	s.change()
	return "", nil
}

func TestQuotaRecoveryModelHeaderRecheckAfterPersistence(t *testing.T) {
	m, before, _, model := quotaGuardFixture(t)
	m.mu.Lock()
	m.auths[before.ID].Metadata = map[string]any{"access_token": "token-a", "account_id": "account-a"}
	m.mu.Unlock()
	before, _ = m.GetByID(before.ID)
	changed := false
	m.SetStore(&quotaModelHeaderChangingStore{change: func() {
		changed = true
		registry.GetGlobalRegistry().RegisterClient(before.ID, "codex", []*registry.ModelInfo{{ID: model, Config: &registry.ModelConfig{OverrideHeader: map[string]string{"Authorization": "Bearer token-b"}}}})
	}})
	got, _, err := m.ResetQuotaIfUnchanged(context.Background(), before)
	if err != nil || got != nil || !changed {
		t.Fatalf("reset=%v err=%v changed=%v", got, err, changed)
	}
	after, _ := m.GetByID(before.ID)
	if !reflect.DeepEqual(before, after) {
		t.Fatal("changed final model identity published a quota reset")
	}
}
