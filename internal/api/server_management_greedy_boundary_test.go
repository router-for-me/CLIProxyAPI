package api

import (
	"context"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/api/handlers/management"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func newGreedyBoundaryServer(t *testing.T, strategy string, enabled, home bool) (*Server, *coreauth.Manager, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	path := filepath.Join(t.TempDir(), "config.yaml")
	raw := fmt.Sprintf("port: 8317\nremote-management: {secret-key: test-password}\nrouting: {strategy: %q}\n", strategy)
	if err := os.WriteFile(path, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg.AuthDir = t.TempDir()
	cfg.Home.Enabled = home
	manager := coreauth.NewManager(nil, nil, nil)
	h := management.NewHandler(cfg, path, manager)
	h.SetLocalPassword("test-password")
	s := &Server{cfg: cfg, engine: gin.New(), mgmt: h}
	s.managementRoutesEnabled.Store(enabled)
	s.registerManagementRoutes()
	return s, manager, path
}

func greedyBoundaryRequest(t *testing.T, s *Server, method, route, body string, authorized bool, status int) []byte {
	t.Helper()
	req := httptest.NewRequest(method, route, strings.NewReader(body))
	req.RemoteAddr = "127.0.0.1:1234"
	req.Header.Set("Content-Type", "application/json")
	// A client cannot manufacture the trusted v8 Gin context marker.
	req.Header.Set("X-Management-Config-V8", "true")
	if authorized {
		req.Header.Set("Authorization", "Bearer test-password")
	}
	response := httptest.NewRecorder()
	s.engine.ServeHTTP(response, req)
	if response.Code != status {
		t.Fatalf("%s %s: status=%d want=%d body=%s", method, route, response.Code, status, response.Body.String())
	}
	return response.Body.Bytes()
}

func TestManagementGreedyLegacyStrategyContract(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodPatch} {
		t.Run(method+" rejects greedy", func(t *testing.T) {
			s, _, path := newGreedyBoundaryServer(t, "round-robin", true, false)
			before, _ := os.ReadFile(path)
			greedyBoundaryRequest(t, s, method, "/v0/management/routing/strategy", `{"value":"greedy"}`, true, http.StatusBadRequest)
			after, _ := os.ReadFile(path)
			if string(before) != string(after) {
				t.Fatal("rejected legacy strategy changed configuration")
			}
		})
	}
	s, _, _ := newGreedyBoundaryServer(t, " GREEDY ", true, false)
	if got := string(greedyBoundaryRequest(t, s, http.MethodGet, "/v0/management/routing/strategy", "", true, http.StatusOK)); got != `{"strategy":"GREEDY"}` {
		t.Fatalf("legacy GET changed its unknown-strategy projection: %s", got)
	}
	greedyBoundaryRequest(t, s, http.MethodPut, "/v0/management/routing/strategy", `{"value":" ff "}`, true, http.StatusOK)
	if got := string(greedyBoundaryRequest(t, s, http.MethodGet, "/v0/management/routing/strategy", "", true, http.StatusOK)); got != `{"strategy":"fill-first"}` {
		t.Fatalf("existing legacy strategy alias changed: %s", got)
	}
}

func TestManagementGreedyV8Configuration(t *testing.T) {
	s, _, path := newGreedyBoundaryServer(t, "round-robin", true, false)
	greedyBoundaryRequest(t, s, http.MethodPut, "/v8/management/config/routing/strategy", `"greedy"`, true, http.StatusOK)
	if got := strings.TrimSpace(string(greedyBoundaryRequest(t, s, http.MethodGet, "/v8/management/config/routing/strategy", "", true, http.StatusOK))); got != `"greedy"` {
		t.Fatalf("v8 strategy read=%s", got)
	}
	cfg, err := config.LoadConfig(path)
	if err != nil || cfg.Routing.Strategy != "greedy" {
		t.Fatalf("v8 did not publish a runtime-readable greedy configuration: %v", err)
	}
	before, _ := os.ReadFile(path)
	greedyBoundaryRequest(t, s, http.MethodPut, "/v8/management/config/routing/strategy", `{"invalid":`, true, http.StatusBadRequest)
	after, _ := os.ReadFile(path)
	if string(before) != string(after) {
		t.Fatal("malformed v8 strategy update changed configuration")
	}
	for _, tc := range []struct {
		name                      string
		enabled, home, authorized bool
		status                    int
	}{
		{"missing key", true, false, false, http.StatusUnauthorized},
		{"disabled", false, false, true, http.StatusNotFound},
		{"home", true, true, true, http.StatusNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, _, _ := newGreedyBoundaryServer(t, "round-robin", tc.enabled, tc.home)
			greedyBoundaryRequest(t, s, http.MethodPut, "/v8/management/config/routing/strategy", `"greedy"`, tc.authorized, tc.status)
		})
	}
}

