package auth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/MoeForge/moego/ojson"
	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

func routingTestAuth(id string) *Auth {
	return &Auth{ID: id, Provider: "codex", Status: StatusActive, Metadata: map[string]any{"access_token": "test-token-" + id}, Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth}}
}
func setRoutingTestQuota(s *SoonestResetSelector, a *Auth, observed time.Time, windows ...RoutingWindow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.records == nil {
		s.records = make(map[string]routingQuotaRecord)
	}
	s.records[a.ID] = routingQuotaRecord{fingerprint: quotaFingerprint(a), observedAt: observed, windows: windows}
}
func routingTestWindow(key string, now time.Time, reset time.Duration, used float64, seconds int64) RoutingWindow {
	return RoutingWindow{Key: key, ResetsAt: now.Add(reset), UsedPercent: used, seconds: seconds}
}

func TestSoonestResetRankingAndUnknowns(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name   string
		mutate func(*SoonestResetSelector, *Auth, *Auth)
		want   string
	}{
		{"weekly-before-short", nil, "b"},
		{"priority", func(s *SoonestResetSelector, a, b *Auth) { a.Attributes["priority"] = "4" }, "a"},
		{"missing", func(s *SoonestResetSelector, a, b *Auth) { delete(s.records, "b") }, "a"},
		{"stale", func(s *SoonestResetSelector, a, b *Auth) {
			r := s.records["b"]
			r.observedAt = now.Add(-16 * time.Minute)
			s.records["b"] = r
		}, "a"},
		{"expired", func(s *SoonestResetSelector, a, b *Auth) {
			r := s.records["b"]
			r.windows[1].ResetsAt = now
			s.records["b"] = r
		}, "a"},
		{"token-replaced", func(s *SoonestResetSelector, a, b *Auth) { b.Metadata["access_token"] = "new-token" }, "a"},
		{"tie", func(s *SoonestResetSelector, a, b *Auth) {
			r := s.records["b"]
			r.windows[1].ResetsAt = now.Add(48 * time.Hour)
			s.records["b"] = r
		}, "a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := &SoonestResetSelector{}
			a, b := routingTestAuth("a"), routingTestAuth("b")
			setRoutingTestQuota(s, a, now, routingTestWindow("short", now, time.Hour, 10, 18000), routingTestWindow("weekly", now, 48*time.Hour, 50, 604800))
			setRoutingTestQuota(s, b, now, routingTestWindow("short", now, 4*time.Hour, 10, 18000), routingTestWindow("weekly", now, 24*time.Hour, 50, 604800))
			if tc.mutate != nil {
				tc.mutate(s, a, b)
			}
			if got := s.rank([]*Auth{b, a}, "gpt-5.4", now)[0].ID; got != tc.want {
				t.Fatalf("rank=%s want=%s", got, tc.want)
			}
		})
	}
	s := &SoonestResetSelector{}
	a, b := routingTestAuth("a"), routingTestAuth("b")
	if got := s.rank([]*Auth{b, a}, "", now)[0].ID; got != "a" {
		t.Fatalf("missing fallback=%s", got)
	}
}

func TestRoutingQuotaParserContracts(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct {
		name, provider, body string
		valid                bool
		count                int
	}{
		{"codex", "codex", fmt.Sprintf(`{"rate_limit":{"primary_window":{"used_percent":24,"limit_window_seconds":18000,"reset_at":%d},"secondary_window":{"used_percent":80,"limit_window_seconds":604800,"reset_at":%d}},"code_review_rate_limit":{"primary_window":{"used_percent":100}}}`, now.Add(time.Hour).Unix(), now.Add(24*time.Hour).Unix()), true, 2},
		{"free-weekly-primary", "codex", `{"rate_limit":{"primary_window":{"used_percent":0,"limit_window_seconds":604800,"reset_after_seconds":3600},"secondary_window":null}}`, true, 1},
		{"claude", "claude", `{"five_hour":{"utilization":0,"resets_at":"2026-11-01T01:00:00Z"},"seven_day":{"utilization":84,"resets_at":"2026-11-03T00:00:00Z"},"seven_day_opus":{"utilization":100,"resets_at":"2026-11-02T00:00:00Z"},"extra_usage":{"utilization":100}}`, true, 3},
		{"claude-idle-short-and-model-windows", "claude", `{"five_hour":{"utilization":0,"resets_at":null},"seven_day":{"utilization":40,"resets_at":"2026-11-03T00:00:00Z"},"seven_day_opus":{"utilization":null,"resets_at":null},"seven_day_sonnet":{"utilization":0,"resets_at":null}}`, true, 1},
		{"claude-all-idle", "claude", `{"five_hour":{"utilization":0,"resets_at":null},"seven_day":{"utilization":null,"resets_at":null}}`, true, 0},
		{"claude-active-without-reset", "claude", `{"five_hour":{"utilization":10,"resets_at":null},"seven_day":{"utilization":40,"resets_at":"2026-11-03T00:00:00Z"}}`, false, 1},
		{"claude-missing-reset", "claude", `{"five_hour":{"utilization":0}}`, false, 0},
		{"claude-missing-utilization", "claude", `{"five_hour":{"resets_at":null}}`, false, 0},
		{"claude-empty-response", "claude", `{}`, false, 0},
		{"codex-null-named-pool", "codex", `{"rate_limit":{"primary_window":{"used_percent":0,"limit_window_seconds":604800,"reset_after_seconds":3600}},"additional_rate_limits":[{"limit_name":"gpt-5.3-codex-spark","rate_limit":null}]}`, true, 1},
		{"codex-null-main-pool", "codex", `{"rate_limit":null}`, false, 0},
		{"numeric-string", "codex", `{"rate_limit":{"primary_window":{"used_percent":"0","limit_window_seconds":604800,"reset_after_seconds":3600}}}`, false, 0},
		{"unknown-shape", "codex", `{"credits":{"balance":100}}`, false, 0},
		{"malformed", "claude", `{"seven_day":{"utilization":12,"resets_at":"invalid"}}`, false, 0},
		{"negative", "codex", `{"rate_limit":{"primary_window":{"used_percent":-1,"limit_window_seconds":604800,"reset_after_seconds":3600}}}`, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, valid := parseRoutingQuota(tc.provider, []byte(tc.body), now)
			if valid != tc.valid || len(w) != tc.count {
				t.Fatalf("windows=%+v valid=%v", w, valid)
			}
		})
	}
}

