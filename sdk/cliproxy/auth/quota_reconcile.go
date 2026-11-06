package auth

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const (
	codexUsageProbeURL         = "https://chatgpt.com/backend-api/wham/usage"
	defaultQuotaReconcileEvery = 30 * time.Second
	quotaProbeMinGap           = 10 * time.Second
	quotaProbeTimeout          = 20 * time.Second
	quotaProbeMaxBody          = 1 << 20
)

// quotaReconciler keeps quota cooldowns in step with the provider's own usage
// report. A usage_limit_reached response stores a fixed recovery time, but the
// provider can lift the limit earlier (manual reset, plan change, window
// rollover). Without reconciliation the stale cooldown blocks the credential
// until that stored time passes.
type quotaReconciler struct {
	started   atomic.Bool
	wake      atomic.Pointer[chan struct{}]
	lastProbe sync.Map // auth ID -> time.Time
}

// StartQuotaReconciler launches a loop that, when quota-reconcile is enabled, probes Codex credentials sitting in
// a quota cooldown and clears the cooldown once the provider reports usage is
// allowed. It also runs when a request is rejected because of a cooldown.
func (m *Manager) StartQuotaReconciler(parent context.Context, interval time.Duration) {
	if m == nil || !m.quotaReconcile.started.CompareAndSwap(false, true) {
		return
	}
	if interval <= 0 {
		interval = defaultQuotaReconcileEvery
	}
	wake := make(chan struct{}, 1)
	m.quotaReconcile.wake.Store(&wake)
	go func() {
		defer m.quotaReconcile.started.Store(false)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-parent.Done():
				return
			case <-ticker.C:
			case <-wake:
			}
			m.reconcileQuotaCooldowns(parent)
		}
	}()
}

// requestQuotaReconcile asks the reconciler to run soon. It never blocks.
func (m *Manager) requestQuotaReconcile() {
	if m == nil {
		return
	}
	if wake := m.quotaReconcile.wake.Load(); wake != nil {
		select {
		case *wake <- struct{}{}:
		default:
		}
	}
}

func authHasQuotaCooldown(auth *Auth) bool {
	if auth == nil || auth.Disabled || auth.Status == StatusDisabled {
		return false
	}
	if auth.Quota.Exceeded {
		return true
	}
	for _, state := range auth.ModelStates {
		if state != nil && state.Quota.Exceeded {
			return true
		}
	}
	return false
}

func (m *Manager) reconcileQuotaCooldowns(ctx context.Context) {
	if cfg := m.runtimeConfigSnapshot(); cfg == nil || !cfg.QuotaReconcile {
		return
	}
	for _, auth := range m.List() {
		if ctx.Err() != nil {
			return
		}
		if auth == nil || !strings.EqualFold(strings.TrimSpace(auth.Provider), "codex") || !authHasQuotaCooldown(auth) {
			continue
		}
		now := time.Now()
		if last, ok := m.quotaReconcile.lastProbe.Load(auth.ID); ok && now.Sub(last.(time.Time)) < quotaProbeMinGap {
			continue
		}
		m.quotaReconcile.lastProbe.Store(auth.ID, now)

		allowed, err := m.probeCodexUsageAllowed(ctx, auth)
		if err != nil {
			log.Debugf("quota reconcile: probe failed for %s: %v", auth.ID, err)
			continue
		}
		if !allowed {
			continue
		}
		if _, _, errReset := m.ResetQuota(ctx, auth.ID); errReset != nil {
			log.Warnf("quota reconcile: reset failed for %s: %v", auth.ID, errReset)
			continue
		}
		log.Infof("quota reconcile: provider reports usage allowed, cleared stale quota cooldown for %s", auth.ID)
	}
}

// probeCodexUsageAllowed reports whether the provider says the credential may
// make requests right now. An unreadable or partial answer is an error so the
// existing cooldown is kept.
func (m *Manager) probeCodexUsageAllowed(ctx context.Context, auth *Auth) (bool, error) {
	probeCtx, cancel := context.WithTimeout(ctx, quotaProbeTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(probeCtx, http.MethodGet, codexUsageProbeURL, nil)
	if err != nil {
		return false, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "codex_cli_rs/0.164.0")
	if accountID, _ := auth.Metadata["account_id"].(string); strings.TrimSpace(accountID) != "" {
		req.Header.Set("Chatgpt-Account-Id", strings.TrimSpace(accountID))
	}
	resp, err := m.HttpRequest(probeCtx, auth, req)
	if err != nil {
		return false, err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, quotaProbeMaxBody))
	if err != nil {
		return false, err
	}
	if resp.StatusCode != http.StatusOK {
		return false, fmt.Errorf("usage probe returned HTTP %d", resp.StatusCode)
	}
	allowed := gjson.GetBytes(body, "rate_limit.allowed")
	limitReached := gjson.GetBytes(body, "rate_limit.limit_reached")
	if !allowed.Exists() || !limitReached.Exists() {
		return false, fmt.Errorf("usage probe response has no rate_limit state")
	}
	return allowed.Bool() && !limitReached.Bool(), nil
}
