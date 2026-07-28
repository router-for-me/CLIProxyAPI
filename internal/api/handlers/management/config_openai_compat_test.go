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

func TestGetOpenAICompatIncludesDisableCooling(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")

	h := NewHandlerWithoutConfigFilePath(&config.Config{
		OpenAICompatibility: []config.OpenAICompatibility{
			{
				Name:    "Mimo CN",
				BaseURL: "https://token-plan-cn.xiaomimimo.com/v1",
				WireAPI: "responses",
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
			DisableCooling *bool  `json:"disable-cooling"`
			WireAPI        string `json:"wire-api"`
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
	if body.OpenAICompatibility[0].WireAPI != "responses" {
		t.Fatalf("expected wire-api responses, got %q", body.OpenAICompatibility[0].WireAPI)
	}
}

func TestOpenAICompatWireAPIPutAndPatchRoundTrip(t *testing.T) {
	cfg := &config.Config{}
	h := &Handler{cfg: cfg, configFilePath: writeTestConfigFile(t)}

	putRec := httptest.NewRecorder()
	putCtx, _ := gin.CreateTestContext(putRec)
	putCtx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/openai-compatibility", strings.NewReader(`[{"name":"test","base-url":"https://example.com/v1","wire-api":" ReSpOnSeS "}]`))
	putCtx.Request.Header.Set("Content-Type", "application/json")
	h.PutOpenAICompat(putCtx)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200; body=%s", putRec.Code, putRec.Body.String())
	}
	if got := cfg.OpenAICompatibility[0].WireAPI; got != "responses" {
		t.Fatalf("PUT WireAPI = %q, want responses", got)
	}

	patchRec := httptest.NewRecorder()
	patchCtx, _ := gin.CreateTestContext(patchRec)
	patchCtx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/openai-compatibility", strings.NewReader(`{"index":0,"value":{"wire-api":" FuTuRe-PrOtOcOl "}}`))
	patchCtx.Request.Header.Set("Content-Type", "application/json")
	h.PatchOpenAICompat(patchCtx)
	if patchRec.Code != http.StatusOK {
		t.Fatalf("PATCH status = %d, want 200; body=%s", patchRec.Code, patchRec.Body.String())
	}
	if got := cfg.OpenAICompatibility[0].WireAPI; got != "future-protocol" {
		t.Fatalf("PATCH WireAPI = %q, want future-protocol", got)
	}

	getRec := httptest.NewRecorder()
	getCtx, _ := gin.CreateTestContext(getRec)
	getCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/openai-compatibility", nil)
	h.GetOpenAICompat(getCtx)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body=%s", getRec.Code, getRec.Body.String())
	}
	var body struct {
		OpenAICompatibility []struct {
			WireAPI string `json:"wire-api"`
		} `json:"openai-compatibility"`
	}
	if errDecode := json.Unmarshal(getRec.Body.Bytes(), &body); errDecode != nil {
		t.Fatalf("failed to decode GET response: %v", errDecode)
	}
	if got := body.OpenAICompatibility[0].WireAPI; got != "future-protocol" {
		t.Fatalf("GET WireAPI = %q, want future-protocol", got)
	}
}
