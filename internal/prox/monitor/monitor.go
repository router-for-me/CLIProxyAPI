// Package monitor polls quota for providers prox does not route (Kimi and
// Grok) so they show up in the dashboard, history, and notifications.
// Polls are read-only GETs against the same endpoints the CPA Management
// Center uses; no inference requests are sent.
package monitor

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/quota"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/router"
	"gopkg.in/yaml.v3"
)

// Config is the monitor block of plugins.configs.prox.
type Config struct {
	Enabled        bool          `yaml:"enabled" json:"enabled"`
	PollInterval   time.Duration `yaml:"poll_interval" json:"poll_interval"`
	RequestTimeout time.Duration `yaml:"request_timeout" json:"request_timeout"`
	Kimi           bool          `yaml:"kimi" json:"kimi"`
	Xai            bool          `yaml:"xai" json:"xai"`
}

// Default returns monitor defaults.
func Default() Config {
	return Config{Enabled: true, PollInterval: 5 * time.Minute, RequestTimeout: 20 * time.Second, Kimi: true, Xai: true}
}

// Decode reads the monitor block from the prox settings YAML.
func Decode(raw []byte) (Config, error) {
	var wrapper struct {
		Monitor yaml.Node `yaml:"monitor"`
	}
	cfg := Default()
	if len(strings.TrimSpace(string(raw))) != 0 {
		if err := yaml.Unmarshal(raw, &wrapper); err != nil {
			return Config{}, fmt.Errorf("monitor: invalid YAML")
		}
		if wrapper.Monitor.Kind != 0 {
			if err := wrapper.Monitor.Decode(&cfg); err != nil {
				return Config{}, fmt.Errorf("monitor: %w", err)
			}
		}
	}
	if cfg.PollInterval < time.Minute {
		return Config{}, fmt.Errorf("monitor: poll_interval must be at least 1m")
	}
	if cfg.RequestTimeout < time.Second || cfg.RequestTimeout > 2*time.Minute {
		return Config{}, fmt.Errorf("monitor: request_timeout must be 1s-2m")
	}
	return cfg, nil
}

// Family normalizes a CPA provider key to a monitored family, or "".
func Family(provider string) string {
	p := strings.ToLower(strings.TrimSpace(provider))
	switch {
	case strings.HasPrefix(p, "kimi"):
		return "kimi"
	case p == "xai" || p == "grok":
		return "xai"
	}
	return ""
}

// Host supplies the roster and credentials.
type Host interface {
	Roster() ([]router.RosterEntry, error)
	Credential(authIndex string) ([]byte, error)
}

// Fetcher performs the HTTP reads.
type Fetcher interface {
	GetJSON(ctx context.Context, proxyURL, url string, headers map[string]string) ([]byte, error)
}

type entry struct {
	roster   router.RosterEntry
	family   string
	windows  []quota.Window
	plan     string
	err      string
	lastPoll time.Time
	nextPoll time.Time
}

// Monitor polls monitored accounts.
type Monitor struct {
	Now func() time.Time
	// ClientVersion returns the Grok CLI version to present.
	ClientVersion func() string

	mu      sync.Mutex
	cfg     Config
	host    Host
	fetcher Fetcher
	entries map[string]*entry
	polling bool
}

// New returns a monitor.
func New(host Host, fetcher Fetcher, cfg Config) *Monitor {
	return &Monitor{host: host, fetcher: fetcher, cfg: cfg, entries: map[string]*entry{}}
}

func (m *Monitor) now() time.Time {
	if m.Now != nil {
		return m.Now()
	}
	return time.Now()
}

// Configure replaces settings.
func (m *Monitor) Configure(cfg Config) {
	m.mu.Lock()
	m.cfg = cfg
	m.mu.Unlock()
}

func (m *Monitor) wants(cfg Config, family string) bool {
	return cfg.Enabled && ((family == "kimi" && cfg.Kimi) || (family == "xai" && cfg.Xai))
}

// Sync refreshes the roster.
func (m *Monitor) Sync() {
	roster, err := m.host.Roster()
	if err != nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	seen := map[string]bool{}
	for _, r := range roster {
		fam := Family(r.Provider)
		if fam == "" || !m.wants(m.cfg, fam) {
			continue
		}
		seen[r.ID] = true
		e := m.entries[r.ID]
		if e == nil {
			e = &entry{family: fam}
			m.entries[r.ID] = e
		}
		e.roster = r
	}
	for id := range m.entries {
		if !seen[id] {
			delete(m.entries, id)
		}
	}
}

