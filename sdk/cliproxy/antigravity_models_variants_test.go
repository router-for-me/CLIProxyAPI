package cliproxy

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	coreexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	sdktranslator "github.com/router-for-me/CLIProxyAPI/v8/sdk/translator"
	"github.com/tidwall/gjson"
)

func TestAntigravityCatalogRetainsOnlyRawEntitlements(t *testing.T) {
	hints, ok := parseAntigravityModelCapabilityHints([]byte(`{"models":{"gemini-3.8-flash-low":{},"gemini-3.1-pro-high":{}}}`))
	if !ok {
		t.Fatal("catalog parsing failed")
	}
	check := func(hints antigravityModelCapabilityHints) {
		t.Helper()
		if len(hints.ModelIDs) != 2 {
			t.Fatalf("catalog invented upstream IDs: %v", hints.ModelIDs)
		}
		for _, id := range []string{"gemini-3.8-flash-low", "gemini-3.1-pro-high"} {
			if _, exists := hints.ModelIDs[id]; !exists {
				t.Fatalf("raw entitlement %q was lost", id)
			}
		}
		for _, id := range []string{"gemini-3.8-flash", "gemini-3.1-pro"} {
			if !hasAntigravityModelEntitlement(hints, id) {
				t.Fatalf("public model %q should remain entitled", id)
			}
		}
	}
	check(hints)
	value := buildPersistedAntigravityCapabilityHintsValue("test-project", "https://example.invalid", hints)
	restored, okRestored := parsePersistedAntigravityCapabilityHints(map[string]any{antigravityPersistedHintsMetaKey: value}, "test-project", "https://example.invalid")
	if !okRestored {
		t.Fatal("persisted catalog parsing failed")
	}
	check(restored)
	svc := &Service{cfg: &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{
		"antigravity": {{Name: "gemini-3.8-flash", Alias: "flash-alias"}},
	}}}
	auth := &coreauth.Auth{ID: "restored-variant-alias", Provider: "antigravity", Prefix: "account"}
	models := svc.antigravityModelsForHints(auth, restored)
	reg := registry.GetGlobalRegistry()
	reg.RegisterClient(auth.ID, "antigravity", models)
	t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
	for _, id := range []string{"flash-alias", "account/flash-alias", "gemini-3.8-flash"} {
		if got := helps.AntigravityUpstreamModel(auth, id); got != "gemini-3.8-flash-low" {
			t.Fatalf("restored alias %q resolved to %q", id, got)
		}
	}
}

func TestAntigravityPublishedVariantReachesSelectedAccountUpstream(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	for _, tc := range []struct {
		public string
		raw    string
	}{{"gemini-3.8-flash", "gemini-3.8-flash-low"}, {"gemini-3.8-flash", "gemini-3.8-flash-tiered"}, {"gemini-3.1-pro", "gemini-3.1-pro-high"}, {"gemini-3.8-flash", "gemini-3.8-flash-high"}} {
		t.Run(tc.raw, func(t *testing.T) {
			seen := make(chan string, 2)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, err := io.ReadAll(r.Body)
				if err != nil {
					http.Error(w, "read failed", http.StatusBadRequest)
					return
				}
				seen <- gjson.GetBytes(body, "model").String()
				response := `{"response":{"candidates":[{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}]}}`
				if r.URL.Path == "/v1internal:streamGenerateContent" {
					w.Header().Set("Content-Type", "text/event-stream")
					_, _ = io.WriteString(w, "data: "+response+"\n\n")
				} else {
					w.Header().Set("Content-Type", "application/json")
					_, _ = io.WriteString(w, response)
				}
			}))
			defer server.Close()
			auth := &coreauth.Auth{ID: "variant-" + tc.raw, Provider: "antigravity", Status: coreauth.StatusActive,
				Attributes: map[string]string{"base_url": server.URL},
				Metadata:   map[string]any{"project_id": "test-project", "access_token": "synthetic-token", "expired": time.Now().Add(time.Hour).Format(time.RFC3339)},
			}
			reg := registry.GetGlobalRegistry()
			t.Cleanup(func() { reg.UnregisterClient(auth.ID) })
			svc := &Service{cfg: &config.Config{}}
			hints, ok := parseAntigravityModelCapabilityHints([]byte(`{"models":{"` + tc.raw + `":{}}}`))
			if !ok {
				t.Fatal("catalog parsing failed")
			}
			hints.revision = 1
			key := svc.antigravityCapabilityKey(auth)
			antigravityCapabilityMu.Lock()
			antigravityCapabilityCache[key] = antigravityCapabilityCacheEntry{hints: hints}
			antigravityCapabilityMu.Unlock()
			svc.applyAntigravityModelHints(t.Context(), auth, "antigravity", hints, key, auth.RegistrationEpoch, reg.ClientRegistrationEpoch(auth.ID))
			if !reg.ClientSupportsModel(auth.ID, tc.public) {
				t.Fatalf("public model %q was not advertised", tc.public)
			}
			models := reg.GetModelsForClient(auth.ID)
			for _, model := range models {
				encoded, err := json.Marshal(model)
				if err != nil {
					t.Fatal(err)
				}
				if gjson.GetBytes(encoded, "UpstreamModelID").Exists() {
					t.Fatal("internal upstream routing leaked into model metadata")
				}
			}
			if got := helps.AntigravityUpstreamModel(auth, tc.public); got != tc.raw {
				t.Fatalf("selected variant=%q, want %q", got, tc.raw)
			}
			payload := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
			req := coreexecutor.Request{Model: tc.public, Payload: payload}
			opts := coreexecutor.Options{SourceFormat: sdktranslator.FormatGemini, ResponseFormat: sdktranslator.FormatGemini}
			exec := executor.NewAntigravityExecutor(&config.Config{})
			if _, err := exec.Execute(t.Context(), auth, req, opts); err != nil {
				t.Fatal(err)
			}
			if got := <-seen; got != tc.raw {
				t.Fatalf("non-stream model=%q, want %q", got, tc.raw)
			}
			opts.Stream = true
			result, errStream := exec.ExecuteStream(t.Context(), auth, req, opts)
			if errStream != nil {
				t.Fatal(errStream)
			}
			for chunk := range result.Chunks {
				if chunk.Err != nil {
					t.Fatal(chunk.Err)
				}
			}
			if got := <-seen; got != tc.raw {
				t.Fatalf("stream model=%q, want %q", got, tc.raw)
			}
		})
	}
}
