//go:build proxdemo

// Demo server with synthetic accounts and traffic, for dashboard development
// and screenshots. Not part of normal test runs:
//
//	go test -tags proxdemo -run TestDemoServer -timeout 0 ./internal/prox/engine
//
// Then open http://127.0.0.1:18420/management.html and log in with any key.
// The Management Center's own endpoints are stubbed in demo_panel_test.go.
package engine

import (
	"context"
	"fmt"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/gin-gonic/gin"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/ignite"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/monitor"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/notify"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/quota"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/router"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

type demoAccount struct {
	id, provider, email, plan string
	five, weekly, fable       float64 // used percent
	fiveReset, weekReset      time.Duration
	resetCredits              int // Codex banked manual resets
}

var demoAccounts = []demoAccount{
	{"claude-main@kp.dev.json", "claude", "main@kp.dev", "max_20x", 34, 42, 49, 2*time.Hour + 10*time.Minute, 23 * time.Hour, 0},
	{"claude-ops@kp.gg.json", "claude", "ops@kp.gg", "max_20x", 0, 8, 12, 0, 3*24*time.Hour + 9*time.Hour, 0},
	{"claude-team@tk.gg.json", "claude", "team@tk.gg", "max_20x", 0, 0, 0, 0, 4*24*time.Hour + 2*time.Hour, 0},
	{"claude-alt@tk.gg.json", "claude", "alt@tk.gg", "max_20x", 61, 66, 71, 25 * time.Minute, 1*24*time.Hour + 11*time.Hour, 0},
	{"claude-side@gmail.com.json", "claude", "side@gmail.com", "max_5x", 0, 4, 2, 0, 5*24*time.Hour + 14*time.Hour, 0},
	{"codex-main@kp.dev-pro.json", "codex", "main@kp.dev", "pro", 0, 83, 0, 0, 2*24*time.Hour + 6*time.Hour, 2},
	{"codex-ops@kp.gg-pro.json", "codex", "ops@kp.gg", "pro", 0, 100, 0, 0, 3*24*time.Hour + 5*time.Hour, 3},
	{"codex-side@gmail.com-pro.json", "codex", "side@gmail.com", "pro", 0, 41, 0, 0, 6*24*time.Hour + 1*time.Hour, 3},
	{"kimi-main@kp.dev.json", "kimi", "main@kp.dev", "Moderato", 44, 18, 0, 3*time.Hour + 15*time.Minute, 4 * 24 * time.Hour, 0},
	{"xai-main@gmail.com.json", "xai", "main@gmail.com", "SuperGrok", 0, 62, 0, 0, 2*24*time.Hour + 9*time.Hour, 0},
}

type demoHost struct{}

func (demoHost) Roster() ([]router.RosterEntry, error) {
	var out []router.RosterEntry
	for i, a := range demoAccounts {
		out = append(out, router.RosterEntry{ID: a.id, AuthIndex: fmt.Sprint(i), Provider: a.provider, Name: a.id, Email: a.email, Status: "active", Path: "/auths/" + a.id})
	}
	return out, nil
}

func (demoHost) Credential(idx string) ([]byte, error) {
	var i int
	fmt.Sscan(idx, &i)
	a := demoAccounts[i]
	return json.Marshal(map[string]any{"type": a.provider, "access_token": "demo", "account_uuid": a.id, "plan_type": a.plan})
}

type demoFetcher struct{ now func() time.Time }

func (f demoFetcher) Fetch(_ context.Context, c quota.Credential) (quota.Snapshot, error) {
	now := f.now()
	for _, a := range demoAccounts {
		if a.id != c.AccountUUID {
			continue
		}
		wr := now.Add(a.weekReset)
		var fr *time.Time
		if a.fiveReset > 0 {
			t := now.Add(a.fiveReset)
			fr = &t
		}
		s := quota.Snapshot{Provider: a.provider, PlanType: a.plan, PolledAt: now, Identity: a.id, Windows: []quota.Window{
			{Kind: quota.Weekly, UsedPercent: a.weekly, ResetsAt: &wr, Source: quota.SourcePoll, ObservedAt: now},
		}}
		if a.provider == "claude" {
			s.Windows = append(s.Windows,
				quota.Window{Kind: quota.FiveHour, UsedPercent: a.five, ResetsAt: fr, Source: quota.SourcePoll, ObservedAt: now},
				quota.Window{Kind: quota.ModelWeekly, Family: "fable", UsedPercent: a.fable, ResetsAt: &wr, Source: quota.SourcePoll, ObservedAt: now})
		}
		return s, nil
	}
	return quota.Snapshot{}, &quota.Error{Code: "ineligible_credential"}
}

type demoMonFetcher struct{}

func (demoMonFetcher) GetJSON(_ context.Context, _, url string, _ map[string]string) ([]byte, error) {
	switch url {
	case quota.KimiUsageURL, quota.KimiAIUsageURL:
		return []byte(`{"usage":{"used":180,"limit":1000,"reset_in":345600},"limits":[{"window":{"duration":300,"timeUnit":"TIME_UNIT_MINUTE"},"detail":{"limit":200,"used":88,"reset_in":11700}}]}`), nil
	case quota.XaiBillingWeekly:
		end := time.Now().Add(2*24*time.Hour + 9*time.Hour).UTC().Format(time.RFC3339)
		return []byte(`{"config":{"creditUsagePercent":62,"currentPeriod":{"type":"WEEKLY","end":"` + end + `"}}}`), nil
	}
	return nil, &quota.Error{Code: "quota_http_404", Status: 404}
}

// demoIgnite wraps the real ignite host but never sends requests.
type demoIgnite struct{ igniteHost }

func (demoIgnite) Models(string) []string { return []string{"claude-haiku-5-5", "gpt-6-luna"} }
func (demoIgnite) Fire(context.Context, string, string, string) ignite.Result {
	return ignite.Result{}
}

func TestDemoServer(t *testing.T) {
	gin.SetMode(gin.ReleaseMode)
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte(`{"code":0}`)) }))
	defer ok.Close()
	dir := t.TempDir()
	e := Default()
	e.NewFetcher = func() quota.Fetcher { return demoFetcher{now: time.Now} }
	e.SetHost(demoHost{})
	cfg := fmt.Sprintf(`mode: active
state_dir: %s
account_alias: {"claude-main@kp.dev.json": "main"}
ignition:
  enabled: true
  start_hour: 0
  end_hour: 23
notify:
  webhook: {url: %s}
clickhouse:
  url: %s
usage:
  pricing:
    gpt-6-luna: {input: 1.25, output: 10, cache_read: 0.125}
    gpt-6: {input: 2.5, output: 15, cache_read: 0.25}
    kimi-k3: {input: 0.6, output: 2.5, cache_read: 0.15}
`, dir, ok.URL, ok.URL)
	if err := e.Apply(true, []byte(cfg)); err != nil {
		t.Fatal(err)
	}
	e.mu.Lock()
	mcfg := monitor.Default()
	e.mon = monitor.New(hostProxy{e: e}, demoMonFetcher{}, mcfg)
	icfg, _ := ignite.Decode([]byte(cfg))
	e.ign.Close()
	e.ign = ignite.New(demoIgnite{igniteHost{e: e}}, icfg)
	e.mu.Unlock()

	// Let the roster sync and first polls finish. History goes in first:
	// telemetry only accepts samples newer than the last one.
	e.SyncRoster()
	e.mon.Sync()
	seedHistory(e)
	e.PollDue(context.Background(), demoFetcher{now: time.Now})
	for _, o := range e.mon.PollDue(context.Background()) {
		e.tel.Quota(o.Window.ObservedAt, o.AuthID, o.Key, o.Window.UsedPercent, o.Window.ResetsAt)
	}

	seedTraffic(e)
	seedIgnition(e)
	for _, m := range []string{"🟡 Claude#alt · 7d 34% | 01d", "✅ GPT#side · 7d reset, 100% left", "⏰ Claude#ops · 7d resets in 3d with 92% left", "🔑 Grok · credential rejected"} {
		e.ntf.Enqueue(notify.Message{Title: m, Level: "info"})
	}
	e.ign.Tick()

	r := gin.New()
	r.Any(APIBase+"/*path", GinHandler)
	mountDemoPanel(r)
	addr := os.Getenv("PROX_DEMO_ADDR")
	if addr == "" {
		addr = "127.0.0.1:18420"
	}
	t.Logf("demo dashboard: http://%s/management.html", addr)
	go func() {
		for range time.Tick(time.Second) {
			e.ign.Tick()
		}
	}()
	if err := http.ListenAndServe(addr, r); err != nil {
		t.Fatal(err)
	}
}

