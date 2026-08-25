package management

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
)

func TestUpstreamProviderEntryRequestRoundTrip(t *testing.T) {
	const payload = `{"provider_type":"openai-compatibility","api_key_entries":[{"id":42,"name":"Team-A","api_key":"entry-key","proxy_url":"http://proxy.example"}]}`

	var req upstreamProviderReq
	if err := json.Unmarshal([]byte(payload), &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	got := toUpstreamProvider(&req)
	if len(got.APIKeyEntries) != 1 {
		t.Fatalf("got %d API key entries, want 1", len(got.APIKeyEntries))
	}
	entry := got.APIKeyEntries[0]
	if entry.ID != 42 || entry.Name != "Team-A" || entry.APIKey != "entry-key" || entry.ProxyURL != "http://proxy.example" {
		t.Fatalf("converted entry = %+v, want id/name/key/proxy preserved", entry)
	}

	encoded, err := json.Marshal(req)
	if err != nil {
		t.Fatalf("encode request: %v", err)
	}
	if !strings.Contains(string(encoded), `"id":42`) || !strings.Contains(string(encoded), `"name":"Team-A"`) {
		t.Fatalf("encoded request omitted entry identity: %s", encoded)
	}
}

func TestUpstreamProviderResponseExposesEntryIdentity(t *testing.T) {
	const existingResponseKey = "existing-response-key"
	response := toUpstreamProviderResponse(store.UpstreamProvider{
		ID:           7,
		ProviderType: "openai-compatibility",
		Name:         "gateway",
		APIKeyEntries: []store.UpstreamProviderAPIKey{{
			ID:       42,
			Name:     "team-a",
			APIKey:   existingResponseKey,
			ProxyURL: "http://proxy.example",
		}},
	})

	raw, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("encode response: %v", err)
	}
	var decoded struct {
		APIKeyEntries []struct {
			ID       int64  `json:"id"`
			Name     string `json:"name"`
			APIKey   string `json:"api_key"`
			ProxyURL string `json:"proxy_url"`
		} `json:"api_key_entries"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(decoded.APIKeyEntries) != 1 {
		t.Fatalf("response has %d API key entries, want 1", len(decoded.APIKeyEntries))
	}
	entry := decoded.APIKeyEntries[0]
	if entry.ID != 42 || entry.Name != "team-a" || entry.ProxyURL != "http://proxy.example" {
		t.Fatalf("response entry identity = %+v, want id/name/proxy preserved", entry)
	}
	if entry.APIKey != existingResponseKey {
		t.Fatalf("response API key = %q, want existing response behavior", entry.APIKey)
	}
}

func TestUpstreamProviderErrorResponseEntryNameConflictIsSafe(t *testing.T) {
	const secretValue = "entry-secret-must-not-appear"
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	ctx.Request = httptest.NewRequest(http.MethodPost, "/v0/management/upstream-providers", nil)

	(&Handler{}).upstreamProviderErrorResponse(ctx, fmt.Errorf(
		"postgres store: duplicate upstream provider api key entry name: %s", secretValue,
	))

	if recorder.Code != http.StatusConflict {
		t.Fatalf("status = %d, want %d; body = %s", recorder.Code, http.StatusConflict, recorder.Body.String())
	}
	body := recorder.Body.String()
	if !strings.Contains(body, "already exists") {
		t.Fatalf("conflict response = %s, want safe conflict message", body)
	}
	if strings.Contains(body, secretValue) {
		t.Fatalf("conflict response leaked API key value: %s", body)
	}
}