func TestManagementGreedyUsageAndInventoryBoundary(t *testing.T) {
	const usage = `{"seven_day":{"utilization":100,"resets_at":"2030-01-01T00:00:00Z"},"five_hour":null}`
	var calls atomic.Int64
	upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, usage)
	}))
	defer upstream.Close()
	oldTransport := http.DefaultTransport
	transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
	}}
	http.DefaultTransport = transport
	defer func() { http.DefaultTransport = oldTransport; transport.CloseIdleConnections() }()
	for _, tc := range []struct {
		name, route, url, bearer  string
		enabled, home, authorized bool
		malformed, observed       bool
		status                    int
	}{
		{name: "v0 unchanged despite client marker", route: "/v0/management/api-call?management.config-v8=true", enabled: true, authorized: true, status: 200},
		{name: "v8 records selected usage", route: "/v8/management/requests/api-call", enabled: true, authorized: true, observed: true, status: 200},
		{name: "v8 different provider bearer", route: "/v8/management/requests/api-call", bearer: "Bearer other", enabled: true, authorized: true, status: 200},
		{name: "v8 unrelated URL", route: "/v8/management/requests/api-call", url: "https://other.example/api/oauth/usage", enabled: true, authorized: true, status: 200},
		{name: "v8 malformed body", route: "/v8/management/requests/api-call", enabled: true, authorized: true, malformed: true, status: 400},
		{name: "v8 missing key", route: "/v8/management/requests/api-call", enabled: true, status: 401},
		{name: "v8 disabled", route: "/v8/management/requests/api-call", authorized: true, status: 404},
		{name: "v8 home", route: "/v8/management/requests/api-call", enabled: true, home: true, authorized: true, status: 404},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, manager, _ := newGreedyBoundaryServer(t, "greedy", tc.enabled, tc.home)
			auth, err := manager.Register(context.Background(), &coreauth.Auth{ID: "greedy-boundary.json", FileName: "greedy-boundary.json", Provider: "claude", Attributes: map[string]string{"runtime_only": "true"}, Metadata: map[string]any{"access_token": "selected-token", "email": "selected@example.test"}})
			if err != nil {
				t.Fatal(err)
			}
			url, bearer := tc.url, tc.bearer
			if url == "" {
				url = "https://api.anthropic.com/api/oauth/usage"
			}
			if bearer == "" {
				bearer = "Bearer $TOKEN$"
			}
			body, err := json.Marshal(map[string]any{"authIndex": auth.EnsureIndex(), "method": "GET", "url": url, "header": map[string]string{"Authorization": bearer}})
			if err != nil {
				t.Fatal(err)
			}
			if tc.malformed {
				body = []byte(`{"invalid":`)
			}
			beforeCalls := calls.Load()
			response := greedyBoundaryRequest(t, s, http.MethodPost, tc.route, string(body), tc.authorized, tc.status)
			after, _ := manager.GetByID(auth.ID)
			if !after.QuotaResetSchedule.ObservedAt.IsZero() != tc.observed {
				t.Fatalf("usage schedule recorded=%v want=%v", !after.QuotaResetSchedule.ObservedAt.IsZero(), tc.observed)
			}
			if tc.status == http.StatusOK {
				var forwarded struct {
					Body string `json:"body"`
				}
				if err := json.Unmarshal(response, &forwarded); err != nil || forwarded.Body != usage || calls.Load() != beforeCalls+1 {
					t.Fatalf("upstream response/call changed: %v", err)
				}
			} else if calls.Load() != beforeCalls {
				t.Fatal("rejected management request reached provider")
			}
			// Seed an observation independently so a v0 list cannot pass merely because none exists.
			now := time.Now().UTC()
			seeded := manager.RecordQuotaResetScheduleIfUnchanged(context.Background(), after, coreauth.QuotaResetSchedule{WeeklyResetAt: now.Add(time.Hour), ObservedAt: now})
			if seeded == nil || seeded.QuotaResetSchedule.ObservedAt.IsZero() {
				t.Fatal("inventory fixture lacks a routing reset observation")
			}
			for _, route := range []string{"/v0/management/auth-files", "/v8/management/credentials"} {
				want := tc.status
				if tc.status == 400 {
					want = 200
				}
				inventory := greedyBoundaryRequest(t, s, http.MethodGet, route, "", tc.authorized, want)
				if want == 200 && strings.Contains(string(inventory), `"routing_reset_schedule"`) != strings.HasPrefix(route, "/v8/") {
					t.Fatalf("routing schedule escaped its v8 inventory boundary: %s", inventory)
				}
			}
		})
	}
}
