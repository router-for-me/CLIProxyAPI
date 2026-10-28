package auth

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	internalconfig "github.com/router-for-me/CLIProxyAPI/v8/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

var quotaTestNow = time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)

func TestClaudeQuotaMixedRoundRobinKeepsBothProviders(t *testing.T) {
	selector := &ClaudeQuotaSelector{SnapshotPath: quotaTestSnapshot(t, quotaTestNow, map[string]claudeQuotaAccount{"a-claude": quotaTestAccount(quotaTestNow, time.Hour, time.Hour)}), Fallback: &RoundRobinSelector{}}
	auths := []*Auth{quotaTestAuth("a-claude", "claude"), quotaTestAuth("z-other", "codex")}
	counts := map[string]int{}
	for attempt := 0; attempt < 6; attempt++ {
		selected, errPick := selector.pick(context.Background(), "mixed", "model", cliproxyexecutor.Options{}, auths, quotaTestNow)
		if errPick != nil {
			t.Fatal(errPick)
		}
		counts[selected.ID]++
	}
	if counts["a-claude"] != 3 || counts["z-other"] != 3 {
		t.Fatalf("provider counts=%v", counts)
	}
}

func TestClaudeQuotaInvalidAccountDoesNotBlockHealthyAccount(t *testing.T) {
	accounts := map[string]claudeQuotaAccount{"expired": quotaTestAccount(quotaTestNow, time.Hour, -time.Minute), "healthy": quotaTestAccount(quotaTestNow, 24*time.Hour, time.Hour)}
	selector := &ClaudeQuotaSelector{SnapshotPath: quotaTestSnapshot(t, quotaTestNow, accounts), Fallback: &RoundRobinSelector{}}
	selected, errPick := selector.pick(context.Background(), "claude", "claude-sonnet-4-6", cliproxyexecutor.Options{}, []*Auth{quotaTestAuth("expired", "claude"), quotaTestAuth("healthy", "claude"), quotaTestAuth("unknown", "claude")}, quotaTestNow)
	if errPick != nil || selected == nil || selected.ID != "healthy" {
		t.Fatalf("selected=%v error=%v", selected, errPick)
	}
}

func TestClaudeQuotaZeroWeightCannotWinWeeklyOrder(t *testing.T) {
	zero := quotaTestAuth("zero", "claude")
	zero.Attributes[AttributeWeight] = "0"
	positive := quotaTestAuth("positive", "claude")
	selector := &ClaudeQuotaSelector{SnapshotPath: quotaTestSnapshot(t, quotaTestNow, map[string]claudeQuotaAccount{"zero": quotaTestAccount(quotaTestNow, time.Hour, time.Hour), "positive": quotaTestAccount(quotaTestNow, 24*time.Hour, time.Hour)}), Fallback: &WeightedRoundRobinSelector{}}
	selected, errPick := selector.pick(context.Background(), "claude", "claude-sonnet-4-6", cliproxyexecutor.Options{}, []*Auth{zero, positive}, quotaTestNow)
	if errPick != nil || selected == nil || selected.ID != "positive" {
		t.Fatalf("selected=%v error=%v", selected, errPick)
	}
}

func TestClaudeQuotaPreservesNonClaudeAffinityLifecycle(t *testing.T) {
	affinity := NewSessionAffinitySelector(&RoundRobinSelector{})
	t.Cleanup(affinity.Stop)
	selector := &ClaudeQuotaSelector{Fallback: affinity}
	model := "gpt-model"
	manager := quotaTestManager(t, selector, model, quotaTestAuth(t.Name(), "codex"))
	opts := cliproxyexecutor.Options{Headers: map[string][]string{"X-Session-Id": {"quota-affinity-session"}}}
	selected, errPick := manager.SelectAuth(context.Background(), "codex", model, opts)
	if errPick != nil {
		t.Fatal(errPick)
	}
	bound, _ := selector.LookupAffinity("codex", model, "quota-affinity-session")
	if bound != selected.ID {
		t.Fatalf("binding=%s want %s", bound, selected.ID)
	}
	manager.MarkResult(context.Background(), Result{AuthID: selected.ID, Provider: "codex", Model: model, Options: opts, Error: &Error{HTTPStatus: 429, Message: "quota exhausted"}})
	bound, _ = selector.LookupAffinity("codex", model, "quota-affinity-session")
	if bound != "" {
		t.Fatalf("quota failed binding was retained: %s", bound)
	}
	if observed, ok := sessionAffinitySelector(selector); !ok || observed != affinity {
		t.Fatal("manager cannot access wrapped affinity")
	}
}

