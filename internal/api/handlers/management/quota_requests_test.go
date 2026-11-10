package management

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/misc"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/multiuser"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestPrepareCredentialQuotaRequestAntigravityUserAgent(t *testing.T) {
	for _, tc := range []struct {
		provider, target string
		changed          bool
	}{
		{"antigravity", "https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary", true},
		{"antigravity", "https://daily-cloudcode-pa.sandbox.googleapis.com/v1internal:retrieveUserQuotaSummary", true},
		{"antigravity", "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary", true},
		{"antigravity", "https://daily-cloudcode-pa.googleapis.com/v1internal:loadCodeAssist", true},
		{"antigravity", "https://example.com/v1internal:retrieveUserQuotaSummary", false},
		{"antigravity", "https://daily-cloudcode-pa.googleapis.com/v1internal:generateContent", false},
		{"codex", "https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary", false},
	} {
		t.Run(tc.provider+tc.target, func(t *testing.T) {
			auth := &coreauth.Auth{Provider: tc.provider}
			target, _ := url.Parse(tc.target)
			headers := map[string]string{"user-agent": "antigravity/cli/1.0.13", "Authorization": "Bearer $TOKEN$"}
			if err := prepareCredentialQuotaRequest(auth, target, headers); err != nil {
				t.Fatal(err)
			}
			if tc.changed {
				if headers["User-Agent"] != misc.AntigravityUserAgent() || headers["user-agent"] != "" {
					t.Fatalf("quota user agent was not replaced with the current Hub version: %v", headers)
				}
			} else if headers["user-agent"] != "antigravity/cli/1.0.13" || headers["User-Agent"] != "" {
				t.Fatal("unrelated request user agent changed")
			}
			if headers["Authorization"] != "Bearer $TOKEN$" {
				t.Fatal("user agent normalization changed the token placeholder")
			}
		})
	}
}

func TestPrepareCredentialQuotaRequestQoderPinsRegionalOpenAPI(t *testing.T) {
	for _, tc := range []struct {
		provider, requested, want string
	}{
		{"qoder", "https://openapi.qoder.com.cn/api/v2/quota/usage", "openapi.qoder.sh"},
		{"qoder-cn", "https://openapi.qoder.sh/api/v2/quota/usage", "openapi.qoder.com.cn"},
		{"qoder-cn", "https://openapi.qoder.com.cn/api/v2/user/plan", "openapi.qoder.com.cn"},
	} {
		auth := &coreauth.Auth{Provider: tc.provider}
		target, _ := url.Parse(tc.requested)
		if err := prepareCredentialQuotaRequest(auth, target, map[string]string{}); err != nil {
			t.Fatal(err)
		}
		if target.Host != tc.want {
			t.Fatalf("%s quota host = %s, want %s", tc.provider, target.Host, tc.want)
		}
	}
	if !hubQuotaRouteAllowed("qoder-cn", "openapi.qoder.com.cn", "/api/v2/quota/usage") || hubQuotaRouteAllowed("qoder", "openapi.qoder.sh", "/api/v1/userinfo") {
		t.Fatal("Qoder hub quota allowlist is wrong")
	}
}

func TestPrepareCredentialQuotaRequestKimiDomain(t *testing.T) {
	for _, tc := range []struct {
		name       string
		attributes map[string]string
		metadata   map[string]any
		host       string
	}{
		{"com overrides filename", map[string]string{"domain": "kimi.com"}, nil, "api.kimi.com"},
		{"ai domain", nil, map[string]any{"domain": "kimi.ai"}, "api.kimi.ai"},
		{"ai base URL", nil, map[string]any{"base_url": "https://api.kimi.ai/coding"}, "api.kimi.ai"},
		{"com metadata type", nil, map[string]any{"type": "kimi"}, "api.kimi.com"},
		{"ai metadata type", nil, map[string]any{"type": "kimi-ai"}, "api.kimi.ai"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			auth := &coreauth.Auth{ID: "kimi-ai.json", Provider: "kimi", Attributes: tc.attributes, Metadata: tc.metadata}
			target, _ := url.Parse("https://api.kimi.com/coding/v1/usages")
			if err := prepareCredentialQuotaRequest(auth, target, nil); err != nil {
				t.Fatal(err)
			}
			if target.Host != tc.host {
				t.Fatalf("quota host = %q, want %q", target.Host, tc.host)
			}
		})
	}
}

