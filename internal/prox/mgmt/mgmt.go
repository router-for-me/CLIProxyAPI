// Package mgmt serves the plugin's management API (authenticated by CPA) and
// its static dashboard resources (unauthenticated, data-free).
package mgmt

import (
	"github.com/MoeForge/moego/json"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/config"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/router"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/telemetry"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

const (
	APIBase      = "/prox/api"
	maxBodyBytes = 64 << 10
	clearPhrase  = "clear sessions"
)

// RuntimeInfo describes the plugin process for the overview.
type RuntimeInfo struct {
	Version        string    `json:"version"`
	StartedAt      time.Time `json:"started_at"`
	WorkerRunning  bool      `json:"worker_running"`
	WorkerFailed   bool      `json:"worker_failed"`
	StoreWritable  bool      `json:"state_writable"`
	StoreReason    string    `json:"state_reason,omitempty"`
	StoreLoad      string    `json:"state_load"`
	LastSaveError  string    `json:"last_save_error,omitempty"`
	LastSavedAt    time.Time `json:"last_saved_at,omitempty"`
	Generation     string    `json:"config_generation"`
	ConfigError    string    `json:"config_error,omitempty"`
	ScheduleAcross bool      `json:"schedule_across_priorities"`
	Panics         uint64    `json:"recovered_panics"`
}

// Server answers management and resource requests.
type Server struct {
	Router    *router.Router
	Telemetry *telemetry.Recorder
	Now       func() time.Time
	Info      func() RuntimeInfo
	// Refresh schedules polls and wakes the poller. Empty authID means all.
	Refresh func(authID string) (int, time.Duration)
}

type route struct {
	method string
	path   string
	handle func(*Server, pluginapi.ManagementRequest) pluginapi.ManagementResponse
}

var routes = []route{
	{http.MethodGet, "/status", (*Server).status},
	{http.MethodGet, "/overview", (*Server).overview},
	{http.MethodGet, "/accounts", (*Server).accounts},
	{http.MethodGet, "/sessions", (*Server).sessions},
	{http.MethodGet, "/activity", (*Server).activity},
	{http.MethodGet, "/decision", (*Server).decision},
	{http.MethodGet, "/usage", (*Server).usage},
	{http.MethodGet, "/settings", (*Server).settings},
	{http.MethodPost, "/settings/validate", (*Server).validate},
	{http.MethodPost, "/refresh", (*Server).refresh},
	{http.MethodPost, "/simulate", (*Server).simulate},
	{http.MethodPost, "/sessions/clear", (*Server).clearSessions},
	{http.MethodGet, "/export", (*Server).export},
}

// Routes lists the management routes for registration.
func Routes() []pluginapi.ManagementRoute {
	out := make([]pluginapi.ManagementRoute, 0, len(routes))
	for _, r := range routes {
		out = append(out, pluginapi.ManagementRoute{Method: r.method, Path: APIBase + r.path})
	}
	return out
}

// Handle dispatches one management or resource request.
func (s *Server) Handle(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	path := strings.TrimPrefix(req.Path, APIBase)
	for _, r := range routes {
		if r.path == path && r.method == req.Method {
			if req.Method != http.MethodGet {
				if len(req.Body) > maxBodyBytes {
					return problem(http.StatusRequestEntityTooLarge, "body_too_large", "Request body exceeds 64 KiB.")
				}
				ct := strings.ToLower(req.Headers.Get("Content-Type"))
				if !strings.HasPrefix(ct, "application/json") {
					return problem(http.StatusUnsupportedMediaType, "json_required", "Send Content-Type: application/json.")
				}
			}
			return r.handle(s, req)
		}
	}
	return problem(http.StatusNotFound, "not_found", "No such route.")
}

func jsonResponse(status int, v any) pluginapi.ManagementResponse {
	body, err := json.Marshal(v)
	if err != nil {
		return problem(http.StatusInternalServerError, "encode_failed", "Response could not be encoded.")
	}
	return pluginapi.ManagementResponse{StatusCode: status, Headers: apiHeaders(), Body: body}
}

func apiHeaders() http.Header {
	return http.Header{
		"Content-Type":           {"application/json; charset=utf-8"},
		"Cache-Control":          {"no-store"},
		"X-Content-Type-Options": {"nosniff"},
		"Referrer-Policy":        {"no-referrer"},
	}
}

func problem(status int, code, message string) pluginapi.ManagementResponse {
	return jsonResponse(status, map[string]any{"error": code, "message": message})
}

func (s *Server) now() time.Time { return s.Now() }

// status keeps the original plugin's fields for existing tooling and adds a summary.
func (s *Server) status(pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	info := s.Info()
	st := s.Router.Status()
	cfg := s.Router.Config()
	accounts := map[string]any{}
	for _, a := range s.Router.Accounts() {
		accounts[a.Display] = map[string]any{"provider": a.Provider, "quota_state": a.QuotaState, "routing": a.Routing, "last_error": a.PollError}
	}
	var last *router.Decision
	if ds := s.Router.Decisions("", "", 1); len(ds) > 0 {
		last = &ds[0]
	}
	// The original plugin's fields keep their names and values; new fields are additive.
	policy := cfg.Strategy
	if policy == config.StrategyResetFirst {
		policy = "weekly_reset_first"
	}
	out := map[string]any{
		"version": info.Version, "selection_policy": policy, "strategy": cfg.Strategy, "mode": cfg.Mode,
		"poll_interval": cfg.PollInterval.String(), "max_age": cfg.MaxAge.String(),
		"accounts": accounts, "last_decision": last,
		"picks": st.Counters.Picks, "routed": st.Counters.Routed, "worker_failed": info.WorkerFailed,
		"sessions": st.Sessions, "counters": st.Counters,
	}
	if st.RosterError != "" {
		out["list_error"] = st.RosterError
	}
	return jsonResponse(http.StatusOK, out)
}

type providerSummary struct {
	Total        int `json:"total"`
	Eligible     int `json:"eligible"`
	Limited      int `json:"limited"`
	Exhausted    int `json:"exhausted"`
	Blocked      int `json:"blocked"`
	Unknown      int `json:"unknown"`
	Sessions     int `json:"active_sessions"`
	ExpiringSoon int `json:"expiring_soon"`
}

type upcoming struct {
	AuthID    string    `json:"auth_id"`
	Account   string    `json:"account"`
	Provider  string    `json:"provider"`
	ResetsAt  time.Time `json:"resets_at"`
	Remaining float64   `json:"remaining_percent"`
}

func (s *Server) overview(pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	now := s.now()
	accounts := s.enrich(s.Router.Accounts(), now)
	st := s.Router.Status()
	summary := map[string]*providerSummary{"claude": {}, "codex": {}}
	var resets []upcoming
	var lastRefresh *time.Time
	for _, a := range accounts {
		ps := summary[a.Provider]
		if ps == nil || !a.Present {
			continue
		}
		ps.Total++
		switch {
		case a.QuotaState == "exhausted":
			ps.Exhausted++
		case a.Routing == "eligible":
			ps.Eligible++
		case a.Routing == "blocked":
			ps.Blocked++
		case a.QuotaState == "unknown" || a.QuotaState == "stale":
			ps.Unknown++
		default:
			ps.Limited++
		}
		if a.ExpiringSoon {
			ps.ExpiringSoon++
		}
		if a.NextReset != nil && a.NextReset.After(now) && a.NextReset.Sub(now) <= 24*time.Hour {
			resets = append(resets, upcoming{a.ID, label(a.AccountView), a.Provider, *a.NextReset, a.Remaining})
		}
		for _, t := range []*time.Time{a.LastRefreshAt, a.LastObserved} {
			if t != nil && (lastRefresh == nil || t.After(*lastRefresh)) {
				lastRefresh = t
			}
		}
	}
	for p, n := range st.SessionsByProv {
		if ps := summary[p]; ps != nil {
			ps.Sessions = n
		}
	}
	sort.Slice(resets, func(i, j int) bool { return resets[i].ResetsAt.Before(resets[j].ResetsAt) })
	if len(resets) > 6 {
		resets = resets[:6]
	}
	cfg := s.Router.Config()
	return jsonResponse(http.StatusOK, map[string]any{
		"now":          now,
		"plugin":       s.Info(),
		"mode":         cfg.Mode,
		"policy":       cfg.Strategy,
		"roster":       map[string]any{"ok": st.RosterOK, "error": st.RosterError, "at": st.RosterAt},
		"counters":     st.Counters,
		"summary":      summary,
		"upcoming":     resets,
		"last_refresh": lastRefresh,
		"next_up":      s.Router.NextUp(),
		"accounts":     accounts,
		"affinity": map[string]any{
			"enabled": cfg.AffinityEnabled, "ttl": cfg.AffinityTTL.String(), "scope": cfg.AffinityScope,
			"inherit_parent": cfg.AffinityInheritParent, "rebind_policy": cfg.RebindPolicy,
		},
	})
}

// AccountDetail adds burn estimates to an account view.
type AccountDetail struct {
	router.AccountView
	Burn map[string]telemetry.Estimate `json:"burn"`
}

func label(a router.AccountView) string {
	if a.Alias != "" {
		return a.Alias
	}
	return a.Display
}

func (s *Server) enrich(views []router.AccountView, now time.Time) []AccountDetail {
	out := make([]AccountDetail, 0, len(views))
	for _, a := range views {
		d := AccountDetail{AccountView: a, Burn: map[string]telemetry.Estimate{}}
		for _, w := range a.Windows {
			d.Burn[w.Key] = s.Telemetry.Burn(a.ID, w.Key, now, w.Used, w.ResetsAt)
		}
		out = append(out, d)
	}
	return out
}

func (s *Server) accounts(pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	return jsonResponse(http.StatusOK, map[string]any{"now": s.now(), "accounts": s.enrich(s.Router.Accounts(), s.now())})
}

func (s *Server) sessions(pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	cfg := s.Router.Config()
	return jsonResponse(http.StatusOK, map[string]any{
		"now": s.now(), "sessions": s.Router.Sessions(),
		"ttl": cfg.AffinityTTL.String(), "enabled": cfg.AffinityEnabled, "mode": cfg.Mode,
	})
}

func (s *Server) activity(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	limit, _ := strconv.Atoi(req.Query.Get("limit"))
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	provider := req.Query.Get("provider")
	if provider != "" && provider != "claude" && provider != "codex" {
		return problem(http.StatusBadRequest, "invalid_provider", "provider must be claude or codex.")
	}
	return jsonResponse(http.StatusOK, map[string]any{
		"now": s.now(), "decisions": s.Router.Decisions(provider, req.Query.Get("outcome"), limit), "labels": s.Router.Labels(),
	})
}

func (s *Server) decision(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	id, err := strconv.ParseUint(req.Query.Get("id"), 10, 64)
	if err != nil {
		return problem(http.StatusBadRequest, "invalid_id", "id must be a decision number.")
	}
	d, ok := s.Router.Decision(id)
	if !ok {
		return problem(http.StatusNotFound, "decision_expired", "That decision is no longer retained.")
	}
	return jsonResponse(http.StatusOK, map[string]any{"decision": d, "labels": s.Router.Labels()})
}

func (s *Server) usage(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	now := s.now()
	span, step := 24*time.Hour, time.Hour
	switch req.Query.Get("range") {
	case "", "24h":
	case "6h":
		span, step = 6*time.Hour, 15*time.Minute
	case "7d":
		span, step = 7*24*time.Hour, 6*time.Hour
	default:
		return problem(http.StatusBadRequest, "invalid_range", "range must be 6h, 24h, or 7d.")
	}
	since := now.Add(-span)
	out := map[string]any{"now": now, "range": span.String(), "step": step.String()}
	providers := map[string]any{}
	for _, p := range []string{"claude", "codex"} {
		totals := s.Telemetry.Totals(p, since)
		share := cacheShare(p, totals)
		providers[p] = map[string]any{
			"series":            s.Telemetry.Series(p, since, now, step),
			"totals":            totals,
			"cache_read_share":  share,
			"cache_share_basis": shareBasis(p),
		}
	}
	out["providers"] = providers
	perAccount := map[string]any{}
	for _, a := range s.Router.Accounts() {
		t := s.Telemetry.Totals(a.Provider+"|"+a.ID, since)
		if t.Requests > 0 {
			perAccount[a.ID] = t
		}
	}
	out["accounts"] = perAccount
	out["labels"] = s.Router.Labels()
	return jsonResponse(http.StatusOK, out)
}

// cacheShare is cache-read tokens as a share of prompt tokens. Claude reports
// cache tokens separately from input; Codex/OpenAI input already includes them.
func cacheShare(provider string, c telemetry.Counts) *float64 {
	var denom float64
	if provider == "claude" {
		denom = float64(c.InputTokens + c.CacheRead + c.CacheCreation)
	} else {
		denom = float64(c.InputTokens)
	}
	if denom <= 0 {
		return nil
	}
	v := float64(c.CacheRead) / denom
	if v > 1 {
		v = 1
	}
	return &v
}

func shareBasis(provider string) string {
	if provider == "claude" {
		return "cache_read / (input + cache_read + cache_creation)"
	}
	return "cached / input (input includes cached tokens)"
}

func (s *Server) settings(pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	cfg := s.Router.Config()
	return jsonResponse(http.StatusOK, map[string]any{
		"config":     cfg.Public(),
		"schema":     config.Schema(),
		"generation": cfg.Generation(),
		"patch_path": APIBase + "/config",
	})
}

type change struct {
	Field  string `json:"field"`
	From   any    `json:"from"`
	To     any    `json:"to"`
	Effect string `json:"effect"`
}

func (s *Server) validate(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	cfg := s.Router.Config()
	next, err := config.DecodeJSONPatch(cfg, req.Body)
	if err != nil {
		var errs config.Errors
		if e, ok := err.(config.Errors); ok {
			errs = e
		}
		return jsonResponse(http.StatusUnprocessableEntity, map[string]any{"valid": false, "errors": errs})
	}
	before, after := cfg.Public(), next.Public()
	effects := map[string]string{}
	for _, f := range config.Schema() {
		effects[f.Name] = f.Effect
	}
	var changes []change
	for k, v := range after {
		if b, _ := json.Marshal(before[k]); string(b) != mustJSON(v) {
			eff := effects[k]
			if eff == "" {
				eff = "Applies after CPA reloads the plugin."
			}
			changes = append(changes, change{Field: k, From: before[k], To: v, Effect: eff})
		}
	}
	sort.Slice(changes, func(i, j int) bool { return changes[i].Field < changes[j].Field })
	return jsonResponse(http.StatusOK, map[string]any{"valid": true, "changes": changes, "generation": next.Generation()})
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func (s *Server) refresh(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	var body struct {
		AuthID string `json:"auth_id"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return problem(http.StatusBadRequest, "invalid_body", "Body must be a JSON object.")
	}
	n, wait := s.Refresh(body.AuthID)
	if wait > 0 {
		resp := problem(http.StatusTooManyRequests, "refresh_rate_limited", "Manual refresh is limited to once every 30 seconds.")
		resp.Headers.Set("Retry-After", strconv.Itoa(int(wait.Seconds())+1))
		return resp
	}
	if n == 0 && body.AuthID != "" {
		return problem(http.StatusNotFound, "unknown_account", "No such account.")
	}
	return jsonResponse(http.StatusAccepted, map[string]any{"scheduled": n})
}

func (s *Server) simulate(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	var body struct {
		Provider   string   `json:"provider"`
		Model      string   `json:"model"`
		Strategies []string `json:"strategies"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return problem(http.StatusBadRequest, "invalid_body", "Body must be a JSON object.")
	}
	if body.Provider != "claude" && body.Provider != "codex" {
		return problem(http.StatusBadRequest, "invalid_provider", "provider must be claude or codex.")
	}
	if len(body.Model) > 128 {
		return problem(http.StatusBadRequest, "invalid_model", "model is too long.")
	}
	valid := map[string]bool{config.StrategyResetFirst: true, config.StrategyQuotaPressure: true, config.StrategyBalanced: true, config.StrategyManualPriority: true}
	if len(body.Strategies) == 0 {
		body.Strategies = []string{config.StrategyResetFirst, config.StrategyQuotaPressure, config.StrategyBalanced, config.StrategyManualPriority}
	}
	for _, st := range body.Strategies {
		if !valid[st] {
			return problem(http.StatusBadRequest, "invalid_strategy", "Unknown strategy.")
		}
	}
	cfg := s.Router.Config()
	results := s.Router.Simulate(body.Provider, body.Model, body.Strategies)
	// Actual routing: the most recent applied or observed choices for new sessions.
	var actual []router.Decision
	for _, d := range s.Router.Decisions(body.Provider, "", 200) {
		if d.Outcome == router.OutcomeNewSession || d.Outcome == router.OutcomeNoSession || d.Outcome == router.OutcomeDelegated {
			actual = append(actual, d)
			if len(actual) == 20 {
				break
			}
		}
	}
	return jsonResponse(http.StatusOK, map[string]any{
		"current_policy": cfg.Strategy, "mode": cfg.Mode, "results": results, "recent": actual, "labels": s.Router.Labels(),
	})
}

func (s *Server) clearSessions(req pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	var body struct {
		Provider string `json:"provider"`
		AuthID   string `json:"auth_id"`
		Session  string `json:"session"`
		Confirm  string `json:"confirm"`
	}
	if err := json.Unmarshal(req.Body, &body); err != nil {
		return problem(http.StatusBadRequest, "invalid_body", "Body must be a JSON object.")
	}
	if body.Provider != "" && body.Provider != "claude" && body.Provider != "codex" {
		return problem(http.StatusBadRequest, "invalid_provider", "provider must be claude or codex.")
	}
	broad := body.Session == ""
	if broad && body.Confirm != clearPhrase {
		return problem(http.StatusPreconditionRequired, "confirmation_required", `Clearing more than one session requires confirm: "`+clearPhrase+`".`)
	}
	n := s.Router.ClearSessions(body.Provider, body.AuthID, body.Session)
	return jsonResponse(http.StatusOK, map[string]any{"cleared": n})
}

// export returns a redacted diagnostic bundle: no account IDs (only display
// labels), no session hashes, no tokens, no host messages.
func (s *Server) export(pluginapi.ManagementRequest) pluginapi.ManagementResponse {
	now := s.now()
	cfg := s.Router.Config()
	pub := cfg.Public()
	pub["account_alias"] = len(cfg.AccountAlias)
	pub["account_priority"] = len(cfg.AccountPriority)
	pub["state_dir"] = "(redacted)"
	handles := map[string]string{}
	handle := func(id string) string {
		if id == "" {
			return ""
		}
		if h, ok := handles[id]; ok {
			return h
		}
		h := "account-" + strconv.Itoa(len(handles)+1)
		handles[id] = h
		return h
	}
	var accounts []map[string]any
	for _, a := range s.Router.Accounts() {
		accounts = append(accounts, map[string]any{
			"account": handle(a.ID), "provider": a.Provider, "plan": a.Plan, "present": a.Present, "disabled": a.Disabled,
			"quota_state": a.QuotaState, "quota_reason": a.QuotaReason, "routing": a.Routing, "routing_reason": a.RoutingReason,
			"windows": a.Windows, "poll_error": a.PollError, "health": a.Health, "active_sessions": a.ActiveSess,
		})
	}
	var decisions []map[string]any
	for _, d := range s.Router.Decisions("", "", 100) {
		var ranked []map[string]any
		for _, r := range d.Ranked {
			ranked = append(ranked, map[string]any{"account": handle(r.AuthID), "reason": r.Reason})
		}
		var skipped []map[string]any
		for _, sk := range d.Skipped {
			skipped = append(skipped, map[string]any{"account": handle(sk.AuthID), "reason": sk.Reason})
		}
		decisions = append(decisions, map[string]any{
			"id": d.ID, "at": d.At, "provider": d.Provider, "model": d.Model, "outcome": d.Outcome, "cause": d.Cause,
			"account": handle(d.AuthID), "bound": handle(d.BoundAuthID), "applied": d.Applied, "session_kind": d.SessionKind,
			"ranked": ranked, "skipped": skipped,
		})
	}
	info := s.Info()
	return jsonResponse(http.StatusOK, map[string]any{
		"generated_at": now, "plugin_version": info.Version, "runtime": map[string]any{
			"worker_running": info.WorkerRunning, "worker_failed": info.WorkerFailed, "state_writable": info.StoreWritable,
			"state_reason": info.StoreReason, "state_load": info.StoreLoad, "save_failing": info.LastSaveError != "", // the error text can name the state path
		},
		"config": pub, "status": s.Router.Status(), "accounts": accounts, "decisions": decisions,
		"sessions": len(s.Router.Sessions()),
	})
}
