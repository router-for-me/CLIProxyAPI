package router

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/prox/quota"
)

const (
	transientWindow    = 5 * time.Minute
	transientThreshold = 3
	circuitBase        = time.Minute
	circuitMax         = 10 * time.Minute
	defaultRateLimit   = time.Minute
	modelBlockDefault  = time.Hour
	modelMissingBlock  = 10 * time.Minute
	pollBackoffBase    = time.Minute
	pollBackoffMax     = 30 * time.Minute
	manualRefreshEvery = 30 * time.Second
	removedRetention   = 24 * time.Hour
	probeOffset        = 30 * time.Second
	probeTolerance     = 10 * time.Second
	probeLate          = 10 * time.Minute
	staleCooldownMin   = 10 * time.Minute
	staleQuotaMaxAge   = 15 * time.Minute
	staleMinRemaining  = 0.01
)

// health is per-account failure state learned from real traffic and polls.
type health struct {
	AuthFailedAt     time.Time            `json:"auth_failed_at,omitempty"`
	AuthFailStamp    string               `json:"auth_fail_stamp,omitempty"` // credential file stamp when the rejection was seen
	AuthFailStatus   int                  `json:"auth_fail_status,omitempty"`
	PollAuthRejected bool                 `json:"poll_auth_rejected,omitempty"`
	RateLimitedUntil time.Time            `json:"rate_limited_until,omitempty"`
	ModelBlocked     map[string]time.Time `json:"model_blocked,omitempty"`
	TransientCount   int                  `json:"transient_count,omitempty"`
	TransientSince   time.Time            `json:"transient_since,omitempty"`
	CircuitUntil     time.Time            `json:"circuit_until,omitempty"`
	CircuitLevel     int                  `json:"circuit_level,omitempty"`
	LastSuccessAt    time.Time            `json:"last_success_at,omitempty"`
	LastFailureAt    time.Time            `json:"last_failure_at,omitempty"`
	LastStatus       int                  `json:"last_status,omitempty"`
}

type account struct {
	ID            string
	AuthIndex     string
	Provider      string
	Display       string
	PlanType      string
	HostStatus    string
	HostMessage   string
	Disabled      bool
	Unavailable   bool
	CooldownUntil time.Time
	RuntimeOnly   bool
	CustomURL     bool
	Priority      int
	Present       bool
	RemovedAt     time.Time
	Stamp         string
	Identity      string

	Snapshot    quota.Snapshot
	HasSnapshot bool

	NextPoll     time.Time
	PollFailures int
	PollError    string
	ManualPoll   bool
	PassiveAt    time.Time
	LastPollAt   time.Time

	Health health
}

// SyncRoster applies a credential listing. Accounts missing from the listing
// are marked removed; a changed account identity resets learned state.
func (r *Router) SyncRoster(entries []RosterEntry) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	seen := map[string]bool{}
	for _, e := range entries {
		provider := normalizeProvider(e.Provider)
		if !supportedProvider(provider) || e.ID == "" {
			continue
		}
		seen[e.ID] = true
		a := r.accounts[e.ID]
		identity := hashParts(provider, e.Account, e.Email)
		if a == nil {
			a = &account{ID: e.ID, NextPoll: now}
			r.accounts[e.ID] = a
		} else if a.Identity != "" && a.Identity != identity {
			// A different upstream account now sits behind this credential ID.
			*a = account{ID: e.ID, NextPoll: now}
		}
		stamp := fmt.Sprintf("%q:%d:%d", e.Path, e.Size, e.ModTime.UnixNano())
		if a.Stamp != "" && a.Stamp != stamp {
			// Credential file rewritten (token refresh or re-login): re-verify soon.
			a.NextPoll = now
		}
		a.Stamp = stamp
		a.Identity = identity
		a.AuthIndex = e.AuthIndex
		a.Provider = provider
		a.Display = displayName(e)
		if t := planFromRoster(e.AccountType); t != "" {
			a.PlanType = t
		}
		a.HostStatus = e.Status
		a.HostMessage = sanitizeMessage(e.StatusMsg)
		a.Disabled = e.Disabled || strings.EqualFold(e.Status, "disabled")
		a.Unavailable = e.Unavailable
		a.CooldownUntil = time.Time{}
		if a.Unavailable {
			a.CooldownUntil = e.NextRetry
		}
		a.RuntimeOnly = e.RuntimeOnly
		a.CustomURL = e.BaseURL != ""
		a.Priority = e.Priority
		a.Present = true
		a.RemovedAt = time.Time{}
	}
	for id, a := range r.accounts {
		if seen[id] {
			continue
		}
		if a.Present {
			a.Present = false
			a.RemovedAt = now
		} else if !a.RemovedAt.IsZero() && now.Sub(a.RemovedAt) > removedRetention {
			delete(r.accounts, id)
		}
	}
	r.rosterOK = true
	r.rosterErr = ""
	r.rosterAt = now
	r.dirty = true
}

