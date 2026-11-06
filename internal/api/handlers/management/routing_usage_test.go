package management

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

func TestRoutingUsageScheduleNormalization(t *testing.T) {
	now := time.Date(2026, 10, 5, 0, 0, 0, 0, time.UTC)
	weekly, five := now.Add(24*time.Hour), now.Add(time.Hour)
	for _, tc := range []struct {
		name, provider, body string
		valid                bool
		weekly, five         time.Time
	}{
		{"claude", "claude", `{"seven_day":{"utilization":100,"resets_at":"2026-10-06T00:00:00Z"},"five_hour":{"utilization":0,"resets_at":"2026-10-05T01:00:00Z"}}`, true, weekly, five},
		{"codex reversed", "codex", `{"rate_limit":{"primary_window":{"limit_window_seconds":604800,"reset_at":1791244800},"secondary_window":{"limit_window_seconds":18000,"reset_at":1791162000}}}`, true, weekly, five},
		{"codex one window", "codex", `{"rateLimit":{"primaryWindow":{"limitWindowSeconds":604800,"resetAt":1791244800},"secondaryWindow":null}}`, true, weekly, time.Time{}},
		{"claude unknown five hour", "claude", `{"seven_day":{"utilization":30,"resets_at":"2026-10-06T00:00:00Z"},"five_hour":{"utilization":0,"resets_at":null}}`, true, weekly, time.Time{}},
		{name: "invalid date", provider: "claude", body: `{"seven_day":{"utilization":30,"resets_at":"tomorrow"},"five_hour":{"utilization":0}}`},
		{name: "no reset", provider: "claude", body: `{"seven_day":{"utilization":30},"five_hour":{"utilization":0}}`},
		{name: "conflicting alias", provider: "codex", body: `{"rate_limit":{},"rateLimit":{"primaryWindow":{}}}`},
		{name: "unknown duration", provider: "codex", body: `{"rate_limit":{"primary_window":{"limit_window_seconds":3600,"reset_at":1791244800},"secondary_window":null}}`},
		{name: "duplicate weekly", provider: "codex", body: `{"rate_limit":{"primary_window":{"limit_window_seconds":604800,"reset_at":1791244800},"secondary_window":{"limit_window_seconds":604800,"reset_at":1791162000}}}`},
		{name: "malformed", provider: "claude", body: `{"seven_day":`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s, ok := routingUsageSchedule(tc.provider, []byte(tc.body), now)
			if ok != tc.valid {
				t.Fatalf("valid=%v want %v", ok, tc.valid)
			}
			if ok && (!s.WeeklyResetAt.Equal(tc.weekly) || !s.FiveHourResetAt.Equal(tc.five) || !s.ObservedAt.Equal(now)) {
				t.Fatalf("schedule=%+v", s)
			}
		})
	}
}

func TestRoutingUsageAPICallObservation(t *testing.T) {
	usage := `{"seven_day":{"utilization":100,"resets_at":"2030-01-01T00:00:00Z"},"five_hour":{"utilization":0,"resets_at":"2030-01-01T01:00:00Z"}}`
	for _, tc := range []struct {
		name, provider, url, authorization, account string
		redirect, mutate, accepted                  bool
		status                                      int
	}{
		{name: "selected exhausted usage", accepted: true},
		{name: "redirect", redirect: true},
		{name: "unrelated URL", url: "https://untrusted.example/api/oauth/usage"},
		{name: "different bearer", authorization: "Bearer other"},
		{name: "newer mutation", mutate: true},
		{name: "provider failure", status: 401},
		{name: "codex account mismatch", provider: "codex", account: "different"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			provider := tc.provider
			if provider == "" {
				provider = "claude"
			}
			m := coreauth.NewManager(nil, nil, nil)
			a, err := m.Register(context.Background(), &coreauth.Auth{ID: "routing-usage", Provider: provider, Attributes: map[string]string{"runtime_only": "true"}, Metadata: map[string]any{"access_token": "selected-token", "account_id": "selected-account"}})
			if err != nil {
				t.Fatal(err)
			}
			calls := 0
			upstream := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if tc.mutate {
					current, _ := m.GetByID(a.ID)
					current.Disabled = true
					if _, errUpdate := m.Update(context.Background(), current); errUpdate != nil {
						t.Error(errUpdate)
					}
				}
				if tc.redirect && calls == 1 {
					http.Redirect(w, r, "/redirected", http.StatusFound)
					return
				}
				w.Header().Set("X-Routing-Test", "preserved")
				status := tc.status
				if status == 0 {
					status = 200
				}
				w.WriteHeader(status)
				_, _ = io.WriteString(w, usage)
			}))
			defer upstream.Close()
			old := http.DefaultTransport
			transport := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return (&net.Dialer{}).DialContext(ctx, network, upstream.Listener.Addr().String())
			}}
			http.DefaultTransport = transport
			defer func() { http.DefaultTransport = old; transport.CloseIdleConnections() }()
			url := tc.url
			if url == "" {
				url = "https://api.anthropic.com/api/oauth/usage"
				if provider == "codex" {
					url = "https://chatgpt.com/backend-api/wham/usage"
				}
			}
			authorization := tc.authorization
			if authorization == "" {
				authorization = "Bearer $TOKEN$"
			}
			headers := map[string]string{"Authorization": authorization}
			if provider == "codex" {
				headers["Chatgpt-Account-Id"] = tc.account
			}
			body, _ := json.Marshal(map[string]any{"authIndex": a.EnsureIndex(), "method": "GET", "url": url, "header": headers})
			h := &Handler{authManager: m}
			router := gin.New()
			router.POST("/", h.APICall)
			recorder := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(string(body)))
			req.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, req)
			if recorder.Code != 200 {
				t.Fatalf("management status=%d", recorder.Code)
			}
			var result apiCallResponse
			if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if result.Body != usage || http.Header(result.Header).Get("X-Routing-Test") != "preserved" {
				t.Fatal("upstream response changed")
			}
			after, _ := m.GetByID(a.ID)
			if (!after.QuotaResetSchedule.ObservedAt.IsZero()) != tc.accepted {
				t.Fatalf("schedule recorded=%v want%v", !after.QuotaResetSchedule.ObservedAt.IsZero(), tc.accepted)
			}
			if tc.accepted {
				if after.QuotaResetSchedule.WeeklyResetAt.Year() != 2030 || !reflect.DeepEqual(a.Quota, after.Quota) {
					t.Fatal("observation invalid or cooldown changed")
				}
				entry := h.buildAuthFileEntry(after)
				if entry == nil || entry["routing_reset_schedule"] == nil {
					t.Fatal("management inventory lacks routing observation")
				}
			}
		})
	}
}
