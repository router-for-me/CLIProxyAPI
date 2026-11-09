package auth

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/ojson"
	"github.com/google/uuid"
	log "github.com/sirupsen/logrus"
)

const codexBankedURL = "https://chatgpt.com/backend-api/wham/rate-limit-reset-credits"
const claudeBankedURL = "https://api.anthropic.com/api/oauth/usage?cedar_ember=1&skip_spend=1"

var ErrBankedResetDisabled = errors.New("banked reset spending is disabled")
var ErrBankedResetBackoff = errors.New("banked reset check is backing off")
var ErrNoBankedReset = errors.New("no usable banked reset is available")

// BankedReset is a provider credit or grant, without credential material.
type BankedReset struct {
	ID            string     `json:"id"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	Count         int64      `json:"count"`
	Usable        bool       `json:"usable"`
	RequiresLimit bool       `json:"requires_limit,omitempty"`
}

// BankedResetStatus is a read-only provider snapshot.
type BankedResetStatus struct {
	Resets        []BankedReset `json:"resets"`
	Count         int64         `json:"count"`
	Exhausted     bool          `json:"exhausted"`
	CooldownUntil *time.Time    `json:"cooldown_until,omitempty"`
}

// BankedResetResult confirms an upstream outcome, not merely an HTTP success.
type BankedResetResult struct {
	Code      string `json:"code"`
	RequestID string `json:"request_id"`
	CreditID  string `json:"credit_id"`
}

type bankedResetAccount struct {
	mu         sync.Mutex
	nextCheck  time.Time
	retryAfter time.Time
	failures   int
	restored   bool
	// After an ambiguous POST, only the same request may be retried. This is
	// persisted before dispatch and survives restarts/HA copies in auth metadata.
	pending *bankedResetOperation
	last    *bankedResetOperation
}

type bankedResetOperation struct {
	AuthID       string    `json:"auth_id,omitempty"`
	AccountKey   string    `json:"account_key"`
	CreditID     string    `json:"credit_id"`
	RequestID    string    `json:"request_id"`
	Organization string    `json:"organization,omitempty"`
	Reason       string    `json:"reason"`
	Code         string    `json:"code,omitempty"`
	At           time.Time `json:"at"`
}

func bankedAccountKey(a *Auth) string {
	identity := authMetadataString(a, "account_id")
	if a.Provider == "claude" {
		identity = authMetadataString(a, "account_uuid")
		if identity == "" {
			identity = strings.ToLower(authMetadataString(a, "email"))
		}
		if organization := authMetadataString(a, "organization_uuid"); organization != "" {
			identity = organization + ":" + identity
		}
	}
	if identity == "" {
		identity = a.ID
	}
	return a.Provider + ":" + identity
}

func (m *Manager) bankedAccount(a *Auth) *bankedResetAccount {
	value, _ := m.bankedResetAccounts.LoadOrStore(bankedAccountKey(a), &bankedResetAccount{})
	return value.(*bankedResetAccount)
}

func bankedEligible(a *Auth) bool {
	return a != nil && !a.Disabled && a.Status != StatusDisabled && a.AuthKind() != AuthKindAPIKey &&
		(a.Provider == "codex" || a.Provider == "claude")
}

func (m *Manager) bankedPolicy(provider string, automatic bool) (time.Duration, bool) {
	cfg := m.runtimeConfigSnapshot()
	if cfg == nil || m.HomeEnabled() {
		return 0, false
	}
	p := cfg.QuotaExceeded.BankedReset
	if p.Mode != "auto" && (automatic || p.Mode != "manual") {
		return 0, false
	}
	if automatic && len(p.Providers) > 0 {
		found := false
		for _, name := range p.Providers {
			if name == provider {
				found = true
			}
		}
		if !found {
			return 0, false
		}
	}
	lead := 2 * time.Hour
	if p.UseBefore != "" {
		parsed, err := time.ParseDuration(p.UseBefore)
		if err != nil || parsed < 0 {
			return 0, false
		}
		lead = parsed
	}
	return lead, true
}

func bankedTime(v ojson.Result) *time.Time {
	if !v.Exists() || v.Type == ojson.Null {
		return nil
	}
	if t, err := time.Parse(time.RFC3339Nano, v.String()); err == nil {
		return &t
	}
	n, err := strconv.ParseFloat(v.String(), 64)
	if err != nil || n <= 0 {
		return nil
	}
	t := time.Unix(int64(n), 0)
	if n > 1e12 {
		t = time.UnixMilli(int64(n))
	}
	return &t
}

func (m *Manager) bankedRequest(ctx context.Context, a *Auth, method, target string, payload any) ([]byte, error) {
	var body []byte
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			return nil, errors.New("invalid reset payload")
		}
	}
	headers := http.Header{"Accept": {"application/json"}, "Content-Type": {"application/json"}}
	if a.Provider == "codex" {
		headers.Set("ChatGPT-Account-Id", authMetadataString(a, "account_id"))
		headers.Set("OpenAI-Beta", "codex-1")
		headers.Set("Originator", "Codex Desktop")
	} else {
		headers.Set("anthropic-beta", "oauth-2025-04-20")
		headers.Set("User-Agent", "claude-cli/2.1.280 (external, cli)")
	}
	// HttpRequest injects current credentials and respects the credential proxy.
	req, err := http.NewRequestWithContext(ctx, method, target, strings.NewReader(string(body)))
	if err != nil {
		return nil, errors.New("could not build reset request")
	}
	req.Header = headers
	resp, err := m.HttpRequest(ctx, a, req)
	if err != nil {
		return nil, errors.New("banked reset provider request failed")
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debug("banked reset response close failed")
		}
	}()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("banked reset provider status %d", resp.StatusCode)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil || len(data) > 1<<20 || !json.Valid(data) {
		return nil, errors.New("invalid banked reset provider response")
	}
	return data, nil
}

func (m *Manager) readBankedStatus(ctx context.Context, a *Auth, now time.Time) (BankedResetStatus, error) {
	status := BankedResetStatus{Resets: []BankedReset{}}
	target := codexBankedURL
	if a.Provider == "claude" {
		target = claudeBankedURL
	}
	data, err := m.bankedRequest(ctx, a, http.MethodGet, target, nil)
	if err != nil {
		return status, err
	}
	if a.Provider == "codex" {
		credits := ojson.GetBytesNoCopy(data, "credits")
		if !credits.IsArray() {
			return status, errors.New("invalid reset credits response")
		}
		for _, credit := range credits.Array() {
			if credit.Get("status").String() != "available" || credit.Get("reset_type").String() != "codex_rate_limits" {
				continue
			}
			id := credit.Get("id").String()
			if id == "" {
				continue
			}
			rawExpiry := credit.Get("expires_at")
			expiry := bankedTime(rawExpiry)
			if rawExpiry.Exists() && rawExpiry.Type != ojson.Null && expiry == nil {
				continue
			}
			if expiry != nil && !expiry.After(now) {
				continue
			}
			status.Resets = append(status.Resets, BankedReset{ID: id, ExpiresAt: expiry, Count: 1, Usable: true})
		}
		// Check even when ChatGPT is still answering by charging purchased credits.
		usage, errUsage := m.bankedRequest(ctx, a, http.MethodGet, "https://chatgpt.com/backend-api/wham/usage", nil)
		if errUsage != nil {
			return status, errUsage
		}
		limits := ojson.GetBytesNoCopy(usage, "rate_limit")
		if !limits.IsObject() {
			return status, errors.New("missing codex usage windows")
		}
		status.Exhausted = limits.Get("limit_reached").Bool() || (limits.Get("allowed").Type == ojson.False) ||
			limits.Get("primary_window.used_percent").Float() >= 100 || limits.Get("secondary_window.used_percent").Float() >= 100
	} else {
		block := ojson.GetBytesNoCopy(data, "cedar_ember")
		if !block.Exists() {
			return status, nil
		} // API may not offer the experiment.
		if !block.IsObject() {
			return status, errors.New("invalid claude reset grants response")
		}
		if !block.Get("eligible").Bool() {
			return status, nil
		}
		status.Exhausted = block.Get("at_limit").Bool()
		status.CooldownUntil = bankedTime(block.Get("cooldown_until"))
		for _, grant := range block.Get("grants").Array() {
			id := grant.Get("id").String()
			count := grant.Get("resets_left").Int()
			if id == "" || count <= 0 || grant.Get("paused").Bool() {
				continue
			}
			rawExpiry, rawStart := grant.Get("ends_at"), grant.Get("starts_at")
			expiry, start := bankedTime(rawExpiry), bankedTime(rawStart)
			if rawExpiry.Exists() && rawExpiry.Type != ojson.Null && expiry == nil || rawStart.Exists() && rawStart.Type != ojson.Null && start == nil {
				continue
			}
			if expiry != nil && !expiry.After(now) || start != nil && start.After(now) {
				continue
			}
			requires := grant.Get("use_requires_limit")
			status.Resets = append(status.Resets, BankedReset{ID: id, ExpiresAt: expiry, Count: count,
				Usable: grant.Get("usable_now").Bool(), RequiresLimit: !requires.Exists() || requires.Bool()})
		}
	}
	sort.SliceStable(status.Resets, func(i, j int) bool {
		a, b := status.Resets[i].ExpiresAt, status.Resets[j].ExpiresAt
		return a != nil && (b == nil || a.Before(*b))
	})
	for _, reset := range status.Resets {
		status.Count += reset.Count
	}
	return status, nil
}

// ListBankedResets never redeems, even when mode is off or a reset is expiring.
func (m *Manager) ListBankedResets(ctx context.Context, authID string) (BankedResetStatus, error) {
	a, ok := m.GetByID(authID)
	if !ok || !bankedEligible(a) {
		return BankedResetStatus{}, errors.New("unsupported reset credential")
	}
	state := m.bankedAccount(a)
	state.mu.Lock()
	defer state.mu.Unlock()
	if time.Now().Before(state.retryAfter) {
		return BankedResetStatus{}, ErrBankedResetBackoff
	}
	status, err := m.readBankedStatus(ctx, a, time.Now())
	if err != nil {
		state.fail(time.Now())
	}
	return status, err
}

func (s *bankedResetAccount) fail(now time.Time) {
	s.failures = min(s.failures+1, 6)
	s.retryAfter = now.Add(time.Duration(1<<s.failures) * 15 * time.Second)
}

func (m *Manager) saveBankedOperation(ctx context.Context, authID string, op *bankedResetOperation) error {
	// Use the credential's mutation lock and merge only this metadata field so a
	// concurrently rotated access/refresh token is never overwritten.
	release := m.lockAuthMutation(authID)
	defer release()
	m.mu.Lock()
	defer m.mu.Unlock()
	a := m.auths[authID]
	if a == nil || bankedAccountKey(a) != op.AccountKey {
		return errors.New("reset credential removed or replaced")
	}
	if a.Metadata == nil {
		a.Metadata = make(map[string]any)
	}
	prior, existed := a.Metadata["banked_reset_operation"]
	data, _ := json.Marshal(op)
	var record map[string]any
	_ = json.Unmarshal(data, &record)
	a.Metadata["banked_reset_operation"] = record
	a.Generation++
	err := m.persistLocked(ctx, a)
	if err != nil {
		if existed {
			a.Metadata["banked_reset_operation"] = prior
		} else {
			delete(a.Metadata, "banked_reset_operation")
		}
	}
	return err
}

func bankedOperation(a *Auth) *bankedResetOperation {
	if a.Metadata == nil {
		return nil
	}
	data, err := json.Marshal(a.Metadata["banked_reset_operation"])
	if err != nil {
		return nil
	}
	var op bankedResetOperation
	if json.Unmarshal(data, &op) != nil || op.RequestID == "" {
		return nil
	}
	return &op
}

// RedeemBankedReset serializes manual and automatic use by provider account.
// creditID may be empty to select the earliest-expiring usable reset.
func (m *Manager) RedeemBankedReset(ctx context.Context, authID, creditID string) (BankedResetResult, error) {
	return m.useBankedReset(ctx, authID, creditID, false, time.Now())
}

func (m *Manager) useBankedReset(ctx context.Context, authID, creditID string, automatic bool, now time.Time) (BankedResetResult, error) {
	var result BankedResetResult
	a, ok := m.GetByID(authID)
	if !ok || !bankedEligible(a) {
		return result, errors.New("unsupported reset credential")
	}
	if _, enabled := m.bankedPolicy(a.Provider, automatic); !enabled {
		return result, ErrBankedResetDisabled
	}
	accountKey := bankedAccountKey(a)
	state := m.bankedAccount(a)
	state.mu.Lock()
	defer state.mu.Unlock()
	m.restoreBankedLedger(state, accountKey)
	if now.Before(state.retryAfter) {
		return result, ErrBankedResetBackoff
	}
	if automatic && now.Before(state.nextCheck) {
		return result, nil
	}
	if !automatic {
		previous := state.last
		if previous == nil {
			previous = bankedOperation(a)
		}
		if previous != nil && bankedResetConfirmed(a.Provider, previous.Code) && now.Before(previous.At.Add(time.Minute)) && (creditID == "" || creditID == previous.CreditID) {
			return BankedResetResult{Code: previous.Code, RequestID: previous.RequestID, CreditID: previous.CreditID}, nil
		}
	}

	a, ok = m.GetByID(authID)
	if !ok || !bankedEligible(a) || bankedAccountKey(a) != accountKey {
		return result, errors.New("reset credential removed, disabled or replaced")
	}
	lead, enabled := m.bankedPolicy(a.Provider, automatic)
	if !enabled {
		return result, ErrBankedResetDisabled
	}
	status, err := m.readBankedStatus(ctx, a, now)
	if err != nil {
		state.fail(now)
		return result, err
	}
	// Empty accounts need no usage probe on every generation. With credits, the
	// live usage check above runs before every generation (no stale-quota cache).
	if status.Count == 0 {
		state.nextCheck = now.Add(time.Minute)
	}
	if state.pending == nil {
		if saved := bankedOperation(a); saved != nil && saved.Code == "" {
			state.pending = saved
		}
	}
	op := state.pending
	retrying := op != nil
	if op != nil && op.AccountKey != accountKey {
		return result, errors.New("reset intent belongs to a replaced credential")
	}
	if op != nil && creditID != "" && op.CreditID != creditID {
		return result, errors.New("another reset redemption is unresolved")
	}
	if op == nil {
		if status.CooldownUntil != nil && status.CooldownUntil.After(now) {
			state.retryAfter = *status.CooldownUntil
			return result, ErrBankedResetBackoff
		}
		for _, reset := range status.Resets {
			if creditID != "" && creditID != reset.ID || !reset.Usable || reset.RequiresLimit && !status.Exhausted {
				continue
			}
			reason := "manual"
			if automatic {
				if status.Exhausted {
					reason = "exhausted"
				} else if lead > 0 && reset.ExpiresAt != nil && reset.ExpiresAt.Sub(now) <= lead {
					reason = "expiry"
				} else {
					continue
				}
			}
			// Claude grants can contain several resets. Count identifies this use;
			// Codex credits always have count 1. Retries keep exactly the same UUID.
			requestID := uuid.NewSHA1(uuid.NameSpaceURL, []byte(bankedAccountKey(a)+":"+reset.ID+":"+strconv.FormatInt(reset.Count, 10))).String()
			if saved := bankedOperation(a); saved != nil && saved.CreditID == reset.ID {
				if bankedResetConfirmed(a.Provider, saved.Code) && saved.RequestID == requestID {
					if automatic {
						continue
					}
					return BankedResetResult{Code: saved.Code, RequestID: saved.RequestID, CreditID: saved.CreditID}, nil
				}
				if bankedResetNoSpend(a.Provider, saved.Code) {
					requestID = uuid.NewSHA1(uuid.NameSpaceURL, []byte(requestID+":"+saved.RequestID)).String()
				}
			}
			op = &bankedResetOperation{AuthID: authID, AccountKey: accountKey, CreditID: reset.ID, RequestID: requestID, Reason: reason, At: now}
			if a.Provider == "claude" {
				profile, errProfile := m.bankedRequest(ctx, a, http.MethodGet, "https://api.anthropic.com/api/oauth/profile", nil)
				if errProfile != nil {
					state.fail(now)
					return result, errProfile
				}
				org := ojson.GetBytesNoCopy(profile, "organization.uuid").String()
				if _, errUUID := uuid.Parse(org); errUUID != nil {
					state.fail(now)
					return result, errors.New("invalid claude organization")
				}
				op.Organization = org
			}
			if errSave := m.saveBankedOperation(ctx, authID, op); errSave != nil {
				state.fail(now)
				return result, errors.New("could not persist reset intent; nothing sent")
			}
			state.pending = op
			break
		}
	}
	if op == nil {
		state.failures = 0
		if !automatic {
			return result, ErrNoBankedReset
		}
		return result, nil
	}
	// Claude grants can contain multiple uses. Its Management Center bounds
	// ambiguous retries to ten minutes; do not rely on indefinite provider
	// deduplication after that window. Keep the intent unresolved for inspection.
	if a.Provider == "claude" && retrying && (op.At.IsZero() || op.At.After(now) || now.Sub(op.At) >= 10*time.Minute) {
		state.fail(now)
		return result, errors.New("ambiguous Claude reset is outside the safe retry window; inspect provider usage before resolving the intent")
	}
	// Re-read the login and setting immediately before sending a mutation.
	a, ok = m.GetByID(authID)
	if !ok || !bankedEligible(a) || bankedAccountKey(a) != accountKey {
		return result, errors.New("reset credential removed, disabled or replaced")
	}
	if _, enabled = m.bankedPolicy(a.Provider, automatic); !enabled {
		return result, ErrBankedResetDisabled
	}
	target := codexBankedURL + "/consume"
	payload := map[string]string{"credit_id": op.CreditID, "redeem_request_id": op.RequestID}
	if a.Provider == "claude" {
		target = "https://api.anthropic.com/api/organizations/" + op.Organization + "/reset_rate_limits"
		payload = map[string]string{"program": "cedar_ember", "grant_id": op.CreditID, "request_id": op.RequestID}
	}
	log.WithFields(log.Fields{"auth_index": a.Index, "provider": a.Provider, "request_id": op.RequestID, "reason": op.Reason}).Info("banked reset redemption requested")
	data, err := m.bankedRequest(ctx, a, http.MethodPost, target, payload)
	if err != nil {
		state.fail(now)
		return result, err
	}
	code := ojson.GetBytesNoCopy(data, "code").String()
	if a.Provider == "claude" {
		code = ojson.GetBytesNoCopy(data, "result").String()
	}
	result = BankedResetResult{Code: code, RequestID: op.RequestID, CreditID: op.CreditID}
	confirmed := bankedResetConfirmed(a.Provider, code)
	if !confirmed {
		// A first, explicit no-spend answer settles this attempt. A refusal
		// after an ambiguous earlier POST does not prove that earlier use failed.
		if !retrying && bankedResetNoSpend(a.Provider, code) {
			op.Code = code
			op.At = now
			if errSave := m.saveBankedOperation(ctx, authID, op); errSave == nil {
				state.pending = nil
			}
		}
		// Retain the intent: a refusal on a retry cannot disprove an earlier spend.
		state.fail(now)
		log.WithFields(log.Fields{"auth_index": a.Index, "request_id": op.RequestID}).Warn("banked reset redemption not confirmed")
		return result, errors.New("banked reset redemption not confirmed")
	}
	op.Code = code
	op.At = now
	// Keep intent pending until BOTH provider confirmation and cooldown clearing
	// succeed. A restart retries the same provider idempotency key.
	if _, _, errReset := m.ResetQuota(ctx, authID); errReset != nil {
		state.fail(now)
		return result, errors.New("reset redeemed but cooldown persistence failed")
	}
	journalID := authID
	if origin, exists := m.GetByID(op.AuthID); exists && bankedAccountKey(origin) == accountKey {
		journalID = origin.ID
		if origin.ID != authID {
			if _, _, errReset := m.ResetQuota(ctx, origin.ID); errReset != nil {
				state.fail(now)
				return result, errors.New("reset redeemed but original credential cooldown persistence failed")
			}
		}
	}
	if errSave := m.saveBankedOperation(ctx, journalID, op); errSave != nil {
		state.fail(now)
		return result, errors.New("reset redeemed but audit persistence failed")
	}
	state.pending = nil
	state.last = op
	state.failures = 0
	state.retryAfter = time.Time{}
	state.nextCheck = now.Add(time.Minute) // provider usage can take seconds to settle.
	log.WithFields(log.Fields{"auth_index": a.Index, "provider": a.Provider, "request_id": op.RequestID, "reason": op.Reason, "result": code}).Info("banked reset redeemed; cooldown cleared")
	return result, nil
}

// CheckBankedResets is the automatic sweep, also available to an external
// scheduler. It is harmless in off/manual modes and checks only configured providers.
func (m *Manager) CheckBankedResets(ctx context.Context, providers []string) {
	if cfg := m.runtimeConfigSnapshot(); cfg == nil || cfg.QuotaExceeded.BankedReset.Mode != "auto" || m.HomeEnabled() {
		return
	}
	for _, a := range m.List() {
		if !bankedEligible(a) {
			continue
		}
		selected := len(providers) == 0
		for _, provider := range providers {
			if provider == a.Provider {
				selected = true
			}
		}
		if !selected {
			continue
		}
		if _, enabled := m.bankedPolicy(a.Provider, true); !enabled {
			continue
		}
		if _, err := m.useBankedReset(ctx, a.ID, "", true, time.Now()); err != nil && !errors.Is(err, ErrBankedResetBackoff) && !errors.Is(err, ErrBankedResetDisabled) {
			log.WithFields(log.Fields{"auth_index": a.Index, "provider": a.Provider}).WithError(err).Warn("banked reset check failed; backing off")
		}
	}
}

func (m *Manager) recoverBankedReset(ctx context.Context, authID string, execErr error) bool {
	if statusCodeFromError(execErr) != http.StatusTooManyRequests {
		return false
	}
	a, ok := m.GetByID(authID)
	if !ok || !bankedEligible(a) {
		return false
	}
	if _, enabled := m.bankedPolicy(a.Provider, true); !enabled {
		return false
	}
	state := m.bankedAccount(a)
	state.mu.Lock()
	// A recently confirmed reset must not cause an unbounded retry loop.
	recent := time.Now().Before(state.nextCheck)
	state.mu.Unlock()
	if recent {
		return false
	}
	result, err := m.useBankedReset(ctx, authID, "", true, time.Now())
	return err == nil && result.Code != ""
}

func (m *Manager) runBankedResetChecks(ctx context.Context) {
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			m.CheckBankedResets(ctx, nil)
		}
	}
}

func bankedResetConfirmed(provider, code string) bool {
	return code == "reset" || provider == "codex" && code == "already_redeemed" || provider == "claude" && code == "already_used"
}

func bankedResetNoSpend(provider, code string) bool {
	if provider == "codex" {
		return code == "nothing_to_reset" || code == "no_credit"
	}
	switch code {
	case "not_limited", "cooldown", "ineligible", "unavailable":
		return true
	}
	return false
}

// Duplicate login files share a provider account. On restart, recover the intent
// from any alias before looking at a newer credit inventory. Finish the journal
// on its originating credential, so another restart cannot resurrect it.
func (m *Manager) restoreBankedLedger(state *bankedResetAccount, accountKey string) {
	if state.restored {
		return
	}
	for _, candidate := range m.List() {
		if bankedAccountKey(candidate) != accountKey {
			continue
		}
		saved := bankedOperation(candidate)
		if saved == nil {
			continue
		}
		if saved.AuthID == "" {
			saved.AuthID = candidate.ID
		}
		if saved.Code == "" && state.pending == nil {
			state.pending = saved
		}
		if saved.AccountKey == accountKey && bankedResetConfirmed(candidate.Provider, saved.Code) && (state.last == nil || saved.At.After(state.last.At)) {
			state.last = saved
		}
	}
	if state.last != nil {
		state.nextCheck = state.last.At.Add(time.Minute)
	}
	state.restored = true
}