// staleCPACooldown detects a long CPA cooldown despite fresh, available quota.
func staleCPACooldown(a *account, now time.Time) bool {
	if a == nil || !a.Unavailable || !a.CooldownUntil.After(now.Add(staleCooldownMin)) {
		return false
	}
	hasFiveHour := false
	for _, w := range a.Snapshot.Windows {
		if w.Kind == quota.FiveHour {
			hasFiveHour = true
		}
		// Comparing utilization preserves the strict 0.01% remaining boundary.
		if now.Sub(w.ObservedAt) >= staleQuotaMaxAge || w.UsedPercent >= 100-staleMinRemaining {
			return false
		}
	}
	return hasFiveHour
}

// RosterFailed records that the credential listing could not be read.
func (r *Router) RosterFailed(code string) {
	r.mu.Lock()
	r.rosterErr = code
	r.mu.Unlock()
}

// DuePolls returns accounts whose quota should be read now.
func (r *Router) DuePolls() []PollTask {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	var out []PollTask
	for _, a := range r.accounts {
		if !a.Present || a.Disabled || a.RuntimeOnly || a.CustomURL || a.AuthIndex == "" {
			continue
		}
		probe := r.probeAt(a, now)
		probeDue := !probe.IsZero() && !now.Before(probe)
		if now.Before(a.NextPoll) && !probeDue {
			continue
		}
		if !probeDue && !a.ManualPoll && a.PollFailures == 0 && r.cfg.PassiveSkip > 0 &&
			!a.PassiveAt.IsZero() && now.Sub(a.PassiveAt) < r.cfg.PassiveSkip &&
			!a.Snapshot.PolledAt.IsZero() && now.Sub(a.Snapshot.PolledAt) < r.cfg.PassiveSkipMax {
			a.NextPoll = r.nextPoll(a.ID, now)
			continue
		}
		out = append(out, PollTask{AuthID: a.ID, AuthIndex: a.AuthIndex, Provider: a.Provider})
	}
	sortTasks(out)
	return out
}

// NextPollAt is the earliest scheduled poll, for the poller's timer.
func (r *Router) NextPollAt() time.Time {
	r.mu.Lock()
	defer r.mu.Unlock()
	var next time.Time
	for _, a := range r.accounts {
		if !a.Present || a.Disabled || a.RuntimeOnly || a.CustomURL || a.AuthIndex == "" {
			continue
		}
		at := a.NextPoll
		if probe := r.probeAt(a, r.now()); !probe.IsZero() && probe.Before(at) {
			at = probe
		}
		if next.IsZero() || at.Before(next) {
			next = at
		}
	}
	return next
}

// ApplyPoll records a poll result. Errors keep the previous snapshot, which
// ages into "stale" after max_age.
func (r *Router) ApplyPoll(authID string, snap quota.Snapshot, plan string, err error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	a := r.accounts[authID]
	if a == nil {
		return
	}
	now := r.now()
	a.LastPollAt = now
	a.ManualPoll = false
	r.dirty = true
	if err != nil {
		a.PollFailures++
		a.PollError = "quota_refresh_failed"
		var retryAfter time.Duration
		delay := pollBackoffBase << min(a.PollFailures-1, 5)
		if qerr, ok := quota.IsError(err); ok {
			a.PollError = qerr.Code
			retryAfter = qerr.RetryAfter
			if qerr.AuthRejected() {
				a.Health.PollAuthRejected = true
			}
		}
		a.NextPoll = now.Add(max(r.jittered(min(delay, pollBackoffMax)), retryAfter))
		return
	}
	if a.Snapshot.Identity != "" && snap.Identity != "" && a.Snapshot.Identity != snap.Identity {
		// Same credential ID, different upstream account: learned state no longer applies.
		a.Health = health{}
		a.Snapshot = quota.Snapshot{}
	}
	var newer []quota.Window
	for _, w := range a.Snapshot.Windows {
		if w.Source == quota.SourcePassive && w.ObservedAt.After(snap.PolledAt) {
			newer = append(newer, w)
		}
	}
	a.Snapshot.ReplacePolled(snap)
	a.Snapshot.MergeObserved(newer)
	a.HasSnapshot = true
	if plan != "" {
		a.PlanType = plan
	} else if snap.PlanType != "" {
		a.PlanType = snap.PlanType
	}
	a.PollFailures = 0
	a.PollError = ""
	a.Health.PollAuthRejected = false
	// A rejected login is cleared by a successful poll only after the credential
	// file changed (re-login or refresh); otherwise real traffic must succeed.
	if a.Health.AuthFailStamp != a.Stamp {
		a.Health.AuthFailedAt = time.Time{}
		a.Health.AuthFailStatus = 0
		a.Health.AuthFailStamp = ""
	}
	a.NextPoll = r.nextPoll(a.ID, now)
}

