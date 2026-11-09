package auth

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	exec "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

type bankedFixture struct {
	provider   string
	mu         sync.Mutex
	credits    string
	exhausted  bool
	postCode   string
	failPost   bool
	posts      []map[string]string
	reads      int
	executions int
	first429   bool
}

func (f *bankedFixture) Identifier() string                                { return f.provider }
func (f *bankedFixture) Refresh(_ context.Context, a *Auth) (*Auth, error) { return a, nil }
func (f *bankedFixture) Execute(_ context.Context, _ *Auth, _ exec.Request, _ exec.Options) (exec.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.executions++
	if f.first429 && f.executions == 1 {
		f.exhausted = true
		return exec.Response{}, &Error{HTTPStatus: 429, Message: "quota exhausted"}
	}
	if f.exhausted {
		return exec.Response{}, errors.New("paid credits would be used")
	}
	return exec.Response{Payload: []byte(`{"type":"response.completed"}`)}, nil
}
func (f *bankedFixture) ExecuteStream(ctx context.Context, a *Auth, r exec.Request, o exec.Options) (*exec.StreamResult, error) {
	if _, err := f.Execute(ctx, a, r, o); err != nil {
		return nil, err
	}
	ch := make(chan exec.StreamChunk)
	close(ch)
	return &exec.StreamResult{Chunks: ch}, nil
}
func (f *bankedFixture) CountTokens(context.Context, *Auth, exec.Request, exec.Options) (exec.Response, error) {
	return exec.Response{}, nil
}
func (f *bankedFixture) HttpRequest(_ context.Context, _ *Auth, r *http.Request) (*http.Response, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	body := ""
	if r.Method == http.MethodPost {
		var payload map[string]string
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			return nil, err
		}
		f.posts = append(f.posts, payload)
		if f.failPost {
			return nil, errors.New("ambiguous transport error")
		}
		f.exhausted = false
		if f.provider == "codex" {
			body = `{"code":"` + f.postCode + `"}`
		} else {
			body = `{"result":"` + f.postCode + `"}`
		}
	} else {
		f.reads++
		switch {
		case strings.HasSuffix(r.URL.Path, "/profile"):
			body = `{"organization":{"uuid":"00000000-0000-0000-0000-000000000001"}}`
		case f.provider == "codex" && strings.HasSuffix(r.URL.Path, "/usage"):
			if f.exhausted {
				body = `{"rate_limit":{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":100}}}`
			} else {
				body = `{"rate_limit":{"allowed":true,"limit_reached":false,"primary_window":{"used_percent":1}}}`
			}
		default:
			body = f.credits
		}
	}
	return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
}