func TestPrepareCredentialQuotaRequestKimiRegionalProviders(t *testing.T) {
	for _, provider := range []string{"kimi-ai", "kimi_ai", "kimi.ai"} {
		auth := &coreauth.Auth{Provider: provider}
		target, _ := url.Parse("https://api.kimi.com/coding/v1/usages")
		if !hubQuotaRouteAllowed(provider, target.Hostname(), target.Path) {
			t.Fatalf("regional provider %q cannot query quota", provider)
		}
		if err := prepareCredentialQuotaRequest(auth, target, nil); err != nil || target.Host != "api.kimi.ai" {
			t.Fatalf("regional provider %q resolves to %q: %v", provider, target.Host, err)
		}
	}
}

func TestPrepareCredentialQuotaRequestMetaDCA(t *testing.T) {
	target, _ := url.Parse("https://api.meta.ai/muse-code/key")
	auth := &coreauth.Auth{Provider: "meta", Metadata: map[string]any{"dca_token": " dca:quota ", "api_key": "LLM|inference"}}
	headers := map[string]string{"Authorization": "Bearer $DCA_TOKEN$"}
	if err := prepareCredentialQuotaRequest(auth, target, headers); err != nil || headers["Authorization"] != "Bearer dca:quota" {
		t.Fatalf("DCA substitution failed: %v", err)
	}
	for _, token := range []string{"", "LLM|inference", "dca:bad\r\nheader"} {
		auth.Metadata["dca_token"] = token
		headers["Authorization"] = "Bearer $DCA_TOKEN$"
		if err := prepareCredentialQuotaRequest(auth, target, headers); err == nil {
			t.Fatal("missing or invalid DCA must not fall back to the inference key")
		}
	}
}

