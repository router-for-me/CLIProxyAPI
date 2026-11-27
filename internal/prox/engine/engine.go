// Package engine runs prox natively inside CPA: it owns the quota router,
// telemetry, state persistence, and the background roster sync and quota
// poller. It replaces the plugin ABI adapter used by the standalone
// quota-router plugin.
package engine

import (
	"context"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/mgmt"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/quota"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/router"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/session"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/store"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/telemetry"
	coreusage "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/usage"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
	log "github.com/sirupsen/logrus"
)

// Version is the prox engine version shown in the dashboard.
const Version = "0.1.0"

const (
	rosterEvery    = 30 * time.Second
	saveEvery      = time.Second
	historyEvery   = time.Minute
	housekeepEvery = 5 * time.Minute
	pollWorkers    = 3
	stateFile      = "state.json"
	historyFile    = "history.json"
)

// Host is the subset of CPA state the engine reads.
type Host interface {
	// Roster lists every credential known to CPA.
	Roster() ([]router.RosterEntry, error)
	// Credential returns the credential JSON for one auth index.
	Credential(authIndex string) ([]byte, error)
}

// Engine is the prox runtime. Create it with New.
type Engine struct {
	// Now and NewFetcher are replaced in tests.
	Now        func() time.Time
	NewFetcher func() quota.Fetcher

	mu          sync.Mutex
	host        Host
	enabled     bool
	cfg         config.Config
	router      *router.Router
	tel         *telemetry.Recorder
	store       *store.Store
	loadRes     string
	fetcher     quota.Fetcher
	server      *mgmt.Server
	cancel      context.CancelFunc
	done        chan struct{}
	wake        chan struct{}
	started     time.Time
	saveErr     string
	savedAt     time.Time
	configErr   string
	panics      atomic.Uint64
	failed      atomic.Bool
	running     atomic.Bool
	rosterEmpty atomic.Bool
}

// New returns an idle engine.
func New() *Engine { return &Engine{} }

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now()
	}
	return time.Now()
}

// SetHost installs the credential source.
func (e *Engine) SetHost(h Host) {
	e.mu.Lock()
	e.host = h
	e.mu.Unlock()
}

// Enabled reports whether the engine routes requests.
func (e *Engine) Enabled() bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.enabled && e.router != nil
}

// ScheduleAcrossPriorities reports whether the engine wants candidates from
// every CPA priority tier.
func (e *Engine) ScheduleAcrossPriorities() bool {
	if e == nil {
		return false
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.enabled && e.cfg.ScheduleAcrossPriorities
}

// Apply decodes the prox settings block and starts, reconfigures, or stops
// the engine. raw is the YAML subtree of plugins.configs.prox.
func (e *Engine) Apply(enabled bool, raw []byte) error {
	if !enabled {
		e.mu.Lock()
		wasEnabled := e.enabled
		e.enabled = false
		e.mu.Unlock()
		if wasEnabled {
			e.Stop()
		}
		return nil
	}
	cfg, err := config.Decode(raw)
	if err != nil {
		e.mu.Lock()
		e.configErr = err.Error()
		e.mu.Unlock()
		return err
	}
	e.Configure(cfg)
	return nil
}

// Configure applies settings. The first call creates state (loading it from
// disk); later calls keep bindings, quota, and history.
func (e *Engine) Configure(cfg config.Config) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.configErr = ""
	e.enabled = true
	if e.router == nil {
		e.started = e.now()
		e.router = router.New(cfg, e.Now)
		e.tel = telemetry.New(cfg.HistoryRetention)
		e.openStoreLocked(cfg.StateDir)
		e.server = &mgmt.Server{Router: e.router, Telemetry: e.tel, Now: e.now, Info: e.info, Refresh: e.refresh}
	} else {
		e.router.Configure(cfg)
		e.tel.SetRetention(cfg.HistoryRetention)
	}
	e.cfg = cfg
	if e.cancel == nil && e.host != nil {
		e.startLocked()
	}
}