func newBankedFixture(t *testing.T, provider, mode string) (*Manager, *bankedFixture, string) {
	t.Helper()
	m := NewManager(nil, nil, nil)
	cfg := &config.Config{}
	cfg.QuotaExceeded.BankedReset = config.BankedResetConfig{Mode: mode, UseBefore: "2h"}
	m.SetConfig(cfg)
	f := &bankedFixture{provider: provider, postCode: "reset"}
	m.RegisterExecutor(f)
	a := &Auth{ID: t.Name(), Provider: provider, Status: StatusActive, Metadata: map[string]any{"access_token": "test-only", "account_id": t.Name(), "email": t.Name()}}
	if _, err := m.Register(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	return m, f, a.ID
}
func resetCredits(now time.Time, expiry time.Duration) string {
	return `{"credits":[{"id":"later","status":"available","reset_type":"codex_rate_limits","expires_at":"` + now.Add(expiry+time.Hour).Format(time.RFC3339) + `"},{"id":"earliest","status":"available","reset_type":"codex_rate_limits","expires_at":"` + now.Add(expiry).Format(time.RFC3339) + `"}]}`
}
func claudeGrants(now time.Time, limit, requires bool, expiry time.Duration) string {
	b, _ := json.Marshal(map[string]any{"cedar_ember": map[string]any{"eligible": true, "at_limit": limit, "grants": []any{map[string]any{"id": "grant_1", "resets_left": 1, "usable_now": true, "use_requires_limit": requires, "ends_at": now.Add(expiry).Format(time.RFC3339)}}}})
	return string(b)
}
func TestBankedResetRules(t *testing.T) {
	for _, tc := range []struct {
		name, provider, mode string
		exhausted            bool
		expiry               time.Duration
		count                int
	}{
		{"exhausted-before-paid", "codex", "auto", true, 24 * time.Hour, 1},
		{"one-percent-gained", "codex", "auto", false, time.Hour, 1},
		{"not-yet-expiring", "codex", "auto", false, 24 * time.Hour, 0},
		{"already-expired", "codex", "auto", false, -3 * time.Hour, 0},
		{"default-off", "codex", "", true, time.Hour, 0},
		{"manual-does-not-auto", "codex", "manual", true, time.Hour, 0},
		{"claude-exhausted", "claude", "auto", true, 24 * time.Hour, 1},
		{"claude-expiry", "claude", "auto", false, time.Hour, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m, f, id := newBankedFixture(t, tc.provider, tc.mode)
			now := time.Now()
			f.exhausted = tc.exhausted
			if tc.provider == "codex" {
				f.credits = resetCredits(now, tc.expiry)
			} else {
				f.credits = claudeGrants(now, tc.exhausted, false, tc.expiry)
			}
			a, _ := m.GetByID(id)
			a.Quota = QuotaState{Exceeded: true, Reason: "quota", NextRecoverAt: now.Add(time.Hour)}
			a.Unavailable = true
			a.NextRetryAfter = now.Add(time.Hour)
			a.ModelStates = map[string]*ModelState{"gpt-test": {Unavailable: true, Quota: a.Quota, NextRetryAfter: now.Add(time.Hour)}}
			if _, err := m.Update(context.Background(), a); err != nil {
				t.Fatal(err)
			}
			m.CheckBankedResets(context.Background(), nil)
			if len(f.posts) != tc.count {
				t.Fatalf("posts=%d want %d", len(f.posts), tc.count)
			}
			updated, _ := m.GetByID(id)
			if tc.count > 0 {
				if updated.Quota.Exceeded || updated.Unavailable || updated.ModelStates["gpt-test"].Quota.Exceeded {
					t.Fatal("cooldown not cleared")
				}
				if bankedOperation(updated) == nil || bankedOperation(updated).Code != "reset" {
					t.Fatal("no durable audit")
				}
				if tc.provider == "codex" && f.posts[0]["credit_id"] != "earliest" {
					t.Fatal("did not choose earliest")
				}
			} else if !updated.Quota.Exceeded {
				t.Fatal("cleared cooldown without reset")
			}
			if tc.mode == "" && f.reads != 0 {
				t.Fatal("disabled mode made a request")
			}
		})
	}
}
func TestBankedResetReadOnlyNoResetsAndClaudeGuards(t *testing.T) {
	for _, provider := range []string{"codex", "claude"} {
		t.Run(provider, func(t *testing.T) {
			m, f, id := newBankedFixture(t, provider, "auto")
			now := time.Now()
			f.exhausted = true
			if provider == "codex" {
				f.credits = resetCredits(now, time.Minute)
			} else {
				f.credits = claudeGrants(now, false, true, time.Minute)
			}
			if _, err := m.ListBankedResets(context.Background(), id); err != nil {
				t.Fatal(err)
			}
			if len(f.posts) != 0 {
				t.Fatal("listing spent a reset")
			}
			if provider == "claude" {
				m.CheckBankedResets(context.Background(), nil)
				if len(f.posts) != 0 {
					t.Fatal("ignored requires-limit")
				}
			}
			f.credits = `{"credits":[],"cedar_ember":{"eligible":false}}`
			m.CheckBankedResets(context.Background(), nil)
			if len(f.posts) != 0 {
				t.Fatal("spent a missing reset")
			}
		})
	}
}
func TestBankedResetConcurrency(t *testing.T) {
	m, f, id := newBankedFixture(t, "codex", "auto")
	f.exhausted = true
	f.credits = resetCredits(time.Now(), time.Hour)
	var wg sync.WaitGroup
	for i := 0; i < 30; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); m.CheckBankedResets(context.Background(), nil) }()
	}
	wg.Wait()
	if len(f.posts) != 1 {
		t.Fatalf("posts=%d want 1", len(f.posts))
	}
	a, _ := m.GetByID(id)
	if a.Quota.Exceeded {
		t.Fatal("still cooling")
	}
}
func TestBankedResetAmbiguousRetryKeepsIDAcrossRestart(t *testing.T) {
	m, f, id := newBankedFixture(t, "codex", "auto")
	now := time.Now()
	f.exhausted = true
	f.failPost = true
	f.credits = resetCredits(now, time.Hour)
	if _, err := m.useBankedReset(context.Background(), id, "", true, now); err == nil {
		t.Fatal("expected transport failure")
	}
	m.CheckBankedResets(context.Background(), nil)
	if len(f.posts) != 1 {
		t.Fatal("backoff did not suppress retry")
	}
	a, _ := m.GetByID(id)
	replacement := NewManager(nil, nil, nil)
	replacement.SetConfig(m.runtimeConfigSnapshot())
	replacement.RegisterExecutor(f)
	if _, err := replacement.Register(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	f.failPost = false
	if _, err := replacement.useBankedReset(context.Background(), id, "", true, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(f.posts) != 2 || f.posts[0]["redeem_request_id"] != f.posts[1]["redeem_request_id"] {
		t.Fatal("retry id changed")
	}
}
func TestBankedResetUnconfirmedDoesNotClearCooldown(t *testing.T) {
	m, f, id := newBankedFixture(t, "codex", "auto")
	f.exhausted = true
	f.postCode = "not_limited"
	f.credits = resetCredits(time.Now(), time.Hour)
	a, _ := m.GetByID(id)
	a.Quota.Exceeded = true
	if _, err := m.Update(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	m.CheckBankedResets(context.Background(), nil)
	a, _ = m.GetByID(id)
	if !a.Quota.Exceeded {
		t.Fatal("unconfirmed reset cleared quota")
	}
}
func TestBankedResetPreflightPreventsPaidExecution(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(strconvBool(stream), func(t *testing.T) {
			m, f, id := newBankedFixture(t, "codex", "auto")
			f.exhausted = true
			f.credits = resetCredits(time.Now(), 24*time.Hour)
			model := "gpt-banked-preflight"
			registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}})
			defer registry.GetGlobalRegistry().UnregisterClient(id)
			var err error
			if stream {
				_, err = m.ExecuteStream(context.Background(), []string{"codex"}, exec.Request{Model: model}, exec.Options{})
			} else {
				_, err = m.Execute(context.Background(), []string{"codex"}, exec.Request{Model: model}, exec.Options{})
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(f.posts) != 1 || f.executions != 1 {
				t.Fatalf("posts=%d executions=%d", len(f.posts), f.executions)
			}
		})
	}
}
func strconvBool(v bool) string {
	if v {
		return "stream"
	}
	return "nonstream"
}