func TestRoutingStatusPluginSchedulerState(t *testing.T) {
	for _, tc := range []struct {
		name      string
		scheduler PluginScheduler
		want      string
	}{
		{"absent", nil, "no_eligible_accounts"},
		{"inactive-host", &inactivePluginScheduler{}, "no_eligible_accounts"},
		{"active", &fakePluginScheduler{}, "plugin_scheduler"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := NewManager(nil, &SoonestResetSelector{}, nil)
			m.SetPluginScheduler(tc.scheduler)
			status := m.RoutingStatus(context.Background(), "codex", "", time.Now())
			if status.FallbackReason != tc.want || status.NextAuthIndex != "" {
				t.Fatalf("status=%+v", status)
			}
		})
	}
}

func TestRoutingQuotaCodexExhaustionDeadlines(t *testing.T) {
	now := time.Date(2026, 11, 1, 0, 0, 0, 0, time.UTC)
	for _, flag := range []string{`"limit_reached":true`, `"allowed":false`} {
		for _, tc := range []struct {
			name        string
			short, week float64
			want        time.Duration
		}{
			{"short-only", 100, 40, time.Hour},
			{"week-only", 10, 100, 24 * time.Hour},
			{"both", 100, 100, 24 * time.Hour},
			{"denied-without-full-counter", 99, 40, 24 * time.Hour},
		} {
			t.Run(flag+"/"+tc.name, func(t *testing.T) {
				body := fmt.Sprintf(`{"rate_limit":{%s,"primary_window":{"used_percent":%g,"limit_window_seconds":18000,"reset_at":%d},"secondary_window":{"used_percent":%g,"limit_window_seconds":604800,"reset_at":%d}},"additional_rate_limits":[{"limit_name":"gpt-5.3-codex-spark","rate_limit":{"allowed":false,"primary_window":{"used_percent":25,"limit_window_seconds":604800,"reset_at":%d}}}]}`, flag, tc.short, now.Add(time.Hour).Unix(), tc.week, now.Add(24*time.Hour).Unix(), now.Add(12*time.Hour).Unix())
				windows, valid := parseRoutingQuota("codex", []byte(body), now)
				if !valid || len(windows) != 3 {
					t.Fatalf("windows=%+v valid=%v", windows, valid)
				}
				if windows[0].UsedPercent != tc.short || windows[1].UsedPercent != tc.week {
					t.Fatal("group denial changed usage counters")
				}
				s := &SoonestResetSelector{}
				a := routingTestAuth("deadline")
				setRoutingTestQuota(s, a, now, windows...)
				if until := s.blockedUntil(a, "gpt-5.4", now); !until.Equal(now.Add(tc.want)) {
					t.Fatalf("blocked until %v want %v", until, now.Add(tc.want))
				}
				if until := s.blockedUntil(a, "gpt-5.3-codex-spark", now); !until.Equal(now.Add(12 * time.Hour)) {
					t.Fatalf("named pool denial was affected by default pool counters: %v", until)
				}
			})
		}
	}
}

func TestSoonestResetExhaustionAndModelWindows(t *testing.T) {
	now := time.Now()
	s := &SoonestResetSelector{}
	a := routingTestAuth("a")
	a.Provider = "claude"
	short := routingTestWindow("five_hour", now, time.Hour, 100, 18000)
	week := routingTestWindow("seven_day", now, 24*time.Hour, 20, 604800)
	opus := routingTestWindow("seven_day_opus", now, 12*time.Hour, 100, 604800)
	opus.model = "opus"
	setRoutingTestQuota(s, a, now, short, week, opus)
	if until := s.blockedUntil(a, "claude-sonnet-4-6", now); !until.Equal(short.ResetsAt) {
		t.Fatalf("short limit=%v", until)
	}
	if until := s.blockedUntil(a, "claude-opus-4-6", now); !until.Equal(opus.ResetsAt) {
		t.Fatalf("opus limit=%v", until)
	}
	short.UsedPercent = 0
	setRoutingTestQuota(s, a, now, short, week, opus)
	if until := s.blockedUntil(a, "claude-sonnet-4-6", now); !until.IsZero() {
		t.Fatalf("unrelated opus quota blocks sonnet: %v", until)
	}
	_, window, _, _ := s.view(a, "claude-opus-4-6", now)
	if window.Key != "seven_day_opus" {
		t.Fatalf("window=%+v", window)
	}
}

type routingTestExecutor struct {
	provider  string
	http      func(context.Context, *Auth, *http.Request) (*http.Response, error)
	refreshes atomic.Int32
}

