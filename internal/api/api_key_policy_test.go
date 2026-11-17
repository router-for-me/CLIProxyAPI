package api

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/apikeylimits"
	proxyconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	sdkconfig "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

const policyTestProvider = "policytest"

type policyTestStatusError struct{}

func (policyTestStatusError) Error() string   { return "policy test upstream refused" }
func (policyTestStatusError) StatusCode() int { return http.StatusTeapot }

// policyTestExecutor records which credential was selected and fails with a
// recognizable status so tests do not depend on response translation.
type policyTestExecutor struct {
	mu       sync.Mutex
	attempts []string
}

func (e *policyTestExecutor) Identifier() string { return policyTestProvider }

func (e *policyTestExecutor) record(a *auth.Auth) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.attempts = append(e.attempts, a.ID)
}

func (e *policyTestExecutor) Execute(_ context.Context, a *auth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.record(a)
	return cliproxyexecutor.Response{}, policyTestStatusError{}
}

func (e *policyTestExecutor) ExecuteStream(_ context.Context, a *auth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	e.record(a)
	return nil, policyTestStatusError{}
}

func (e *policyTestExecutor) Refresh(_ context.Context, a *auth.Auth) (*auth.Auth, error) {
	return a, nil
}

func (e *policyTestExecutor) CountTokens(_ context.Context, a *auth.Auth, _ cliproxyexecutor.Request, _ cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	e.record(a)
	return cliproxyexecutor.Response{}, policyTestStatusError{}
}

func (e *policyTestExecutor) HttpRequest(context.Context, *auth.Auth, *http.Request) (*http.Response, error) {
	return nil, policyTestStatusError{}
}

func (e *policyTestExecutor) snapshot() []string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return append([]string(nil), e.attempts...)
}

type policyTestEnv struct {
	server   *Server
	exec     *policyTestExecutor
	indexes  map[string]string
	authIDs  []string
	modelOne string
	modelTwo string
}

func newPolicyTestEnv(t *testing.T) *policyTestEnv {
	t.Helper()
	env := &policyTestEnv{
		indexes:  map[string]string{},
		authIDs:  []string{"policy-auth-a-" + t.Name(), "policy-auth-b-" + t.Name()},
		modelOne: "policy-model-shared-" + t.Name(),
		modelTwo: "policy-model-b-only-" + t.Name(),
	}
	env.server = newTestServerWithConfig(t, &proxyconfig.Config{SDKConfig: sdkconfig.SDKConfig{
		APIKeys: []string{"key-free", "key-disabled", "key-a"},
	}})
	env.exec = &policyTestExecutor{}
	env.server.handlers.AuthManager.RegisterExecutor(env.exec)
	for i, id := range env.authIDs {
		models := []*registry.ModelInfo{{ID: env.modelOne, Object: "model", OwnedBy: "policy"}}
		if i == 1 {
			models = append(models, &registry.ModelInfo{ID: env.modelTwo, Object: "model", OwnedBy: "policy"})
		}
		registry.GetGlobalRegistry().RegisterClient(id, policyTestProvider, models)
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		registered, err := env.server.handlers.AuthManager.Register(context.Background(), &auth.Auth{
			ID: id, Provider: policyTestProvider, Status: auth.StatusActive,
			Attributes: map[string]string{"path": "/auths/" + id + ".json"},
		})
		if err != nil {
			t.Fatal(err)
		}
		env.indexes[id] = registered.EnsureIndex()
	}
	updated := *env.server.getConfig()
	updated.APIKeyPolicies = []proxyconfig.APIKeyPolicy{
		{Key: "key-disabled", Disabled: true},
		{Key: "key-a", Name: "Team A", AllowedAuths: []string{env.indexes[env.authIDs[0]]}},
	}
	env.server.UpdateClients(&updated)
	return env
}

func (env *policyTestEnv) do(t *testing.T, method, path, key, body string) *httptest.ResponseRecorder {
	t.Helper()
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+key)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	env.server.engine.ServeHTTP(rr, req)
	return rr
}

