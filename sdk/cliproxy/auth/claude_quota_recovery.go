package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"

	"github.com/MoeForge/moego/ojson"
	log "github.com/sirupsen/logrus"
)

const claudeQuotaCheckInterval = 5 * time.Minute
const claudeUsageURL = "https://api.anthropic.com/api/oauth/usage"
const claudeUsageMaxBody = 1 << 20

const claudeQuotaProbeConcurrency = 4

// claudeQuotaProbeSet tracks in-flight usage probes. The zero value is ready.
type claudeQuotaProbeSet struct {
	mu       sync.Mutex
	inFlight map[string]struct{}
	slots    chan struct{}
	wg       sync.WaitGroup
}

// tryStart reserves the probe of authID. It returns false when a probe for
// authID is in flight, so one account never has two probes.
func (s *claudeQuotaProbeSet) tryStart(authID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.inFlight[authID]; ok {
		return false
	}
	if s.inFlight == nil {
		s.inFlight = make(map[string]struct{})
		s.slots = make(chan struct{}, claudeQuotaProbeConcurrency)
	}
	s.inFlight[authID] = struct{}{}
	s.wg.Add(1)
	return true
}

// acquire waits for a free probe slot. It returns false when ctx ends first.
// Only a goroutine started after tryStart calls it, so s.slots is set.
func (s *claudeQuotaProbeSet) acquire(ctx context.Context) bool {
	select {
	case s.slots <- struct{}{}:
		return true
	case <-ctx.Done():
		return false
	}
}

func (s *claudeQuotaProbeSet) release() { <-s.slots }

func (s *claudeQuotaProbeSet) finish(authID string) {
	s.mu.Lock()
	delete(s.inFlight, authID)
	s.mu.Unlock()
	s.wg.Done()
}

func (s *claudeQuotaProbeSet) wait() { s.wg.Wait() }

// Reuse the auth refresh lifecycle so shutdown cancels outstanding quota reads.
func (m *Manager) watchClaudeQuota(ctx context.Context) {
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
		}
		m.reconcileClaudeQuota(ctx)
		timer.Reset(claudeQuotaCheckInterval)
	}
}

func claudeQuotaCandidate(a *Auth) bool {
	if a == nil || a.Provider != "claude" || a.AuthKind() == AuthKindAPIKey || a.Disabled || a.Status == StatusDisabled || hasUnauthorizedAuthFailure(a) || authMetadataString(a, "access_token") == "" {
		return false
	}
	now := time.Now()
	blocked := a.Quota.Exceeded && a.Quota.Reason == "credential_quota" && a.Quota.NextRecoverAt.After(now)
	// Under a quota, a sibling result can leave an old server error on the
	// auth. The live deadlines below show whether it still blocks. Other
	// errors, such as 401 or 403, are about the credential and stay.
	staleServerError := a.Quota.Exceeded && (a.Quota.Reason == "quota" || a.Quota.Reason == "credential_quota") && isServerError(a.LastError)
	if a.LastError != nil && a.LastError.HTTPStatus != http.StatusTooManyRequests && !staleServerError {
		return false
	}
	// These deadlines come from failures other than quota, or from a forced
	// cooldown in the config, so usage data cannot clear them.
	if a.credentialRetryAfter.After(now) || a.nonAuthCredentialRetryAfter.After(now) || a.nonQuotaCooldownUntil.After(now) || a.forceCooldownUntil.After(now) || a.quotaForceCooldownUntil.After(now) || a.credQuotaForceUntil.After(now) {
		return false
	}
	// Usage does not establish recovery from unrelated model/transport failures.
	for _, s := range a.ModelStates {
		// The reset keeps a disabled model as it is, so it cannot stop recovery.
		if s == nil || s.Status == StatusDisabled {
			continue
		}
		if hasLiveNonQuotaBlock(s, now) || hasLiveNonQuotaBlock(s.credentialQuotaShadow, now) {
			return false
		}
		quota := isQuotaModelState(s)
		// A quota state can keep an old error from before the quota. The live
		// deadlines above show whether that error still blocks the model. A
		// model without quota passes only with a server error whose retry
		// deadline has expired.
		expiredServerError := isServerError(s.LastError) && !s.NextRetryAfter.IsZero() && !s.NextRetryAfter.After(now)
		if !quota && !expiredServerError && ((s.LastError != nil && s.LastError.HTTPStatus != http.StatusTooManyRequests) || s.Unavailable || s.Status == StatusError) {
			return false
		}
		if quota && isModelStateActiveCooldown(s, now) {
			blocked = true
		}
	}
	return blocked
}

// hasLiveNonQuotaBlock reports whether s has a live cooldown that a usage
// report cannot clear: a non-quota or forced deadline, or a block without quota.
func hasLiveNonQuotaBlock(s *ModelState, now time.Time) bool {
	if s == nil {
		return false
	}
	if s.nonQuotaCooldownUntil.After(now) || s.nonCapacityCooldownUntil.After(now) || s.forceCooldownUntil.After(now) || s.quotaForceCooldownUntil.After(now) {
		return true
	}
	return !isQuotaModelState(s) && isModelStateActiveCooldown(s, now)
}

func isServerError(err *Error) bool {
	return err != nil && err.HTTPStatus >= http.StatusInternalServerError
}

func isQuotaModelState(s *ModelState) bool {
	return s.Quota.Exceeded && (s.Quota.Reason == "quota" || s.Quota.Reason == "credential_quota")
}