func (e *routingTestExecutor) Identifier() string { return e.provider }
func (e *routingTestExecutor) Execute(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *routingTestExecutor) ExecuteStream(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (*cliproxyexecutor.StreamResult, error) {
	return nil, errors.New("unused")
}
func (e *routingTestExecutor) CountTokens(context.Context, *Auth, cliproxyexecutor.Request, cliproxyexecutor.Options) (cliproxyexecutor.Response, error) {
	return cliproxyexecutor.Response{}, nil
}
func (e *routingTestExecutor) Refresh(_ context.Context, a *Auth) (*Auth, error) {
	e.refreshes.Add(1)
	return a, nil
}
func (e *routingTestExecutor) HttpRequest(ctx context.Context, a *Auth, r *http.Request) (*http.Response, error) {
	if e.http == nil {
		return nil, errors.New("unexpected probe")
	}
	return e.http(ctx, a, r)
}

func TestSoonestResetManagerAffinityPriorityAndPinnedContinuation(t *testing.T) {
	ctx := WithCodexClientRequest(context.Background(), true)
	now := time.Now()
	s := &SoonestResetSelector{}
	affinity := NewSessionAffinitySelector(s)
	defer affinity.Stop()
	m := NewManager(nil, affinity, nil)
	m.RegisterExecutor(&routingTestExecutor{provider: "codex"})
	model := "routing-regression-model"
	auths := make([]*Auth, 0, 2)
	for _, id := range []string{"a", "b"} {
		a, err := m.Register(ctx, routingTestAuth(t.Name()+id))
		if err != nil {
			t.Fatal(err)
		}
		auths = append(auths, a)
		registry.GetGlobalRegistry().RegisterClient(a.ID, "codex", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(a.ID) })
	}
	a, b := auths[0], auths[1]
	setRoutingTestQuota(s, a, now, routingTestWindow("weekly", now, 24*time.Hour, 20, 604800))
	setRoutingTestQuota(s, b, now, routingTestWindow("weekly", now, 48*time.Hour, 20, 604800))
	pick := func(session string, extra map[string]any) *Auth {
		t.Helper()
		opts := cliproxyexecutor.Options{Headers: http.Header{"Session_id": []string{session}}, Metadata: extra}
		got, err := m.SelectAuth(ctx, "codex", model, opts)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	if got := pick("first", nil); got.ID != a.ID {
		t.Fatalf("first=%s", got.ID)
	}
	setRoutingTestQuota(s, b, now, routingTestWindow("weekly", now, 12*time.Hour, 20, 604800))
	if got := pick("first", nil); got.ID != a.ID {
		t.Fatalf("usable affinity moved=%s", got.ID)
	}
	if got := pick("new", nil); got.ID != b.ID {
		t.Fatalf("new session=%s", got.ID)
	}
	if got := pick("continuation", map[string]any{cliproxyexecutor.PinnedAuthMetadataKey: a.ID}); got.ID != a.ID {
		t.Fatalf("pinned continuation moved=%s", got.ID)
	}
	setRoutingTestQuota(s, a, now, routingTestWindow("short", now, time.Hour, 100, 18000))
	if got := pick("first", nil); got.ID != b.ID {
		t.Fatalf("exhausted session did not fail over=%s", got.ID)
	}
	status := m.RoutingStatus(ctx, "codex", model, now)
	if status.NextAuthIndex != b.Index || !status.SessionAffinity {
		t.Fatalf("status=%+v", status)
	}
	doc := ojson.NewDocString(`{}`)
	if err := doc.Set("status", status); err != nil {
		t.Fatal(err)
	}
	payload := doc.Bytes()
	if strings.Contains(string(payload), "test-token-") || strings.Contains(string(payload), a.ID) {
		t.Fatal("status leaked credential material or ID")
	}
	setRoutingTestQuota(s, b, now, routingTestWindow("weekly", now, time.Hour, 100, 604800))
	if _, err := m.SelectAuth(ctx, "codex", model, cliproxyexecutor.Options{}); err == nil {
		t.Fatal("all exhausted must fail")
	}
}

func TestRoutingQuotaProbeContractAndNoRefresh(t *testing.T) {
	m := NewManager(nil, &SoonestResetSelector{}, nil)
	exec := &routingTestExecutor{provider: "codex"}
	exec.http = func(ctx context.Context, a *Auth, r *http.Request) (*http.Response, error) {
		if r.URL.String() != "https://chatgpt.com/backend-api/wham/usage" || r.Header.Get("Chatgpt-Account-Id") != "account" || IsCodexUseSIWCForRequest(ctx, a) {
			t.Error("wrong quota request contract")
		}
		if _, deadline := ctx.Deadline(); deadline {
			t.Error("probe set a request deadline")
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"rate_limit":{"primary_window":{"used_percent":0,"limit_window_seconds":604800,"reset_after_seconds":3600}}}`))}, nil
	}
	m.RegisterExecutor(exec)
	a := routingTestAuth("probe")
	a.Metadata["account_id"] = "account"
	a.Metadata["use_siwc"] = true
	windows, code := m.fetchRoutingQuota(context.Background(), a)
	if code != "" || len(windows) != 1 || exec.refreshes.Load() != 0 {
		t.Fatalf("probe=%+v code=%s refreshes=%d", windows, code, exec.refreshes.Load())
	}
}

func TestRoutingQuotaRefreshDiscoversAccountsAndRetainsLastObservation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	s := &SoonestResetSelector{}
	m := NewManager(nil, s, nil)
	var calls atomic.Int32
	fail := atomic.Bool{}
	exec := &routingTestExecutor{provider: "codex", http: func(_ context.Context, a *Auth, r *http.Request) (*http.Response, error) {
		calls.Add(1)
		if fail.Load() {
			return &http.Response{StatusCode: 429, Body: io.NopCloser(strings.NewReader("private body"))}, nil
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"rate_limit":{"primary_window":{"used_percent":15,"limit_window_seconds":604800,"reset_after_seconds":3600}}}`))}, nil
	}}
	m.RegisterExecutor(exec)
	var auths []*Auth
	for i := 0; i < 9; i++ {
		a, err := m.Register(ctx, routingTestAuth(fmt.Sprintf("%s-%02d", t.Name(), i)))
		if err != nil {
			t.Fatal(err)
		}
		auths = append(auths, a)
	}
	now := time.Now()
	// Explicit timestamps exercise more accounts than worker slots without sleeping.
	for i := 0; i < 3; i++ {
		m.refreshRoutingQuotas(ctx, now.Add(time.Duration(i)*30*time.Second)).Wait()
	}
	if calls.Load() != 9 {
		t.Fatalf("never-observed accounts starved, calls=%d", calls.Load())
	}
	for _, a := range auths {
		if r := s.record(a); r.observedAt.IsZero() {
			t.Fatalf("no observation for %s", a.ID)
		}
	}
	m.refreshRoutingQuotas(ctx, now.Add(time.Minute)).Wait()
	if calls.Load() != 9 {
		t.Fatal("polled account before refresh interval")
	}
	previous := s.record(auths[0])
	fail.Store(true)
	m.refreshRoutingQuotas(ctx, now.Add(6*time.Minute)).Wait()
	current := s.record(auths[0])
	if current.errorCode != "rate_limited" || !current.observedAt.Equal(previous.observedAt) || len(current.windows) != 1 {
		t.Fatalf("failed refresh erased last observation: %+v", current)
	}
	before := calls.Load()
	_ = m.RoutingStatus(ctx, "codex", "", now)
	if calls.Load() != before || exec.refreshes.Load() != 0 {
		t.Fatal("status read polled quota or refreshed credentials")
	}
}

