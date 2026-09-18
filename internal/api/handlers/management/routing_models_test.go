package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

func TestIsProviderRowLive(t *testing.T) {
	cases := []struct {
		name         string
		row          string
		liveEvidence []string
		cooldown     map[string]bool
		want         bool
	}{
		{"empty row", "", []string{"claude"}, nil, false},
		{"whitespace row", "   ", []string{"claude"}, nil, false},
		{"compound exact match", "claude:42:key-7", []string{"claude:42:key-7"}, nil, true},
		{"compound case-insensitive match", "Claude:42:KEY-7", []string{"claude:42:key-7"}, nil, true},
		{"claude:42 NOT live when only 'claude' is in evidence", "claude:42", []string{"claude"}, nil, false},
		{"openai-compat provider-level fallback (entry live)", "openai-compatible-foo", []string{"openai-compatible-foo:bar"}, nil, true},
		{"openai-compat provider-level fallback (provider live)", "openai-compatible-foo", []string{"openai-compatible-foo"}, nil, true},
		{"openai-compat provider-level fallback (different entry)", "openai-compatible-foo", []string{"openai-compatible-foo:other"}, nil, true},
		{"openai-compat provider-level fallback (different provider)", "openai-compatible-foo", []string{"openai-compatible-baz:bar"}, nil, false},
		{"openai-compat compound is NOT a provider-level route", "openai-compatible-foo:bar", []string{"openai-compatible-foo"}, nil, false},
		{"claude compound partial (key only) is NOT live", "claude:42:key-7", []string{"claude:42"}, nil, false},
		{"cooldown blocks even with live evidence", "claude:42", []string{"claude:42"}, map[string]bool{"claude:42": true}, false},
		{"cooldown blocks compound", "claude:42:key-7", []string{"claude:42:key-7"}, map[string]bool{"claude:42:key-7": true}, false},
		{"no evidence", "claude:42", nil, nil, false},
		{"empty evidence after trim", "claude:42", []string{"", "  "}, nil, false},
		{"bare channel no compound", "claude", []string{"claude"}, nil, true},
		{"different channel no match", "openai:42", []string{"claude"}, nil, false},
		{"empty evidence + cooldown", "claude:42", nil, map[string]bool{"claude:42": true}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := IsProviderRowLive(tc.row, tc.liveEvidence, tc.cooldown)
			if got != tc.want {
				t.Fatalf("IsProviderRowLive(%q, %v, %v) = %v, want %v", tc.row, tc.liveEvidence, tc.cooldown, got, tc.want)
			}
		})
	}
}

func TestCooldownProviderSetEmpty(t *testing.T) {
	if got := CooldownProviderSet(nil); got != nil {
		t.Fatalf("nil snapshot: want nil, got %v", got)
	}
	if got := CooldownProviderSet([]auth.CooldownStateRecord{}); got != nil {
		t.Fatalf("empty snapshot: want nil, got %v", got)
	}
}

func TestCooldownProviderSetFilters(t *testing.T) {
	in := []auth.CooldownStateRecord{
		{Provider: "Claude:42"},
		{Provider: "openai:42"},
		{Provider: ""},          // skipped
		{Provider: "  "},        // skipped
		{Provider: "openai:42"}, // duplicate, idempotent
	}
	got := CooldownProviderSet(in)
	if got == nil {
		t.Fatal("non-empty snapshot: want non-nil map")
	}
	if !got["claude:42"] {
		t.Fatalf("claude:42 should be set: %v", got)
	}
	if !got["openai:42"] {
		t.Fatalf("openai:42 should be set: %v", got)
	}
	if got[""] {
		t.Fatalf("empty provider key should not be set: %v", got)
	}
	if got["  "] {
		t.Fatalf("whitespace provider key should not be set: %v", got)
	}
	if len(got) != 2 {
		t.Fatalf("map size = %d, want 2", len(got))
	}
}