// Observation is one window reading for history.
type Observation struct {
	AuthID string
	Window quota.Window
	Key    string
}

// PollDue polls every due account and returns the new readings. It is a
// no-op while a previous call is still running.
func (m *Monitor) PollDue(ctx context.Context) []Observation {
	m.mu.Lock()
	if m.polling {
		m.mu.Unlock()
		return nil
	}
	m.polling = true
	cfg := m.cfg
	now := m.now()
	var due []*entry
	for _, e := range m.entries {
		if !e.roster.Disabled && !now.Before(e.nextPoll) {
			due = append(due, e)
		}
	}
	m.mu.Unlock()
	defer func() {
		m.mu.Lock()
		m.polling = false
		m.mu.Unlock()
	}()
	var out []Observation
	for _, e := range due {
		if ctx.Err() != nil {
			break
		}
		pctx, cancel := context.WithTimeout(ctx, cfg.RequestTimeout)
		windows, plan, err := m.poll(pctx, e)
		cancel()
		at := m.now()
		m.mu.Lock()
		e.lastPoll = at
		e.nextPoll = at.Add(cfg.PollInterval)
		if err != nil {
			e.err = errCode(err)
			if qe, ok := quota.IsError(err); ok && qe.RetryAfter > cfg.PollInterval {
				e.nextPoll = at.Add(qe.RetryAfter)
			}
		} else {
			e.err, e.windows = "", windows
			if plan != "" {
				e.plan = plan
			}
			for _, w := range windows {
				out = append(out, Observation{AuthID: e.roster.ID, Window: w, Key: key(w)})
			}
		}
		m.mu.Unlock()
	}
	return out
}

// Refresh makes one account (or all, when authID is empty) due now.
func (m *Monitor) Refresh(authID string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for id, e := range m.entries {
		if authID == "" || id == authID {
			e.nextPoll = time.Time{}
			n++
		}
	}
	return n
}

func errCode(err error) string {
	if qe, ok := quota.IsError(err); ok {
		return qe.Code
	}
	return err.Error()
}

func key(w quota.Window) string {
	if w.Family != "" {
		return string(w.Kind) + ":" + w.Family
	}
	return string(w.Kind)
}

type credential struct {
	AccessToken string `json:"access_token"`
	APIKey      string `json:"api_key"`
	ProxyURL    string `json:"proxy_url"`
	BaseURL     string `json:"base_url"`
	BaseURLAlt  string `json:"base-url"`
	Domain      string `json:"domain"`
	Type        string `json:"type"`
	Sub         string `json:"sub"`
	UserID      string `json:"user_id"`
	UsingAPI    any    `json:"using_api"`
	PlanType    string `json:"plan_type"`
}

func (m *Monitor) poll(ctx context.Context, e *entry) ([]quota.Window, string, error) {
	raw, err := m.host.Credential(e.roster.AuthIndex)
	if err != nil {
		return nil, "", &quota.Error{Code: "credential_read_failed"}
	}
	var c credential
	if json.Unmarshal(raw, &c) != nil {
		return nil, "", &quota.Error{Code: "credential_decode_failed"}
	}
	now := m.now().UTC()
	switch e.family {
	case "kimi":
		if c.AccessToken == "" {
			return nil, "", &quota.Error{Code: "ineligible_credential"}
		}
		base := c.BaseURL
		if base == "" {
			base = c.BaseURLAlt
		}
		url := quota.KimiUsageURLFor(c.Domain, base, firstNonEmpty(c.Type, e.roster.Provider))
		body, err := m.fetcher.GetJSON(ctx, c.ProxyURL, url, map[string]string{"Authorization": "Bearer " + c.AccessToken, "Accept": "application/json"})
		if err != nil {
			return nil, "", err
		}
		snap, err := quota.ParseKimiUsage(body, now)
		if err != nil {
			return nil, "", &quota.Error{Code: err.Error()}
		}
		return snap.Windows, c.PlanType, nil
	case "xai":
		if usingAPI(c.UsingAPI) || (c.AccessToken == "" && c.APIKey != "") {
			// API-billed accounts have no subscription quota to read.
			return nil, "", &quota.Error{Code: "api_billing_no_quota"}
		}
		if c.AccessToken == "" {
			return nil, "", &quota.Error{Code: "ineligible_credential"}
		}
		version := "1.0.46"
		if m.ClientVersion != nil {
			version = m.ClientVersion()
		}
		headers := quota.XaiHeaders(c.AccessToken, firstNonEmpty(c.Sub, c.UserID), version)
		var windows []quota.Window
		var firstErr error
		for _, url := range []string{quota.XaiBillingWeekly, quota.XaiBillingMonthly} {
			body, err := m.fetcher.GetJSON(ctx, c.ProxyURL, url, headers)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				continue
			}
			w, err := quota.ParseXaiBilling(body, now)
			if err != nil || w == nil {
				continue
			}
			if !hasKind(windows, w.Kind) {
				windows = append(windows, *w)
			}
		}
		if len(windows) == 0 {
			if firstErr != nil {
				return nil, "", firstErr
			}
			return nil, "", &quota.Error{Code: "empty_xai_billing"}
		}
		return windows, c.PlanType, nil
	}
	return nil, "", &quota.Error{Code: "unsupported_provider"}
}