func TestRoutingQuotaRefreshAllIdleClearsExhaustedSnapshot(t *testing.T) {
	ctx := context.Background()
	s := &SoonestResetSelector{}
	m := NewManager(nil, s, nil)
	m.RegisterExecutor(&routingTestExecutor{provider: "claude", http: func(_ context.Context, _ *Auth, _ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"five_hour":{"utilization":0,"resets_at":null},"seven_day":{"utilization":null,"resets_at":null},"seven_day_opus":null,"seven_day_sonnet":{"utilization":0,"resets_at":null}}`))}, nil
	}})
	a := routingTestAuth(t.Name())
	a.Provider = "claude"
	a, err := m.Register(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	setRoutingTestQuota(s, a, now.Add(-time.Minute), routingTestWindow("seven_day", now, 24*time.Hour, 100, 604800))
	if s.blockedUntil(a, "claude-sonnet-4-6", now).IsZero() {
		t.Fatal("fixture did not block exhausted account")
	}
	m.refreshRoutingQuotas(ctx, now).Wait()
	r, window, state, until := s.view(a, "claude-sonnet-4-6", time.Now())
	if r.errorCode != "" || r.observedAt.Before(now) || len(r.windows) != 0 || window != nil || state != "missing" || !until.IsZero() {
		t.Fatalf("all-idle refresh retained exhausted data: record=%+v window=%+v state=%s until=%v", r, window, state, until)
	}
}

func TestSoonestResetWebsocketPreferenceAndOneAccount(t *testing.T) {
	now := time.Now()
	s := &SoonestResetSelector{}
	a, b := routingTestAuth("a"), routingTestAuth("b")
	b.Attributes["websockets"] = "true"
	setRoutingTestQuota(s, a, now, routingTestWindow("weekly", now, time.Hour, 10, 604800))
	setRoutingTestQuota(s, b, now, routingTestWindow("weekly", now, 24*time.Hour, 10, 604800))
	ctx := cliproxyexecutor.WithDownstreamWebsocket(context.Background())
	got, err := s.Pick(ctx, "codex", "model", cliproxyexecutor.Options{}, []*Auth{a, b})
	if err != nil || got.ID != b.ID {
		t.Fatalf("WS preference got=%v err=%v", got, err)
	}
	got, err = s.Pick(ctx, "codex", "model", cliproxyexecutor.Options{}, []*Auth{a})
	if err != nil || got.ID != a.ID {
		t.Fatalf("one account got=%v err=%v", got, err)
	}
	b.Disabled = true
	got, err = s.Pick(ctx, "codex", "model", cliproxyexecutor.Options{}, []*Auth{a, b})
	if err != nil || got.ID != a.ID {
		t.Fatalf("disabled WS account got=%v err=%v", got, err)
	}
}

func TestSoonestResetResolvedModelAndPriorityFailover(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	s := &SoonestResetSelector{}
	m := NewManager(nil, s, nil)
	m.RegisterExecutor(&routingTestExecutor{provider: "claude"})
	const alias = "routing-test-alias"
	const target = "claude-opus-4-6"
	m.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{"claude": {{Name: target, Alias: alias, Fork: true}}})
	var auths []*Auth
	for i, id := range []string{t.Name() + "a", t.Name() + "b"} {
		a := routingTestAuth(id)
		a.Provider = "claude"
		if i == 0 {
			a.Attributes["priority"] = "10"
		}
		saved, err := m.Register(ctx, a)
		if err != nil {
			t.Fatal(err)
		}
		auths = append(auths, saved)
		registry.GetGlobalRegistry().RegisterClient(id, "claude", []*registry.ModelInfo{{ID: alias}, {ID: target}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	a, b := auths[0], auths[1]
	global := routingTestWindow("seven_day", now, 24*time.Hour, 10, 604800)
	opus := routingTestWindow("seven_day_opus", now, 12*time.Hour, 100, 604800)
	opus.model = "opus"
	setRoutingTestQuota(s, a, now, global, opus)
	setRoutingTestQuota(s, b, now, global)
	got, err := m.SelectAuth(ctx, "claude", alias, cliproxyexecutor.Options{})
	if err != nil || got.ID != b.ID {
		t.Fatalf("model quota should skip exhausted higher priority: got=%v err=%v", got, err)
	}
	if status := m.RoutingStatus(ctx, "claude", alias, now); status.NextAuthIndex != b.Index {
		t.Fatalf("status differs from selection: %+v", status)
	}
	opus.UsedPercent = 20
	setRoutingTestQuota(s, a, now, global, opus)
	got, err = m.SelectAuth(ctx, "claude", alias, cliproxyexecutor.Options{})
	if err != nil || got.ID != a.ID {
		t.Fatalf("usable higher priority ignored: got=%v err=%v", got, err)
	}
}

func TestRoutingQuotaRefreshDiscardsReplacedCredential(t *testing.T) {
	ctx := context.Background()
	s := &SoonestResetSelector{}
	m := NewManager(nil, s, nil)
	started, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int32
	m.RegisterExecutor(&routingTestExecutor{provider: "codex", http: func(context.Context, *Auth, *http.Request) (*http.Response, error) {
		if calls.Add(1) == 1 {
			close(started)
			<-release
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"rate_limit":{"primary_window":{"used_percent":90,"limit_window_seconds":604800,"reset_after_seconds":3600}}}`))}, nil
	}})
	a, err := m.Register(ctx, routingTestAuth(t.Name()))
	if err != nil {
		t.Fatal(err)
	}
	pending := m.refreshRoutingQuotas(ctx, time.Now())
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("probe never started")
	}
	replacement := a.Clone()
	replacement.Metadata["access_token"] = "replacement-token"
	replacement, err = m.Update(ctx, replacement)
	if err != nil {
		close(release)
		t.Fatal(err)
	}
	close(release)
	pending.Wait()
	if r := s.record(replacement); !r.observedAt.IsZero() {
		t.Fatal("old-token observation attached to replacement")
	}
	m.refreshRoutingQuotas(ctx, time.Now()).Wait()
	if r := s.record(replacement); r.observedAt.IsZero() {
		t.Fatal("replacement did not get its own observation")
	}
}