func quotaTestAccount(now time.Time, weekly, fiveHour time.Duration) claudeQuotaAccount {
	eligible := true
	return claudeQuotaAccount{WeeklyReset: now.Add(weekly), FiveHourReset: now.Add(fiveHour), Eligible: &eligible}
}

func quotaTestSnapshot(t *testing.T, now time.Time, accounts map[string]claudeQuotaAccount) string {
	t.Helper()
	raw, err := json.Marshal(claudeQuotaSnapshot{ObservedAt: now, Accounts: accounts})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "quota.json")
	if err := os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	return path
}

func quotaTestAuth(id, provider string) *Auth {
	return &Auth{ID: id, Provider: provider, Status: StatusActive, Attributes: map[string]string{AttributeAuthKind: AuthKindOAuth}}
}

func quotaTestError(t *testing.T, err error, code string) {
	t.Helper()
	var target *Error
	if !errors.As(err, &target) || target.Code != code || target.HTTPStatus != 503 {
		t.Fatalf("error = %v, want %s with HTTP 503", err, code)
	}
}

func TestClaudeQuotaSelectorOrdering(t *testing.T) {
	for _, tc := range []struct {
		name string
		a, b claudeQuotaAccount
		want string
	}{
		{"weekly before five hour", quotaTestAccount(quotaTestNow, time.Hour, 4*time.Hour), quotaTestAccount(quotaTestNow, 2*time.Hour, time.Hour), "a"},
		{"five hour breaks weekly tie", quotaTestAccount(quotaTestNow, time.Hour, 3*time.Hour), quotaTestAccount(quotaTestNow, time.Hour, time.Hour), "b"},
		{"ID breaks exact tie", quotaTestAccount(quotaTestNow, time.Hour, time.Hour), quotaTestAccount(quotaTestNow, time.Hour, time.Hour), "a"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			selector := &ClaudeQuotaSelector{SnapshotPath: quotaTestSnapshot(t, quotaTestNow, map[string]claudeQuotaAccount{"a": tc.a, "b": tc.b}), Fallback: &FillFirstSelector{}}
			for _, ids := range [][]string{{"b", "a"}, {"a", "b"}} {
				picked, err := selector.pick(context.Background(), "claude", "claude-sonnet-4-6", cliproxyexecutor.Options{}, []*Auth{quotaTestAuth(ids[0], "claude"), quotaTestAuth(ids[1], "claude")}, quotaTestNow)
				if err != nil || picked == nil || picked.ID != tc.want {
					t.Fatalf("picked = %v, err = %v, want %s", picked, err, tc.want)
				}
			}
		})
	}
}