func TestBankedResetManualDuplicatesShareConfirmation(t *testing.T) {
	m, f, id := newBankedFixture(t, "codex", "manual")
	f.credits = resetCredits(time.Now(), 24*time.Hour)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if _, err := m.RedeemBankedReset(context.Background(), id, "earliest"); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if len(f.posts) != 1 {
		t.Fatalf("manual duplicate posts=%d", len(f.posts))
	}
}

func TestBankedResetProviderRestrictionAndManualChoice(t *testing.T) {
	m, f, id := newBankedFixture(t, "codex", "auto")
	now := time.Now()
	f.credits = resetCredits(now, time.Hour)
	cfg := &config.Config{}
	cfg.QuotaExceeded.BankedReset = config.BankedResetConfig{Mode: "auto", Providers: []string{"claude"}}
	m.SetConfig(cfg)
	m.CheckBankedResets(context.Background(), nil)
	if f.reads != 0 {
		t.Fatal("ignored automatic provider restriction")
	}
	result, err := m.RedeemBankedReset(context.Background(), id, "later")
	if err != nil {
		t.Fatal(err)
	}
	if result.CreditID != "later" || len(f.posts) != 1 || f.posts[0]["credit_id"] != "later" {
		t.Fatal("manual selection ignored")
	}
}

func TestBankedResetErrorBackoffGrows(t *testing.T) {
	m, f, id := newBankedFixture(t, "codex", "auto")
	now := time.Now()
	f.exhausted = true
	f.failPost = true
	f.credits = resetCredits(now, time.Hour)
	_, _ = m.useBankedReset(context.Background(), id, "", true, now)
	a, _ := m.GetByID(id)
	state := m.bankedAccount(a)
	if !state.retryAfter.Equal(now.Add(30 * time.Second)) {
		t.Fatal("first backoff")
	}
	_, _ = m.useBankedReset(context.Background(), id, "", true, now.Add(time.Minute))
	if !state.retryAfter.Equal(now.Add(2 * time.Minute)) {
		t.Fatal("second backoff did not grow")
	}
}

