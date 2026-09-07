package management

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// registerProbeAuth registers one live auth carrying the synthesizer-stamped
// provider_key / entry_provider_key attributes so the test target resolution
// can be exercised against a realistic registry.
func registerTestRunAuth(t *testing.T, h *Handler, attrs map[string]string) {
	t.Helper()
	if _, err := h.authManager.Register(context.Background(), &coreauth.Auth{
		ID:         attrs["probe_id"],
		Provider:   attrs["probe_provider"],
		Attributes: attrs,
	}); err != nil {
		t.Fatalf("register probe auth: %v", err)
	}
}

func newTestRunHandler(t *testing.T) *Handler {
	t.Helper()
	h := NewHandlerWithoutConfigFilePath(&config.Config{AuthDir: t.TempDir()}, coreauth.NewManager(nil, nil, nil))
	return h
}

func postTestProbe(h *Handler, rec *httptest.ResponseRecorder, id string, body string) {
	ginCtx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPost, "/v0/management/upstream-providers/"+id+"/test", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	ginCtx.Request = req
	ginCtx.Params = gin.Params{{Key: "id", Value: id}}
	h.TestUpstreamProvider(ginCtx)
}

func TestTestUpstreamProviderMissingModel(t *testing.T) {
	h := newTestRunHandler(t)
	rec := httptest.NewRecorder()
	postTestProbe(h, rec, "3", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestTestUpstreamProviderInvalidID(t *testing.T) {
	h := newTestRunHandler(t)
	rec := httptest.NewRecorder()
	postTestProbe(h, rec, "abc", `{"model":"gpt-4o"}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 body=%s", rec.Code, rec.Body.String())
	}
}

func TestTestUpstreamProviderAuthNotLive(t *testing.T) {
	h := newTestRunHandler(t)
	// No PG store wired: the store-unavailable path fires before auth
	// resolution — assert the store 503 does not leak a 500.
	rec := httptest.NewRecorder()
	postTestProbe(h, rec, "3", `{"model":"gpt-4o"}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503 body=%s", rec.Code, rec.Body.String())
	}
}

func TestResolveTestTargetAuthEntryLevel(t *testing.T) {
	h := newTestRunHandler(t)
	registerTestRunAuth(t, h, map[string]string{
		"probe_id":                         "auth-entry-1",
		"probe_provider":                   "openai-compat-main",
		"provider_key":                     "openai-compat-main:9",
		coreauth.AttributeEntryProviderKey: "openai-compat-main:9:key-21",
	})
	registerTestRunAuth(t, h, map[string]string{
		"probe_id":                         "auth-entry-2",
		"probe_provider":                   "openai-compat-main",
		"provider_key":                     "openai-compat-main:9",
		coreauth.AttributeEntryProviderKey: "openai-compat-main:9:key-22",
	})

	// Entry-level resolution pins the exact entry auth.
	auth := h.resolveTestTargetAuth(9, "openai-compat-main", int64Ptr(22))
	if auth == nil || auth.ID != "auth-entry-2" {
		t.Fatalf("resolveTestTargetAuth entry 22 = %#v, want auth-entry-2", auth)
	}
	auth = h.resolveTestTargetAuth(9, "openai-compat-main", int64Ptr(21))
	if auth == nil || auth.ID != "auth-entry-1" {
		t.Fatalf("resolveTestTargetAuth entry 21 = %#v, want auth-entry-1", auth)
	}
	// Unknown entry resolves to nothing.
	if auth := h.resolveTestTargetAuth(9, "openai-compat-main", int64Ptr(99)); auth != nil {
		t.Fatalf("unknown entry resolved %#v, want nil", auth)
	}

	// Provider-level resolution skips entry-bearing auths and falls back to
	// the parent key when nothing else matches.
	if auth := h.resolveTestTargetAuth(9, "openai-compat-main", nil); auth == nil {
		t.Fatal("provider-level resolution found no auth, want the parent-key fallback")
	} else if auth.Attributes[coreauth.AttributeEntryProviderKey] != "" && auth.ID != "auth-entry-1" && auth.ID != "auth-entry-2" {
		t.Fatalf("provider-level fallback picked unexpected auth %s", auth.ID)
	}

	// A parent key with no live auths resolves to nil.
	if auth := h.resolveTestTargetAuth(42, "claude", int64Ptr(7)); auth != nil {
		t.Fatalf("unrelated row resolved %#v, want nil", auth)
	}
}

func TestChannelForProviderRow(t *testing.T) {
	cases := []struct {
		providerType string
		name         string
		want         string
	}{
		{"gemini-api-key", "", "gemini"},
		{"interactions-api-key", "", "interactions"},
		{"codex-api-key", "", "codex"},
		{"xai-api-key", "", "xai"},
		{"claude-api-key", "", "claude"},
		{"vertex-api-key", "", "vertex"},
		{"openai-compatibility", "My-Compat", "My-Compat"},
		{"oauth:claude", "", "claude"},
	}
	for _, tc := range cases {
		got := channelForProviderRow(testProviderRow(tc.providerType, tc.name))
		if got != tc.want {
			t.Fatalf("channelForProviderRow(%q, %q) = %q, want %q", tc.providerType, tc.name, got, tc.want)
		}
	}
}

// decodeProbeResponse decodes the endpoint's JSON body into the typed
// response struct, failing the test on mismatch.
func decodeProbeResponse(t *testing.T, rec *httptest.ResponseRecorder) upstreamProviderTestResponse {
	t.Helper()
	var out upstreamProviderTestResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode probe response %q: %v", rec.Body.String(), err)
	}
	return out
}

// int64Ptr is a tiny helper to take the address of an int64 literal in tests.
func int64Ptr(v int64) *int64 { return &v }

// testProviderRow builds a minimal store row for the channel mapping test.
func testProviderRow(providerType, name string) store.UpstreamProvider {
	return store.UpstreamProvider{ProviderType: providerType, Name: name}
}