func TestAPIKeyPolicyDisabledKeyIsRejected(t *testing.T) {
	env := newPolicyTestEnv(t)
	before := apikeylimits.Default().Usage("key-disabled")[apikeylimits.Total]
	rr := env.do(t, http.MethodPost, "/v1/chat/completions", "key-disabled", `{"model":"`+env.modelOne+`","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusUnauthorized || !strings.Contains(rr.Body.String(), "api_key_disabled") {
		t.Fatalf("status=%d body=%s, want 401 api_key_disabled", rr.Code, rr.Body.String())
	}
	if got := env.exec.snapshot(); len(got) != 0 {
		t.Fatalf("disabled key reached executor: %v", got)
	}
	after := apikeylimits.Default().Usage("key-disabled")[apikeylimits.Total]
	if after.Rejected != before.Rejected+1 || after.RejectedReasons["api_key_disabled"] != before.RejectedReasons["api_key_disabled"]+1 ||
		after.LastRejectedAt == nil || after.Requests != before.Requests {
		t.Fatalf("rejection not counted: before=%+v after=%+v", before, after)
	}
}

func TestAPIKeyPolicyRestrictsCredentialSelection(t *testing.T) {
	env := newPolicyTestEnv(t)
	body := `{"model":"` + env.modelOne + `","messages":[{"role":"user","content":"hi"}]}`
	if rr := env.do(t, http.MethodPost, "/v1/chat/completions", "key-a", body); rr.Code != http.StatusTeapot {
		t.Fatalf("status=%d body=%s, want upstream 418", rr.Code, rr.Body.String())
	}
	// The failed credential cools down; later requests must not spill to other credentials.
	for i := 0; i < 3; i++ {
		if rr := env.do(t, http.MethodPost, "/v1/chat/completions", "key-a", body); rr.Code < 400 {
			t.Fatalf("status=%d body=%s, want an error while the only allowed credential cools down", rr.Code, rr.Body.String())
		}
	}
	for _, id := range env.exec.snapshot() {
		if id != env.authIDs[0] {
			t.Fatalf("restricted key used %s; attempts=%v", id, env.exec.snapshot())
		}
	}
	// A model only served by a credential outside the allow-list is forbidden.
	rr := env.do(t, http.MethodPost, "/v1/chat/completions", "key-a", `{"model":"`+env.modelTwo+`","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusForbidden {
		t.Fatalf("status=%d body=%s, want 403", rr.Code, rr.Body.String())
	}
	// Keys without a policy still reach every credential.
	before := len(env.exec.snapshot())
	if rr = env.do(t, http.MethodPost, "/v1/chat/completions", "key-free", `{"model":"`+env.modelTwo+`","messages":[{"role":"user","content":"hi"}]}`); rr.Code != http.StatusTeapot {
		t.Fatalf("unrestricted status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := env.exec.snapshot(); len(got) == before || got[len(got)-1] != env.authIDs[1] {
		t.Fatalf("unrestricted key attempts=%v", got)
	}
}

func TestAPIKeyPolicyFiltersModelList(t *testing.T) {
	env := newPolicyTestEnv(t)
	ids := func(key string) map[string]bool {
		rr := env.do(t, http.MethodGet, "/v1/models", key, "")
		if rr.Code != http.StatusOK {
			t.Fatalf("models status=%d body=%s", rr.Code, rr.Body.String())
		}
		var payload struct {
			Data []struct {
				ID string `json:"id"`
			} `json:"data"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		out := map[string]bool{}
		for _, m := range payload.Data {
			out[m.ID] = true
		}
		return out
	}
	restricted := ids("key-a")
	if !restricted[env.modelOne] || restricted[env.modelTwo] {
		t.Fatalf("restricted model list = %v", restricted)
	}
	free := ids("key-free")
	if !free[env.modelOne] || !free[env.modelTwo] {
		t.Fatalf("unrestricted model list = %v", free)
	}
}

func TestAPIKeyPolicyReloadTakesEffect(t *testing.T) {
	env := newPolicyTestEnv(t)
	updated := *env.server.getConfig()
	updated.APIKeyPolicies = []proxyconfig.APIKeyPolicy{{Key: "key-a", Disabled: true}}
	env.server.UpdateClients(&updated)
	rr := env.do(t, http.MethodPost, "/v1/chat/completions", "key-a", `{"model":"`+env.modelOne+`","messages":[]}`)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("status=%d body=%s, want 401 after reload", rr.Code, rr.Body.String())
	}
	if rr = env.do(t, http.MethodPost, "/v1/chat/completions", "key-disabled", `{"model":"`+env.modelOne+`","messages":[]}`); rr.Code == http.StatusUnauthorized {
		t.Fatalf("policy removed by reload must re-enable key-disabled; body=%s", rr.Body.String())
	}
}

func (env *policyTestEnv) setPolicies(t *testing.T, keys []string, policies ...proxyconfig.APIKeyPolicy) {
	t.Helper()
	updated := *env.server.getConfig()
	updated.APIKeys = keys
	updated.APIKeyPolicies = policies
	env.server.UpdateClients(&updated)
}

func TestAPIKeyPolicyExpiredKeyIsRejected(t *testing.T) {
	env := newPolicyTestEnv(t)
	env.setPolicies(t, []string{"key-old", "key-new"},
		proxyconfig.APIKeyPolicy{Key: "key-old", ExpiresAt: "2020-01-01T00:00:00Z"},
		proxyconfig.APIKeyPolicy{Key: "key-new", ExpiresAt: "2999-01-01T00:00:00Z"},
	)
	body := `{"model":"` + env.modelOne + `","messages":[{"role":"user","content":"hi"}]}`
	if rr := env.do(t, http.MethodPost, "/v1/chat/completions", "key-old", body); rr.Code != http.StatusUnauthorized || !strings.Contains(rr.Body.String(), "api_key_expired") {
		t.Fatalf("expired key: status=%d body=%s", rr.Code, rr.Body.String())
	}
	if rr := env.do(t, http.MethodPost, "/v1/chat/completions", "key-new", body); rr.Code == http.StatusUnauthorized {
		t.Fatalf("unexpired key rejected: %s", rr.Body.String())
	}
}

func TestAPIKeyPolicyAllowedModels(t *testing.T) {
	env := newPolicyTestEnv(t)
	env.setPolicies(t, []string{"key-models"}, proxyconfig.APIKeyPolicy{Key: "key-models", AllowedModels: []string{"policy-model-shared-*"}})
	// Check the catalog first: failed executions below put credentials into cooldown.
	models := env.do(t, http.MethodGet, "/v1/models", "key-models", "").Body.String()
	if !strings.Contains(models, env.modelOne) || strings.Contains(models, env.modelTwo) {
		t.Fatalf("model list not filtered by allowed-models: %s", models)
	}
	rr := env.do(t, http.MethodPost, "/v1/chat/completions", "key-models", `{"model":"`+env.modelTwo+`","messages":[{"role":"user","content":"hi"}]}`)
	if rr.Code != http.StatusForbidden || !strings.Contains(rr.Body.String(), "model_not_allowed") {
		t.Fatalf("disallowed model: status=%d body=%s", rr.Code, rr.Body.String())
	}
	if got := env.exec.snapshot(); len(got) != 0 {
		t.Fatalf("disallowed model reached executor: %v", got)
	}
	if rr = env.do(t, http.MethodPost, "/v1/chat/completions", "key-models", `{"model":"`+env.modelOne+`","messages":[{"role":"user","content":"hi"}]}`); rr.Code != http.StatusTeapot {
		t.Fatalf("allowed model: status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestAPIKeyPolicyQuotaExceeded(t *testing.T) {
	env := newPolicyTestEnv(t)
	key := "key-quota-" + t.Name()
	env.setPolicies(t, []string{key}, proxyconfig.APIKeyPolicy{Key: key, Limits: proxyconfig.APIKeyLimits{TokensPerDay: 10}})
	apikeylimits.Default().Add(key, 10, 0)
	rr := env.do(t, http.MethodPost, "/v1/chat/completions", key, `{"model":"`+env.modelOne+`","messages":[]}`)
	if rr.Code != http.StatusTooManyRequests || !strings.Contains(rr.Body.String(), "api_key_quota_exceeded") {
		t.Fatalf("quota: status=%d body=%s", rr.Code, rr.Body.String())
	}
	if ra := rr.Header().Get("Retry-After"); ra == "" || ra == "0" {
		t.Fatalf("Retry-After = %q", ra)
	}
	if got := env.exec.snapshot(); len(got) != 0 {
		t.Fatalf("over-quota key reached executor: %v", got)
	}
}
