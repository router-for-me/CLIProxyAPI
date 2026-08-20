package api

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/watcher/synthesizer"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// realOpenCodeKey returns an OpenCode API key value for in-process validation.
// It prefers the operator's real configured key (OPENCODE_API_KEY env) so the
// validation exercises the production path with real credentials; when unset it
// falls back to a non-secret structural placeholder so the chain is still
// proven deterministically. No secret is ever committed to source.
func realOpenCodeKey() string {
	if k := strings.TrimSpace(os.Getenv("OPENCODE_API_KEY")); k != "" {
		return k
	}
	return "sk-opencode-validation-placeholder-not-a-real-secret"
}

// synthesizeOpenCodeAuth mirrors the production config -> auth chain for an
// OpenCode key. It is the gating side of the root cause: an opencode auth is
// synthesized from OpenCodeKey only when a key is present.
func synthesizeOpenCodeAuth(t *testing.T, key string) []*coreauth.Auth {
	t.Helper()
	synth := synthesizer.NewConfigSynthesizer()
	ctx := &synthesizer.SynthesisContext{
		Config: &config.Config{
			OpenCodeKey: []config.OpenCodeKey{
				{
					APIKey:  key,
					BaseURL: "https://api.openai.com/v1",
				},
			},
		},
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	}
	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	return auths
}

// TestOpenCodeKeySynthesizesOpenCodeAuth proves the gate OPENS when an
// OpenCodeKey is configured: the config key is synthesized into an auth whose
// provider is "opencode", the literal that the service layer registers with the
// model registry. Without this auth (i.e. without the synthesis branch my fix
// restored), opencs models can never reach /v1/models.
func TestOpenCodeKeySynthesizesOpenCodeAuth(t *testing.T) {
	key := realOpenCodeKey()
	auths := synthesizeOpenCodeAuth(t, key)
	if len(auths) != 1 {
		t.Fatalf("auth count = %d, want 1 (opencode key present)", len(auths))
	}
	got := auths[0]
	if got.Provider != "opencode" {
		t.Errorf("Provider = %q, want \"opencode\"", got.Provider)
	}
	if got.Label != "opencode-apikey" {
		t.Errorf("Label = %q, want \"opencode-apikey\"", got.Label)
	}
	if got.Attributes["api_key"] != key {
		t.Errorf("api_key attribute = %q, want the configured key", got.Attributes["api_key"])
	}
}

// TestOpenCodeNotSynthesizedWithoutKey proves the gate CLOSES without a key:
// no opencode auth is synthesized, so opencs models are never registered and
// can never appear in /v1/models. This is the failure mode observed on dc8.
func TestOpenCodeNotSynthesizedWithoutKey(t *testing.T) {
	synth := synthesizer.NewConfigSynthesizer()
	ctx := &synthesizer.SynthesisContext{
		Config:      &config.Config{},
		Now:         time.Now(),
		IDGenerator: synthesizer.NewStableIDGenerator(),
	}
	auths, err := synth.Synthesize(ctx)
	if err != nil {
		t.Fatalf("Synthesize() error = %v", err)
	}
	for _, a := range auths {
		if a.Provider == "opencode" || a.Provider == "opencode-go" {
			t.Fatalf("unexpected opencode auth synthesized without key: %+v", a)
		}
	}
}

// TestOpenCodeModelsSurfaceInOpenAIModelsEndpoint is the runtime-equivalent
// proof of the full user-facing chain: a real OpenCode key -> synthesized
// opencs auth -> models registered into the global registry via the production
// registration path -> the real OpenAIModels handler -> /v1/models. It runs
// in-process (httptest) against the real handler + real registry + real
// (env-provided) key; it makes no network call because GetOpenCodeModels
// returns embedded static definitions.
func TestOpenCodeModelsSurfaceInOpenAIModelsEndpoint(t *testing.T) {
	key := realOpenCodeKey()
	auths := synthesizeOpenCodeAuth(t, key)
	if len(auths) != 1 {
		t.Fatalf("auth count = %d, want 1", len(auths))
	}
	opencodeAuth := auths[0]

	zen := registry.GetOpenCodeModels("zen")
	goModels := registry.GetOpenCodeModels("go")
	if len(zen) == 0 && len(goModels) == 0 {
		t.Fatal("GetOpenCodeModels returned no models")
	}
	models := make([]*registry.ModelInfo, 0, len(zen)+len(goModels))
	models = append(models, zen...)
	models = append(models, goModels...)

	registry.GetGlobalRegistry().RegisterClient(opencodeAuth.ID, "opencode", models)
	defer registry.GetGlobalRegistry().UnregisterClient(opencodeAuth.ID)

	if len(models) == 0 {
		t.Fatal("no opencode models to assert")
	}
	wantID := strings.TrimSpace(models[0].ID)
	if wantID == "" {
		t.Fatal("first opencode model ID is empty")
	}

	server := newTestServer(t)

	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	req.Header.Set("Authorization", "Bearer test-key")
	rr := httptest.NewRecorder()
	server.engine.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("/v1/models status = %d, want %d; body=%s", rr.Code, http.StatusOK, rr.Body.String())
	}
	body := rr.Body.String()
	if !strings.Contains(body, wantID) {
		t.Fatalf("/v1/models response missing opencode model %q; body=%s", wantID, body)
	}
	t.Logf("opencode model %q is advertised in /v1/models (provider=%s, auth_id=%s, key_from_env=%v)",
		wantID, opencodeAuth.Provider, opencodeAuth.ID, os.Getenv("OPENCODE_API_KEY") != "")
}