func TestSoonestResetCodexNamedPoolDoesNotUseDefaultQuota(t *testing.T) {
	now := time.Now()
	s := &SoonestResetSelector{}
	a := routingTestAuth("pool")
	main := routingTestWindow("secondary_window", now, 24*time.Hour, 100, 604800)
	spark := routingTestWindow("additional:gpt53codexspark:primary_window", now, time.Hour, 10, 18000)
	spark.model = "gpt53codexspark"
	setRoutingTestQuota(s, a, now, main, spark)
	if until := s.blockedUntil(a, "gpt-5.3-codex-spark", now); !until.IsZero() {
		t.Fatalf("default pool blocked independent named pool: %v", until)
	}
	if until := s.blockedUntil(a, "gpt-5.4", now); until.IsZero() {
		t.Fatal("ordinary request ignored main quota")
	}
	_, w, _, _ := s.view(a, "gpt-5.3-codex-spark", now)
	if w == nil || w.Key != spark.Key {
		t.Fatalf("wrong active quota window %+v", w)
	}
}

func antigravityRoutingTestAuth(id string, now time.Time) *Auth {
	a := routingTestAuth(id)
	a.Provider = "antigravity"
	a.Metadata["project_id"] = "test-project"
	a.Metadata["expired"] = now.Add(time.Hour).Format(time.RFC3339)
	return a
}

