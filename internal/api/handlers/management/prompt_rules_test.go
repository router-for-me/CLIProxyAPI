package management

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
)

// newPromptRulesTestHandler returns a handler bound to a temp config.yaml so
// persistLocked succeeds without affecting any real config file.
//
// Note: we deliberately do NOT call gin.SetMode here — that function is
// not safe to call from concurrent test goroutines (Codex review §17 surface
// area) and other tests in this package already set the mode at startup.
func newPromptRulesTestHandler(t *testing.T, cfg *config.Config) *Handler {
	t.Helper()
	dir := t.TempDir()
	configPath := filepath.Join(dir, "config.yaml")
	// Seed the file so SaveConfigPreserveComments has something to round-trip.
	if err := os.WriteFile(configPath, []byte("port: 8080\n"), 0o600); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	manager := coreauth.NewManager(nil, nil, nil)
	if cfg == nil {
		cfg = &config.Config{}
	}
	return NewHandler(cfg, configPath, manager)
}

func newJSONReq(t *testing.T, method, path string, body any) *http.Request {
	t.Helper()
	var rdr *bytes.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rdr = bytes.NewReader(raw)
	} else {
		rdr = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rdr)
	req.Header.Set("Content-Type", "application/json")
	return req
}

func TestPromptRules_API_PutGet_Roundtrip(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	h := newPromptRulesTestHandler(t, nil)

	body := []config.PromptRule{
		{
			Name: "inject-style", Enabled: true,
			Target: "system", Action: "inject",
			Content: "<!-- pr:s --> Be brief.", Marker: "<!-- pr:s -->",
		},
	}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = newJSONReq(t, http.MethodPut, "/v0/management/prompt-rules", body)
	h.PutPromptRules(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}

	rec = httptest.NewRecorder()
	ctx, _ = gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodGet, "/v0/management/prompt-rules", nil)
	h.GetPromptRules(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET expected 200, got %d body=%s", rec.Code, rec.Body.String())
	}
	var got struct {
		PromptRules []config.PromptRule `json:"prompt-rules"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got.PromptRules) != 1 || got.PromptRules[0].Name != "inject-style" {
		t.Fatalf("round-trip lost data: %+v", got.PromptRules)
	}
	if got.PromptRules[0].Position != "append" {
		t.Fatalf("expected default position append; got %q", got.PromptRules[0].Position)
	}
}

func TestPromptRules_API_PutWithInvalidRegexReturns400(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	h := newPromptRulesTestHandler(t, nil)

	body := []config.PromptRule{{
		Name: "bad", Enabled: true, Target: "system", Action: "strip",
		Pattern: "(unclosed",
	}}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = newJSONReq(t, http.MethodPut, "/v0/management/prompt-rules", body)
	h.PutPromptRules(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for invalid regex; got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPromptRules_API_PutWithEmptyContentReturns400(t *testing.T) {
	// v2: marker no longer needs to be inside content. Empty content remains
	// invalid, so we test that path here.
	t.Setenv("MANAGEMENT_PASSWORD", "")
	h := newPromptRulesTestHandler(t, nil)

	body := []config.PromptRule{{
		Name: "bad", Enabled: true, Target: "system", Action: "inject",
		Content: "",
	}}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = newJSONReq(t, http.MethodPut, "/v0/management/prompt-rules", body)
	h.PutPromptRules(ctx)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400; got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPromptRules_API_PutWithMarkerNotInContent_AcceptedV2(t *testing.T) {
	// v2 codification: PUT a rule whose marker does not appear inside content
	// must succeed (anchor semantics, not sentinel).
	t.Setenv("MANAGEMENT_PASSWORD", "")
	h := newPromptRulesTestHandler(t, nil)

	body := []config.PromptRule{{
		Name: "anchor", Enabled: true, Target: "system", Action: "inject",
		Content: " (proxy)", Marker: "qwen", Position: "append",
	}}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = newJSONReq(t, http.MethodPut, "/v0/management/prompt-rules", body)
	h.PutPromptRules(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 under v2; got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPromptRules_API_PatchByName(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	h := newPromptRulesTestHandler(t, &config.Config{
		PromptRules: []config.PromptRule{{
			Name: "r1", Enabled: true, Target: "system", Action: "inject",
			Content: "<!-- pr:r1 --> a", Marker: "<!-- pr:r1 -->", Position: "append",
		}},
	})

	match := "r1"
	patch := struct {
		Match *string            `json:"match"`
		Value *config.PromptRule `json:"value"`
	}{
		Match: &match,
		Value: &config.PromptRule{
			Name: "r1", Enabled: true, Target: "system", Action: "inject",
			Content: "<!-- pr:r1 --> NEW", Marker: "<!-- pr:r1 -->", Position: "prepend",
		},
	}
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = newJSONReq(t, http.MethodPatch, "/v0/management/prompt-rules", patch)
	h.PatchPromptRule(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("PATCH expected 200; got %d body=%s", rec.Code, rec.Body.String())
	}
	if h.cfg.PromptRules[0].Position != "prepend" {
		t.Fatalf("expected position updated to prepend; got %+v", h.cfg.PromptRules[0])
	}
	if h.cfg.PromptRules[0].Content != "<!-- pr:r1 --> NEW" {
		t.Fatalf("expected content updated; got %q", h.cfg.PromptRules[0].Content)
	}
}

func TestPromptRules_API_DeleteByName(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	h := newPromptRulesTestHandler(t, &config.Config{
		PromptRules: []config.PromptRule{
			{Name: "a", Enabled: true, Target: "system", Action: "strip", Pattern: "x"},
			{Name: "b", Enabled: true, Target: "system", Action: "strip", Pattern: "y"},
		},
	})

	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/prompt-rules?name=a", nil)
	h.DeletePromptRule(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("DELETE expected 200; got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(h.cfg.PromptRules) != 1 || h.cfg.PromptRules[0].Name != "b" {
		t.Fatalf("expected only 'b' to remain; got %+v", h.cfg.PromptRules)
	}
}

func TestPromptRules_API_DeleteUnknownNameReturns404(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	h := newPromptRulesTestHandler(t, nil)
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	ctx.Request = httptest.NewRequest(http.MethodDelete, "/v0/management/prompt-rules?name=missing", nil)
	h.DeletePromptRule(ctx)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404; got %d body=%s", rec.Code, rec.Body.String())
	}
}

func TestPromptRules_API_PutEmptyBodyClears(t *testing.T) {
	t.Setenv("MANAGEMENT_PASSWORD", "")
	h := newPromptRulesTestHandler(t, &config.Config{
		PromptRules: []config.PromptRule{
			{Name: "a", Enabled: true, Target: "system", Action: "strip", Pattern: "x"},
		},
	})
	rec := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(rec)
	req := httptest.NewRequest(http.MethodPut, "/v0/management/prompt-rules", bytes.NewReader(nil))
	req.Header.Set("Content-Type", "application/json")
	ctx.Request = req
	h.PutPromptRules(ctx)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 for empty body (clears); got %d body=%s", rec.Code, rec.Body.String())
	}
	if len(h.cfg.PromptRules) != 0 {
		t.Fatalf("expected list cleared; got %+v", h.cfg.PromptRules)
	}
}
