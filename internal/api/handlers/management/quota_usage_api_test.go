package management

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestQuotaUsageAPICallRecovery(t *testing.T) {
	claudeUsage := `{"five_hour":{"utilization":20},"seven_day":{"utilization":10}}`
	codexUsage := `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":20},"secondary_window":{"used_percent":10}}}`
	for _, tc := range []struct {
		name, provider, url, response string
		status                        int
		legacy, redirect, recover     bool
		headers                       map[string]string
		mutate                        func(*coreauth.Auth)
	}{
		{name: "claude capacity", provider: "claude", recover: true},
		{name: "codex capacity", provider: "codex", response: codexUsage, recover: true},
		{name: "legacy unchanged", provider: "claude", legacy: true},
		{name: "exhausted", provider: "claude", response: `{"five_hour":{"utilization":100},"seven_day":{"utilization":10}}`},
		{name: "malformed", provider: "claude", response: `{"five_hour":`},
		{name: "missing capacity", provider: "claude", response: `{}`},
		{name: "upstream unauthorized", provider: "claude", status: http.StatusUnauthorized},
		{name: "upstream quota rejected", provider: "claude", status: http.StatusTooManyRequests},
		{name: "redirected", provider: "claude", redirect: true},
		{name: "different endpoint", provider: "claude", url: "https://api.anthropic.com/api/oauth/profile"},
		{name: "different host", provider: "claude", url: "https://untrusted.example/api/oauth/usage"},
		{name: "query", provider: "claude", url: "https://api.anthropic.com/api/oauth/usage?cedar_ember=1"},
		{name: "host override", provider: "claude", headers: map[string]string{"Host": "untrusted.example"}},
		{name: "bearer override", provider: "claude", headers: map[string]string{"Authorization": "Bearer different-token"}},
		{name: "account override", provider: "codex", response: codexUsage, headers: map[string]string{"Chatgpt-Account-Id": "different-account"}},
		{name: "missing account selector", provider: "codex", response: codexUsage, headers: map[string]string{"Chatgpt-Account-Id": ""}},
		{name: "newer failure", provider: "claude", mutate: func(a *coreauth.Auth) {
			a.NextRetryAfter = a.NextRetryAfter.Add(time.Hour)
			a.Quota.NextRecoverAt = a.NextRetryAfter
			a.LastError = &coreauth.Error{HTTPStatus: 429, Message: "newer quota"}
		}},
		{name: "disabled during fetch", provider: "claude", mutate: func(a *coreauth.Auth) { a.Disabled = true }},
		{name: "unrelated failure during fetch", provider: "claude", mutate: func(a *coreauth.Auth) { a.LastError = &coreauth.Error{HTTPStatus: 403, Message: "forbidden"} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			manager := coreauth.NewManager(nil, nil, nil)
			deadline := time.Now().Add(time.Hour).Round(0)
			auth, err := manager.Register(context.Background(), &coreauth.Auth{
				ID: "usage-" + tc.provider, Provider: tc.provider, Status: coreauth.StatusError, Unavailable: true,
				Metadata:  map[string]any{"access_token": "selected-token", "account_id": "selected-account"},
				LastError: &coreauth.Error{HTTPStatus: 429, Message: "quota exceeded"},
				Quota:     coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: deadline}, NextRetryAfter: deadline,
			})
			if err != nil {
				t.Fatal(err)
			}
			expected := auth.Clone()
			usage := tc.response
			if usage == "" {
				usage = claudeUsage
			}
			status := tc.status
			if status == 0 {
				status = http.StatusOK
			}
			calls := 0
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if tc.mutate != nil {
					current, _ := manager.GetByID(auth.ID)
					tc.mutate(current)
					if _, errUpdate := manager.Update(context.Background(), current); errUpdate != nil {
						t.Error(errUpdate)
					}
					expected, _ = manager.GetByID(auth.ID)
				}
				if tc.redirect && calls == 1 {
					http.Redirect(w, r, "/redirected", http.StatusFound)
					return
				}
				w.Header().Set("X-Upstream-Evidence", "preserved")
				w.WriteHeader(status)
				_, _ = io.WriteString(w, usage)
			}))
			defer upstream.Close()
			oldTransport := http.DefaultTransport
			transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
			}}
			http.DefaultTransport = transport
			defer func() { http.DefaultTransport = oldTransport; transport.CloseIdleConnections() }()
			url := tc.url
			if url == "" {
				url = "https://api.anthropic.com/api/oauth/usage"
				if tc.provider == "codex" {
					url = "https://chatgpt.com/backend-api/wham/usage"
				}
			}
			headers := map[string]string{"Authorization": "Bearer $TOKEN$"}
			if tc.provider == "codex" {
				headers["Chatgpt-Account-Id"] = "selected-account"
			}
			for k, v := range tc.headers {
				headers[k] = v
			}
			body, err := json.Marshal(map[string]any{"authIndex": auth.EnsureIndex(), "method": "GET", "url": url, "header": headers})
			if err != nil {
				t.Fatal(err)
			}
			h := &Handler{authManager: manager}
			router := gin.New()
			if tc.legacy {
				router.POST("/", h.APICall)
			} else {
				router.POST("/", h.APICallV8)
			}
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, req)
			if recorder.Code != http.StatusOK {
				t.Fatalf("management response=%d: %s", recorder.Code, recorder.Body.String())
			}
			var response apiCallResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.StatusCode != status || response.Body != usage || response.Header["X-Upstream-Evidence"][0] != "preserved" {
				t.Fatalf("forwarded response changed: %+v", response)
			}
			current, _ := manager.GetByID(auth.ID)
			if tc.recover {
				if current.Quota.Exceeded || current.Unavailable || !current.NextRetryAfter.IsZero() || current.LastError != nil {
					t.Fatalf("quota remains blocked: %+v", current)
				}
			} else if !reflect.DeepEqual(current, expected) {
				t.Fatalf("unproven recovery changed credential: current=%+v expected=%+v", current, expected)
			}
			wantCalls := 1
			if tc.redirect {
				wantCalls = 2
			}
			if calls != wantCalls {
				t.Fatalf("upstream calls=%d, want %d", calls, wantCalls)
			}
		})
	}
}

