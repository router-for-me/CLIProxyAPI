package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const (
	codexQuotaUsageURL       = "https://chatgpt.com/backend-api/wham/usage"
	codexQuotaProbeWorkers   = 4
	codexQuotaProbeBodyLimit = 1 << 20
)

type codexQuotaProbeBucket struct {
	key     string
	auth    *Auth
	authIDs []string
}

type codexQuotaProbeOutcome struct {
	recovered bool
	resetAt   time.Time
	err       error
}

type codexWhamUsageResponse struct {
	PlanType  string `json:"plan_type"`
	RateLimit struct {
		LimitReached  *bool `json:"limit_reached"`
		PrimaryWindow struct {
			UsedPercent       *float64        `json:"used_percent"`
			ResetAt           json.RawMessage `json:"reset_at"`
			ResetAfterSeconds *float64        `json:"reset_after_seconds"`
		} `json:"primary_window"`
	} `json:"rate_limit"`
}

// StartCodexQuotaProbe starts the non-consuming Codex quota-status loop.
// Starting it again replaces the previous loop.
func (m *Manager) StartCodexQuotaProbe(parent context.Context) {
	if m == nil {
		return
	}
	if parent == nil {
		parent = context.Background()
	}
	ctx, cancel := context.WithCancel(parent)
	wake := make(chan struct{}, 1)

	m.mu.Lock()
	previous := m.quotaProbeCancel
	m.quotaProbeCancel = cancel
	m.quotaProbeWake = wake
	m.mu.Unlock()
	if previous != nil {
		previous()
	}
	go m.runCodexQuotaProbeLoop(ctx, wake)
	m.signalCodexQuotaProbe()
}

// StopCodexQuotaProbe stops the Codex quota-status loop.
func (m *Manager) StopCodexQuotaProbe() {
	if m == nil {
		return
	}
	m.mu.Lock()
	cancel := m.quotaProbeCancel
	m.quotaProbeCancel = nil
	m.quotaProbeWake = nil
	m.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}

