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

func TestPatchKimiKeyUpdatesFields(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{KimiKey: []config.KimiKey{{
			APIKey:  "sk-old",
			Service: config.KimiServiceOpenPlatform,
			Region:  config.KimiRegionDomestic,
			Name:    "old",
		}}},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/kimi-api-key", strings.NewReader(`{
		"index": 0,
		"value": {
			"api-key": "sk-new",
			"service": "coding-plan",
			"name": "desk",
			"priority": 3,
			"disable-cooling": true,
			"models": [{"name":"k3","alias":"kimi-k3"}]
		}
	}`))
	ctx.Request.Header.Set("Content-Type", "application/json")

	h.PatchKimiKey(ctx)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(h.cfg.KimiKey) != 1 {
		t.Fatalf("kimi-api-key count = %d, want 1", len(h.cfg.KimiKey))
	}
	entry := h.cfg.KimiKey[0]
	if entry.APIKey != "sk-new" {
		t.Fatalf("api-key = %q, want sk-new", entry.APIKey)
	}
	if entry.Service != config.KimiServiceCodingPlan {
		t.Fatalf("service = %q, want %s", entry.Service, config.KimiServiceCodingPlan)
	}
	if entry.Name != "desk" {
		t.Fatalf("name = %q, want desk", entry.Name)
	}
	if entry.Region != "" {
		t.Fatalf("region = %q, want empty", entry.Region)
	}
	if entry.Priority != 3 {
		t.Fatalf("priority = %d, want 3", entry.Priority)
	}
	if entry.DisableCooling == nil || !*entry.DisableCooling {
		t.Fatalf("disable-cooling = %v, want true", entry.DisableCooling)
	}
	if len(entry.Models) != 1 || entry.Models[0].Name != "k3" || entry.Models[0].Alias != "kimi-k3" {
		t.Fatalf("models = %#v", entry.Models)
	}
}

func TestPutAndGetKimiKeysIncludeAuthIndexField(t *testing.T) {
	h := &Handler{
		cfg:            &config.Config{},
		configFilePath: writeTestConfigFile(t),
	}

	putRec := httptest.NewRecorder()
	putCtx, _ := gin.CreateTestContext(putRec)
	putCtx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/kimi-api-key", strings.NewReader(`[
		{"api-key":"sk-open","service":"open-platform","region":"international"}
	]`))
	putCtx.Request.Header.Set("Content-Type", "application/json")
	h.PutKimiKeys(putCtx)
	if putRec.Code != http.StatusOK {
		t.Fatalf("PUT status = %d, want 200; body=%s", putRec.Code, putRec.Body.String())
	}
	if len(h.cfg.KimiKey) != 1 || h.cfg.KimiKey[0].Service != config.KimiServiceOpenPlatform {
		t.Fatalf("stored key = %+v", h.cfg.KimiKey)
	}

	getRec := httptest.NewRecorder()
	getCtx, _ := gin.CreateTestContext(getRec)
	getCtx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/kimi-api-key", nil)
	h.GetKimiKeys(getCtx)
	if getRec.Code != http.StatusOK {
		t.Fatalf("GET status = %d, want 200; body=%s", getRec.Code, getRec.Body.String())
	}
	var body map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(getRec.Body.Bytes(), &body); errUnmarshal != nil {
		t.Fatalf("decode GET body: %v", errUnmarshal)
	}
	if _, ok := body["kimi-api-key"]; !ok {
		t.Fatalf("GET body = %s, want kimi-api-key", getRec.Body.String())
	}
}

func TestPatchKimiKeyRejectsOpenPlatformWithoutRegion(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{KimiKey: []config.KimiKey{{
			APIKey:  "sk-code",
			Service: config.KimiServiceCodingPlan,
		}}},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/kimi-api-key", strings.NewReader(`{
		"index": 0,
		"value": {"service": "open-platform"}
	}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchKimiKey(ctx)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if len(h.cfg.KimiKey) != 1 || h.cfg.KimiKey[0].Service != config.KimiServiceCodingPlan {
		t.Fatalf("key should be unchanged, got %+v", h.cfg.KimiKey)
	}
}