func seedTraffic(e *Engine) {
	rng := rand.New(rand.NewSource(7))
	now := time.Now()
	type mix struct {
		provider, model, client string
		auths                   []string
		weight                  int
		in, out, cache          int64
	}
	mixes := []mix{
		{"claude", "claude-opus-5-5", "claude", []string{"claude-main@kp.dev.json", "claude-ops@kp.gg.json", "claude-alt@tk.gg.json", "claude-side@gmail.com.json"}, 8, 6000, 1800, 90000},
		{"claude", "claude-sonnet-5-5", "claude", []string{"claude-main@kp.dev.json", "claude-alt@tk.gg.json"}, 5, 4000, 1200, 60000},
		{"claude", "claude-haiku-5-5", "claude", []string{"claude-ops@kp.gg.json"}, 4, 2000, 300, 20000},
		{"codex", "gpt-6-luna", "codex", []string{"codex-main@kp.dev-pro.json", "codex-side@gmail.com-pro.json"}, 6, 9000, 2500, 70000},
		{"codex", "gpt-6", "codex", []string{"codex-main@kp.dev-pro.json"}, 3, 12000, 4000, 110000},
		{"kimi", "kimi-k3", "claude", []string{"kimi-main@kp.dev.json"}, 2, 5000, 1500, 30000},
		{"xai", "grok-5-code", "other", []string{"xai-main@gmail.com.json"}, 1, 3000, 900, 10000},
	}
	total := 0
	for _, m := range mixes {
		total += m.weight
	}
	for i := 0; i < 2600; i++ {
		// More traffic in recent days and during the day.
		ago := time.Duration(rng.ExpFloat64()*48) * time.Hour
		if ago > 7*24*time.Hour {
			ago = time.Duration(rng.Intn(7*24)) * time.Hour
		}
		at := now.Add(-ago - time.Duration(rng.Intn(3600))*time.Second)
		pick := rng.Intn(total)
		var m mix
		for _, x := range mixes {
			if pick < x.weight {
				m = x
				break
			}
			pick -= x.weight
		}
		auth := m.auths[rng.Intn(len(m.auths))]
		sid := fmt.Sprintf("%s:demo-%d", m.client, rng.Intn(40))
		f := func(v int64) int64 { return v/2 + rng.Int63n(v) }
		rec := coreusage.Record{Provider: m.provider, AuthID: auth, Model: m.model, SessionID: sid, RequestedAt: at, Latency: time.Duration(800+rng.Intn(9000)) * time.Millisecond,
			Detail: coreusage.Detail{InputTokens: f(m.in), OutputTokens: f(m.out), CacheReadTokens: f(m.cache), CacheCreationTokens: f(m.in) / 3}}
		if m.provider != "claude" {
			rec.Detail.InputTokens += rec.Detail.CacheReadTokens
			rec.Detail.CacheCreationTokens = 0
		}
		if rng.Intn(60) == 0 {
			rec.Failed, rec.Fail.StatusCode = true, []int{429, 529, 500}[rng.Intn(3)]
		}
		e.HandleUsage(context.Background(), rec)
	}
	// Routing decisions for the Activity and Sessions pages.
	for i := 0; i < 60; i++ {
		p := []string{"claude", "codex"}[i%2]
		var cands []pluginapi.SchedulerAuthCandidate
		for _, a := range demoAccounts {
			if a.provider == p {
				cands = append(cands, pluginapi.SchedulerAuthCandidate{ID: a.id, Provider: p})
			}
		}
		h := map[string][]string{"X-Claude-Code-Session-Id": {fmt.Sprintf("sess-%d", i%9)}}
		model := "claude-opus-5-5"
		if p == "codex" {
			h = map[string][]string{"Session-Id": {fmt.Sprintf("thread-%d", i%7)}}
			model = "gpt-6-luna"
		}
		e.Pick(pluginapi.SchedulerPickRequest{Provider: p, Providers: []string{p}, Model: model, Candidates: cands, Options: pluginapi.SchedulerOptions{Headers: h}})
	}
}

