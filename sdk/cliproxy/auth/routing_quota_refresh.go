package auth

import (
	"context"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// StartRoutingQuotaRefresh starts one service-owned worker. Probes never refresh tokens,
// mutate credentials or update the upstream cooldown store. Cancellation owns their lifetime.
func (m *Manager) StartRoutingQuotaRefresh(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			m.refreshRoutingQuotas(ctx, time.Now())
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
			}
		}
	}()
}

func (m *Manager) refreshRoutingQuotas(ctx context.Context, now time.Time) *sync.WaitGroup {
	pending := &sync.WaitGroup{}
	if ctx.Err() != nil || m.HomeEnabled() {
		return pending
	}
	s := soonestSelector(m.Selector())
	if s == nil {
		return pending
	}
	auths := m.List()
	sort.Slice(auths, func(i, j int) bool {
		a, b := s.record(auths[i]), s.record(auths[j])
		if !a.attemptedAt.Equal(b.attemptedAt) {
			return a.attemptedAt.Before(b.attemptedAt)
		}
		return auths[i].ID < auths[j].ID
	})
	present := make(map[string]bool, len(auths))
	for _, a := range auths {
		present[a.ID] = true
	}
	s.mu.Lock()
	for id := range s.records {
		if !present[id] {
			delete(s.records, id)
		}
	}
	s.mu.Unlock()
	// Four independent probes prevent one stalled account from serializing the pool.
	// No request deadlines are added after connection establishment.
	for _, a := range auths {
		if ctx.Err() != nil {
			return pending
		}
		if a.Disabled || a.Status == StatusDisabled || !routingQuotaSupported(a) {
			continue
		}
		fingerprint := quotaFingerprint(a)
		s.mu.Lock()
		if s.records == nil {
			s.records = make(map[string]routingQuotaRecord)
		}
		if s.inFlight == nil {
			s.inFlight = make(map[string]bool)
		}
		r := s.records[a.ID]
		if r.fingerprint != fingerprint {
			r = routingQuotaRecord{fingerprint: fingerprint}
		}
		if s.inFlight[a.ID] || len(s.inFlight) >= 4 || (!r.attemptedAt.IsZero() && now.Sub(r.attemptedAt) < RoutingQuotaRefreshInterval) {
			s.mu.Unlock()
			continue
		}
		r.attemptedAt = now
		s.records[a.ID] = r
		s.inFlight[a.ID] = true
		s.mu.Unlock()
		pending.Add(1)
		go func(a *Auth) {
			defer pending.Done()
			windows, code := m.fetchRoutingQuota(ctx, a)
			observedAt := time.Now()
			// An old token's result must never replace the current credential's snapshot.
			current, exists := m.GetByID(a.ID)
			s.mu.Lock()
			defer s.mu.Unlock()
			delete(s.inFlight, a.ID)
			if !exists || quotaFingerprint(current) != quotaFingerprint(a) {
				return
			}
			r := s.records[a.ID]
			if r.fingerprint != quotaFingerprint(a) {
				return
			}
			r.errorCode = code
			if code == "" {
				r.observedAt = observedAt
				r.windows = windows
			}
			s.records[a.ID] = r
		}(a)
	}
	return pending
}

func (m *Manager) fetchRoutingQuota(ctx context.Context, a *Auth) ([]RoutingWindow, string) {
	token, _ := a.Metadata["access_token"].(string)
	if token == "" {
		return nil, "missing_token"
	}
	method, target := http.MethodGet, "https://chatgpt.com/backend-api/wham/usage"
	var body io.Reader
	switch a.Provider {
	case "claude":
		target = "https://api.anthropic.com/api/oauth/usage"
	case "antigravity":
		// The executor refreshes an expiring token inside HttpRequest; probes must not.
		if !antigravityTokenUsable(a, time.Now()) {
			return nil, "token_expiring"
		}
		method, target = http.MethodPost, antigravityQuotaBaseURL(a)+antigravityQuotaPath
		body = strings.NewReader(antigravityQuotaBody(a))
	}
	// Account usage always authenticates the original ChatGPT OAuth partition,
	// including credentials that use SIWC for non-Codex inference clients.
	if a.Provider == "codex" {
		ctx = WithCodexClientRequest(ctx, true)
	}
	req, errRequest := http.NewRequestWithContext(ctx, method, target, body)
	if errRequest != nil {
		return nil, "request_failed"
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Content-Type", "application/json")
	// The Antigravity executor sets its own User-Agent and bearer token.
	if a.Provider == "codex" {
		req.Header.Set("User-Agent", "codex-tui/0.154.0")
	} else if a.Provider == "claude" {
		req.Header.Set("User-Agent", "claude-cli/2.1.280 (external, cli)")
	}
	if a.Provider == "claude" {
		req.Header.Set("Anthropic-Beta", "oauth-2025-04-20")
	}
	if a.Provider == "codex" {
		if account, ok := a.Metadata["account_id"].(string); ok && account != "" {
			req.Header.Set("Chatgpt-Account-Id", account)
		}
	}
	// Existing executors supply credential headers, proxy settings and TLS behavior.
	resp, errRequest := m.HttpRequest(ctx, a, req)
	if errRequest != nil {
		return nil, "request_failed"
	}
	if resp == nil || resp.Body == nil {
		return nil, "request_failed"
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.WithField("error_code", "response_close_failed").Warn("routing quota response body close failed")
		}
	}()
	if resp.StatusCode != http.StatusOK {
		switch resp.StatusCode {
		case 401, 403:
			return nil, "auth_error"
		case 429:
			return nil, "rate_limited"
		default:
			return nil, "upstream_error"
		}
	}
	const maxBody = 1 << 20
	data, errRead := io.ReadAll(io.LimitReader(resp.Body, maxBody+1))
	if errRead != nil {
		return nil, "read_failed"
	}
	if len(data) > maxBody {
		return nil, "malformed"
	}
	windows, valid := parseRoutingQuota(a.Provider, data, time.Now())
	if !valid {
		return nil, "malformed"
	}
	return windows, ""
}
