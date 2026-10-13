package cliproxy

import (
	"net/http"
	"net/http/httptest"
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

func TestAntigravityCapabilityKeyChangesWithUserAgent(t *testing.T) {
	auth := antigravityTestAuth("ua-key", "https://example.invalid")
	before := antigravityCapabilityKeyForRoute(auth, "https://example.invalid", "")
	auth.Metadata["user_agent"] = "antigravity/cli/1.3.4 (aidev_client)"
	after := antigravityCapabilityKeyForRoute(auth, "https://example.invalid", "")
	if before == after {
		t.Fatal("changing the configured User-Agent must invalidate the cached catalog")
	}
}