func TestRoutingQuotaAntigravityParserContracts(t *testing.T) {
	now := time.Date(2026, 10, 21, 10, 16, 4, 0, time.UTC)
	// Shape of a reported retrieveUserQuotaSummary payload: untouched 3p buckets carry a rolling reset.
	observed := `{"groups":[{"displayName":"Claude and GPT models","buckets":[{"bucketId":"3p-5h","window":"5h","remainingFraction":1,"resetTime":"2026-10-21T15:16:05Z"},{"bucketId":"3p-weekly","window":"weekly","remainingFraction":1,"resetTime":"2026-10-28T10:16:05Z"}]},{"displayName":"Gemini Models","buckets":[{"bucketId":"gemini-5h","window":"5h","remainingFraction":0.848104,"resetTime":"2026-10-21T13:29:53Z","description":"You have used some of your 5-hour limit"},{"bucketId":"gemini-weekly","window":"weekly","remainingFraction":0.9620177,"resetTime":"2026-10-28T08:29:53Z"}]},{"displayName":"Gemini 4 Argon","buckets":[{"bucketId":"gemini-argon-wtu-weekly","window":"weekly","remainingFraction":0,"resetTime":"2026-10-24T08:00:00Z"}]}]}`
	for _, tc := range []struct {
		name, body string
		valid      bool
		count      int
	}{
		{"observed", observed, true, 5},
		{"top-level-buckets", `{"buckets":[{"bucketId":"3p-weekly","window":"weekly","remainingFraction":0.5,"resetTime":"2026-10-28T10:16:05Z"}]}`, true, 1},
		{"snake-case", `{"groups":[{"buckets":[{"bucket_id":"gemini-weekly","window":"weekly","remaining_fraction":0.5,"reset_time":"2026-10-28T10:16:05Z"}]}]}`, true, 1},
		{"full-without-reset", `{"groups":[{"buckets":[{"bucketId":"3p-5h","window":"5h","remainingFraction":1}]}]}`, true, 0},
		{"disabled-bucket", `{"groups":[{"buckets":[{"bucketId":"3p-5h","window":"5h","remainingFraction":1,"disabled":true},{"bucketId":"3p-weekly","window":"weekly","remainingFraction":0,"resetTime":"2026-10-28T10:16:05Z"}]}]}`, true, 1},
		{"unknown-pool-ignored", `{"groups":[{"buckets":[{"bucketId":"credits-monthly","window":"other"},{"bucketId":"3p-weekly","window":"weekly","remainingFraction":0.5,"resetTime":"2026-10-28T10:16:05Z"}]}]}`, true, 1},
		{"overfull-clamped", `{"buckets":[{"bucketId":"3p-weekly","window":"weekly","remainingFraction":1.2,"resetTime":"2026-10-28T10:16:05Z"}]}`, true, 1},
		{"used-without-reset", `{"groups":[{"buckets":[{"bucketId":"3p-5h","window":"5h","remainingFraction":0.4}]}]}`, false, 0},
		{"missing-fraction", `{"groups":[{"buckets":[{"bucketId":"gemini-weekly","window":"weekly","resetTime":"2026-10-28T10:16:05Z"}]}]}`, false, 0},
		{"string-fraction", `{"groups":[{"buckets":[{"bucketId":"gemini-weekly","window":"weekly","remainingFraction":"0.5","resetTime":"2026-10-28T10:16:05Z"}]}]}`, false, 0},
		{"unknown-window", `{"groups":[{"buckets":[{"bucketId":"gemini-weekly","window":"fortnight","remainingFraction":0.5,"resetTime":"2026-10-28T10:16:05Z"}]}]}`, false, 0},
		{"invalid-reset", `{"groups":[{"buckets":[{"bucketId":"gemini-weekly","window":"weekly","remainingFraction":0.5,"resetTime":"soon"}]}]}`, false, 0},
		{"distant-reset", `{"groups":[{"buckets":[{"bucketId":"gemini-weekly","window":"weekly","remainingFraction":0.5,"resetTime":"2027-01-01T00:00:00Z"}]}]}`, false, 0},
		{"partly-malformed", `{"groups":[{"buckets":[{"bucketId":"3p-weekly","window":"weekly","remainingFraction":0.5,"resetTime":"2026-10-28T10:16:05Z"},{"bucketId":"gemini-weekly","window":"weekly"}]}]}`, false, 1},
		{"groups-not-array", `{"groups":{"buckets":[]}}`, false, 0},
		{"empty-groups", `{"groups":[]}`, false, 0},
		{"only-unknown-pools", `{"groups":[{"buckets":[{"bucketId":"credits-monthly","window":"monthly","remainingFraction":0.5}]}]}`, false, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, valid := parseRoutingQuota("antigravity", []byte(tc.body), now)
			if valid != tc.valid || len(w) != tc.count {
				t.Fatalf("windows=%+v valid=%v", w, valid)
			}
		})
	}
	windows, _ := parseRoutingQuota("antigravity", []byte(observed), now)
	byKey := make(map[string]RoutingWindow, len(windows))
	for _, w := range windows {
		byKey[w.Key] = w
	}
	if w := byKey["gemini-5h"]; w.model != antigravityGeminiPool || w.seconds != 18000 || w.UsedPercent < 15.18 || w.UsedPercent > 15.2 || !w.ResetsAt.Equal(time.Date(2026, 10, 21, 13, 29, 53, 0, time.UTC)) {
		t.Fatalf("gemini-5h=%+v", w)
	}
	if w := byKey["3p-weekly"]; w.model != antigravityThirdParty || w.seconds != 604800 || w.UsedPercent != 0 {
		t.Fatalf("3p-weekly=%+v", w)
	}
	if w := byKey["gemini-argon-wtu-weekly"]; w.model != antigravityGeminiPool+"-argon" || w.UsedPercent != 100 {
		t.Fatalf("argon=%+v", w)
	}
}

