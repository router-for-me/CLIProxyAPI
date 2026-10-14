package cliproxy

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestAntigravityModelProbeUsesConfiguredClientIdentity(t *testing.T) {
	const cliUA = "antigravity/cli/1.3.4 (aidev_client; os_type=linux; arch=amd64)"
	cases := []struct {
		name      string
		configure func(attrs map[string]string, meta map[string]any)
		wantUA    string
		wantModel string
	}{
		{
			name:      "default",
			configure: func(map[string]string, map[string]any) {},
			wantUA:    misc.AntigravityUserAgent(),
			wantModel: "",
		},
		{
			name:      "metadata user_agent",
			configure: func(_ map[string]string, meta map[string]any) { meta["user_agent"] = cliUA },
			wantUA:    cliUA,
			wantModel: "gemini-cli-only",
		},
		{
			name:      "attribute user_agent",
			configure: func(attrs map[string]string, _ map[string]any) { attrs["user_agent"] = cliUA },
			wantUA:    cliUA,
			wantModel: "gemini-cli-only",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resetAntigravityCapabilityCache()
			t.Cleanup(resetAntigravityCapabilityCache)
			var (
				mu        sync.Mutex
				gotUA     string
				gotCustom string
			)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				gotUA, gotCustom = r.Header.Get("User-Agent"), r.Header.Get("X-Custom")
				mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				// Mirror the upstream behavior: the catalog depends on the client identity.
				if r.Header.Get("User-Agent") == cliUA {
					_, _ = w.Write([]byte(`{"models":{"gemini-base":{},"gemini-cli-only":{}}}`))
					return
				}
				_, _ = w.Write([]byte(`{"models":{"gemini-base":{}}}`))
			}))
			t.Cleanup(server.Close)

			auth := antigravityTestAuth("ua-"+tc.name, server.URL)
			auth.Attributes["header:X-Custom"] = "custom-value"
			tc.configure(auth.Attributes, auth.Metadata)

			svc := &Service{cfg: &config.Config{}}
			hints := svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)

			mu.Lock()
			defer mu.Unlock()
			if gotUA != tc.wantUA {
				t.Fatalf("model probe User-Agent = %q, want %q", gotUA, tc.wantUA)
			}
			if gotCustom != "custom-value" {
				t.Fatalf("model probe X-Custom = %q, want custom header from auth attributes", gotCustom)
			}
			if _, ok := hints.ModelIDs["gemini-base"]; !ok {
				t.Fatalf("expected gemini-base in hints, got %v", hints.ModelIDs)
			}
			_, hasCLIModel := hints.ModelIDs["gemini-cli-only"]
			if (tc.wantModel != "") != hasCLIModel {
				t.Fatalf("gemini-cli-only present = %v, want %v (hints %v)", hasCLIModel, tc.wantModel != "", hints.ModelIDs)
			}
		})
	}
}

