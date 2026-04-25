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
)

func newReasoningDefaultsTestHandler(t *testing.T, cfg *config.Config) (*Handler, string) {
	t.Helper()

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "config.yaml")
	if err := os.WriteFile(configPath, []byte("port: 8317\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return NewHandler(cfg, configPath, nil), configPath
}

func TestDefaultReasoningOnIngressByFormat_Get(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{SDKConfig: config.SDKConfig{DefaultReasoningOnIngressByFormat: map[string]config.ReasoningIngressDefault{
		"openai": {
			Policy: "missing_only",
			Mode:   "effort",
			Value:  "xhigh",
		},
	}}}
	h, _ := newReasoningDefaultsTestHandler(t, cfg)
	router := gin.New()
	router.GET("/v0/management/default-reasoning-on-ingress-by-format", h.GetDefaultReasoningOnIngressByFormat)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/default-reasoning-on-ingress-by-format", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d", rec.Code, http.StatusOK)
	}

	var payload struct {
		Defaults map[string]config.ReasoningIngressDefault `json:"default-reasoning-on-ingress-by-format"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}

	entry, ok := payload.Defaults["openai"]
	if !ok {
		t.Fatalf("response missing openai entry: %s", rec.Body.String())
	}
	if entry.Policy != "missing_only" || entry.Mode != "effort" || entry.Value != "xhigh" {
		t.Fatalf("openai entry = %+v", entry)
	}
}

func TestDefaultReasoningOnIngressByFormat_Put(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	h, configPath := newReasoningDefaultsTestHandler(t, cfg)
	router := gin.New()
	router.PUT("/v0/management/default-reasoning-on-ingress-by-format", h.PutDefaultReasoningOnIngressByFormat)

	req := httptest.NewRequest(
		http.MethodPut,
		"/v0/management/default-reasoning-on-ingress-by-format",
		bytes.NewBufferString(`{"value":{" OPENAI ":{"policy":" MISSING_ONLY ","mode":" effort ","value":" XHIGH "},"claude":{"policy":"force_override","mode":"adaptive_effort","value":"medium"}}}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	openAIEntry, ok := cfg.DefaultReasoningOnIngressByFormat[config.ReasoningIngressFormatOpenAI]
	if !ok {
		t.Fatalf("cfg missing %q entry", config.ReasoningIngressFormatOpenAI)
	}
	if openAIEntry.Policy != "missing_only" || openAIEntry.Mode != "effort" || openAIEntry.Value != "xhigh" {
		t.Fatalf("openai entry = %+v", openAIEntry)
	}

	claudeEntry, ok := cfg.DefaultReasoningOnIngressByFormat[config.ReasoningIngressFormatClaude]
	if !ok {
		t.Fatalf("cfg missing %q entry", config.ReasoningIngressFormatClaude)
	}
	if claudeEntry.Policy != "force_override" || claudeEntry.Mode != "adaptive_effort" || claudeEntry.Value != "medium" {
		t.Fatalf("claude entry = %+v", claudeEntry)
	}

	persisted, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read persisted config: %v", err)
	}
	if !bytes.Contains(persisted, []byte("default-reasoning-on-ingress-by-format")) {
		t.Fatalf("persisted config missing defaults key: %s", string(persisted))
	}
}

func TestDefaultReasoningOnIngressByFormat_PutInvalid(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	h, _ := newReasoningDefaultsTestHandler(t, cfg)
	router := gin.New()
	router.PUT("/v0/management/default-reasoning-on-ingress-by-format", h.PutDefaultReasoningOnIngressByFormat)

	req := httptest.NewRequest(
		http.MethodPut,
		"/v0/management/default-reasoning-on-ingress-by-format",
		bytes.NewBufferString(`{"value":{"openai":{"policy":"missing_only","mode":"effort","value":"ultra"}}}`),
	)
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusBadRequest, rec.Body.String())
	}
	if len(cfg.DefaultReasoningOnIngressByFormat) != 0 {
		t.Fatalf("cfg defaults = %+v, want empty", cfg.DefaultReasoningOnIngressByFormat)
	}
}

func TestReasoningIngressOptions_Get(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cfg := &config.Config{}
	h, _ := newReasoningDefaultsTestHandler(t, cfg)
	router := gin.New()
	router.GET("/v0/management/reasoning-ingress-options", h.GetReasoningIngressOptions)

	req := httptest.NewRequest(http.MethodGet, "/v0/management/reasoning-ingress-options", nil)
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want %d, body=%s", rec.Code, http.StatusOK, rec.Body.String())
	}

	var payload struct {
		Formats []struct {
			Format   string   `json:"format"`
			Policies []string `json:"policies"`
			Modes    []struct {
				Mode   string   `json:"mode"`
				Values []string `json:"values"`
			} `json:"modes"`
		} `json:"formats"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(payload.Formats) == 0 {
		t.Fatalf("formats should not be empty")
	}

	foundOpenAI := false
	for _, format := range payload.Formats {
		if format.Format != config.ReasoningIngressFormatOpenAI {
			continue
		}
		foundOpenAI = true
		if len(format.Policies) == 0 || len(format.Modes) == 0 {
			t.Fatalf("openai options malformed: %+v", format)
		}
	}
	if !foundOpenAI {
		t.Fatalf("formats missing %q: %s", config.ReasoningIngressFormatOpenAI, rec.Body.String())
	}
}