func TestQuotaUsageRequestIdentity(t *testing.T) {
	base := &coreauth.Auth{Provider: "claude", Metadata: map[string]any{"access_token": "selected"}}
	for _, tc := range []struct {
		name   string
		change func(*http.Request, *coreauth.Auth)
		want   bool
	}{
		{name: "selected bearer", want: true},
		{name: "post", change: func(r *http.Request, _ *coreauth.Auth) { r.Method = http.MethodPost }},
		{name: "body", change: func(r *http.Request, _ *coreauth.Auth) { r.Body = io.NopCloser(strings.NewReader("probe")) }},
		{name: "userinfo", change: func(r *http.Request, _ *coreauth.Auth) { r.URL.User = url.User("other") }},
		{name: "duplicate bearer", change: func(r *http.Request, _ *coreauth.Auth) { r.Header.Add("Authorization", "Bearer selected") }},
		{name: "cookie", change: func(r *http.Request, _ *coreauth.Auth) { r.Header.Set("Cookie", "other") }},
		{name: "api key", change: func(r *http.Request, _ *coreauth.Auth) { r.Header.Set("X-Api-Key", "other") }},
		{name: "unknown provider", change: func(_ *http.Request, a *coreauth.Auth) { a.Provider = "other" }},
		{name: "no oauth token", change: func(_ *http.Request, a *coreauth.Auth) { a.Metadata = nil }},
		{name: "codex bearer default account", want: true, change: func(r *http.Request, a *coreauth.Auth) {
			a.Provider = "codex"
			r.URL.Host = "chatgpt.com"
			r.Host = "chatgpt.com"
			r.URL.Path = "/backend-api/wham/usage"
		}},
		{name: "codex unknown account override", change: func(r *http.Request, a *coreauth.Auth) {
			a.Provider = "codex"
			r.URL.Host = "chatgpt.com"
			r.Host = "chatgpt.com"
			r.URL.Path = "/backend-api/wham/usage"
			r.Header.Set("Chatgpt-Account-Id", "unknown")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req, _ := http.NewRequest(http.MethodGet, "https://api.anthropic.com/api/oauth/usage", nil)
			req.Header.Set("Authorization", "Bearer selected")
			a := base.Clone()
			if tc.change != nil {
				tc.change(req, a)
			}
			if got := quotaUsageRequestMatches(req, a); got != tc.want {
				t.Fatalf("request trusted=%v, want %v", got, tc.want)
			}
		})
	}
	if quotaUsageRequestMatches(nil, base) {
		t.Fatal("nil request trusted")
	}
}