func TestDeleteKimiKeyByIndex(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{KimiKey: []config.KimiKey{
			{APIKey: "sk-a", Service: config.KimiServiceCodingPlan},
			{APIKey: "sk-b", Service: config.KimiServiceCodingPlan},
		}},
		configFilePath: writeTestConfigFile(t),
	}

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/kimi-api-key?index=0", nil)
	h.DeleteKimiKey(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if len(h.cfg.KimiKey) != 1 || h.cfg.KimiKey[0].APIKey != "sk-b" {
		t.Fatalf("remaining keys = %+v", h.cfg.KimiKey)
	}
}

func TestDeleteKimiKeyQualifiedMatchRejectsDuplicates(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{KimiKey: []config.KimiKey{
			{APIKey: "sk-shared", Service: config.KimiServiceCodingPlan, Prefix: "a"},
			{APIKey: "sk-shared", Service: config.KimiServiceCodingPlan, Prefix: "b"},
		}},
		configFilePath: writeTestConfigFile(t),
	}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(
		http.MethodDelete,
		"/v0/management/kimi-api-key?api-key=sk-shared&service=coding-plan",
		nil,
	)
	h.DeleteKimiKey(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if len(h.cfg.KimiKey) != 2 {
		t.Fatalf("remaining keys = %+v, want both kept", h.cfg.KimiKey)
	}
}

func TestDeleteKimiKeyQualifiedMatchUsesPrefix(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{KimiKey: []config.KimiKey{
			{APIKey: "sk-shared", Service: config.KimiServiceCodingPlan, Prefix: "a"},
			{APIKey: "sk-shared", Service: config.KimiServiceCodingPlan, Prefix: "b"},
		}},
		configFilePath: writeTestConfigFile(t),
	}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(
		http.MethodDelete,
		"/v0/management/kimi-api-key?api-key=sk-shared&service=coding-plan&prefix=b",
		nil,
	)
	h.DeleteKimiKey(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(h.cfg.KimiKey) != 1 || h.cfg.KimiKey[0].Prefix != "a" {
		t.Fatalf("remaining keys = %+v, want prefix a", h.cfg.KimiKey)
	}
}

func TestDeleteKimiKeyQualifiedMatchDeletesUnique(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{KimiKey: []config.KimiKey{
			{APIKey: "sk-a", Service: config.KimiServiceCodingPlan},
			{APIKey: "sk-b", Service: config.KimiServiceCodingPlan},
		}},
		configFilePath: writeTestConfigFile(t),
	}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(
		http.MethodDelete,
		"/v0/management/kimi-api-key?api-key=sk-a&service=coding-plan",
		nil,
	)
	h.DeleteKimiKey(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}
	if len(h.cfg.KimiKey) != 1 || h.cfg.KimiKey[0].APIKey != "sk-b" {
		t.Fatalf("remaining keys = %+v", h.cfg.KimiKey)
	}
}

func TestDeleteKimiKeyIndexRejectsPrefixMismatch(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{KimiKey: []config.KimiKey{
			{APIKey: "sk-shared", Service: config.KimiServiceCodingPlan, Prefix: "a"},
			{APIKey: "sk-shared", Service: config.KimiServiceCodingPlan, Prefix: "b"},
		}},
		configFilePath: writeTestConfigFile(t),
	}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(
		http.MethodDelete,
		"/v0/management/kimi-api-key?index=0&api-key=sk-shared&service=coding-plan&prefix=b",
		nil,
	)
	h.DeleteKimiKey(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if len(h.cfg.KimiKey) != 2 {
		t.Fatalf("remaining keys = %+v, want both kept", h.cfg.KimiKey)
	}
}

