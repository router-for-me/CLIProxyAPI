package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

// newProbeHandler builds a Handler preconfigured with one OpenAI-Compat
// entry. Tests can hit RemoteProbeModels against a mocked upstream and
// assert that the request reached the upstream with the expected
// credentials.
func newProbeHandler(t *testing.T, entry *config.OpenAICompatibility) *Handler {
	t.Helper()
	t.Setenv("MANAGEMENT_PASSWORD", "")
	var entries []config.OpenAICompatibility
	if entry != nil {
		entries = []config.OpenAICompatibility{*entry}
	}
	return NewHandlerWithoutConfigFilePath(&config.Config{
		OpenAICompatibility: entries,
	}, nil)
}

// runRemoteProbe posts the given body to RemoteProbeModels on the
// supplied handler and returns the recorder. The gin context is set up
// to mimic the production handler invocation (no auth middleware — the
// handler is called directly).
func runRemoteProbe(t *testing.T, h *Handler, body any) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("failed to marshal body: %v", err)
	}
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/remote-probe/models", bytes.NewReader(raw))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.RemoteProbeModels(ctx)
	return rec
}

// TestRemoteProbeModels_ResolvesEntryByName ensures that when the
// dashboard posts a configured entry's `name`, the server pulls the
// per-provider API key from cfg.OpenAICompatibility and forwards it as
// the upstream bearer token, even when the dashboard's body.api_key is
// empty or stale.
func TestRemoteProbeModels_ResolvesEntryByName(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	// Mock upstream that records the Authorization header it receives.
	var gotAuth atomic.Value // string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"mimo-v2.5","owned_by":"mimo"}]}`))
	}))
	defer upstream.Close()

	h := NewHandlerWithoutConfigFilePath(&config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "Mimo CN",
				BaseURL: upstream.URL + "/v1",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "real-per-entry-key"},
				},
			},
		},
	}, nil)

	rec := runRemoteProbe(t, h, map[string]any{
		// Operator-supplied (and wrong) bearer token that the server
		// must override with the per-entry key from cfg.
		"name":    "Mimo CN",
		"api_key": "wrong-global-caller-key",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	got, _ := gotAuth.Load().(string)
	if got != "Bearer real-per-entry-key" {
		t.Fatalf("expected upstream to receive per-entry key, got %q", got)
	}
}

// TestRemoteProbeModels_ResolvesBaseURLFromConfig verifies that when
// the dashboard omits base_url but supplies a configured entry name,
// the server falls back to the entry's configured base URL.
func TestRemoteProbeModels_ResolvesBaseURLFromConfig(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	var gotPath atomic.Value // string
	var gotAuth atomic.Value // string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath.Store(r.URL.Path)
		gotAuth.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"model-1"}]}`))
	}))
	defer upstream.Close()

	h := NewHandlerWithoutConfigFilePath(&config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "Mimo CN",
				BaseURL: upstream.URL, // no trailing /v1 — server appends /v1/models
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "resolved-key"},
				},
			},
		},
	}, nil)

	rec := runRemoteProbe(t, h, map[string]any{
		"name": "Mimo CN",
		// base_url intentionally omitted.
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	got, _ := gotPath.Load().(string)
	if got != "/v1/models" {
		t.Fatalf("expected upstream path %q, got %q", "/v1/models", got)
	}
	auth, _ := gotAuth.Load().(string)
	if auth != "Bearer resolved-key" {
		t.Fatalf("expected Authorization %q, got %q", "Bearer resolved-key", auth)
	}
}

// TestRemoteProbeModels_UnknownNameFallsBackToBodyAPIKey verifies that an
// unknown name does NOT 404: the caller may be probing a brand-new
// provider that has not been saved to config yet (the dashboard's
// add-mode form requires a provider name before the row exists), so the
// server must fall back to body.base_url + body.api_key verbatim, exactly
// as if `name` had been omitted.
func TestRemoteProbeModels_UnknownNameFallsBackToBodyAPIKey(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	var gotAuth atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	defer upstream.Close()

	h := NewHandlerWithoutConfigFilePath(&config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "Mimo CN",
				BaseURL: "https://example.test/v1",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "should-not-be-used"},
				},
			},
		},
	}, nil)

	rec := runRemoteProbe(t, h, map[string]any{
		"name":     "Brand New Provider",
		"base_url": upstream.URL,
		"api_key":  "typed-form-key",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d (fallback to body key), got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	got, _ := gotAuth.Load().(string)
	if got != "Bearer typed-form-key" {
		t.Fatalf("expected body api_key to be used verbatim for unknown name, got %q", got)
	}
}

// TestRemoteProbeModels_SkipsDisabledEntries ensures that a name match
// against a Disabled entry is rejected — the entry must be skipped, not
// returned as the resolved credential.
func TestRemoteProbeModels_SkipsDisabledEntries(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	h := NewHandlerWithoutConfigFilePath(&config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:     "Mimo CN",
				BaseURL:  "https://example.test/v1",
				Disabled: true,
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "should-not-be-used"},
				},
			},
		},
	}, nil)

	rec := runRemoteProbe(t, h, map[string]any{
		"name":    "Mimo CN",
		"api_key": "some-token",
	})

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected status %d (Disabled entry must be skipped), got %d with body %s",
			http.StatusNotFound, rec.Code, rec.Body.String())
	}
}

// TestRemoteProbeModels_EmptyNameFallsBackToBodyAPIKey verifies that an
// empty-string `name` does not trigger the resolve-by-name path, so
// existing dashboards (and explicit per-request overrides) keep
// working with body.api_key verbatim.
func TestRemoteProbeModels_EmptyNameFallsBackToBodyAPIKey(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	var gotAuth atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	defer upstream.Close()

	h := NewHandlerWithoutConfigFilePath(&config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "Mimo CN",
				BaseURL: "https://example.test/v1",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "should-not-be-used"},
				},
			},
		},
	}, nil)

	rec := runRemoteProbe(t, h, map[string]any{
		"base_url": upstream.URL,
		"api_key":  "explicit-override-key",
		"name":     "",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	got, _ := gotAuth.Load().(string)
	if got != "Bearer explicit-override-key" {
		t.Fatalf("expected body api_key to be used verbatim, got %q", got)
	}
}

// TestRemoteProbeModels_NoNameStillUsesBodyAPIKey verifies backward
// compatibility: dashboards that don't yet send `name` continue to
// work, with body.api_key used verbatim (this is the original behavior).
func TestRemoteProbeModels_NoNameStillUsesBodyAPIKey(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	var gotAuth atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	defer upstream.Close()

	h := NewHandlerWithoutConfigFilePath(&config.Config{}, nil)

	rec := runRemoteProbe(t, h, map[string]any{
		"base_url": upstream.URL,
		"api_key":  "legacy-key",
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	got, _ := gotAuth.Load().(string)
	if got != "Bearer legacy-key" {
		t.Fatalf("expected body api_key to be used verbatim, got %q", got)
	}
}

// TestRemoteProbeModels_NameCaseInsensitive verifies that the
// case-insensitive match used by the executor's resolveCompatConfig is
// mirrored here so dashboards can be case-tolerant.
func TestRemoteProbeModels_NameCaseInsensitive(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	var gotAuth atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	defer upstream.Close()

	h := NewHandlerWithoutConfigFilePath(&config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "Mimo CN",
				BaseURL: upstream.URL,
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "configured-key"},
				},
			},
		},
	}, nil)

	rec := runRemoteProbe(t, h, map[string]any{
		"name": "mimo cn", // lowercase
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	got, _ := gotAuth.Load().(string)
	if got != "Bearer configured-key" {
		t.Fatalf("expected per-entry key (case-insensitive match), got %q", got)
	}
}

// TestRemoteProbeModels_RejectsInvalidScheme guards against abuse: the
// handler must reject non-http(s) base_url even when name resolves.
func TestRemoteProbeModels_RejectsInvalidScheme(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	h := NewHandlerWithoutConfigFilePath(&config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "Mimo CN",
				BaseURL: "javascript:alert(1)",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "k"},
				},
			},
		},
	}, nil)

	// Even with a valid name, the body's base_url overrides the entry's
	// and the scheme check still fires (the server uses body.base_url
	// verbatim when present).
	rec := runRemoteProbe(t, h, map[string]any{
		"name":     "Mimo CN",
		"base_url": "file:///etc/passwd",
	})

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusBadRequest, rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "must start with http:// or https://") {
		t.Fatalf("expected scheme-rejection message, got %s", rec.Body.String())
	}
}

// TestRemoteProbeModels_NameResolvesEmptyBodyAPIKey verifies the contract
// the dashboard relies on after the kebab-case fix: the dashboard posts
// `{name, base_url, api_key: ""}` so the server pulls the per-provider
// credential itself instead of relying on a JSON-path parse that may
// not match the actual server-side tag.
func TestRemoteProbeModels_NameResolvesEmptyBodyAPIKey(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	var gotAuth atomic.Value
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAuth.Store(r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":[{"id":"m1"}]}`))
	}))
	defer upstream.Close()

	h := NewHandlerWithoutConfigFilePath(&config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "Mimo CN",
				BaseURL: upstream.URL,
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "configured-key"},
				},
			},
		},
	}, nil)

	rec := runRemoteProbe(t, h, map[string]any{
		"name":     "Mimo CN",
		"base_url": upstream.URL,
		"api_key":  "", // dashboard sends empty when it couldn't parse api-key-entries
	})

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}
	got, _ := gotAuth.Load().(string)
	if got != "Bearer configured-key" {
		t.Fatalf("expected server to resolve per-entry key from cfg, got Authorization %q", got)
	}
}