func (e *Engine) openStoreLocked(dir string) {
	s, err := store.Open(dir)
	if err != nil {
		log.WithError(err).Warn("prox: state directory unavailable, running without persistence")
		e.loadRes = "unavailable"
		return
	}
	e.store = s
	var rs router.State
	res, _ := s.Load(stateFile, &rs)
	e.loadRes = string(res)
	if res == store.Loaded {
		e.router.Import(rs)
	}
	var hs telemetry.State
	if res, _ := s.Load(historyFile, &hs); res == store.Loaded {
		e.tel.Import(hs)
	}
}

func (e *Engine) startLocked() {
	if e.NewFetcher != nil {
		e.fetcher = e.NewFetcher()
	} else {
		e.fetcher = quota.NewHTTPFetcher()
	}
	ctx, cancel := context.WithCancel(context.Background())
	e.cancel = cancel
	e.done = make(chan struct{})
	e.wake = make(chan struct{}, 1)
	done, wake, fetcher := e.done, e.wake, e.fetcher
	e.running.Store(true)
	go func() {
		defer close(done)
		defer e.running.Store(false)
		defer func() {
			if r := recover(); r != nil {
				e.failed.Store(true)
				log.Errorf("prox: background worker panic: %v", r)
			}
		}()
		e.loop(ctx, wake, fetcher)
	}()
}

// Stop halts background work and flushes state. Picks keep working from memory.
func (e *Engine) Stop() {
	e.mu.Lock()
	cancel, done, fetcher := e.cancel, e.done, e.fetcher
	e.cancel, e.done = nil, nil
	e.mu.Unlock()
	if cancel != nil {
		cancel()
		<-done
	}
	if f, ok := fetcher.(*quota.HTTPFetcher); ok {
		f.Close()
	}
	e.Flush()
}

// Shutdown stops, flushes, and releases the state lock.
func (e *Engine) Shutdown() {
	e.Stop()
	e.mu.Lock()
	if e.store != nil {
		e.store.Close()
		e.store = nil
	}
	e.mu.Unlock()
}

// Flush writes state and history now.
func (e *Engine) Flush() {
	e.mu.Lock()
	r, tel, st := e.router, e.tel, e.store
	e.mu.Unlock()
	if r == nil || st == nil {
		return
	}
	r.TakeDirty()
	e.recordSave(st.Save(stateFile, r.Export()))
	tel.TakeDirty()
	e.recordSave(st.Save(historyFile, tel.Export()))
}

func (e *Engine) recordSave(err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if err != nil {
		e.saveErr = err.Error()
		return
	}
	e.saveErr = ""
	e.savedAt = e.now()
}

func (e *Engine) loop(ctx context.Context, wake chan struct{}, fetcher quota.Fetcher) {
	e.SyncRoster()
	go e.pollLoop(ctx, wake, fetcher)
	tick := time.NewTicker(saveEvery)
	defer tick.Stop()
	var lastHistory, lastHousekeep, lastSample time.Time
	lastRoster := time.Now()
	started := lastRoster
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		}
		now := time.Now()
		// Auths may load after the engine starts, so sync the roster quickly
		// until it shows accounts (or the first minute passes).
		every := rosterEvery
		if e.rosterEmpty.Load() && now.Sub(started) < time.Minute {
			every = 2 * time.Second
		}
		if now.Sub(lastRoster) >= every {
			lastRoster = now
			if e.SyncRoster() > 0 {
				e.wakePoller()
			}
		}
		e.mu.Lock()
		r, tel, st := e.router, e.tel, e.store
		e.mu.Unlock()
		if now.Sub(lastSample) >= time.Minute {
			lastSample = now
			e.sampleQuota()
		}
		if st != nil && r.TakeDirty() {
			e.recordSave(st.Save(stateFile, r.Export()))
		}
		if st != nil && now.Sub(lastHistory) >= historyEvery && tel.TakeDirty() {
			lastHistory = now
			e.recordSave(st.Save(historyFile, tel.Export()))
		}
		if now.Sub(lastHousekeep) >= housekeepEvery {
			lastHousekeep = now
			tel.Prune(e.now())
			r.Sweep()
		}
	}
}

func (e *Engine) pollLoop(ctx context.Context, wake chan struct{}, fetcher quota.Fetcher) {
	for {
		e.PollDue(ctx, fetcher)
		wait := 30 * time.Second
		if next := e.router.NextPollAt(); !next.IsZero() {
			wait = min(wait, max(time.Second, next.Sub(e.now())))
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-wake:
			timer.Stop()
		case <-timer.C:
		}
	}
}

