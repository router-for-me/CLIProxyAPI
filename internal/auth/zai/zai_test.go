package zai

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func TestServiceExchangeCode(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/token" {
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["provider"] != "zai" || body["state"] != "state" {
				t.Fatalf("unexpected token request: %#v, err=%v", body, err)
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"token":"zcode-jwt","zai":{"access_token":"oauth-token"},"user":{"user_id":"u1","email":"user@example.com"}}}`))
			return
		}
		if r.URL.Path == "/business" {
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body["token"] != "oauth-token" {
				t.Fatalf("unexpected business request: %#v, err=%v", body, err)
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"access_token":"business-token","expires_in":900}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	service := NewService(server.Client())
	service.SetEndpoints("", server.URL+"/token", server.URL+"/business", server.URL+"/userinfo")
	bundle, err := service.ExchangeCode(context.Background(), "code", "http://127.0.0.1/callback", "state")
	if err != nil {
		t.Fatalf("ExchangeCode() error = %v", err)
	}
	if bundle.BusinessToken != "business-token" || bundle.OAuthToken != "oauth-token" || bundle.User.Email != "user@example.com" || bundle.ExpiresIn != 900 {
		t.Fatalf("unexpected bundle: %#v", bundle)
	}
}

func TestBuildAuthorizeURL(t *testing.T) {
	service := NewService(nil)
	parsed, err := url.Parse(service.BuildAuthorizeURL("state value", "http://127.0.0.1/callback"))
	if err != nil {
		t.Fatal(err)
	}
	if parsed.Query().Get("client_id") != DefaultAppID || parsed.Query().Get("state") != "state value" || parsed.Query().Get("redirect_uri") == "" {
		t.Fatalf("unexpected authorize URL: %s", parsed)
	}
}

func TestBuildAuthorizeURLDefaultRedirect(t *testing.T) {
	// Z.AI rejects every loopback redirect URI for this client; the
	// desktop scheme must be the default.
	service := NewService(nil)
	parsed, err := url.Parse(service.BuildAuthorizeURL("state", ""))
	if err != nil {
		t.Fatal(err)
	}
	if got := parsed.Query().Get("redirect_uri"); got != DefaultRedirectURI {
		t.Fatalf("default redirect_uri = %q, want %q", got, DefaultRedirectURI)
	}
}

func TestInitFlowAndWaitForFlow(t *testing.T) {
	polls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if r.URL.Path == "/oauth/cli/init" {
			if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
				t.Fatalf("init request missing bearer token: %q", auth)
			}
			_, _ = w.Write([]byte(`{"code":0,"data":{"flow_id":"flow-1","poll_token":"server-token","authorize_url":"https://chat.z.ai/api/oauth/authorize?x=1","expires_at":0,"poll_interval_sec":0}}`))
			return
		}
		if r.URL.Path == "/oauth/cli/poll/flow-1" {
			polls++
			if auth != "Bearer server-token" {
				t.Fatalf("poll request bearer = %q", auth)
			}
			status := "pending"
			if polls >= 2 {
				status = "ready"
			}
			payload, _ := json.Marshal(map[string]any{
				"code": 0,
				"data": map[string]any{
					"status": status,
					"token":  "zcode-jwt",
					"user":   map[string]any{"user_id": "u1", "email": "user@example.com", "name": "User"},
					"zai":    map[string]any{"access_token": "oauth-token"},
				},
			})
			_, _ = w.Write(payload)
			return
		}
		http.NotFound(w, r)
	}))
	defer server.Close()

	service := NewService(server.Client())
	service.SetCLIBaseURL(server.URL)
	flow, err := service.InitFlow(context.Background())
	if err != nil {
		t.Fatalf("InitFlow() error = %v", err)
	}
	if flow.FlowID != "flow-1" || flow.PollToken != "server-token" || flow.AuthorizeURL == "" {
		t.Fatalf("unexpected flow: %#v", flow)
	}
	bundle, err := service.WaitForFlow(context.Background(), flow)
	if err != nil {
		t.Fatalf("WaitForFlow() error = %v", err)
	}
	if bundle.OAuthToken != "oauth-token" || bundle.ZCodeToken != "zcode-jwt" || bundle.User.Email != "user@example.com" {
		t.Fatalf("unexpected bundle: %#v", bundle)
	}
	if polls < 2 {
		t.Fatalf("expected at least 2 polls, got %d", polls)
	}
}

func TestMintAPIKeySendsSingleBearerPrefix(t *testing.T) {
	var authHeaders []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authHeaders = append(authHeaders, r.Header.Get("Authorization"))
		switch {
		case strings.HasSuffix(r.URL.Path, "/customer/getCustomerInfo"):
			_, _ = w.Write([]byte(`{"code":0,"data":{"organizations":[{"organizationId":"org-1","organizationName":"默认机构","projects":[{"projectId":"proj-1","projectName":"默认项目"}]}]}}`))
		case strings.HasSuffix(r.URL.Path, "/api_keys"):
			_, _ = w.Write([]byte(`{"code":0,"data":{"apiKey":"zk-key"}}`))
		case strings.Contains(r.URL.Path, "/copy/"):
			_, _ = w.Write([]byte(`{"code":0,"data":{"secretKey":"sk-secret"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	service := NewService(server.Client())
	service.SetBizBaseURL(server.URL)
	minted, err := service.MintAPIKey(context.Background(), "biz-token")
	if err != nil {
		t.Fatalf("MintAPIKey() error = %v", err)
	}
	if minted != "zk-key.sk-secret" {
		t.Fatalf("minted = %q", minted)
	}
	if len(authHeaders) == 0 {
		t.Fatalf("no requests captured")
	}
	for _, header := range authHeaders {
		if header != "Bearer biz-token" {
			t.Fatalf("authorization = %q, want single Bearer prefix", header)
		}
	}
}