func (m *Manager) reconcileClaudeQuota(ctx context.Context) {
	if ctx.Err() != nil || m.HomeEnabled() {
		return
	}
	for _, listed := range m.List() {
		if ctx.Err() != nil {
			return
		}
		if !claudeQuotaCandidate(listed) {
			continue
		}
		// AGENTS.md forbids a deadline after the connection opens, so a stalled
		// probe must hold only its own slot and not the loop.
		if !m.claudeQuotaProbes.tryStart(listed.ID) {
			continue
		}
		// Subscribe first and take the snapshot after it. Each change after
		// the snapshot then cancels the probe.
		changes, stopWatch := m.WatchAuthChanges(listed.ID)
		a, ok := m.GetByID(listed.ID)
		if !ok || !claudeQuotaCandidate(a) {
			stopWatch()
			m.claudeQuotaProbes.finish(listed.ID)
			continue
		}
		go m.runClaudeQuotaProbe(ctx, a, changes, stopWatch)
	}
}

// runClaudeQuotaProbe runs one reserved probe. A change of the auth, such as
// a new token or proxy, cancels it, so the reservation does not outlive the
// credential that it probes.
func (m *Manager) runClaudeQuotaProbe(ctx context.Context, a *Auth, changes <-chan struct{}, stopWatch func()) {
	defer m.claudeQuotaProbes.finish(a.ID)
	defer stopWatch()
	probeCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-changes:
			cancel()
		case <-probeCtx.Done():
		}
	}()
	if !m.claudeQuotaProbes.acquire(probeCtx) {
		return
	}
	defer m.claudeQuotaProbes.release()
	m.probeClaudeQuota(probeCtx, a)
}

func (m *Manager) probeClaudeQuota(ctx context.Context, a *Auth) {
	probeCtx := ctx
	if rt := m.roundTripperFor(a); rt != nil {
		probeCtx = context.WithValue(probeCtx, "cliproxy.roundtripper", rt)
	}
	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, claudeUsageURL, nil)
	if err != nil {
		return
	}
	req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	req.Header.Set("User-Agent", "CLIProxyAPI")
	resp, err := m.HttpRequest(probeCtx, a, req)
	if err != nil {
		return
	}
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, claudeUsageMaxBody+1))
	errClose := resp.Body.Close()
	// Redirects and incomplete responses cannot establish recovery.
	if len(body) > claudeUsageMaxBody || errRead != nil || errClose != nil || resp.StatusCode != http.StatusOK || resp.Request == nil || resp.Request.Response != nil || resp.Request.URL.String() != claudeUsageURL || resp.Request.Header.Get("Authorization") != "Bearer "+authMetadataString(a, "access_token") {
		return
	}
	if !claudeUsageAvailable(body) {
		return
	}
	if errRestore := m.restoreClaudeQuota(ctx, a); errRestore != nil {
		log.WithError(errRestore).Warn("Failed to persist restored Claude quota")
	}
}

// Only a complete shared-window report with capacity in every present window
// can release an account-wide quota block. Extra usage credits are not evidence.
func claudeUsageAvailable(body []byte) bool {
	if !ojson.ValidBytes(body) {
		return false
	}
	root := ojson.ParseBytes(body)
	if !root.IsObject() {
		return false
	}
	five, week := root.Get("five_hour"), root.Get("seven_day")
	if !five.Exists() && five.Raw != "null" {
		return false
	}
	if !week.IsObject() {
		return false
	}
	available := true
	root.ForEach(func(k, v ojson.Result) bool {
		name := k.String()
		switch name {
		case "extra_usage", "spend", "member_dashboard_available", "seven_day_breakdown":
			return true
		case "weekly_scoped_shares":
			if v.IsArray() && len(v.Array()) > 0 && !root.Get("limits").IsArray() {
				available = false
				return false
			}
			if v.Raw != "null" && !v.IsArray() {
				available = false
				return false
			}
			return true
		case "limits":
			if v.Raw == "null" {
				return true
			}
			if !v.IsArray() {
				available = false
				return false
			}
			for _, limit := range v.Array() {
				percent := limit.Get("percent")
				if !limit.IsObject() || percent.Type != ojson.Number || percent.Float() < 0 || percent.Float() >= 100 {
					available = false
					return false
				}
			}
			return true
		}
		// Missing/null optional model windows carry no additional restriction.
		// Unknown populated fields must look like a validated quota window.
		if v.Raw == "null" {
			return true
		}
		locked := v.Get("locked_reason")
		if locked.Exists() && locked.Raw != "null" && locked.String() != "" {
			available = false
			return false
		}
		u := v.Get("utilization")
		if !v.IsObject() || u.Type != ojson.Number || u.Float() < 0 || u.Float() >= 100 {
			available = false
			return false
		}
		return true
	})
	return available
}

func (m *Manager) restoreClaudeQuota(ctx context.Context, snapshot *Auth) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	_, _, err := m.resetQuota(ctx, snapshot.ID, func(current *Auth) bool {
		return ctx.Err() == nil && !m.HomeEnabled() && current.RegistrationEpoch == snapshot.RegistrationEpoch && authMetadataString(current, "access_token") == authMetadataString(snapshot, "access_token") && current.failureSeq == snapshot.failureSeq && claudeQuotaCandidate(current)
	}, true)
	if err != nil {
		return fmt.Errorf("restore Claude quota: %w", err)
	}
	return nil
}