func TestClaudeQuotaSelectorFailClosed(t *testing.T) {
	for _, tc := range []struct {
		name     string
		observed time.Time
		account  claudeQuotaAccount
		raw      string
		missing  bool
		code     string
	}{
		{name: "missing file", missing: true},
		{name: "invalid JSON", raw: "{"},
		{name: "missing accounts", raw: `{"observed_at":"2026-10-28T12:00:00Z"}`},
		{name: "missing observation", raw: `{"accounts":{}}`},
		{name: "stale", observed: quotaTestNow.Add(-2*time.Minute - time.Nanosecond), account: quotaTestAccount(quotaTestNow, time.Hour, time.Hour)},
		{name: "future observation", observed: quotaTestNow.Add(time.Nanosecond), account: quotaTestAccount(quotaTestNow, time.Hour, time.Hour)},
		{name: "unknown account", raw: `{"observed_at":"2026-10-28T12:00:00Z","accounts":{}}`},
		{name: "unknown eligibility", observed: quotaTestNow, account: claudeQuotaAccount{WeeklyReset: quotaTestNow.Add(time.Hour), FiveHourReset: quotaTestNow.Add(time.Hour)}},
		{name: "five hour expired at boundary", observed: quotaTestNow, account: quotaTestAccount(quotaTestNow, time.Hour, 0)},
		{name: "weekly expired at boundary", observed: quotaTestNow, account: quotaTestAccount(quotaTestNow, 0, time.Hour)},
		{name: "missing windows", observed: quotaTestNow, account: func() claudeQuotaAccount {
			a := quotaTestAccount(quotaTestNow, time.Hour, time.Hour)
			a.FiveHourReset = time.Time{}
			return a
		}()},
		{name: "quota exhausted", observed: quotaTestNow, account: func() claudeQuotaAccount {
			a := quotaTestAccount(quotaTestNow, time.Hour, time.Hour)
			*a.Eligible = false
			return a
		}(), code: "claude_quota_exhausted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "missing.json")
			if !tc.missing {
				if tc.raw != "" {
					if err := os.WriteFile(path, []byte(tc.raw), 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					path = quotaTestSnapshot(t, tc.observed, map[string]claudeQuotaAccount{"a": tc.account})
				}
			}
			selector := &ClaudeQuotaSelector{SnapshotPath: path}
			picked, err := selector.pick(context.Background(), "claude", "claude-sonnet-4-6", cliproxyexecutor.Options{}, []*Auth{quotaTestAuth("a", "claude")}, quotaTestNow)
			if picked != nil {
				t.Fatalf("invalid Claude selected: %s", picked.ID)
			}
			code := tc.code
			if code == "" {
				code = "claude_quota_unavailable"
			}
			quotaTestError(t, err, code)
			// The same failed snapshot must leave another provider available.
			picked, err = selector.pick(context.Background(), "mixed", "claude-sonnet-4-6", cliproxyexecutor.Options{}, []*Auth{quotaTestAuth("a", "claude"), quotaTestAuth("other", "antigravity")}, quotaTestNow)
			if err != nil || picked == nil || picked.ID != "other" {
				t.Fatalf("mixed picked=%v err=%v, want other", picked, err)
			}
		})
	}
}

func TestClaudeQuotaSelectorSnapshotAgeBoundary(t *testing.T) {
	selector := &ClaudeQuotaSelector{SnapshotPath: quotaTestSnapshot(t, quotaTestNow.Add(-2*time.Minute), map[string]claudeQuotaAccount{"a": quotaTestAccount(quotaTestNow, time.Hour, time.Hour)})}
	picked, err := selector.pick(context.Background(), "claude", "claude-sonnet-4-6", cliproxyexecutor.Options{}, []*Auth{quotaTestAuth("a", "claude")}, quotaTestNow)
	if err != nil || picked == nil || picked.ID != "a" {
		t.Fatalf("exactly two minutes old picked=%v err=%v", picked, err)
	}
	_, err = selector.pick(context.Background(), "claude", "claude-sonnet-4-6", cliproxyexecutor.Options{}, []*Auth{quotaTestAuth("a", "claude")}, quotaTestNow.Add(time.Nanosecond))
	quotaTestError(t, err, "claude_quota_unavailable")
}

func TestClaudeQuotaSelectorFamilyGuard(t *testing.T) {
	for _, tc := range []struct {
		model   string
		blocked []string
		allowed bool
	}{
		{"claude-opus-4-6", []string{"OPUS"}, false},
		{"claude-sonnet-4-6", []string{"opus"}, true},
		{"unresolved-alias", []string{"opus"}, false},
		{"claude-sonnet-4-6", []string{""}, false},
	} {
		t.Run(tc.model+"/"+tc.blocked[0], func(t *testing.T) {
			a := quotaTestAccount(quotaTestNow, time.Hour, time.Hour)
			a.BlockedModelFamilies = tc.blocked
			selector := &ClaudeQuotaSelector{SnapshotPath: quotaTestSnapshot(t, quotaTestNow, map[string]claudeQuotaAccount{"a": a})}
			picked, err := selector.pick(context.Background(), "claude", tc.model, cliproxyexecutor.Options{}, []*Auth{quotaTestAuth("a", "claude")}, quotaTestNow)
			if tc.allowed {
				if err != nil || picked == nil {
					t.Fatalf("picked=%v err=%v", picked, err)
				}
			} else {
				if picked != nil {
					t.Fatal("blocked Claude selected")
				}
				quotaTestError(t, err, "claude_quota_exhausted")
			}
		})
	}
}

