package auth

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

type claudeQuotaExecutor struct {
	ProviderExecutor
	request func(*http.Request) (*http.Response, error)
}

func (*claudeQuotaExecutor) Identifier() string { return "claude" }
func (e *claudeQuotaExecutor) HttpRequest(_ context.Context, a *Auth, r *http.Request) (*http.Response, error) {
	r.Header.Set("Authorization", "Bearer "+authMetadataString(a, "access_token"))
	return e.request(r)
}

func TestClaudeQuotaReconciliationHTTP(t *testing.T) {
	for _, tc := range []struct {
		name    string
		status  int
		body    string
		recover bool
	}{
		{"manual reset", 200, `{"five_hour":null,"seven_day":{"utilization":78},"iguana_necktie":null,"tangelo":null,"limits":[{"kind":"weekly_all","percent":78,"is_active":true},{"kind":"weekly_scoped","percent":0,"is_active":false}],"weekly_scoped_shares":[{"limit_index":1,"used_percent_of_weekly":0,"allowance_percent_of_weekly":50}],"spend":{"enabled":false},"member_dashboard_available":false,"seven_day_breakdown":{"rows":[{"percent":100}]}}`, true},
		{"still exhausted", 200, `{"five_hour":null,"seven_day":{"utilization":100}}`, false},
		{"upstream error", 429, `{"error":"limited"}`, false},
		{"incomplete report", 200, `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			withQuotaCooldownEnabled(t)
			m := NewManager(nil, nil, nil)
			a := &Auth{ID: t.Name(), Provider: "claude", Status: StatusActive, Metadata: map[string]any{"access_token": "test-oauth"}}
			if _, err := m.Register(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			delay := 5 * 24 * time.Hour
			m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: "claude", Model: "claude-fable-5-2", CredentialScope: true, RetryAfter: &delay, Error: &Error{HTTPStatus: 429, Message: "weekly quota"}})
			calls := 0
			m.RegisterExecutor(&claudeQuotaExecutor{request: func(r *http.Request) (*http.Response, error) {
				calls++
				if r.Method != "GET" || r.URL.String() != claudeUsageURL || r.Header.Get("anthropic-beta") != "oauth-2025-04-20" {
					t.Fatalf("unexpected request: %s %s", r.Method, r.URL)
				}
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body)), Request: r}, nil
			}})
			m.reconcileClaudeQuota(context.Background())
			if calls != 1 {
				t.Fatalf("usage requests=%d", calls)
			}
			after, _ := m.GetByID(a.ID)
			if after.Quota.Exceeded == tc.recover {
				t.Fatalf("quota exceeded=%v, recovery expected=%v", after.Quota.Exceeded, tc.recover)
			}
			if tc.recover {
				m.reconcileClaudeQuota(context.Background())
				if calls != 1 {
					t.Fatal("healthy credential was probed")
				}
			}
		})
	}
}

type claudeQuotaRecoveryStore struct {
	Store
	saved chan struct{}
}

func (s *claudeQuotaRecoveryStore) Save(_ context.Context, a *Auth) (string, error) {
	if !a.Quota.Exceeded {
		select {
		case s.saved <- struct{}{}:
		default:
		}
	}
	return "", nil
}

func TestClaudeQuotaAutoRefreshRecovery(t *testing.T) {
	withQuotaCooldownEnabled(t)
	m := NewManager(nil, nil, nil)
	a := &Auth{ID: t.Name(), Provider: "claude", Status: StatusActive, Metadata: map[string]any{"access_token": "test-oauth", "refresh_token": "test-refresh", "expired": time.Now().Add(24 * time.Hour).Format(time.RFC3339)}}
	if _, err := m.Register(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	delay := 5 * 24 * time.Hour
	m.MarkResult(context.Background(), Result{AuthID: a.ID, Provider: "claude", Model: "claude-fable-5-2", CredentialScope: true, RetryAfter: &delay, Error: &Error{HTTPStatus: 429, Message: "weekly quota"}})
	m.RegisterExecutor(&claudeQuotaExecutor{request: func(r *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"five_hour":null,"seven_day":{"utilization":78}}`)), Request: r}, nil
	}})
	saved := make(chan struct{}, 1)
	m.SetStore(&claudeQuotaRecoveryStore{saved: saved})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	m.StartAutoRefresh(ctx, time.Hour)
	defer m.StopAutoRefresh()
	select {
	case <-saved:
		after, _ := m.GetByID(a.ID)
		if after.Quota.Exceeded {
			t.Fatal("auto-refresh did not restore quota")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("auto-refresh left the account blocked until the old weekly deadline")
	}
}