func (m *Manager) signalCodexQuotaProbe() {
	if m == nil {
		return
	}
	m.mu.RLock()
	wake := m.quotaProbeWake
	m.mu.RUnlock()
	if wake == nil {
		return
	}
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (m *Manager) runCodexQuotaProbeLoop(ctx context.Context, wake <-chan struct{}) {
	for {
		now := time.Now()
		due, next := m.collectCodexQuotaProbeBuckets(now)
		if len(due) > 0 {
			m.probeDueCodexQuotaBuckets(ctx, due)
			continue
		}

		if next.IsZero() {
			select {
			case <-ctx.Done():
				return
			case <-wake:
				continue
			}
		}

		wait := time.Until(next)
		if wait < 0 {
			wait = 0
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			stopAndDrainQuotaProbeTimer(timer)
			return
		case <-wake:
			stopAndDrainQuotaProbeTimer(timer)
		case <-timer.C:
		}
	}
}

func stopAndDrainQuotaProbeTimer(timer *time.Timer) {
	if timer == nil || timer.Stop() {
		return
	}
	select {
	case <-timer.C:
	default:
	}
}

func (m *Manager) collectCodexQuotaProbeBuckets(now time.Time) ([]codexQuotaProbeBucket, time.Time) {
	if m == nil {
		return nil, time.Time{}
	}
	type bucketState struct {
		bucket codexQuotaProbeBucket
		dueAt  time.Time
	}
	buckets := make(map[string]*bucketState)

	m.mu.RLock()
	for _, auth := range m.auths {
		if auth == nil || !m.codexCooldownPolicyForAuth(auth).quotaProbeEnabled {
			continue
		}
		dueAt, ok := codexUsageQuotaProbeAt(auth, now)
		if !ok {
			continue
		}
		key := codexQuotaBucketKey(auth)
		state := buckets[key]
		if state == nil {
			state = &bucketState{bucket: codexQuotaProbeBucket{key: key, auth: auth.Clone()}, dueAt: dueAt}
			buckets[key] = state
		}
		state.bucket.authIDs = append(state.bucket.authIDs, auth.ID)
		if state.dueAt.IsZero() || (!dueAt.IsZero() && dueAt.Before(state.dueAt)) {
			state.dueAt = dueAt
			state.bucket.auth = auth.Clone()
		} else if dueAt.Equal(state.dueAt) && state.bucket.auth != nil && auth.ID < state.bucket.auth.ID {
			state.bucket.auth = auth.Clone()
		}
	}
	m.mu.RUnlock()

	due := make([]codexQuotaProbeBucket, 0)
	var next time.Time
	for _, state := range buckets {
		sort.Strings(state.bucket.authIDs)
		if state.dueAt.IsZero() || !state.dueAt.After(now) {
			due = append(due, state.bucket)
			continue
		}
		if next.IsZero() || state.dueAt.Before(next) {
			next = state.dueAt
		}
	}
	sort.Slice(due, func(i, j int) bool { return due[i].key < due[j].key })
	return due, next
}

func codexUsageQuotaProbeAt(auth *Auth, now time.Time) (time.Time, bool) {
	if auth == nil {
		return time.Time{}, false
	}
	var next time.Time
	found := false
	consider := func(quota QuotaState) {
		if !quota.Exceeded || quota.Reason != "usage_limit_reached" {
			return
		}
		found = true
		candidate := quota.NextProbeAt
		if candidate.IsZero() {
			candidate = nextCodexQuotaProbeAt(now, quota.NextRecoverAt)
		}
		if next.IsZero() || candidate.Before(next) {
			next = candidate
		}
	}
	consider(auth.Quota)
	for _, state := range auth.ModelStates {
		if state != nil {
			consider(state.Quota)
		}
	}
	return next, found
}

func codexQuotaBucketKey(auth *Auth) string {
	provider := strings.ToLower(strings.TrimSpace(auth.Provider))
	accountID := authMetadataString(auth, "account_id")
	if accountID == "" {
		accountID = authMetadataString(auth, "chatgpt_account_id")
	}
	if accountID == "" {
		accountID = authAttribute(auth, "account_id")
	}
	planType := authMetadataString(auth, "plan_type")
	if planType == "" {
		planType = authAttribute(auth, "plan_type")
	}
	if accountID == "" || planType == "" {
		return strings.Join([]string{provider, "auth", strings.TrimSpace(auth.ID)}, "|")
	}
	return strings.Join([]string{provider, strings.ToLower(accountID), strings.ToLower(planType)}, "|")
}

func (m *Manager) probeDueCodexQuotaBuckets(ctx context.Context, buckets []codexQuotaProbeBucket) {
	if len(buckets) == 0 {
		return
	}
	workers := codexQuotaProbeWorkers
	if workers > len(buckets) {
		workers = len(buckets)
	}
	jobs := make(chan codexQuotaProbeBucket)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for bucket := range jobs {
				if ctx.Err() != nil {
					return
				}
				outcome := m.probeCodexQuotaBucket(ctx, bucket)
				if ctx.Err() != nil {
					return
				}
				m.applyCodexQuotaProbeOutcome(context.Background(), bucket.key, outcome, time.Now())
			}
		}()
	}
	for _, bucket := range buckets {
		select {
		case <-ctx.Done():
			close(jobs)
			wg.Wait()
			return
		case jobs <- bucket:
		}
	}
	close(jobs)
	wg.Wait()
}