func TestSoonestResetAntigravityModelPools(t *testing.T) {
	now := time.Now()
	s := &SoonestResetSelector{}
	a := antigravityRoutingTestAuth("pools", now)
	if _, _, state, _ := s.view(a, "claude-opus-4-6-thinking", now); state != "missing" {
		t.Fatalf("unobserved Antigravity state=%s", state)
	}
	thirdParty := routingTestWindow("3p-5h", now, time.Hour, 100, 18000)
	thirdParty.model = antigravityThirdParty
	thirdWeek := routingTestWindow("3p-weekly", now, 72*time.Hour, 40, 604800)
	thirdWeek.model = antigravityThirdParty
	gemini := routingTestWindow("gemini-weekly", now, 48*time.Hour, 100, 604800)
	gemini.model = antigravityGeminiPool
	argon := routingTestWindow("gemini-argon-wtu-weekly", now, 24*time.Hour, 30, 604800)
	argon.model = antigravityGeminiPool + "-argon"
	setRoutingTestQuota(s, a, now, thirdParty, thirdWeek, gemini, argon)
	for _, tc := range []struct {
		model   string
		blocked time.Time
		window  string
		state   string
	}{
		{"claude-opus-4-6-thinking", thirdParty.ResetsAt, "3p-weekly", "fresh"},
		{"claude-sonnet-4-6(high)", thirdParty.ResetsAt, "3p-weekly", "fresh"},
		{"gpt-oss-120b-medium", thirdParty.ResetsAt, "3p-weekly", "fresh"},
		{"gemini-3-flash", gemini.ResetsAt, "gemini-weekly", "fresh"},
		{"gemini-4-neon-xhigh", gemini.ResetsAt, "gemini-weekly", "fresh"},
		// Argon has its own pool, so the exhausted default Gemini pool does not apply.
		{"gemini-4-argon-high", time.Time{}, "gemini-argon-wtu-weekly", "fresh"},
		{"tab_flash_lite_preview", time.Time{}, "", "missing"},
	} {
		t.Run(tc.model, func(t *testing.T) {
			_, w, state, until := s.view(a, tc.model, now)
			if !until.Equal(tc.blocked) || state != tc.state {
				t.Fatalf("state=%s until=%v want %s %v", state, until, tc.state, tc.blocked)
			}
			if (w == nil) != (tc.window == "") || (w != nil && w.Key != tc.window) {
				t.Fatalf("window=%+v want %s", w, tc.window)
			}
		})
	}
	// Stale and expired observations follow the shared semantics.
	setRoutingTestQuota(s, a, now.Add(-16*time.Minute), thirdWeek)
	if _, _, state, _ := s.view(a, "claude-opus-4-6-thinking", now); state != "stale" {
		t.Fatalf("stale state=%s", state)
	}
	elapsed := routingTestWindow("3p-5h", now, 0, 100, 18000)
	elapsed.model = antigravityThirdParty
	setRoutingTestQuota(s, a, now, elapsed, thirdWeek)
	if _, _, state, until := s.view(a, "claude-opus-4-6-thinking", now); state != "expired" || !until.IsZero() {
		t.Fatalf("expired state=%s until=%v", state, until)
	}
	// Antigravity API keys and other providers stay outside quota routing.
	key := antigravityRoutingTestAuth("api-key", now)
	key.Attributes[AttributeAuthKind] = AuthKindAPIKey
	if _, _, state, _ := s.view(key, "claude-opus-4-6-thinking", now); state != "unsupported" {
		t.Fatalf("api key state=%s", state)
	}
}