// SyncRoster reads the credential list and returns how many Claude/Codex
// accounts it found.
func (e *Engine) SyncRoster() int {
	e.mu.Lock()
	host, r := e.host, e.router
	e.mu.Unlock()
	if host == nil || r == nil {
		return 0
	}
	roster, err := host.Roster()
	if err != nil {
		r.RosterFailed("credential_list_failed")
		return 0
	}
	n := 0
	for _, entry := range roster {
		if p := strings.ToLower(strings.TrimSpace(entry.Provider)); p == "claude" || p == "codex" {
			n++
		}
	}
	r.SyncRoster(roster)
	e.rosterEmpty.Store(n == 0)
	return n
}

func (e *Engine) wakePoller() {
	e.mu.Lock()
	wake := e.wake
	e.mu.Unlock()
	if wake != nil {
		select {
		case wake <- struct{}{}:
		default:
		}
	}
}

// PollDue polls every account that is due, a few at a time.
func (e *Engine) PollDue(ctx context.Context, fetcher quota.Fetcher) {
	tasks := e.router.DuePolls()
	if len(tasks) == 0 {
		return
	}
	sem := make(chan struct{}, pollWorkers)
	var wg sync.WaitGroup
	for _, t := range tasks {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(t router.PollTask) {
			defer wg.Done()
			defer func() { <-sem }()
			defer func() {
				if recover() != nil {
					e.panics.Add(1)
				}
			}()
			snap, plan, err := e.pollOne(ctx, fetcher, t)
			if ctx.Err() != nil {
				return
			}
			e.router.ApplyPoll(t.AuthID, snap, plan, err)
			if err == nil {
				for _, w := range snap.Windows {
					e.tel.Quota(w.ObservedAt, t.AuthID, w.Key(), w.UsedPercent, w.ResetsAt)
				}
			}
		}(t)
	}
	wg.Wait()
}

func (e *Engine) pollOne(ctx context.Context, fetcher quota.Fetcher, t router.PollTask) (quota.Snapshot, string, error) {
	e.mu.Lock()
	host := e.host
	timeout := e.cfg.RequestTimeout
	e.mu.Unlock()
	raw, err := host.Credential(t.AuthIndex)
	if err != nil {
		return quota.Snapshot{}, "", &quota.Error{Code: "credential_read_failed"}
	}
	var cred quota.Credential
	var extra struct {
		PlanType string `json:"plan_type"`
	}
	if json.Unmarshal(raw, &cred) != nil {
		return quota.Snapshot{}, "", &quota.Error{Code: "credential_decode_failed"}
	}
	_ = json.Unmarshal(raw, &extra)
	if strings.TrimSpace(cred.Type) == "" {
		cred.Type = t.Provider
	}
	// Quota polls are read-only side requests, not inference traffic; bound
	// them so a hung usage endpoint cannot stall the poller.
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	snap, err := fetcher.Fetch(ctx, cred)
	return snap, planLabel(extra.PlanType), err
}

func planLabel(p string) string {
	p = strings.TrimSpace(p)
	if p == "" || len(p) > 24 {
		return ""
	}
	return p
}

func (e *Engine) sampleQuota() {
	for _, a := range e.router.Accounts() {
		for _, w := range a.Windows {
			e.tel.Quota(w.ObservedAt, a.ID, w.Key, w.Used, w.ResetsAt)
		}
	}
}

func (e *Engine) refresh(authID string) (int, time.Duration) {
	n, wait := e.router.RequestRefresh(authID)
	if n > 0 {
		e.wakePoller()
	}
	return n, wait
}

func (e *Engine) info() mgmt.RuntimeInfo {
	e.mu.Lock()
	defer e.mu.Unlock()
	i := mgmt.RuntimeInfo{
		Version: Version, StartedAt: e.started, WorkerRunning: e.running.Load(), WorkerFailed: e.failed.Load(),
		StoreLoad: e.loadRes, LastSaveError: e.saveErr, LastSavedAt: e.savedAt, Generation: e.cfg.Generation(),
		ConfigError: e.configErr, ScheduleAcross: e.cfg.ScheduleAcrossPriorities, Panics: e.panics.Load(),
	}
	if e.store != nil {
		i.StoreWritable, i.StoreReason = e.store.Writable()
	} else {
		i.StoreReason = "state_dir_unavailable"
	}
	return i
}