func (m *Manager) probeCodexQuotaBucket(ctx context.Context, bucket codexQuotaProbeBucket) codexQuotaProbeOutcome {
	if bucket.auth == nil {
		return codexQuotaProbeOutcome{err: fmt.Errorf("quota probe auth is nil")}
	}
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, codexQuotaUsageURL, nil)
	if errRequest != nil {
		return codexQuotaProbeOutcome{err: errRequest}
	}
	req.Header.Set("Accept", "application/json")
	resp, errDo := m.HttpRequest(ctx, bucket.auth, req)
	if errDo != nil {
		return codexQuotaProbeOutcome{err: errDo}
	}
	if resp == nil {
		return codexQuotaProbeOutcome{err: fmt.Errorf("quota probe returned nil response")}
	}
	if resp.Body == nil {
		return codexQuotaProbeOutcome{err: fmt.Errorf("quota probe returned nil response body")}
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("close Codex quota probe response: %v", errClose)
		}
	}()
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, codexQuotaProbeBodyLimit))
	if errRead != nil {
		return codexQuotaProbeOutcome{err: fmt.Errorf("read quota probe response: %w", errRead)}
	}
	if resp.StatusCode != http.StatusOK {
		return codexQuotaProbeOutcome{err: fmt.Errorf("quota probe returned HTTP %d", resp.StatusCode)}
	}
	var payload codexWhamUsageResponse
	if errUnmarshal := json.Unmarshal(body, &payload); errUnmarshal != nil {
		return codexQuotaProbeOutcome{err: fmt.Errorf("parse quota probe response: %w", errUnmarshal)}
	}
	if payload.RateLimit.LimitReached == nil || payload.RateLimit.PrimaryWindow.UsedPercent == nil {
		return codexQuotaProbeOutcome{err: fmt.Errorf("quota probe response is missing limiter fields")}
	}
	resetAt := parseCodexQuotaResetAt(payload.RateLimit.PrimaryWindow.ResetAt, payload.RateLimit.PrimaryWindow.ResetAfterSeconds, time.Now())
	recovered := !*payload.RateLimit.LimitReached && *payload.RateLimit.PrimaryWindow.UsedPercent < 100
	return codexQuotaProbeOutcome{recovered: recovered, resetAt: resetAt}
}

func parseCodexQuotaResetAt(raw json.RawMessage, resetAfterSeconds *float64, now time.Time) time.Time {
	text := strings.Trim(strings.TrimSpace(string(raw)), `"`)
	if text != "" && text != "null" {
		if unixSeconds, errParse := strconv.ParseInt(text, 10, 64); errParse == nil && unixSeconds > 0 {
			return time.Unix(unixSeconds, 0)
		}
		if unixSeconds, errParse := strconv.ParseFloat(text, 64); errParse == nil && unixSeconds > 0 {
			return time.Unix(int64(unixSeconds), 0)
		}
		if parsed, errParse := time.Parse(time.RFC3339, text); errParse == nil {
			return parsed
		}
	}
	if resetAfterSeconds != nil && *resetAfterSeconds > 0 {
		return now.Add(time.Duration(*resetAfterSeconds * float64(time.Second)))
	}
	return time.Time{}
}

func nextCodexQuotaProbeAfterAttempt(now, resetAt time.Time) time.Time {
	if resetAt.IsZero() || !resetAt.After(now) {
		return now.Add(quotaProbeNearInterval)
	}
	return nextCodexQuotaProbeAt(now, resetAt)
}

func (m *Manager) applyCodexQuotaProbeOutcome(ctx context.Context, bucketKey string, outcome codexQuotaProbeOutcome, now time.Time) {
	if m == nil || bucketKey == "" {
		return
	}
	if outcome.recovered && outcome.err == nil {
		ids := m.codexQuotaAuthIDsForBucket(bucketKey)
		for _, authID := range ids {
			if _, _, errReset := m.ResetQuota(ctx, authID); errReset != nil {
				log.Warnf("failed to reset recovered Codex quota state for %s: %v", authID, errReset)
			}
		}
		return
	}

	snapshots := make([]*Auth, 0)
	m.mu.Lock()
	for _, auth := range m.auths {
		if auth == nil || codexQuotaBucketKey(auth) != bucketKey {
			continue
		}
		changed := updateUsageQuotaProbeState(auth, outcome, now)
		if changed {
			snapshots = append(snapshots, auth.Clone())
		}
	}
	m.mu.Unlock()
	if m.scheduler != nil {
		for _, snapshot := range snapshots {
			m.scheduler.upsertAuth(snapshot)
		}
	}
	if len(snapshots) > 0 {
		m.persistCooldownStates(ctx)
	}
}