func usingAPI(v any) bool {
	switch b := v.(type) {
	case bool:
		return b
	case string:
		return strings.EqualFold(b, "true")
	}
	return false
}

func hasKind(ws []quota.Window, k quota.Kind) bool {
	for _, w := range ws {
		if w.Kind == k {
			return true
		}
	}
	return false
}

func firstNonEmpty(v ...string) string {
	for _, s := range v {
		if strings.TrimSpace(s) != "" {
			return s
		}
	}
	return ""
}

// Views returns monitored accounts in the router's view shape, so the
// dashboard and notifications treat them like routed accounts.
func (m *Monitor) Views() []router.AccountView {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	stale := 2*m.cfg.PollInterval + time.Minute
	out := make([]router.AccountView, 0, len(m.entries))
	for _, e := range m.entries {
		v := router.AccountView{
			ID: e.roster.ID, Display: firstNonEmpty(e.roster.Email, e.roster.Label, e.roster.Name, e.roster.ID),
			Provider: e.family, Plan: e.plan, HostStatus: e.roster.Status, HostMessage: e.roster.StatusMsg,
			Present: true, Disabled: e.roster.Disabled, Unavailable: e.roster.Unavailable,
			PollError: e.err, Polled: !e.lastPoll.IsZero(), Routing: "monitor_only", Remaining: 100,
		}
		if !e.lastPoll.IsZero() {
			lp := e.lastPoll
			v.LastPollAt = &lp
			np := e.nextPoll
			v.NextPollAt = &np
		}
		for _, w := range e.windows {
			wv := router.WindowView{
				Key: key(w), Kind: string(w.Kind), Family: w.Family, Label: label(w), Used: w.UsedPercent,
				Remaining: w.RemainingPercent(), ResetsAt: w.ResetsAt, Source: string(w.Source), ObservedAt: w.ObservedAt,
				Stale: now.Sub(w.ObservedAt) > stale, Passed: w.ResetsAt != nil && !w.ResetsAt.After(now),
			}
			v.Windows = append(v.Windows, wv)
			if wv.Remaining < v.Remaining {
				v.Remaining = wv.Remaining
			}
			if w.ResetsAt != nil && w.ResetsAt.After(now) && (v.NextReset == nil || w.ResetsAt.Before(*v.NextReset)) {
				r := *w.ResetsAt
				v.NextReset = &r
			}
		}
		switch {
		case e.err != "":
			v.QuotaState, v.QuotaReason = "unknown", e.err
		case len(e.windows) == 0:
			v.QuotaState = "unknown"
		case v.Remaining <= 0:
			v.QuotaState = "exhausted"
		default:
			v.QuotaState = "ok"
		}
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

func label(w quota.Window) string {
	names := map[quota.Kind]string{quota.FiveHour: "5-hour", quota.Daily: "Daily", quota.Weekly: "Weekly", quota.Monthly: "Monthly", quota.ModelWeekly: "Weekly"}
	l := names[w.Kind]
	if w.Family != "" {
		l += " · " + w.Family
	}
	return l
}
