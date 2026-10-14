package cliproxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestAntigravityCapabilityProbeRefreshesOnClientVersionChange(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	const oldUA = "antigravity/hub/2.9.1 darwin/arm64"
	const newUA = "antigravity/hub/2.23.0 darwin/arm64"
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		models := map[string]any{"gemini-3.8-flash-high": map[string]any{}}
		switch ua := r.Header.Get("User-Agent"); ua {
		case newUA:
			models["gemini-4-argon-high"] = map[string]any{}
		case oldUA:
		default:
			t.Errorf("unexpected catalog User-Agent: %q", ua)
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"models": models})
	}))
	defer server.Close()
	svc := &Service{cfg: &config.Config{}}
	auth := antigravityTestAuth("client-version-refresh", server.URL)
	auth.Metadata["user_agent"] = oldUA
	oldKey := svc.antigravityCapabilityKey(auth)
	oldHints := svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
	svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
	if calls.Load() != 1 {
		t.Fatal("unchanged client version did not reuse its fresh catalog")
	}
	auth.Metadata[antigravityPersistedHintsMetaKey] = buildPersistedAntigravityCapabilityHintsValue("", server.URL, oldHints)
	auth.Metadata["user_agent"] = newUA
	if svc.antigravityCapabilityKey(auth) == oldKey {
		t.Fatal("client version change retained the old catalog cache key")
	}
	newHints := svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
	if _, ok := newHints.ModelIDs["gemini-4-argon-high"]; !ok || calls.Load() != 2 {
		t.Fatalf("client version change failed to refresh Gemini 4 before cache expiry: models=%v calls=%d", newHints.ModelIDs, calls.Load())
	}
}

func TestAntigravityCapabilityKeyIncludesDefaultUserAgent(t *testing.T) {
	auth := antigravityTestAuth("default-version-key", "https://example.com")
	key := antigravityCapabilityKeyForRoute(auth, "https://example.com", "")
	if !strings.Contains(key, misc.AntigravityUserAgent()) {
		t.Fatal("default runtime client version is absent from the catalog cache key")
	}
	const metadataUA = "antigravity/hub/2.23.0 darwin/arm64"
	const attributeUA = "antigravity/hub/2.24.0 darwin/arm64"
	auth.Metadata["user_agent"] = metadataUA
	if got := antigravityModelUserAgent(auth); got != metadataUA {
		t.Fatalf("metadata User-Agent = %q, want %q", got, metadataUA)
	}
	auth.Attributes["user_agent"] = attributeUA + " " + misc.AntigravityNodeAPIClientUA
	if got := antigravityModelUserAgent(auth); got != attributeUA {
		t.Fatalf("attribute User-Agent = %q, want normalized runtime value %q", got, attributeUA)
	}
}