func (r *Router) nextPoll(id string, now time.Time) time.Time {
	// Keep each account in a stable position within the first five seconds.
	sum := sha256.Sum256([]byte(id))
	offset := time.Duration(uint16(sum[0])<<8|uint16(sum[1])) * (5 * time.Second) / 65536
	return now.Truncate(r.cfg.PollInterval).Add(r.cfg.PollInterval).Add(offset)
}

func (r *Router) probeAt(a *account, now time.Time) time.Time {
	if a.PollFailures > 0 && now.Before(a.NextPoll) {
		return time.Time{}
	}
	var next time.Time
	for _, w := range a.Snapshot.Windows {
		if w.ResetsAt == nil {
			continue
		}
		for _, slot := range []struct{ at, until time.Time }{
			{w.ResetsAt.Add(-probeOffset), *w.ResetsAt},
			{w.ResetsAt.Add(probeOffset), w.ResetsAt.Add(probeLate)},
		} {
			if !now.Before(slot.until) || !w.ObservedAt.Before(slot.at.Add(-probeTolerance)) ||
				!a.LastPollAt.Before(slot.at.Add(-probeTolerance)) {
				continue
			}
			at := slot.at
			if next.IsZero() || at.Before(next) {
				next = at
			}
		}
	}
	return next
}

func (r *Router) jittered(d time.Duration) time.Duration {
	// ±10% spreads polls of many accounts so they do not align.
	f := 0.9 + 0.2*r.jitter()
	return time.Duration(float64(d) * f)
}

// RequestRefresh schedules an immediate poll of one account, or of all
// accounts when authID is empty. It is rate limited per target.
func (r *Router) RequestRefresh(authID string) (int, time.Duration) {
	r.mu.Lock()
	defer r.mu.Unlock()
	now := r.now()
	key := "*" + authID
	if last, ok := r.manualAt[key]; ok && now.Sub(last) < manualRefreshEvery {
		return 0, manualRefreshEvery - now.Sub(last)
	}
	r.manualAt[key] = now
	n := 0
	for _, a := range r.accounts {
		if (authID == "" || a.ID == authID) && a.Present {
			a.NextPoll = now
			a.ManualPoll = true
			n++
		}
	}
	return n, 0
}

// Observe ingests one upstream attempt: passive quota, account health, and
// session binding evidence.
func (r *Router) Observe(o Outcome) {
	r.mu.Lock()
	defer r.mu.Unlock()
	provider := normalizeProvider(o.Provider)
	if !supportedProvider(provider) || o.AuthID == "" {
		return
	}
	now := o.At
	if now.IsZero() {
		now = r.now()
	}
	r.counters.Observed++
	r.dirty = true
	a := r.accounts[o.AuthID]
	if a == nil {
		a = &account{ID: o.AuthID, Provider: provider, Display: redactID(o.AuthID), NextPoll: now}
		r.accounts[o.AuthID] = a
	}
	var observed []quota.Window
	if provider == "claude" {
		observed = quota.ParseClaudeHeaders(o.Headers, now)
	} else {
		observed = quota.ParseCodexHeaders(o.Headers, now)
	}
	if !r.cfg.PassiveQuota {
		// Failure evidence still counts: keep only windows the provider rejected.
		kept := observed[:0]
		for _, w := range observed {
			if w.Rejected {
				kept = append(kept, w)
			}
		}
		observed = kept
	}
	if len(observed) > 0 {
		a.PassiveAt = r.now()
		a.Snapshot.Provider = provider
		a.Snapshot.MergeObserved(observed)
		a.HasSnapshot = true
	}
	h := &a.Health
	if !o.Failed && (o.Status == 0 || o.Status < 400) {
		h.LastSuccessAt = now
		h.TransientCount = 0
		h.CircuitLevel = 0
		h.CircuitUntil = time.Time{}
		h.RateLimitedUntil = time.Time{}
		h.AuthFailedAt = time.Time{}
		h.AuthFailStatus = 0
		h.AuthFailStamp = ""
		if fam := family(o.Model); fam != "" {
			delete(h.ModelBlocked, fam)
		}
	} else {
		h.LastFailureAt = now
		h.LastStatus = o.Status
		r.classifyFailure(a, o, now)
	}
	r.learnLocked(o, provider, now)
}