type quotaTestFallback struct {
	calls int
	ids   []string
	model string
	opts  cliproxyexecutor.Options
	err   error
}

func (f *quotaTestFallback) Pick(_ context.Context, _ string, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	f.calls++
	f.model = model
	f.opts = opts
	f.ids = nil
	for _, a := range auths {
		f.ids = append(f.ids, a.ID)
	}
	if f.err != nil {
		return nil, f.err
	}
	return auths[len(auths)-1], nil
}

func TestClaudeQuotaSelectorNonClaudeUnchanged(t *testing.T) {
	fallback := &quotaTestFallback{}
	selector := &ClaudeQuotaSelector{SnapshotPath: "does-not-exist", Fallback: fallback}
	apiKey := quotaTestAuth("api-key", "claude")
	apiKey.Attributes[AttributeAuthKind] = AuthKindAPIKey
	auths := []*Auth{quotaTestAuth("other", "codex"), apiKey}
	opts := cliproxyexecutor.Options{Metadata: map[string]any{"sentinel": "value"}}
	picked, err := selector.pick(context.Background(), "mixed", "model-alias", opts, auths, quotaTestNow)
	if err != nil || picked != apiKey || fallback.calls != 1 || len(fallback.ids) != 2 || fallback.model != "model-alias" || fallback.opts.Metadata["sentinel"] != "value" {
		t.Fatalf("fallback altered: picked=%v err=%v fallback=%+v", picked, err, fallback)
	}
	fallback.err = errors.New("fallback sentinel")
	_, err = selector.pick(context.Background(), "mixed", "model-alias", opts, auths, quotaTestNow)
	if !errors.Is(err, fallback.err) {
		t.Fatalf("fallback error not preserved: %v", err)
	}
}

type quotaTestPlugin struct {
	calls int
	id    string
}

func (p *quotaTestPlugin) PickAuth(context.Context, pluginapi.SchedulerPickRequest) (pluginapi.SchedulerPickResponse, bool, error) {
	p.calls++
	return pluginapi.SchedulerPickResponse{AuthID: p.id, Handled: true}, true, nil
}
func (p *quotaTestPlugin) SchedulerWantsAcrossPriorities() bool { return true }

func quotaTestManager(t *testing.T, selector Selector, model string, auths ...*Auth) *Manager {
	t.Helper()
	m := NewManager(nil, selector, nil)
	for _, a := range auths {
		m.RegisterExecutor(schedulerProviderTestExecutor{provider: a.Provider})
		if _, err := m.Register(context.Background(), a); err != nil {
			t.Fatal(err)
		}
		id := a.ID
		registry.GetGlobalRegistry().RegisterClient(id, a.Provider, []*registry.ModelInfo{{ID: model}})
		t.Cleanup(func() { registry.GetGlobalRegistry().UnregisterClient(id) })
		m.RefreshSchedulerEntry(id)
	}
	return m
}

