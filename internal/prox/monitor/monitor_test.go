package monitor

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/quota"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/router"
)

type host struct {
	roster []router.RosterEntry
	creds  map[string]map[string]any
}

func (h host) Roster() ([]router.RosterEntry, error) { return h.roster, nil }
func (h host) Credential(idx string) ([]byte, error) { return json.Marshal(h.creds[idx]) }

type fetcher struct {
	urls    []string
	headers []map[string]string
	bodies  map[string]string
}

func (f *fetcher) GetJSON(_ context.Context, _, url string, h map[string]string) ([]byte, error) {
	f.urls = append(f.urls, url)
	f.headers = append(f.headers, h)
	if b, ok := f.bodies[url]; ok {
		return []byte(b), nil
	}
	return nil, &quota.Error{Code: "quota_http_404", Status: 404}
}

func TestMonitorPollsKimiAndGrok(t *testing.T) {
	now := time.Date(2026, 11, 27, 12, 0, 0, 0, time.UTC)
	h := host{
		roster: []router.RosterEntry{
			{ID: "k1", AuthIndex: "1", Provider: "kimi-ai", Email: "a@x"},
			{ID: "g1", AuthIndex: "2", Provider: "xai", Email: "b@x"},
			{ID: "g2", AuthIndex: "3", Provider: "xai"},
			{ID: "c1", AuthIndex: "4", Provider: "claude"},
		},
		creds: map[string]map[string]any{
			"1": {"access_token": "kt", "type": "kimi-ai"},
			"2": {"access_token": "gt", "sub": "user-9"},
			"3": {"api_key": "xai-key", "using_api": true},
		},
	}
	f := &fetcher{bodies: map[string]string{
		quota.KimiAIUsageURL:   `{"usage":{"used":10,"limit":100,"reset_in":7200}}`,
		quota.XaiBillingWeekly: `{"config":{"creditUsagePercent":60,"currentPeriod":{"type":"WEEKLY","end":"2026-12-01T00:00:00Z"}}}`,
	}}
	m := New(h, f, Default())
	m.Now = func() time.Time { return now }
	m.ClientVersion = func() string { return "9.9.9" }
	m.Sync()
	obs := m.PollDue(context.Background())
	if len(obs) != 2 {
		t.Fatalf("observations: %+v", obs)
	}
	views := m.Views()
	if len(views) != 3 {
		t.Fatalf("views: %+v", views)
	}
	byID := map[string]router.AccountView{}
	for _, v := range views {
		byID[v.ID] = v
	}
	if k := byID["k1"]; k.Provider != "kimi" || len(k.Windows) != 1 || k.Windows[0].Remaining != 90 {
		t.Fatalf("kimi view: %+v", k)
	}
	if g := byID["g1"]; g.Provider != "xai" || len(g.Windows) != 1 || g.Windows[0].Kind != "weekly" || g.Windows[0].Remaining != 40 {
		t.Fatalf("grok view: %+v", g)
	}
	if g := byID["g2"]; g.PollError != "api_billing_no_quota" {
		t.Fatalf("api grok: %+v", g)
	}
	for i, u := range f.urls {
		if strings.Contains(u, "grok.com") && (f.headers[i]["x-userid"] != "user-9" || f.headers[i]["x-grok-client-version"] != "9.9.9") {
			t.Fatalf("grok headers: %v", f.headers[i])
		}
	}
	// Not due again until the interval passes.
	if obs := m.PollDue(context.Background()); len(obs) != 0 {
		t.Fatal("expected nothing due")
	}
	now = now.Add(6 * time.Minute)
	if obs := m.PollDue(context.Background()); len(obs) != 2 {
		t.Fatalf("expected repoll, got %d", len(obs))
	}
}

func TestMonitorDisabledProvider(t *testing.T) {
	cfg, err := Decode([]byte("monitor:\n  xai: false\n"))
	if err != nil || cfg.Xai || !cfg.Kimi {
		t.Fatalf("%+v %v", cfg, err)
	}
	m := New(host{roster: []router.RosterEntry{{ID: "g", Provider: "xai"}}}, &fetcher{}, cfg)
	m.Sync()
	if len(m.Views()) != 0 {
		t.Fatal("xai must be skipped")
	}
}
