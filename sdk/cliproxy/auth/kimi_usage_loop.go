package auth

import (
	"context"
	"time"

	log "github.com/sirupsen/logrus"
)

// kimiUsageProbeDefaultInterval is the default polling period (5 minutes) when
// no interval is configured or the configured value is invalid.
const kimiUsageProbeDefaultInterval = 5 * time.Minute

// StartKimiUsageProbe starts a background goroutine that periodically queries
// the /v1/usages endpoint for auths whose base_url matches api.kimi.com/coding.
// When a rolling quota window is exhausted the auth is cooled down to the real
// upstream resetTime; when all windows recover the cooldown is cleared.
// Lifecycle is controlled by the provided ctx; cancel ctx to stop.
// Idempotent — calling again stops the previous probe and starts a new one.
func (m *Manager) StartKimiUsageProbe(ctx context.Context, interval time.Duration) {
	if m == nil {
		return
	}
	// Stop any previous probe first to prevent concurrent loops (e.g. during
	// rapid config updates via the management API).
	m.StopKimiUsageProbe()

	if interval <= 0 {
		interval = kimiUsageProbeDefaultInterval
	}
	if ctx == nil {
		ctx = context.Background()
	}

	m.usageProbeMu.Lock()
	if m.usageProbeCancel != nil {
		m.usageProbeCancel()
	}
	probeCtx, cancel := context.WithCancel(ctx)
	m.usageProbeCancel = cancel
	m.usageProbeWG.Add(1)
	m.usageProbeMu.Unlock()

	go func() {
		defer m.usageProbeWG.Done()
		log.Infof("kimi usage probe started (interval=%s)", interval)
		m.runKimiUsageProbeOnce(probeCtx)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-probeCtx.Done():
				log.Info("kimi usage probe stopped")
				return
			case <-ticker.C:
				m.runKimiUsageProbeOnce(probeCtx)
			}
		}
	}()
}

// StopKimiUsageProbe cancels the probe goroutine and waits for it to exit.
// Safe to call from Shutdown or before starting a new probe.
func (m *Manager) StopKimiUsageProbe() {
	if m == nil {
		return
	}
	m.usageProbeMu.Lock()
	if m.usageProbeCancel != nil {
		m.usageProbeCancel()
		m.usageProbeCancel = nil
	}
	// Wait inside the lock so WatcherGroup.Add in StartKimiUsageProbe
	// cannot run concurrently with Wait (sync.WatcherGroup rule).
	m.usageProbeWG.Wait()
	m.usageProbeMu.Unlock()
}

// runKimiUsageProbeOnce executes one sweep: snapshots all auths, filters for
// Kimi auths, and queries each one's usage to adjust cooldown. Respects context
// cancellation so a shutdown or config update can abort the sweep early.
func (m *Manager) runKimiUsageProbeOnce(ctx context.Context) {
	if ctx == nil {
		ctx = context.Background()
	}
	auths := m.snapshotAuths()
	kimiCount := 0
	for i := range auths {
		// Check for cancellation before each auth so a shutdown or config
		// update can interrupt the sweep without waiting for all auths.
		if ctx.Err() != nil {
			return
		}
		auth := auths[i]
		if !isKimiUsageAuth(auth) {
			continue
		}
		kimiCount++
		if err := m.probeSingleKimiAuth(ctx, auth); err != nil {
			log.Debugf("kimi usage probe auth=%s: %v", auth.ID, err)
		}
	}
	if kimiCount > 0 {
		log.Debugf("kimi usage probe sweep: %d kimi auth(s) checked", kimiCount)
	}
}

// probeSingleKimiAuth handles a single Kimi auth: query usage → decide cooldown
// or recovery. Does NOT apply its own timeout; the caller's context controls
// cancellation (per repo AGENTS.md timeout policy).
func (m *Manager) probeSingleKimiAuth(ctx context.Context, auth *Auth) error {
	windows, err := m.fetchKimiUsage(ctx, auth)
	if err != nil {
		return err
	}

	now := time.Now()

	if recoverAt, ok := kimiUsageCooldown(windows); ok {
		if _, errSet := m.SetAuthQuotaExceeded(ctx, auth.ID, recoverAt, kimiUsageReason); errSet != nil {
			return errSet
		}
		log.Infof("kimi usage probe auth=%s: quota exhausted, cooled down until %s",
			auth.ID, recoverAt.Format(time.RFC3339))
		return nil
	}

	if kimiUsageFullyAvailable(windows) && hasKimiUsageCooldown(auth) {
		// Only clear cooldown states that were set by this probe (kimiUsageReason),
		// to avoid resetting cooldowns from other causes (e.g. Cloudflare challenges,
		// generic 429 backoff). hasKimiUsageCooldown matches any Kimi-probe state,
		// including ones whose NextRetryAfter has already passed, so the probe also
		// resumes registry-suspended models after the reset time.
		if errClear := m.clearKimiUsageCooldown(ctx, auth, now); errClear != nil {
			return errClear
		}
		log.Infof("kimi usage probe auth=%s: quota recovered, cooldown cleared", auth.ID)
	}
	return nil
}