func TestRoutingQuotaAntigravityProbeContract(t *testing.T) {
	now := time.Now()
	m := NewManager(nil, &SoonestResetSelector{}, nil)
	var gotURL, gotBody, gotMethod, gotType string
	exec := &routingTestExecutor{provider: "antigravity", http: func(ctx context.Context, _ *Auth, r *http.Request) (*http.Response, error) {
		if _, deadline := ctx.Deadline(); deadline {
			t.Error("probe set a request deadline")
		}
		body, _ := io.ReadAll(r.Body)
		gotURL, gotBody, gotMethod, gotType = r.URL.String(), string(body), r.Method, r.Header.Get("Content-Type")
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"groups":[{"buckets":[{"bucketId":"3p-weekly","window":"weekly","remainingFraction":0.25,"resetTime":"` + now.Add(24*time.Hour).UTC().Format(time.RFC3339) + `"}]}]}`))}, nil
	}}
	m.RegisterExecutor(exec)
	a := antigravityRoutingTestAuth("probe", now)
	windows, code := m.fetchRoutingQuota(context.Background(), a)
	if code != "" || len(windows) != 1 || windows[0].UsedPercent != 75 || exec.refreshes.Load() != 0 {
		t.Fatalf("probe=%+v code=%s refreshes=%d", windows, code, exec.refreshes.Load())
	}
	if gotMethod != http.MethodPost || gotURL != "https://daily-cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary" || gotBody != `{"project":"test-project"}` || gotType != "application/json" {
		t.Fatalf("request=%s %s %s %s", gotMethod, gotURL, gotBody, gotType)
	}
	// Inference and quota share the credential's configured endpoint.
	a.Attributes["base_url"] = "https://cloudcode-pa.googleapis.com/"
	delete(a.Metadata, "project_id")
	if _, code = m.fetchRoutingQuota(context.Background(), a); code != "" || gotURL != "https://cloudcode-pa.googleapis.com/v1internal:retrieveUserQuotaSummary" || gotBody != `{}` {
		t.Fatalf("custom endpoint code=%s url=%s body=%s", code, gotURL, gotBody)
	}
	// A token the executor would refresh is never probed.
	gotURL = ""
	a.Metadata["expired"] = now.Add(time.Minute).Format(time.RFC3339)
	if _, code = m.fetchRoutingQuota(context.Background(), a); code != "token_expiring" || gotURL != "" {
		t.Fatalf("expiring token code=%s url=%s", code, gotURL)
	}
	delete(a.Metadata, "expired")
	if _, code = m.fetchRoutingQuota(context.Background(), a); code != "token_expiring" || gotURL != "" {
		t.Fatalf("unknown expiry code=%s url=%s", code, gotURL)
	}
}

func TestSoonestResetAntigravityRefreshSelectionAndStatus(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	s := &SoonestResetSelector{}
	m := NewManager(nil, s, nil)
	reset := func(d time.Duration) string { return now.Add(d).UTC().Format(time.RFC3339) }
	bodies := map[string]string{
		"exhausted": `{"groups":[{"buckets":[{"bucketId":"3p-5h","window":"5h","remainingFraction":0,"resetTime":"` + reset(time.Hour) + `"},{"bucketId":"3p-weekly","window":"weekly","remainingFraction":0.5,"resetTime":"` + reset(24*time.Hour) + `"}]}]}`,
		"later":     `{"groups":[{"buckets":[{"bucketId":"3p-weekly","window":"weekly","remainingFraction":0.5,"resetTime":"` + reset(96*time.Hour) + `"}]}]}`,
		"sooner":    `{"groups":[{"buckets":[{"bucketId":"3p-weekly","window":"weekly","remainingFraction":0.5,"resetTime":"` + reset(48*time.Hour) + `"}]}]}`,
		"malformed": `{"groups":[{"buckets":[{"bucketId":"3p-weekly","window":"weekly","remainingFraction":0.5}]}]}`,
	}
	m.RegisterExecutor(&routingTestExecutor{provider: "antigravity", http: func(_ context.Context, a *Auth, _ *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(bodies[strings.TrimPrefix(a.ID, t.Name()+"-")]))}, nil
	}})
	const model = "claude-opus-4-6-thinking"
	byName := make(map[string]*Auth)
	for _, name := range []string{"exhausted", "later", "sooner", "malformed"} {
		id := t.Name() + "-" + name
		saved, err := m.Register(ctx, antigravityRoutingTestAuth(id, now))
		if err != nil {
			t.Fatal(err)
		}
		byName[name] = saved
		registry.GetGlobalRegistry().RegisterClient(id, "antigravity", []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	m.refreshRoutingQuotas(ctx, now).Wait()
	got, err := m.SelectAuth(ctx, "antigravity", model, cliproxyexecutor.Options{})
	if err != nil || got.ID != byName["sooner"].ID {
		t.Fatalf("selection got=%v err=%v", got, err)
	}
	status := m.RoutingStatus(ctx, "antigravity", model, time.Now())
	if status.Strategy != "soonest-reset-first" || status.NextAuthIndex != byName["sooner"].Index || status.FallbackReason != "" {
		t.Fatalf("status=%+v", status)
	}
	rows := make(map[string]RoutingAccountStatus)
	for _, row := range status.Accounts {
		rows[row.AuthIndex] = row
	}
	if row := rows[byName["exhausted"].Index]; row.QuotaState != "fresh" || row.SkippedReason != "quota_exhausted" || row.Eligible {
		t.Fatalf("exhausted row=%+v", row)
	}
	if row := rows[byName["later"].Index]; row.QuotaState != "fresh" || row.Rank != 2 || row.Window == nil || row.Window.Key != "3p-weekly" {
		t.Fatalf("later row=%+v", row)
	}
	if row := rows[byName["malformed"].Index]; row.QuotaState != "malformed" || row.RefreshError != "malformed" || row.Rank != 3 {
		t.Fatalf("malformed row=%+v", row)
	}
}

func TestRoutingStatusAntigravityPools(t *testing.T) {
	ctx := context.Background()
	now := time.Now()
	s := &SoonestResetSelector{}
	m := NewManager(nil, s, nil)
	m.RegisterExecutor(&routingTestExecutor{provider: "antigravity"})
	var auths []*Auth
	for _, name := range []string{"a", "b"} {
		id := t.Name() + "-" + name
		saved, err := m.Register(ctx, antigravityRoutingTestAuth(id, now))
		if err != nil {
			t.Fatal(err)
		}
		auths = append(auths, saved)
		registry.GetGlobalRegistry().RegisterClient(id, "antigravity", []*registry.ModelInfo{{ID: "claude-opus-5-5-high"}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
	}
	window := func(key, pool string, reset time.Duration, used float64) RoutingWindow {
		w := routingTestWindow(key, now, reset, used, 604800)
		w.model = pool
		return w
	}
	// a resets Gemini sooner, b resets Claude/GPT sooner, and b's Gemini pool is spent.
	setRoutingTestQuota(s, auths[0], now, window("gemini-weekly", antigravityGeminiPool, 24*time.Hour, 10), window("3p-weekly", antigravityThirdParty, 96*time.Hour, 10))
	setRoutingTestQuota(s, auths[1], now, window("gemini-weekly", antigravityGeminiPool, 48*time.Hour, 100), window("3p-weekly", antigravityThirdParty, 48*time.Hour, 10))
	rows := func(status RoutingStatus) map[string]RoutingAccountStatus {
		out := make(map[string]RoutingAccountStatus)
		for _, row := range status.Accounts {
			out[row.AuthIndex] = row
		}
		return out
	}
	gemini := m.RoutingStatus(ctx, "antigravity", "", now)
	if gemini.Pool != "gemini" || len(gemini.Pools) != 2 || gemini.Pools[0].Key != "gemini" || gemini.Pools[1].Key != "claude-gpt" || gemini.NextAuthIndex != auths[0].Index {
		t.Fatalf("default pool status=%+v", gemini)
	}
	if row := rows(gemini)[auths[1].Index]; row.SkippedReason != "quota_exhausted" || row.Window == nil || row.Window.Key != "gemini-weekly" {
		t.Fatalf("spent Gemini row=%+v", row)
	}
	other := m.RoutingStatusForPool(ctx, "antigravity", "", "claude-gpt", now)
	if other.Pool != "claude-gpt" || other.NextAuthIndex != auths[1].Index || rows(other)[auths[0].Index].Window.Key != "3p-weekly" {
		t.Fatalf("claude-gpt status=%+v", other)
	}
	// A named model ranks by the windows that apply to it, not by a pool.
	if byModel := m.RoutingStatus(ctx, "antigravity", "claude-opus-5-5-high", now); byModel.Pool != "" || byModel.NextAuthIndex != auths[1].Index {
		t.Fatalf("model status=%+v", byModel)
	}
	if codex := m.RoutingStatus(ctx, "codex", "", now); codex.Pool != "" || codex.Pools != nil {
		t.Fatalf("codex status=%+v", codex)
	}
}
