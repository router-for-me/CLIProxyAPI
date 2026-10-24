package api

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestManagementV8QuotaUsageRoute(t *testing.T) {
	const usage = `{"five_hour":{"utilization":20},"seven_day":{"utilization":10}}`
	var calls atomic.Int32
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Host != "api.anthropic.com" || r.URL.Path != "/api/oauth/usage" || r.Header.Get("Authorization") != "Bearer selected-token" {
			t.Errorf("unexpected provider request: host=%s path=%s authorization=%s", r.Host, r.URL.Path, r.Header.Get("Authorization"))
		}
		w.Header().Set("X-Upstream-Evidence", "preserved")
		_, _ = io.WriteString(w, usage)
	}))
	defer upstream.Close()
	oldTransport := http.DefaultTransport
	transport := oldTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true} // Only the local TLS fixture is dialed.
	transport.DialContext = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = oldTransport; transport.CloseIdleConnections() }()
	for _, tc := range []struct {
		name, route                        string
		authorized, enabled, home, recover bool
		want                               int
	}{
		{"v8 recovers", "/v8/management/requests/api-call", true, true, false, true, 200},
		{"legacy preserves", "/v0/management/api-call", true, true, false, false, 200},
		{"v8 missing key", "/v8/management/requests/api-call", false, true, false, false, 401},
		{"legacy missing key", "/v0/management/api-call", false, true, false, false, 401},
		{"v8 disabled", "/v8/management/requests/api-call", true, false, false, false, 404},
		{"v8 home", "/v8/management/requests/api-call", true, true, true, false, 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte("port: 8317\nremote-management: {secret-key: test-password}\n"), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := config.LoadConfig(path)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Home.Enabled = tc.home
			manager := coreauth.NewManager(nil, nil, nil)
			deadline := time.Now().Add(time.Hour).Round(0)
			auth, err := manager.Register(context.Background(), &coreauth.Auth{
				ID: "selected-claude", Provider: "claude", Status: coreauth.StatusError, Unavailable: true,
				Metadata:  map[string]any{"access_token": "selected-token"},
				LastError: &coreauth.Error{HTTPStatus: 429, Message: "quota exceeded"},
				Quota:     coreauth.QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: deadline}, NextRetryAfter: deadline,
			})
			if err != nil {
				t.Fatal(err)
			}
			before := auth.Clone()
			h := management.NewHandler(cfg, path, manager)
			h.SetLocalPassword("test-password")
			s := &Server{cfg: cfg, engine: gin.New(), mgmt: h}
			s.managementRoutesEnabled.Store(tc.enabled)
			s.registerManagementRoutes()
			body, err := json.Marshal(map[string]any{"authIndex": auth.EnsureIndex(), "method": "GET", "url": "https://api.anthropic.com/api/oauth/usage", "header": map[string]string{"Authorization": "Bearer $TOKEN$"}})
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest(http.MethodPost, tc.route, strings.NewReader(string(body)))
			req.RemoteAddr = "127.0.0.1:1234"
			req.Header.Set("Content-Type", "application/json")
			if tc.authorized {
				req.Header.Set("Authorization", "Bearer test-password")
			}
			callCount := calls.Load()
			recorder := httptest.NewRecorder()
			s.engine.ServeHTTP(recorder, req)
			if recorder.Code != tc.want {
				t.Fatalf("status=%d want=%d body=%s", recorder.Code, tc.want, recorder.Body.String())
			}
			wantCalls := int32(0)
			if tc.want == 200 {
				wantCalls = 1
				var response struct {
					Status int         `json:"status_code"`
					Header http.Header `json:"header"`
					Body   string      `json:"body"`
				}
				if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
					t.Fatal(err)
				}
				if response.Status != 200 || response.Body != usage || response.Header.Get("X-Upstream-Evidence") != "preserved" {
					t.Fatalf("changed provider payload: %+v", response)
				}
			}
			if got := calls.Load() - callCount; got != wantCalls {
				t.Fatalf("provider calls=%d want=%d", got, wantCalls)
			}
			current, _ := manager.GetByID(auth.ID)
			if tc.recover {
				if current.Quota.Exceeded || current.Unavailable || !current.NextRetryAfter.IsZero() || current.LastError != nil {
					t.Fatalf("v8 route left quota blocked: %+v", current)
				}
			} else if !reflect.DeepEqual(current, before) {
				t.Fatalf("route changed credential: before=%+v after=%+v", before, current)
			}
		})
	}
}
