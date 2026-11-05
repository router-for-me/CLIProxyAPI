package claude_test

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	configaccess "github.com/router-for-me/CLIProxyAPI/v8/internal/access/config_access"
	api "github.com/router-for-me/CLIProxyAPI/v8/internal/api"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	access "github.com/router-for-me/CLIProxyAPI/v8/sdk/access"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/api/handlers/claude"
	auth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	executor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	config "github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type routedCooldownError struct{}

func (routedCooldownError) Error() string              { return "synthetic routed rejection" }
func (routedCooldownError) StatusCode() int            { return 429 }
func (routedCooldownError) RetryAfter() *time.Duration { delay := time.Hour; return &delay }

type routedCooldownExecutor struct{ auth.ProviderExecutor }

func (routedCooldownExecutor) Identifier() string { return "claude" }
func (routedCooldownExecutor) Execute(ctx context.Context, _ *auth.Auth, _ executor.Request, _ executor.Options) (executor.Response, error) {
	executor.MarkUpstreamAttempt(ctx)
	return executor.Response{}, routedCooldownError{}
}

// A frontend provider may validate the actual token yet return a public account
// identity. The middleware is required to preserve this distinction.
type routedFrontendProvider struct{ provider, principal string }

func (p routedFrontendProvider) Identifier() string { return p.provider }
func (p routedFrontendProvider) Authenticate(_ context.Context, r *http.Request) (*access.Result, *access.AuthError) {
	if r.Header.Get("Authorization") != "Bearer synthetic-downstream-token" {
		return nil, access.NewInvalidCredentialError()
	}
	return &access.Result{Provider: p.provider, Principal: p.principal}, nil
}

func TestProxyCooldownRoutedAuthenticationProvenance(t *testing.T) {
	gin.SetMode(gin.TestMode)
	const token = "synthetic-downstream-token"
	cfg := &config.SDKConfig{APIKeys: []string{token}}
	// Use the real built-in validator without leaving a change to the access registry.
	var previous access.Provider
	for _, p := range access.RegisteredProviders() {
		if p.Identifier() == access.DefaultAccessProviderName {
			previous = p
		}
	}
	configaccess.Register(cfg)
	t.Cleanup(func() {
		configaccess.Register(nil)
		if previous != nil {
			access.RegisterProvider(access.AccessProviderTypeConfigAPIKey, previous)
		}
	})
	var native access.Provider
	for _, p := range access.RegisteredProviders() {
		if p.Identifier() == access.DefaultAccessProviderName {
			native = p
		}
	}
	if native == nil {
		t.Fatal("missing built-in config-key provider")
	}

	manager := auth.NewManager(nil, &auth.FillFirstSelector{}, nil)
	manager.SetRetryConfig(0, 0, 0)
	manager.RegisterExecutor(routedCooldownExecutor{})
	id, model := t.Name(), "claude-routed-cooldown-model"
	registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: model}})
	t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	if _, err := manager.Register(context.Background(), &auth.Auth{ID: id, Provider: "claude", Status: auth.StatusActive}); err != nil {
		t.Fatal(err)
	}
	_, err := manager.Execute(context.Background(), []string{"claude"}, executor.Request{Model: model}, executor.Options{})
	deadline, ok := auth.ProxyCooldownNextReevaluation(err)
	if !ok {
		t.Fatalf("missing concrete local summary: %v", err)
	}
	msg := handlers.ExecutionErrorMessage(err)
	handler := claude.NewClaudeCodeAPIHandler(handlers.NewBaseAPIHandlers(cfg, manager))
	epoch := deadline.Unix()
	if deadline.Nanosecond() > 0 {
		epoch++
	}
	canonical := strconv.FormatInt(epoch, 10)
	mac := hmac.New(sha256.New, []byte(token))
	_, _ = mac.Write([]byte("CLIProxyAPI/T3/proxy_cooldown/v1\nnext_reevaluation_at=" + canonical + "\n"))
	expectedPrefix := fmt.Sprintf("[[CPA_PROXY_COOLDOWN_V1:%s:%x]] ", canonical, mac.Sum(nil))
	for _, tc := range []struct {
		name     string
		provider access.Provider
		signed   bool
	}{
		{"built-in", native, true},
		{"plugin-public-principal", routedFrontendProvider{"plugin:example:frontend", "customer-42"}, false},
		{"unknown-provider", routedFrontendProvider{"unknown-provider", token}, false},
		{"missing-provenance", routedFrontendProvider{"", token}, false},
		{"unconfigured-native-principal", routedFrontendProvider{access.DefaultAccessProviderName, "customer-42"}, false},
		{"absent-auth", nil, false},
	} {
		for _, stream := range []bool{false, true} {
			transport := map[bool]string{false: "json", true: "terminal-sse"}[stream]
			t.Run(tc.name+"/"+transport, func(t *testing.T) {
				accessManager := access.NewManager()
				if tc.provider != nil {
					accessManager.SetProviders([]access.Provider{tc.provider})
				}
				router := gin.New()
				router.Use(api.AuthMiddleware(accessManager))
				router.POST("/v1/messages", func(c *gin.Context) { claude.WriteProxyCooldownTestResponse(handler, c, msg, stream) })
				rec := httptest.NewRecorder()
				req := httptest.NewRequest("POST", "/v1/messages", nil)
				// Even absent/unknown provenance must not fall back to this inbound token.
				req.Header.Set("Authorization", "Bearer "+token)
				req.Header.Set("X-CLIProxyAPI-Cooldown-Metadata", "signed-v1")
				router.ServeHTTP(rec, req)
				payload := rec.Body.String()
				if stream {
					start := strings.Index(payload, "data: ")
					if start < 0 {
						t.Fatalf("missing terminal SSE payload: %q", payload)
					}
					payload = strings.TrimSpace(payload[start+len("data: "):])
				} else if rec.Code != 429 {
					t.Fatalf("status=%d", rec.Code)
				}
				var body struct {
					Error struct {
						Message string `json:"message"`
					} `json:"error"`
				}
				if err := json.Unmarshal([]byte(payload), &body); err != nil {
					t.Fatal(err)
				}
				expected := "synthetic routed rejection"
				if tc.signed {
					expected = expectedPrefix + expected
				}
				if body.Error.Message != expected {
					t.Fatalf("provider provenance: signed=%t message=%q", tc.signed, body.Error.Message)
				}
			})
		}
	}
}