func TestClaudeQuotaManagerNativeGuard(t *testing.T) {
	for _, mixed := range []bool{false, true} {
		for _, pluginEnabled := range []bool{false, true} {
			name := "single"
			if mixed {
				name = "mixed"
			}
			if pluginEnabled {
				name += "/plugin"
			}
			t.Run(name, func(t *testing.T) {
				now := time.Now().UTC()
				model := "claude-sonnet-4-6"
				early := quotaTestAuth(t.Name()+"-early", "claude")
				late := quotaTestAuth(t.Name()+"-late", "claude")
				early.Attributes["priority"] = "0"
				late.Attributes["priority"] = "10"
				selector := &ClaudeQuotaSelector{SnapshotPath: quotaTestSnapshot(t, now, map[string]claudeQuotaAccount{early.ID: quotaTestAccount(now, time.Hour, 4*time.Hour), late.ID: quotaTestAccount(now, 24*time.Hour, time.Hour)}), Fallback: &FillFirstSelector{}}
				m := quotaTestManager(t, selector, model, early, late)
				plugin := &quotaTestPlugin{id: late.ID}
				if pluginEnabled {
					m.SetPluginScheduler(plugin)
				}
				if m.useSchedulerFastPath() {
					t.Fatal("native quota selector must bypass built-in scheduler")
				}
				pick := func(tried map[string]struct{}) (*Auth, error) {
					if mixed {
						a, _, _, err := m.pickNextMixed(context.Background(), []string{"claude"}, model, cliproxyexecutor.Options{}, tried)
						return a, err
					}
					a, _, err := m.pickNext(context.Background(), "claude", model, cliproxyexecutor.Options{}, tried)
					return a, err
				}
				picked, err := pick(nil)
				if err != nil || picked == nil || picked.ID != early.ID {
					t.Fatalf("weekly priority picked=%v err=%v", picked, err)
				}
				picked, err = pick(map[string]struct{}{early.ID: {}})
				if err != nil || picked == nil || picked.ID != late.ID {
					t.Fatalf("retry exclusion picked=%v err=%v", picked, err)
				}
				retry := time.Hour
				m.MarkResult(context.Background(), Result{AuthID: early.ID, Provider: "claude", Model: model, Error: &Error{HTTPStatus: 429, Message: "rate limited"}, RetryAfter: &retry})
				picked, err = pick(nil)
				if err != nil || picked == nil || picked.ID != late.ID {
					t.Fatalf("cooldown exclusion picked=%v err=%v", picked, err)
				}
				selector.SnapshotPath = filepath.Join(t.TempDir(), "missing.json")
				picked, err = pick(nil)
				if picked != nil {
					t.Fatalf("missing snapshot selected %s", picked.ID)
				}
				quotaTestError(t, err, "claude_quota_unavailable")
				if plugin.calls != 0 {
					t.Fatalf("plugin bypassed guard: %d calls", plugin.calls)
				}
			})
		}
	}
}

func TestClaudeQuotaManagerOAuthAliasFamilies(t *testing.T) {
	for _, target := range []string{"claude-sonnet-4-6", "claude-opus-4-6"} {
		t.Run(target, func(t *testing.T) {
			now := time.Now().UTC()
			route := "quota-friendly-alias"
			a := quotaTestAuth(t.Name(), "claude")
			account := quotaTestAccount(now, time.Hour, time.Hour)
			account.BlockedModelFamilies = []string{"opus"}
			m := quotaTestManager(t, &ClaudeQuotaSelector{SnapshotPath: quotaTestSnapshot(t, now, map[string]claudeQuotaAccount{a.ID: account})}, route, a)
			registry.GetGlobalRegistry().RegisterClient(a.ID, "claude", []*registry.ModelInfo{{ID: route}, {ID: target}})
			m.SetOAuthModelAlias(map[string][]internalconfig.OAuthModelAlias{"claude": {{Name: target, Alias: route, Fork: true}}})
			m.RefreshSchedulerEntry(a.ID)
			for _, mixed := range []bool{false, true} {
				var picked *Auth
				var err error
				if mixed {
					picked, _, _, err = m.pickNextMixed(context.Background(), []string{"claude"}, route, cliproxyexecutor.Options{}, nil)
				} else {
					picked, _, err = m.pickNext(context.Background(), "claude", route, cliproxyexecutor.Options{}, nil)
				}
				if target == "claude-sonnet-4-6" {
					if err != nil || picked == nil || picked.ID != a.ID {
						t.Fatalf("alias to sonnet picked=%v err=%v", picked, err)
					}
				} else {
					if picked != nil {
						t.Fatal("alias bypassed opus restriction")
					}
					quotaTestError(t, err, "claude_quota_exhausted")
				}
			}
		})
	}
}