func (r *Router) classifyFailure(a *account, o Outcome, now time.Time) {
	h := &a.Health
	switch {
	case o.Status == 401 || o.Status == 403:
		h.AuthFailedAt = now
		h.AuthFailStatus = o.Status
		h.AuthFailStamp = a.Stamp
	case o.Status == 429:
		retry := quota.RetryAfter(o.Headers.Get("Retry-After"), now)
		if a.Provider == "claude" {
			rej := quota.ClassifyClaude429(o.Headers, now)
			if rej.Shared {
				return // windows were marked full from the same headers
			}
			if rej.ModelOnly {
				until := now.Add(modelBlockDefault)
				if rej.Until != nil {
					until = *rej.Until
				}
				r.blockModel(a, o.Model, until)
				return
			}
		} else if anyFull(quota.ParseCodexHeaders(o.Headers, now)) {
			return
		}
		if retry == 0 {
			retry = defaultRateLimit
		}
		h.RateLimitedUntil = now.Add(retry)
	case o.Status == 404:
		r.blockModel(a, o.Model, now.Add(modelMissingBlock))
	case o.Status == 0 || o.Status == 408 || o.Status >= 500:
		if h.TransientSince.IsZero() || now.Sub(h.TransientSince) > transientWindow {
			h.TransientSince = now
			h.TransientCount = 0
		}
		h.TransientCount++
		if h.TransientCount >= transientThreshold {
			h.CircuitUntil = now.Add(min(circuitBase<<min(h.CircuitLevel, 4), circuitMax))
			h.CircuitLevel++
			h.TransientCount = 0
			h.TransientSince = time.Time{}
		}
	}
}

func (r *Router) blockModel(a *account, model string, until time.Time) {
	fam := family(model)
	if fam == "" {
		fam = strings.ToLower(strings.TrimSpace(model))
	}
	if fam == "" {
		return
	}
	if a.Health.ModelBlocked == nil {
		a.Health.ModelBlocked = map[string]time.Time{}
	}
	a.Health.ModelBlocked[fam] = until
}

func anyFull(ws []quota.Window) bool {
	for _, w := range ws {
		if w.Full() {
			return true
		}
	}
	return false
}

// family maps a model name to the family used by model-specific windows and blocks.
func family(model string) string {
	m := strings.ToLower(model)
	for _, f := range []string{"opus", "sonnet", "haiku", "fable"} {
		if strings.Contains(m, f) {
			return f
		}
	}
	return ""
}

func hashParts(parts ...string) string {
	sum := sha256.Sum256([]byte(strings.Join(parts, "\x00")))
	return hex.EncodeToString(sum[:8])
}

func planFromRoster(t string) string {
	t = strings.TrimSpace(t)
	switch strings.ToLower(t) {
	case "", "oauth", "api_key", "apikey":
		return ""
	}
	if len(t) > 24 {
		return ""
	}
	return t
}

func sanitizeMessage(s string) string {
	s = strings.TrimSpace(s)
	if len(s) > 120 {
		s = s[:120]
	}
	low := strings.ToLower(s)
	for _, bad := range []string{"bearer", "token", "sk-", "key"} {
		if strings.Contains(low, bad) {
			return "status detail withheld"
		}
	}
	return s
}

func displayName(e RosterEntry) string {
	name := e.Name
	if name == "" {
		name = e.ID
	}
	return redactID(name)
}

// redactID masks e-mail addresses embedded in credential names:
// claude-kishan@gmail.com.json → claude-k•••@•••.com.json.
func redactID(s string) string {
	at := strings.IndexByte(s, '@')
	if at < 0 {
		if len(s) > 40 {
			return s[:20] + "…" + s[len(s)-12:]
		}
		return s
	}
	start := at
	for start > 0 && !strings.ContainsRune("-_/ ", rune(s[start-1])) {
		start--
	}
	local := s[start:at]
	keep := ""
	if len(local) > 0 {
		keep = local[:1]
	}
	rest := s[at+1:]
	ext := ""
	if strings.HasSuffix(rest, ".json") {
		ext = ".json"
		rest = strings.TrimSuffix(rest, ".json")
	}
	tld := ""
	if dot := strings.LastIndexByte(rest, '.'); dot >= 0 {
		tld = rest[dot:]
	}
	return s[:start] + keep + "•••@•••" + tld + ext
}

func sortTasks(ts []PollTask) {
	sort.Slice(ts, func(i, j int) bool { return ts[i].AuthID < ts[j].AuthID })
}
