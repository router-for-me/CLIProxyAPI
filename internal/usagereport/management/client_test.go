package management

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

func TestFetchAuthFiles(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v0/management/auth-files" {
			t.Fatalf("path = %s", r.URL.Path)
		}
		if got := r.Header.Get("X-Management-Key"); got != "xipconfig" {
			t.Fatalf("X-Management-Key = %q", got)
		}
		_, _ = w.Write([]byte(`{"files":[{"auth_index":"idx1","provider":"codex","email":"leechuck01@gmail.com","account":"leechuck01@gmail.com","id_token":{"plan_type":"pro","chatgpt_subscription_active_until":"2026-05-05T17:19:33+00:00"},"success":388,"failed":10,"disabled":false,"unavailable":false}]}`))
	}))
	defer server.Close()

	client := Client{BaseURL: server.URL, ManagementKey: "xipconfig", TLSSkipVerify: true}
	files, err := client.FetchAuthFiles(context.Background())
	if err != nil {
		t.Fatalf("FetchAuthFiles: %v", err)
	}
	if len(files) != 1 {
		t.Fatalf("len(files) = %d", len(files))
	}
	if files[0].AuthIndex != "idx1" || files[0].PlanType != "pro" || files[0].Success != 388 || files[0].Failed != 10 {
		t.Fatalf("bad file: %+v", files[0])
	}
	if files[0].SubscriptionActiveUntil.IsZero() {
		t.Fatal("SubscriptionActiveUntil is zero")
	}
}

func TestNewClientReusesConnection(t *testing.T) {
	var mu sync.Mutex
	connCount := 0

	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"files":[]}`))
	}))
	srv.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			mu.Lock()
			connCount++
			mu.Unlock()
		}
	}
	srv.StartTLS()
	defer srv.Close()

	client := NewClient(srv.URL, "key", true)
	for i := 0; i < 3; i++ {
		if _, err := client.FetchAuthFiles(context.Background()); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}

	mu.Lock()
	got := connCount
	mu.Unlock()

	if got != 1 {
		t.Errorf("expected 1 TCP connection for 3 sequential calls (keep-alive reuse), got %d", got)
	}
}

func TestParseCodexQuotaWindows(t *testing.T) {
	raw := []byte(`{
  "account_id": "user-123",
  "email": "dev@example.com",
  "plan_type": "pro",
  "rate_limit": {
    "allowed": true,
    "limit_reached": false,
    "primary_window": {"used_percent": 21, "limit_window_seconds": 18000, "reset_at": 1777828364},
    "secondary_window": {"used_percent": 54, "limit_window_seconds": 604800, "reset_at": 1777958969}
  },
  "additional_rate_limits": [{
    "limit_name": "GPT-5.3-Codex-Spark",
    "rate_limit": {
      "allowed": true,
      "limit_reached": false,
      "primary_window": {"used_percent": 0, "limit_window_seconds": 18000, "reset_at": 1777845527},
      "secondary_window": {"used_percent": 59, "limit_window_seconds": 604800, "reset_at": 1777885393}
    }
  }]
}`)

	windows, err := ParseCodexQuotaWindows(raw, AuthFile{AuthIndex: "idx1", Email: "fallback@example.com", AccountID: "fallback-account", PlanType: "team"})
	if err != nil {
		t.Fatalf("ParseCodexQuotaWindows returned error: %v", err)
	}
	if len(windows) != 4 {
		t.Fatalf("len(windows) = %d", len(windows))
	}
	first := windows[0]
	if first.AuthIndex != "idx1" || first.WindowID != "five-hour" || first.Label != "5 小时限额" {
		t.Fatalf("bad first window identity: %+v", first)
	}
	if first.Email != "dev@example.com" || first.AccountID != "user-123" || first.PlanType != "pro" {
		t.Fatalf("bad account fields: %+v", first)
	}
	if first.UsedPercent != 21 || first.RemainingPercent != 79 || first.LimitWindowSeconds != 18000 || !first.Allowed || first.LimitReached {
		t.Fatalf("bad first window values: %+v", first)
	}
	if windows[2].WindowID != "gpt-5-3-codex-spark-five-hour" || windows[2].Label != "GPT-5.3-Codex-Spark 5 小时限额" {
		t.Fatalf("bad additional window: %+v", windows[2])
	}
}