func TestClaudeQuotaSelectorExcludesExhaustedFiveHourAccount(t *testing.T) {
	early := quotaTestAccount(quotaTestNow, time.Hour, time.Hour)
	*early.Eligible = false
	later := quotaTestAccount(quotaTestNow, 24*time.Hour, 4*time.Hour)
	selector := &ClaudeQuotaSelector{SnapshotPath: quotaTestSnapshot(t, quotaTestNow, map[string]claudeQuotaAccount{"early-exhausted": early, "later": later}), Fallback: &FillFirstSelector{}}
	picked, err := selector.pick(context.Background(), "claude", "claude-sonnet-4-6", cliproxyexecutor.Options{}, []*Auth{quotaTestAuth("early-exhausted", "claude"), quotaTestAuth("later", "claude")}, quotaTestNow)
	if err != nil || picked == nil || picked.ID != "later" {
		t.Fatalf("exhausted five hour candidate won: picked=%v err=%v", picked, err)
	}
}

func TestClaudeQuotaSelectorMixedFallbackCannotSeeInvalidClaude(t *testing.T) {
	for _, unknown := range []bool{false, true} {
		name := "exhausted"
		if unknown {
			name = "unknown"
		}
		t.Run(name, func(t *testing.T) {
			account := quotaTestAccount(quotaTestNow, time.Hour, time.Hour)
			if unknown {
				account.Eligible = nil
			} else {
				*account.Eligible = false
			}
			fallback := &quotaTestFallback{}
			selector := &ClaudeQuotaSelector{SnapshotPath: quotaTestSnapshot(t, quotaTestNow, map[string]claudeQuotaAccount{"invalid": account}), Fallback: fallback}
			picked, err := selector.pick(context.Background(), "mixed", "claude-sonnet-4-6", cliproxyexecutor.Options{}, []*Auth{quotaTestAuth("other", "antigravity"), quotaTestAuth("invalid", "claude")}, quotaTestNow)
			if err != nil || picked == nil || picked.ID != "other" || len(fallback.ids) != 1 || fallback.ids[0] != "other" {
				t.Fatalf("unsafe fallback pool: picked=%v err=%v ids=%v", picked, err, fallback.ids)
			}
		})
	}
}

func TestClaudeQuotaManagerMixedInvalidClaudeKeepsOtherProvider(t *testing.T) {
	now := time.Now().UTC()
	model := "claude-sonnet-4-6"
	claude := quotaTestAuth(t.Name()+"-claude", "claude")
	other := quotaTestAuth(t.Name()+"-other", "antigravity")
	selector := &ClaudeQuotaSelector{SnapshotPath: quotaTestSnapshot(t, now, map[string]claudeQuotaAccount{}), Fallback: &FillFirstSelector{}}
	manager := quotaTestManager(t, selector, model, claude, other)
	plugin := &quotaTestPlugin{id: claude.ID}
	manager.SetPluginScheduler(plugin)
	picked, _, provider, err := manager.pickNextMixed(context.Background(), []string{"claude", "antigravity"}, model, cliproxyexecutor.Options{}, nil)
	if err != nil || picked == nil || picked.ID != other.ID || provider != "antigravity" {
		t.Fatalf("mixed native guard picked=%v provider=%s err=%v", picked, provider, err)
	}
	if plugin.calls != 0 {
		t.Fatalf("plugin bypassed quota guard: %d calls", plugin.calls)
	}
	// When no Claude candidate remains, the plugin retains its ordinary role.
	plugin.id = other.ID
	picked, _, _, err = manager.pickNextMixed(context.Background(), []string{"claude", "antigravity"}, model, cliproxyexecutor.Options{}, map[string]struct{}{claude.ID: {}})
	if err != nil || picked == nil || picked.ID != other.ID || plugin.calls != 1 {
		t.Fatalf("non-Claude scheduling changed: picked=%v err=%v plugin calls=%d", picked, err, plugin.calls)
	}
}