func seedIgnition(e *Engine) {
	now := time.Now()
	ev := func(ago time.Duration, auth, prov, kind, model, detail string) ignite.Event {
		return ignite.Event{At: now.Add(-ago), AuthID: auth, Provider: prov, Kind: kind, Model: model, Detail: detail}
	}
	e.ign.Import(ignite.State{Events: []ignite.Event{
		ev(26*time.Hour, "claude-alt@tk.gg.json", "claude", ignite.EventRateLimited, "claude-haiku-5-5", "Rate limited; paused until tomorrow"),
		ev(9*time.Hour, "codex-main@kp.dev-pro.json", "codex", ignite.EventFired, "gpt-6-luna", ""),
		ev(9*time.Hour-40*time.Second, "codex-main@kp.dev-pro.json", "codex", ignite.EventSuccess, "gpt-6-luna", "window resets in 5h"),
		ev(3*time.Hour, "claude-main@kp.dev.json", "claude", ignite.EventFired, "claude-haiku-5-5", ""),
		ev(3*time.Hour-30*time.Second, "claude-main@kp.dev.json", "claude", ignite.EventSuccess, "claude-haiku-5-5", "window resets in 5h"),
		ev(40*time.Minute, "claude-ops@kp.gg.json", "claude", ignite.EventTransient, "claude-haiku-5-5", "upstream returned 529"),
	}})
}