func TestQuotaAPICallsWithoutCredentialExport(t *testing.T) {
	var receivedHost, receivedAuthorization, receivedUserAgent string
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedHost, receivedAuthorization, receivedUserAgent = r.Host, r.Header.Get("Authorization"), r.Header.Get("User-Agent")
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"limits":[],"subs_usage":{}}`)
	}))
	defer upstream.Close()
	transport := upstream.Client().Transport.(*http.Transport).Clone()
	transport.TLSClientConfig.ServerName = "127.0.0.1"
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}
	original := http.DefaultTransport
	http.DefaultTransport = transport
	t.Cleanup(func() { http.DefaultTransport = original; transport.CloseIdleConnections() })

	store, errOpen := multiuser.Open(t.TempDir(), time.Hour)
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	for _, tc := range []struct {
		provider, domain, target, token, host, method string
	}{
		{"kimi", "kimi.com", "https://api.kimi.ai/coding/v1/usages", "$TOKEN$", "api.kimi.com", "GET"},
		{"kimi", "kimi.ai", "https://api.kimi.com/coding/v1/usages", "$TOKEN$", "api.kimi.ai", "GET"},
		{"meta", "", "https://api.meta.ai/muse-code/key", "$DCA_TOKEN$", "api.meta.ai", "POST"},
		{"antigravity", "", "https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary", "$TOKEN$", "daily-cloudcode-pa.googleapis.com", "POST"},
	} {
		t.Run(tc.provider+tc.domain, func(t *testing.T) {
			manager := coreauth.NewManager(nil, nil, nil)
			auth, errRegister := manager.Register(coreauth.WithSkipPersist(context.Background()), &coreauth.Auth{
				ID: "quota.json", Provider: tc.provider, Metadata: map[string]any{
					"owner": "alice", "channel_id": "channel", "domain": tc.domain,
					"access_token": "kimi-token", "dca_token": "dca:meta-quota", "api_key": "LLM|not-for-quota",
					"expired": time.Now().Add(time.Hour).Format(time.RFC3339),
				},
			})
			if errRegister != nil {
				t.Fatal(errRegister)
			}
			h := &Handler{cfg: &config.Config{}, authManager: manager, hubStore: store}
			route := base64.RawURLEncoding.EncodeToString([]byte(auth.ID))
			router := gin.New()
			router.Use(func(c *gin.Context) {
				c.Set("hubUser", multiuser.User{Username: c.GetHeader("X-Test-User"), Role: "member"})
			})
			router.POST("/management", h.APICall)
			router.POST("/credentials/:id/api-call", h.HubQuotaAPICall)
			router.GET("/credentials/:id/download", h.HubDownloadCredential)
			body, _ := json.Marshal(map[string]any{
				"authIndex": auth.EnsureIndex(), "method": tc.method, "url": tc.target,
				"header": map[string]string{"Authorization": "Bearer " + tc.token, "User-Agent": "antigravity/cli/1.0.13"}, "data": "{}",
			})
			for _, endpoint := range []string{"/management", "/credentials/" + route + "/api-call"} {
				for _, username := range []string{"alice", "bob"} {
					recorder := httptest.NewRecorder()
					req := httptest.NewRequest(http.MethodPost, endpoint, strings.NewReader(string(body)))
					req.Header.Set("Content-Type", "application/json")
					req.Header.Set("X-Test-User", username)
					router.ServeHTTP(recorder, req)
					if recorder.Code != http.StatusOK || receivedHost != tc.host {
						t.Fatalf("%s quota query at %s: status=%d host=%q body=%s", username, endpoint, recorder.Code, receivedHost, recorder.Body.String())
					}
					expected := "Bearer kimi-token"
					if tc.provider == "meta" {
						expected = "Bearer dca:meta-quota"
					}
					if receivedAuthorization != expected {
						t.Fatal("quota request did not use its provider-specific token")
					}
					if tc.provider == "antigravity" && receivedUserAgent != misc.AntigravityUserAgent() {
						t.Fatalf("%s forwarded a stale Antigravity user agent: %q", endpoint, receivedUserAgent)
					}
				}
			}
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/credentials/"+route+"/download", nil)
			req.Header.Set("X-Test-User", "bob")
			router.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusForbidden {
				t.Fatalf("another member's credential export must remain forbidden: %d", recorder.Code)
			}
		})
	}
}

func TestHubCredentialsIncludesParsedCodexClaims(t *testing.T) {
	store, errOpen := multiuser.Open(t.TempDir(), time.Hour)
	if errOpen != nil {
		t.Fatal(errOpen)
	}
	manager := coreauth.NewManager(nil, nil, nil)
	claims := `{"https://api.openai.com/auth":{"chatgpt_account_id":"account-fixture","chatgpt_plan_type":"pro"}}`
	jwt := "e30." + base64.RawURLEncoding.EncodeToString([]byte(claims)) + ".signature"
	_, errRegister := manager.Register(coreauth.WithSkipPersist(context.Background()), &coreauth.Auth{
		ID: "codex.json", Provider: "codex", Attributes: map[string]string{"path": t.TempDir() + "/codex.json"},
		Metadata: map[string]any{"owner": "alice", "id_token": jwt, "access_token": "secret-token"},
	})
	if errRegister != nil {
		t.Fatal(errRegister)
	}
	h := &Handler{cfg: &config.Config{}, authManager: manager, hubStore: store}
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/credentials", nil)
	c.Set("hubUser", multiuser.User{Username: "bob", Role: "member"})
	h.HubCredentials(c)
	var payload struct {
		Credentials []struct {
			Claims map[string]any `json:"id_token"`
		} `json:"credentials"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Credentials) != 1 || payload.Credentials[0].Claims["chatgpt_account_id"] != "account-fixture" {
		t.Fatalf("missing parsed account claims: %s", recorder.Body.String())
	}
	if strings.Contains(recorder.Body.String(), jwt) || strings.Contains(recorder.Body.String(), "secret-token") {
		t.Fatal("credential summary exposed a raw token")
	}
}
