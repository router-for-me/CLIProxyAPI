package management

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
)

func TestOpenAICompatAzureManagementRoundTrip(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	h := NewHandler(&config.Config{OpenAICompatibility: []config.OpenAICompatibility{{
		Name:    "azure",
		BaseURL: "https://resource.openai.azure.com",
		Azure: &config.OpenAICompatibilityAzure{
			Deployment: "old-deployment",
			APIVersion: "2024-10-21",
		},
	}}}, writeTestConfigFile(t), nil)

	patchRec := httptest.NewRecorder()
	patchCtx, _ := gin.CreateTestContext(patchRec)
	patchCtx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/openai-compatibility", strings.NewReader(`{
		"name":"azure",
		"value":{"azure":{"deployment":" new-deployment ","api-version":" 2025-04-01-preview "}}
	}`))
	patchCtx.Request.Header.Set("Content-Type", "application/json")
	h.PatchOpenAICompat(patchCtx)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, body = %s", patchRec.Code, patchRec.Body.String())
	}

	getRec := httptest.NewRecorder()
	getCtx, _ := gin.CreateTestContext(getRec)
	getCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/openai-compatibility", nil)
	h.GetOpenAICompat(getCtx)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, body = %s", getRec.Code, getRec.Body.String())
	}
	var body struct {
		OpenAICompatibility []config.OpenAICompatibility `json:"openai-compatibility"`
	}
	if err := json.Unmarshal(getRec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	if len(body.OpenAICompatibility) != 1 || body.OpenAICompatibility[0].Azure == nil {
		t.Fatalf("azure management response = %#v", body.OpenAICompatibility)
	}
	azure := body.OpenAICompatibility[0].Azure
	if azure.Deployment != "new-deployment" || azure.APIVersion != "2025-04-01-preview" {
		t.Fatalf("azure = %#v", azure)
	}
}

func TestGetOpenAICompatIncludesDisableCooling(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	h := NewHandlerWithoutConfigFilePath(&config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "Mimo CN",
				BaseURL: "https://token-plan-cn.xiaomimimo.com/v1",
				APIKeyEntries: []config.OpenAICompatibilityAPIKey{
					{APIKey: "test-key"},
				},
				Models: []config.OpenAICompatibilityModel{
					{Name: "mimo-v2.5", Alias: ""},
				},
				DisableCooling: true,
			},
		},
	}, nil)

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/openai-compatibility", nil)
	h.GetOpenAICompat(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d with body %s", http.StatusOK, rec.Code, rec.Body.String())
	}

	var body struct {
		OpenAICompatibility []struct {
			DisableCooling *bool `json:"disable-cooling"`
		} `json:"openai-compatibility"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if len(body.OpenAICompatibility) != 1 {
		t.Fatalf("expected 1 openai-compatibility entry, got %d", len(body.OpenAICompatibility))
	}
	if body.OpenAICompatibility[0].DisableCooling == nil || !*body.OpenAICompatibility[0].DisableCooling {
		t.Fatalf("expected disable-cooling to be present and true, got %#v", body.OpenAICompatibility[0].DisableCooling)
	}
}