// Server returns the management server, or nil before the first Configure.
func (e *Engine) Server() *mgmt.Server {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.server
}

func (e *Engine) activeRouter() *router.Router {
	if e == nil {
		return nil
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if !e.enabled {
		return nil
	}
	return e.router
}

// Pick chooses an account for a scheduler request. A response with Handled
// false defers to the next scheduler (plugins, then CPA's built-in routing).
func (e *Engine) Pick(req pluginapi.SchedulerPickRequest) (resp pluginapi.SchedulerPickResponse) {
	defer func() {
		if r := recover(); r != nil {
			e.panics.Add(1)
			log.Errorf("prox: pick panic: %v", r)
			resp = pluginapi.SchedulerPickResponse{}
		}
	}()
	r := e.activeRouter()
	if r == nil {
		return pluginapi.SchedulerPickResponse{}
	}
	d := r.Pick(toPickRequest(req))
	if d.Provider == "claude" || d.Provider == "codex" {
		e.tel.Decision(d.At, d.Provider, d.AuthID, d.Outcome)
	}
	if !d.Applied {
		return pluginapi.SchedulerPickResponse{}
	}
	return pluginapi.SchedulerPickResponse{AuthID: d.AuthID, Handled: true}
}

func toPickRequest(req pluginapi.SchedulerPickRequest) router.PickRequest {
	provider := strings.ToLower(strings.TrimSpace(req.Provider))
	if provider == "" && len(req.Providers) == 1 {
		provider = strings.ToLower(strings.TrimSpace(req.Providers[0]))
	}
	for _, other := range req.Providers {
		if strings.ToLower(strings.TrimSpace(other)) != provider {
			provider = "mixed"
		}
	}
	out := router.PickRequest{Provider: provider, Model: req.Model, Stream: req.Stream}
	out.Session, out.HasSession = session.FromRequest(http.Header(req.Options.Headers), req.Options.Metadata)
	if pinned, ok := req.Options.Metadata["pinned_auth_id"].(string); ok {
		out.PinnedAuthID = strings.TrimSpace(pinned)
	}
	for _, c := range req.Candidates {
		out.Candidates = append(out.Candidates, router.Candidate{ID: strings.TrimSpace(c.ID), Provider: c.Provider, Priority: c.Priority, Status: c.Status})
	}
	return out
}

// HandleUsage implements coreusage.Plugin. It feeds request outcomes and
// rate-limit headers back into the router. The client API key is used only
// to derive the caller scope and is not retained.
func (e *Engine) HandleUsage(_ context.Context, rec coreusage.Record) {
	defer func() {
		if r := recover(); r != nil {
			e.panics.Add(1)
			log.Errorf("prox: usage panic: %v", r)
		}
	}()
	r := e.activeRouter()
	if r == nil {
		return
	}
	provider := strings.ToLower(strings.TrimSpace(rec.Provider))
	if provider != "claude" && provider != "codex" {
		return
	}
	at := rec.RequestedAt.Add(rec.Latency)
	if rec.RequestedAt.IsZero() {
		at = e.now()
	}
	status := rec.Fail.StatusCode
	// Locally synthesized failures are not upstream refusals and must not
	// change account health.
	if rec.Fail.Code == coreusage.FailureStreamTruncatedCode || rec.Fail.Code == coreusage.FailureRequestScopedCode {
		status = 0
	}
	o := router.Outcome{At: at, AuthID: rec.AuthID, Provider: provider, Model: rec.Model, Failed: rec.Failed && status != 0, Status: status, Headers: rec.ResponseHeaders}
	if !o.Failed && o.Status == 0 {
		o.Status = http.StatusOK
	}
	o.Session, o.HasSession = session.FromUsage(rec.SessionID, rec.ParentSessionID, rec.APIKey)
	r.Observe(o)
	e.wakePoller()
	d := rec.Detail
	e.tel.Usage(at, provider, rec.AuthID, rec.Failed, rec.Fail.StatusCode, d.InputTokens, d.OutputTokens, d.ReasoningTokens, d.CacheReadTokens, d.CacheCreationTokens)
}