func TestDeleteKimiKeyIndexDeletesMatchingPrefix(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{KimiKey: []config.KimiKey{
			{APIKey: "sk-shared", Service: config.KimiServiceCodingPlan, Prefix: "a"},
			{APIKey: "sk-shared", Service: config.KimiServiceCodingPlan, Prefix: "b"},
		}},
		configFilePath: writeTestConfigFile(t),
	}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(
		http.MethodDelete,
		"/v0/management/kimi-api-key?index=1&api-key=sk-shared&service=coding-plan&prefix=b",
		nil,
	)
	h.DeleteKimiKey(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}
	if len(h.cfg.KimiKey) != 1 || h.cfg.KimiKey[0].Prefix != "a" {
		t.Fatalf("remaining keys = %+v, want prefix a", h.cfg.KimiKey)
	}
}

func TestDeleteKimiKeyIndexRejectsIdentityMismatch(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{KimiKey: []config.KimiKey{
			{APIKey: "sk-a", Service: config.KimiServiceCodingPlan},
			{APIKey: "sk-b", Service: config.KimiServiceCodingPlan},
		}},
		configFilePath: writeTestConfigFile(t),
	}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(
		http.MethodDelete,
		"/v0/management/kimi-api-key?index=0&api-key=sk-b&service=coding-plan",
		nil,
	)
	h.DeleteKimiKey(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if len(h.cfg.KimiKey) != 2 {
		t.Fatalf("remaining keys = %+v, want both kept", h.cfg.KimiKey)
	}
}

func TestPutKimiKeysRejectsOpenPlatformWithoutRegion(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{KimiKey: []config.KimiKey{
			{APIKey: "sk-keep", Service: config.KimiServiceCodingPlan},
		}},
		configFilePath: writeTestConfigFile(t),
	}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPut, "/v0/management/kimi-api-key", strings.NewReader(`[
		{"api-key":"sk-open","service":"open-platform"}
	]`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PutKimiKeys(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if len(h.cfg.KimiKey) != 1 || h.cfg.KimiKey[0].APIKey != "sk-keep" {
		t.Fatalf("stored keys = %+v, want original list", h.cfg.KimiKey)
	}
}

func TestPatchKimiKeyMatchRejectsDuplicateAPIKey(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{KimiKey: []config.KimiKey{
			{APIKey: "sk-shared", Service: config.KimiServiceCodingPlan},
			{APIKey: "sk-shared", Service: config.KimiServiceOpenPlatform, Region: config.KimiRegionDomestic},
		}},
		configFilePath: writeTestConfigFile(t),
	}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodPatch, "/v0/management/kimi-api-key", strings.NewReader(`{
		"match": "sk-shared",
		"value": {"name": "desk"}
	}`))
	ctx.Request.Header.Set("Content-Type", "application/json")
	h.PatchKimiKey(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if h.cfg.KimiKey[0].Name != "" || h.cfg.KimiKey[1].Name != "" {
		t.Fatalf("keys were mutated: %+v", h.cfg.KimiKey)
	}
}

func TestDeleteKimiKeyOpenPlatformRequiresRegion(t *testing.T) {
	h := &Handler{
		cfg: &config.Config{KimiKey: []config.KimiKey{
			{APIKey: "sk-open", Service: config.KimiServiceOpenPlatform, Region: config.KimiRegionDomestic},
			{APIKey: "sk-open", Service: config.KimiServiceOpenPlatform, Region: config.KimiRegionInternational},
		}},
		configFilePath: writeTestConfigFile(t),
	}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/kimi-api-key?api-key=sk-open&service=open-platform", nil)
	h.DeleteKimiKey(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body=%s", rec.Code, rec.Body.String())
	}
	if len(h.cfg.KimiKey) != 2 {
		t.Fatalf("remaining keys = %+v, want both kept", h.cfg.KimiKey)
	}
}