func (m *Manager) codexQuotaAuthIDsForBucket(bucketKey string) []string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]string, 0)
	for _, auth := range m.auths {
		if auth != nil && codexQuotaBucketKey(auth) == bucketKey {
			if _, ok := codexUsageQuotaProbeAt(auth, time.Now()); ok {
				ids = append(ids, auth.ID)
			}
		}
	}
	sort.Strings(ids)
	return ids
}

func updateUsageQuotaProbeState(auth *Auth, outcome codexQuotaProbeOutcome, now time.Time) bool {
	if auth == nil {
		return false
	}
	changed := false
	authLevelUsage := auth.Quota.Exceeded && auth.Quota.Reason == "usage_limit_reached"
	modelChanged := false
	update := func(quota *QuotaState, nextRetry *time.Time, unavailable *bool, status *Status, updatedAt *time.Time) {
		if quota == nil || !quota.Exceeded || quota.Reason != "usage_limit_reached" {
			return
		}
		resetAt := quota.NextRecoverAt
		if !outcome.resetAt.IsZero() {
			resetAt = outcome.resetAt
			quota.NextRecoverAt = resetAt
			*nextRetry = resetAt
		}
		quota.NextProbeAt = nextCodexQuotaProbeAfterAttempt(now, resetAt)
		*unavailable = true
		*status = StatusError
		*updatedAt = now
		changed = true
	}
	update(&auth.Quota, &auth.NextRetryAfter, &auth.Unavailable, &auth.Status, &auth.UpdatedAt)
	for _, state := range auth.ModelStates {
		if state == nil {
			continue
		}
		before := changed
		update(&state.Quota, &state.NextRetryAfter, &state.Unavailable, &state.Status, &state.UpdatedAt)
		if changed != before {
			modelChanged = true
		}
	}
	if modelChanged && !authLevelUsage {
		updateAggregatedAvailability(auth, now)
	}
	return changed
}

func (m *Manager) scheduleCodexQuotaProbeNow(authID string) {
	if m == nil || strings.TrimSpace(authID) == "" {
		return
	}
	now := time.Now()
	changed := false
	var snapshot *Auth
	m.mu.Lock()
	if auth := m.auths[authID]; auth != nil && m.codexCooldownPolicyForAuth(auth).quotaProbeEnabled {
		if auth.Quota.Exceeded && auth.Quota.Reason == "usage_limit_reached" {
			auth.Quota.NextProbeAt = now
			auth.Unavailable = true
			auth.Status = StatusError
			auth.UpdatedAt = now
			changed = true
		}
		modelChanged := false
		for _, state := range auth.ModelStates {
			if state != nil && state.Quota.Exceeded && state.Quota.Reason == "usage_limit_reached" {
				state.Quota.NextProbeAt = now
				state.Unavailable = true
				state.Status = StatusError
				state.UpdatedAt = now
				changed = true
				modelChanged = true
			}
		}
		if modelChanged && !(auth.Quota.Exceeded && auth.Quota.Reason == "usage_limit_reached") {
			updateAggregatedAvailability(auth, now)
		}
		if changed {
			snapshot = auth.Clone()
		}
	}
	m.mu.Unlock()
	if changed {
		if m.scheduler != nil && snapshot != nil {
			m.scheduler.upsertAuth(snapshot)
		}
		m.persistCooldownStates(context.Background())
		m.signalCodexQuotaProbe()
	}
}