type bankedFailingStore struct{}

func (*bankedFailingStore) Save(context.Context, *Auth) (string, error) {
	return "", errors.New("disk unavailable")
}
func (*bankedFailingStore) Delete(context.Context, string) error  { return nil }
func (*bankedFailingStore) List(context.Context) ([]*Auth, error) { return nil, nil }
func TestBankedResetIntentMustBeDurableBeforeSending(t *testing.T) {
	m, f, id := newBankedFixture(t, "codex", "auto")
	now := time.Now()
	f.exhausted = true
	f.credits = resetCredits(now, time.Hour)
	m.store = &bankedFailingStore{}
	_, err := m.useBankedReset(context.Background(), id, "", true, now)
	if err == nil || len(f.posts) != 0 {
		t.Fatal("sent despite failed persistence")
	}
	a, _ := m.GetByID(id)
	if bankedOperation(a) != nil {
		t.Fatal("failed intent remained in memory")
	}
	_, _ = m.useBankedReset(context.Background(), id, "", true, now.Add(time.Minute))
	if len(f.posts) != 0 {
		t.Fatal("retry sent unpersisted intent")
	}
}

func TestBankedResetDuplicateCredentialsShareAccountLock(t *testing.T) {
	m, f, id := newBankedFixture(t, "codex", "auto")
	f.exhausted = true
	f.credits = resetCredits(time.Now(), time.Hour)
	a, _ := m.GetByID(id)
	a.ID += "-duplicate"
	a.Index = ""
	if _, err := m.Register(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); m.CheckBankedResets(context.Background(), nil) }()
	}
	wg.Wait()
	if len(f.posts) != 1 {
		t.Fatalf("posts across duplicated login=%d", len(f.posts))
	}
}

func TestBankedResetFirstRefusalCanUseFreshIDAfterQuotaChanges(t *testing.T) {
	m, f, id := newBankedFixture(t, "codex", "auto")
	now := time.Now()
	f.credits = resetCredits(now, time.Hour)
	f.postCode = "nothing_to_reset"
	_, err := m.useBankedReset(context.Background(), id, "", true, now)
	if err == nil {
		t.Fatal("refusal was treated as a reset")
	}
	a, _ := m.GetByID(id)
	restarted := NewManager(nil, nil, nil)
	restarted.SetConfig(m.runtimeConfigSnapshot())
	restarted.RegisterExecutor(f)
	if _, err := restarted.Register(context.Background(), a); err != nil {
		t.Fatal(err)
	}
	f.postCode = "reset"
	if _, err := restarted.useBankedReset(context.Background(), id, "", true, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(f.posts) != 2 || f.posts[0]["redeem_request_id"] == f.posts[1]["redeem_request_id"] {
		t.Fatal("settled no-spend attempt reused the old id")
	}
}

func TestBankedResetFresh429RetriesBeforeFallback(t *testing.T) {
	for _, stream := range []bool{false, true} {
		t.Run(strconvBool(stream), func(t *testing.T) {
			m, f, id := newBankedFixture(t, "codex", "auto")
			f.credits = resetCredits(time.Now(), 24*time.Hour)
			f.first429 = true
			model := "banked-429-" + strconvBool(stream)
			registry.GetGlobalRegistry().RegisterClient(id, "codex", []*registry.ModelInfo{{ID: model}})
			defer registry.GetGlobalRegistry().UnregisterClient(id)
			var err error
			if stream {
				_, err = m.ExecuteStream(context.Background(), []string{"codex"}, exec.Request{Model: model}, exec.Options{})
			} else {
				_, err = m.Execute(context.Background(), []string{"codex"}, exec.Request{Model: model}, exec.Options{})
			}
			if err != nil {
				t.Fatal(err)
			}
			if len(f.posts) != 1 || f.executions != 2 {
				t.Fatalf("posts=%d executions=%d", len(f.posts), f.executions)
			}
		})
	}
}
