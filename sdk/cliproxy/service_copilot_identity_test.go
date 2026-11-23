package cliproxy

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/openai"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestCopilotCanonicalSourceSettings(t *testing.T) {
	const native = "gpt-6-sol"
	const source = "copilot/" + native
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{copilot.Provider: {
			{Name: source, Alias: "fast", Fork: true, ForceMapping: true},
		}},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.SetOAuthModelAlias(cfg.OAuthModelAlias)
	service := &Service{cfg: cfg, coreManager: manager}
	auth := registerCopilotRoutingTestAuth(t, manager, "team", native)
	handler := openai.NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
	for _, tc := range []struct {
		name                  string
		settings              []config.OAuthModelSetting
		sourceSize, aliasSize int
	}{
		{name: "template window"},
		{name: "bare source", settings: []config.OAuthModelSetting{{Name: native, MaxContextLength: 131072}}, sourceSize: 131072, aliasSize: 131072},
		{name: "qualified source", settings: []config.OAuthModelSetting{{Name: source, MaxContextLength: 131072}}, sourceSize: 131072, aliasSize: 131072},
		{name: "alias override", settings: []config.OAuthModelSetting{
			{Name: source, MaxContextLength: 131072},
			{Name: source, Alias: "fast", MaxContextLength: 524288},
		}, sourceSize: 131072, aliasSize: 524288},
		{name: "template restored"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg.OAuthSettings = map[string][]config.OAuthModelSetting{copilot.Provider: tc.settings}
			if !service.refreshModelRegistrationForAuth(auth) {
				t.Fatal("failed to publish Copilot catalog")
			}
			models := GlobalModelRegistry().GetModelsForClient(auth.ID)
			if len(models) != 4 {
				t.Fatalf("published %d models, want original, alias and their prefixed routes", len(models))
			}
			catalog := openai.CodexClientModelsResponseForClient(handler.Models(), "0.153.4", false)
			entries := catalog["models"].([]map[string]any)
			for _, model := range models {
				wantSize := tc.sourceSize
				switch model.ID {
				case "fast", "team/fast":
					wantSize = tc.aliasSize
				case source, "team/" + source:
				default:
					t.Fatalf("unexpected route %q", model.ID)
				}
				if model.SourceModelID != source || model.MetadataModelID != native || model.MaxContextLength != wantSize {
					t.Fatalf("unexpected metadata for %s: %+v", model.ID, model)
				}
				if got := manager.ResolveExecutionModel(auth, model.ID+"(high)"); got != source+"(high)" {
					t.Fatalf("execution model for %s = %s", model.ID, got)
				}
				window, maxWindow := wantSize, wantSize
				if wantSize == 0 {
					window, maxWindow = 272000, 872000
				}
				idx := slices.IndexFunc(entries, func(entry map[string]any) bool { return entry["slug"] == model.ID })
				if idx < 0 {
					t.Fatalf("Codex catalog is missing route %q", model.ID)
				}
				entry := entries[idx]
				if entry["tool_mode"] != "code_mode_only" || fmt.Sprint(entry["context_window"]) != fmt.Sprint(window) ||
					fmt.Sprint(entry["max_context_window"]) != fmt.Sprint(maxWindow) {
					t.Fatalf("Codex template for %s: tool mode=%v context=%v max=%v", model.ID, entry["tool_mode"], entry["context_window"], entry["max_context_window"])
				}
			}
		})
	}
}

func TestCopilotCanonicalSourcePayloadRules(t *testing.T) {
	const native = "gpt-6-astra"
	const source = "copilot/" + native
	cfg := &config.Config{
		OAuthModelAlias: map[string][]config.OAuthModelAlias{"copilot": {
			{Name: source, Alias: "coding", Fork: true},
		}},
	}
	cfg.Payload.Override = []config.PayloadRule{
		{Models: []config.PayloadModelRule{{Name: source}}, Params: map[string]any{"temperature": 0.125}},
		{Models: []config.PayloadModelRule{{Name: native}}, Params: map[string]any{"top_p": 0.875}},
		{Models: []config.PayloadModelRule{{Name: "team/coding(high)"}}, Params: map[string]any{"temperature": 0.25}},
	}
	manager := coreauth.NewManager(nil, nil, nil)
	manager.SetConfig(cfg)
	manager.SetOAuthModelAlias(cfg.OAuthModelAlias)
	manager.SetRetryConfig(0, 0, 0)
	upstreamRequests := make(chan ojson.Result, 8)
	manager.SetRoundTripperProvider(copilotAccountTransport(func(*coreauth.Auth) http.RoundTripper {
		return copilotCatalogTransport(func(req *http.Request) (*http.Response, error) {
			if req.URL.Path != copilot.Responses {
				return nil, fmt.Errorf("unexpected Copilot endpoint %q", req.URL.Path)
			}
			body, errRead := io.ReadAll(req.Body)
			if errRead != nil {
				return nil, errRead
			}
			upstreamRequests <- ojson.ParseBytes(body)
			response := `{"id":"resp_test","object":"response","created_at":1,"status":"completed","model":"gpt-6-astra","output":[{"type":"message","role":"assistant","content":[{"type":"output_text","text":"hello","annotations":[]}]}]}`
			return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response)), Request: req}, nil
		})
	}))
	auth, errRegister := manager.Register(t.Context(), &coreauth.Auth{
		ID: t.Name(), Provider: "copilot", Prefix: "team", Status: coreauth.StatusActive,
		CopilotCatalog: copilot.NewCatalog([]copilot.Model{{ID: native, Endpoints: []string{copilot.Responses}}}, time.Now()),
		Metadata:       map[string]any{"access_token": "test-token", "base_url": "https://api.individual.githubcopilot.com"},
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
	service := &Service{cfg: cfg, coreManager: manager}
	if !service.refreshModelRegistrationForAuth(auth) {
		t.Fatal("failed to publish Copilot catalog")
	}
	router := gin.New()
	handler := openai.NewOpenAIAPIHandler(handlers.NewBaseAPIHandlers(&cfg.SDKConfig, manager))
	router.POST("/v1/chat/completions", handler.ChatCompletions)
	for _, tc := range []struct {
		model       string
		temperature float64
	}{
		{source, 0.125},
		{"coding", 0.125},
		{"coding(high)", 0.125},
		{"team/" + source, 0.125},
		{"team/coding", 0.125},
		{"team/coding(high)", 0.25},
	} {
		t.Run(tc.model, func(t *testing.T) {
			payload := fmt.Sprintf(`{"model":%q,"messages":[{"role":"user","content":"hello"}]}`, tc.model)
			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(payload)).WithContext(t.Context())
			req.Header.Set("Content-Type", "application/json")
			response := httptest.NewRecorder()
			router.ServeHTTP(response, req)
			if response.Code != http.StatusOK {
				t.Fatalf("request returned %d: %s", response.Code, response.Body.String())
			}
			select {
			case wire := <-upstreamRequests:
				if got := wire.Get("model").String(); got != native {
					t.Errorf("upstream model = %q, want %q", got, native)
				}
				if got := wire.Get("temperature").Float(); got != tc.temperature {
					t.Errorf("upstream temperature = %v, want %v", got, tc.temperature)
				}
				if got := wire.Get("top_p").Float(); got != 0.875 {
					t.Errorf("upstream top_p = %v, want 0.875", got)
				}
			default:
				t.Fatal("request did not reach the Copilot upstream")
			}
		})
	}
}