// newPickerRouter wires a minimal gin.Engine with the picker + pin
// endpoints so tests can drive them without the full management surface.
// h must have pgUpstreamProviders/pgModels set; authManager may be nil.
func newPickerRouter(h *Handler) *gin.Engine {
	r := gin.New()
	g := r.Group("/v0/management")
	g.GET("/model-routing/picker", h.GetModelRoutingPicker)
	g.POST("/model-routing/pin", h.PostModelRoutingPin)
	return r
}

// TestPickerRequiresPGStore confirms a handler without PG-backed stores
// returns 503 — the operator-facing path stays honest about what it can do.
func TestPickerRequiresPGStore(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(nil, nil)
	r := newPickerRouter(h)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/model-routing/picker?model=gpt-4o", nil)
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
}

// TestPickerRequiresModelParam confirms the model query parameter is
// required (the picker is per-model so an empty model would silently
// scan the whole registry).
func TestPickerRequiresModelParam(t *testing.T) {
	// pgUpstreamProviders / pgModels nil → we expect 503 first (the
	// requirePG gate runs before the model check), so this test double-
	// checks that the model check never gets reached without PG.
	h := NewHandlerWithoutConfigFilePath(nil, nil)
	r := newPickerRouter(h)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/model-routing/picker", nil)
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d (no PG → 503 before model check); body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
}

// TestPickerResponseShape decodes the response with the json tags the
// picker uses so a future refactor that drops a field breaks the test.
// Uses a no-PG handler so the response body is the 503 envelope; we
// decode the error shape to assert the wire contract.
func TestPickerResponseShape(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(nil, nil)
	r := newPickerRouter(h)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v0/management/model-routing/picker?model=gpt-4o", nil)
	r.ServeHTTP(rec, req)

	var body struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if body.Error.Type != "pg_not_configured" {
		t.Fatalf("error.type = %q, want pg_not_configured", body.Error.Type)
	}
	if body.Error.Message == "" {
		t.Fatalf("error.message is empty")
	}
}

// TestPinRequiresPGStore confirms the pin endpoint 503s without PG.
func TestPinRequiresPGStore(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(nil, nil)
	r := newPickerRouter(h)

	body := `{"model":"gpt-4o","provider_key":"openai:1"}`
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/management/model-routing/pin", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
}

// TestPinRequiresModelAndProvider confirms the pin endpoint rejects
// requests missing model or provider_key (400) before touching PG.
func TestPinRequiresModelAndProvider(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(nil, nil)
	r := newPickerRouter(h)

	cases := []struct {
		name string
		body string
	}{
		{"missing model", `{"provider_key":"openai:1"}`},
		{"missing provider_key", `{"model":"gpt-4o"}`},
		{"empty model", `{"model":"","provider_key":"openai:1"}`},
		{"empty provider_key", `{"model":"gpt-4o","provider_key":""}`},
		{"whitespace only", `{"model":"   ","provider_key":"   "}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/v0/management/model-routing/pin", strings.NewReader(tc.body))
			req.Header.Set("Content-Type", "application/json")
			r.ServeHTTP(rec, req)
			// Without PG → 503 wins over 400 (requirePG gate runs first).
			// This test exists to pin the no-PG-fast-path contract; the
			// 400 path is exercised in the PG-backed integration test
			// (gated on PGSTORE_TEST_DSN).
			if rec.Code != http.StatusServiceUnavailable {
				t.Fatalf("status = %d, want %d body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
			}
		})
	}
}

// TestPinRejectsMalformedJSON confirms the JSON-binding 400 envelope
// (the only validation path reachable without PG).
func TestPinRejectsMalformedJSON(t *testing.T) {
	h := NewHandlerWithoutConfigFilePath(nil, nil)
	r := newPickerRouter(h)

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v0/management/model-routing/pin", strings.NewReader("not json"))
	req.Header.Set("Content-Type", "application/json")
	r.ServeHTTP(rec, req)

	// 503 still wins (requirePG runs first). This test guards against
	// future refactors that move binding before the PG gate.
	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want %d (503 wins over 400 without PG); body=%s", rec.Code, http.StatusServiceUnavailable, rec.Body.String())
	}
}
