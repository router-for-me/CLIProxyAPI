package registry

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestRegisterAndResolveOfficialSpelling covers the lowercase->official
// spelling mapping used to satisfy the strict upstream gateway matcher.
func TestRegisterAndResolveOfficialSpelling(t *testing.T) {
	restore := SetCommandCodeOfficialSpellingsForTest(map[string]string{
		"qwen/qwen3.8-flash": "Qwen/Qwen3.8-Flash",
	})
	defer restore()

	if got := CommandCodeUpstreamModelID("qwen/qwen3.8-flash"); got != "Qwen/Qwen3.8-Flash" {
		t.Fatalf("lowercase id not rewritten: got %q", got)
	}
	// Mixed-case input still resolves via the lowercase key.
	if got := CommandCodeUpstreamModelID("QWEN/QWEN3.8-FLASH"); got != "Qwen/Qwen3.8-Flash" {
		t.Fatalf("uppercase input not resolved: got %q", got)
	}
	// Unknown ids pass through untouched.
	if got := CommandCodeUpstreamModelID("deepseek/deepseek-v4-flash"); got != "deepseek/deepseek-v4-flash" {
		t.Fatalf("unknown id must pass through: got %q", got)
	}
	if got := CommandCodeUpstreamModelID(""); got != "" {
		t.Fatalf("empty id must stay empty: got %q", got)
	}
}

// TestRemoteCatalogRecordsOfficialSpellings verifies the remote catalog
// fetcher records the official spelling for camel-case ids before the
// lowercase canonicalization step.
func TestRemoteCatalogRecordsOfficialSpellings(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"object":"list","data":[` +
			`{"id":"Qwen/Qwen3.8-Flash","object":"model","created":1,"owned_by":"command-code","name":"Qwen Flash","context_length":1000000},` +
			`{"id":"deepseek/deepseek-v4-flash","object":"model","created":1,"owned_by":"command-code","name":"DS","context_length":1000000}` +
			`]}`))
	}))
	defer srv.Close()

	restoreURL := commandCodeRemoteCatalogURL
	commandCodeRemoteCatalogURL = srv.URL
	t.Cleanup(func() { commandCodeRemoteCatalogURL = restoreURL })

	models, _, ok := fetchCommandCodeRemoteCatalogHTTP(t.Context())
	if !ok {
		t.Fatal("remote catalog fetch failed")
	}
	if len(models) != 2 {
		t.Fatalf("expected 2 models, got %d", len(models))
	}
	// Registry-facing IDs remain lowercase.
	for _, m := range models {
		if m.ID == "Qwen/Qwen3.8-Flash" || m.ID == "QWEN/QWEN3.8-FLASH" {
			t.Fatalf("catalog ID must be canonicalized to lowercase, got %q", m.ID)
		}
	}

	if got := CommandCodeUpstreamModelID("qwen/qwen3.8-flash"); got != "Qwen/Qwen3.8-Flash" {
		t.Fatalf("remote catalog spelling not recorded: got %q", got)
	}
	if got := CommandCodeUpstreamModelID("deepseek/deepseek-v4-flash"); got != "deepseek/deepseek-v4-flash" {
		t.Fatalf("all-lowercase official id must map to itself: got %q", got)
	}
}

// TestBuiltinBootstrapRecordsOfficialSpellings ensures staticCommandCodeModels
// seeds the spelling table so cold-start requests can already route
// camel-case models before any dynamic catalog lands.
func TestBuiltinBootstrapRecordsOfficialSpellings(t *testing.T) {
	captured := CommandCodeUpstreamModelID("qwen/qwen3.7-flash")
	if captured != "Qwen/Qwen3.7-Flash" && captured != "qwen/qwen3.7-flash" {
		t.Fatalf("unexpected spelling resolution: %q", captured)
	}

	staticCommandCodeModels() // seeds the table from builtin metadata

	if got := CommandCodeUpstreamModelID("moonshotai/kimi-k3"); got != "moonshotai/Kimi-K3" {
		t.Fatalf("builtin kimi-k3 spelling not recorded: got %q", got)
	}
	if got := CommandCodeUpstreamModelID("zai-org/glm-5.3"); got != "zai-org/GLM-5.3" {
		t.Fatalf("builtin glm-5.3 spelling not recorded: got %q", got)
	}
	if got := CommandCodeUpstreamModelID("qwen/qwen3.8-max"); got != "Qwen/Qwen3.8-Max" {
		t.Fatalf("builtin qwen3.8-max spelling not recorded: got %q", got)
	}
	// An all-lowercase official spelling maps to itself.
	if got := CommandCodeUpstreamModelID("xiaomi/mimo-v2.5"); got != "xiaomi/mimo-v2.5" {
		t.Fatalf("builtin mimo id must be unchanged: got %q", got)
	}
}