func TestAntigravityModelProbeHeaderUserAgentOverridesConfigured(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	const headerUA = "antigravity/cli/9.9.9 (aidev_client)"
	var (
		mu    sync.Mutex
		gotUA string
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		gotUA = r.Header.Get("User-Agent")
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"models":{"gemini-base":{}}}`))
	}))
	t.Cleanup(server.Close)

	auth := antigravityTestAuth("ua-header", server.URL)
	auth.Metadata["user_agent"] = "antigravity/cli/1.3.4 (aidev_client)"
	auth.Attributes["header:User-Agent"] = headerUA
	if got := antigravityModelFetchScope(auth).UserAgent; got != headerUA {
		t.Fatalf("override = %q, want header:User-Agent %q", got, headerUA)
	}
	svc := &Service{cfg: &config.Config{}}
	svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
	mu.Lock()
	defer mu.Unlock()
	if gotUA != headerUA {
		t.Fatalf("model probe User-Agent = %q, want %q", gotUA, headerUA)
	}
}

func TestAntigravityCapabilityKeyChangesWithUserAgent(t *testing.T) {
	const endpoint = "https://example.invalid"
	base := antigravityTestAuth("ua-key", endpoint)
	defaultKey := antigravityCapabilityKeyForRoute(base, endpoint, "")
	if got := antigravityModelFetchScope(base); got != (antigravityCatalogScope{}) {
		t.Fatalf("scope without configuration = %+v, want empty", got)
	}

	withMetadata := base.Clone()
	withMetadata.Metadata["user_agent"] = "antigravity/cli/1.3.4 (aidev_client)"
	metadataKey := antigravityCapabilityKeyForRoute(withMetadata, endpoint, "")

	withHeader := withMetadata.Clone()
	withHeader.Attributes["header:User-Agent"] = "antigravity/cli/9.9.9 (aidev_client)"
	headerKey := antigravityCapabilityKeyForRoute(withHeader, endpoint, "")

	if defaultKey == metadataKey {
		t.Fatal("changing user_agent must invalidate the cached catalog")
	}
	if metadataKey == headerKey {
		t.Fatal("changing header:User-Agent must invalidate the cached catalog")
	}
}

func TestAntigravityCapabilityKeyChangesWithHeaderOverrides(t *testing.T) {
	const endpoint = "https://example.invalid"
	base := antigravityTestAuth("header-key", endpoint)
	defaultKey := antigravityCapabilityKeyForRoute(base, endpoint, "")

	keyWith := func(name, value string) string {
		auth := base.Clone()
		auth.Attributes["header:"+name] = value
		return antigravityCapabilityKeyForRoute(auth, endpoint, "")
	}
	for _, tc := range []struct{ name, first, second string }{
		{"Authorization", "Bearer first-secret", "Bearer second-secret"},
		{"Host", "a.example.invalid", "b.example.invalid"},
		{"X-Goog-User-Project", "project-a", "project-b"},
	} {
		firstKey := keyWith(tc.name, tc.first)
		if firstKey == defaultKey || firstKey == keyWith(tc.name, tc.second) {
			t.Fatalf("changing header:%s must invalidate the cached catalog", tc.name)
		}
		if firstKey != keyWith(tc.name, tc.first) {
			t.Fatalf("the same header:%s must keep the catalog key stable", tc.name)
		}
		if strings.Contains(firstKey, tc.first) {
			t.Fatalf("catalog key must not contain the header:%s value", tc.name)
		}
	}

	host := base.Clone()
	host.Attributes["header:Host"] = "a.example.invalid"
	if got := antigravityModelFetchScope(host); got.UserAgent != "" || got.HeadersFingerprint == "" {
		t.Fatalf("header:Host scope = %+v, want only a headers fingerprint", got)
	}
	refreshed := base.Clone()
	refreshed.Metadata["access_token"] = "rotated-token"
	if antigravityCapabilityKeyForRoute(refreshed, endpoint, "") != defaultKey {
		t.Fatal("routine OAuth token rotation must not invalidate the cached catalog")
	}
}

func TestPersistedAntigravityCapabilityHintsAreScopedToOverrides(t *testing.T) {
	const (
		project  = "project-1"
		endpoint = "https://example.invalid"
		cliUA    = "antigravity/cli/1.3.4 (aidev_client)"
	)
	hints := antigravityModelCapabilityHints{ModelIDs: map[string]struct{}{"gemini-cli-only": {}}}
	uaScope := antigravityCatalogScope{UserAgent: cliUA}
	headersScope := antigravityCatalogScope{HeadersFingerprint: "0123456789abcdef"}

	scoped := map[string]any{antigravityPersistedHintsMetaKey: buildPersistedAntigravityCapabilityHintsValue(project, endpoint, uaScope, hints)}
	if _, ok := parsePersistedAntigravityCapabilityHints(scoped, project, endpoint, uaScope); !ok {
		t.Fatal("catalog persisted for a User-Agent must load for the same User-Agent")
	}
	if _, ok := parsePersistedAntigravityCapabilityHints(scoped, project, endpoint, antigravityCatalogScope{}); ok {
		t.Fatal("catalog persisted for an override must not load for the default identity")
	}
	if _, ok := parsePersistedAntigravityCapabilityHints(scoped, project, endpoint, antigravityCatalogScope{UserAgent: "antigravity/cli/9.9.9"}); ok {
		t.Fatal("catalog persisted for one User-Agent must not load for another")
	}

	headersScoped := map[string]any{antigravityPersistedHintsMetaKey: buildPersistedAntigravityCapabilityHintsValue(project, endpoint, headersScope, hints)}
	if _, ok := parsePersistedAntigravityCapabilityHints(headersScoped, project, endpoint, headersScope); !ok {
		t.Fatal("catalog persisted for header overrides must load for the same overrides")
	}
	if _, ok := parsePersistedAntigravityCapabilityHints(headersScoped, project, endpoint, antigravityCatalogScope{HeadersFingerprint: "fedcba9876543210"}); ok {
		t.Fatal("catalog persisted for one set of header overrides must not load for another")
	}
	if _, ok := parsePersistedAntigravityCapabilityHints(headersScoped, project, endpoint, antigravityCatalogScope{}); ok {
		t.Fatal("catalog persisted for header overrides must not load without them")
	}

	unscoped := map[string]any{antigravityPersistedHintsMetaKey: buildPersistedAntigravityCapabilityHintsValue(project, endpoint, antigravityCatalogScope{}, hints)}
	persisted := unscoped[antigravityPersistedHintsMetaKey].(map[string]any)
	if _, hasUA := persisted["user_agent"]; hasUA {
		t.Fatal("default identity must not persist a user_agent field")
	}
	if _, hasHeaders := persisted["headers_fingerprint"]; hasHeaders {
		t.Fatal("default headers must not persist a headers_fingerprint field")
	}
	if _, ok := parsePersistedAntigravityCapabilityHints(unscoped, project, endpoint, antigravityCatalogScope{}); !ok {
		t.Fatal("catalogs without override fields must keep loading for the default scope")
	}
	if _, ok := parsePersistedAntigravityCapabilityHints(unscoped, project, endpoint, uaScope); ok {
		t.Fatal("catalogs without override fields must not load for an override")
	}
}
