package auth

import (
	"context"
	"crypto/sha256"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

const RoutingQuotaRefreshInterval = 5 * time.Minute
const RoutingQuotaMaxAge = 15 * time.Minute

// RoutingWindow is a validated provider subscription window, never an upstream body.
type RoutingWindow struct {
	Key         string    `json:"key"`
	ResetsAt    time.Time `json:"resets_at"`
	UsedPercent float64   `json:"used_percent"`
	seconds     int64
	model       string
	denied      bool
}

type routingQuotaRecord struct {
	fingerprint [32]byte
	observedAt  time.Time
	attemptedAt time.Time
	windows     []RoutingWindow
	errorCode   string
}

// SoonestResetSelector ranks new sessions after the manager's normal eligibility checks.
// It deliberately uses the custom-selector path, preserving upstream alias resolution.
type SoonestResetSelector struct {
	mu       sync.RWMutex
	records  map[string]routingQuotaRecord
	inFlight map[string]bool
}

func soonestSelector(selector Selector) *SoonestResetSelector {
	if affinity, ok := selector.(*SessionAffinitySelector); ok {
		selector = affinity.fallback
	}
	s, _ := selector.(*SoonestResetSelector)
	return s
}

func quotaFingerprint(a *Auth) [32]byte {
	// Token replacement or re-registration invalidates observations without retaining secrets.
	token, _ := a.Metadata["access_token"].(string)
	account, _ := a.Metadata["account_id"].(string)
	return sha256.Sum256([]byte(fmt.Sprintf("%d:%d:%s:%s:%s", a.RegistrationEpoch, a.CredentialVersion, a.Provider, account, token)))
}

func (s *SoonestResetSelector) record(a *Auth) routingQuotaRecord {
	s.mu.RLock()
	r := s.records[a.ID]
	s.mu.RUnlock()
	if r.fingerprint != quotaFingerprint(a) {
		return routingQuotaRecord{}
	}
	return r
}

// routingQuotaSupported reports whether the refresh worker can observe the credential's quota.
func routingQuotaSupported(a *Auth) bool {
	if a.AuthKind() != AuthKindOAuth {
		return false
	}
	switch a.Provider {
	case "codex", "claude", "antigravity":
		return true
	}
	return false
}

func applicableWindow(w RoutingWindow, model string) bool {
	if w.model == "" {
		return true
	}
	if strings.HasPrefix(w.model, antigravityPoolPrefix) {
		return antigravityPoolApplies(w.model, model)
	}
	model = strings.ToLower(model)
	if w.model == "opus" || w.model == "sonnet" {
		return strings.Contains(model, w.model)
	}
	if w.model == "fable" {
		return isClaudeFableModel(model)
	}
	return normalizeQuotaModel(model) == w.model
}

func isClaudeFableModel(model string) bool {
	return strings.HasPrefix(strings.ToLower(canonicalModelKey(model)), "claude-fable-")
}

func normalizeQuotaModel(model string) string {
	var b strings.Builder
	for _, c := range strings.ToLower(canonicalModelKey(model)) {
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// RoutingPool is a provider quota pool shared by a family of models.
type RoutingPool struct {
	Key   string `json:"key"`
	Label string `json:"label"`
}

// RoutingPools lists the pools a provider meters per model family, in display order.
// Providers whose quota applies to every model have none.
func RoutingPools(provider string) []RoutingPool {
	if provider != "antigravity" {
		return nil
	}
	pools := make([]RoutingPool, 0, len(antigravityRoutingPools))
	for _, p := range antigravityRoutingPools {
		pools = append(pools, p.RoutingPool)
	}
	return pools
}

// routingPoolWindow maps a public pool key to the window pool it selects.
func routingPoolWindow(provider, pool string) (string, bool) {
	if provider == "antigravity" {
		for _, p := range antigravityRoutingPools {
			if p.Key == pool {
				return p.window, true
			}
		}
	}
	return "", false
}

func (s *SoonestResetSelector) view(a *Auth, model string, now time.Time) (routingQuotaRecord, *RoutingWindow, string, time.Time) {
	return s.viewScoped(a, model, "", now)
}

// viewScoped reads one window pool instead of the windows that apply to model when pool is set.
func (s *SoonestResetSelector) viewScoped(a *Auth, model, pool string, now time.Time) (routingQuotaRecord, *RoutingWindow, string, time.Time) {
	if !routingQuotaSupported(a) {
		return routingQuotaRecord{}, nil, "unsupported", time.Time{}
	}
	r := s.record(a)
	if r.observedAt.IsZero() {
		state := "missing"
		if r.errorCode == "malformed" {
			state = "malformed"
		}
		return r, nil, state, time.Time{}
	}
	if now.Before(r.observedAt) || now.Sub(r.observedAt) > RoutingQuotaMaxAge {
		return r, nil, "stale", time.Time{}
	}
	var selected *RoutingWindow
	var blockedUntil time.Time
	expired := false
	// Codex named pools and Antigravity Gemini sub-pools are independent of the default request pool.
	namedPool := false
	for _, w := range r.windows {
		if pool == "" && isNamedQuotaPool(a.Provider, w) && applicableWindow(w, model) {
			namedPool = true
			break
		}
	}
	for _, window := range r.windows {
		if namedPool && isDefaultQuotaPool(a.Provider, window) {
			continue
		}
		if (pool != "" && window.model != pool) || (pool == "" && !applicableWindow(window, model)) {
			continue
		}
		if !window.ResetsAt.After(now) {
			expired = true
			continue
		}
		w := window
		if (w.UsedPercent >= 100 || w.denied) && w.ResetsAt.After(blockedUntil) {
			blockedUntil = w.ResetsAt
		}
		if selected == nil || w.seconds > selected.seconds || (w.seconds == selected.seconds && (w.ResetsAt.Before(selected.ResetsAt) || (w.ResetsAt.Equal(selected.ResetsAt) && w.Key < selected.Key))) {
			selected = &w
		}
	}
	// An elapsed window is unknown, not renewed quota. Refresh must establish its next cycle.
	if expired {
		return r, nil, "expired", blockedUntil
	}
	if selected == nil {
		return r, nil, "missing", blockedUntil
	}
	return r, selected, "fresh", blockedUntil
}

func isNamedQuotaPool(provider string, w RoutingWindow) bool {
	switch provider {
	case "codex":
		return w.model != ""
	case "antigravity":
		return strings.HasPrefix(w.model, antigravityGeminiPool+"-")
	}
	return false
}

func isDefaultQuotaPool(provider string, w RoutingWindow) bool {
	switch provider {
	case "codex":
		return w.model == ""
	case "antigravity":
		return w.model == antigravityGeminiPool
	}
	return false
}

func (s *SoonestResetSelector) viewForRequest(ctx context.Context, a *Auth, model string, now time.Time) (routingQuotaRecord, *RoutingWindow, string, time.Time) {
	if IsCodexUseSIWCForRequest(ctx, a) {
		return routingQuotaRecord{}, nil, "unsupported", time.Time{}
	}
	return s.view(a, model, now)
}

func (s *SoonestResetSelector) blockedUntil(a *Auth, model string, now time.Time) time.Time {
	_, _, _, until := s.view(a, model, now)
	return until
}

type routingQuotaModelsKey struct{}

func routingQuotaModel(ctx context.Context, a *Auth, fallback string) string {
	if ctx != nil {
		if models, ok := ctx.Value(routingQuotaModelsKey{}).(map[string]string); ok {
			if model, found := models[a.ID]; found {
				return model
			}
		}
	}
	return fallback
}

// soonestResetAffinityCandidates drops the accounts the soonest-reset policy
// holds back, before session affinity picks among them. It returns the input
// unchanged when the fallback selector isn't soonest-reset-first.
func soonestResetAffinityCandidates(ctx context.Context, fallback Selector, auths []*Auth, model string, headers http.Header) (context.Context, []*Auth) {
	quota := soonestSelector(fallback)
	if quota == nil {
		return ctx, auths
	}
	ctx = WithCodexClientRequest(ctx, IsCodexClientRequest(ctx, headers))
	usable := make([]*Auth, 0, len(auths))
	for _, a := range auths {
		if a != nil && (IsCodexUseSIWCForRequest(ctx, a, headers) || quota.blockedUntil(a, routingQuotaModel(ctx, a, model), time.Now()).IsZero()) {
			usable = append(usable, a)
		}
	}
	return ctx, usable
}

// routingQuotaBlock adds the soonest-reset policy's hold to an account that
// isn't otherwise blocked. SIWC requests are never held back.
func (m *Manager) routingQuotaBlock(ctx context.Context, candidate *Auth, model string, now time.Time, blocked bool, reason blockReason, next time.Time, clientHeaders ...http.Header) (bool, blockReason, time.Time) {
	if quota := m.routingQuota.Load(); quota != nil && !blocked && !IsCodexUseSIWCForRequest(ctx, candidate, clientHeaders...) {
		if until := quota.blockedUntil(candidate, model, now); !until.IsZero() {
			return true, blockReasonCooldown, until
		}
	}
	return blocked, reason, next
}

func (m *Manager) routingQuotaContext(ctx context.Context, selector Selector, auths []*Auth, model string) context.Context {
	if soonestSelector(selector) == nil {
		return ctx
	}
	models := make(map[string]string, len(auths))
	for _, a := range auths {
		models[a.ID] = m.selectionModelForAuth(a, model)
	}
	return context.WithValue(ctx, routingQuotaModelsKey{}, models)
}

func (s *SoonestResetSelector) rank(auths []*Auth, model string, now time.Time) []*Auth {
	return s.rankForRequest(context.Background(), auths, model, now)
}

func (s *SoonestResetSelector) rankForRequest(ctx context.Context, auths []*Auth, model string, now time.Time) []*Auth {
	ranked := append([]*Auth(nil), auths...)
	windows := make(map[string]*RoutingWindow, len(auths))
	for _, a := range auths {
		_, windows[a.ID], _, _ = s.viewForRequest(ctx, a, routingQuotaModel(ctx, a, model), now)
	}
	sort.SliceStable(ranked, func(i, j int) bool {
		a, b := ranked[i], ranked[j]
		if authPriority(a) != authPriority(b) {
			return authPriority(a) > authPriority(b)
		}
		wa, wb := windows[a.ID], windows[b.ID]
		if (wa != nil) != (wb != nil) {
			return wa != nil
		}
		if wa != nil && !wa.ResetsAt.Equal(wb.ResetsAt) {
			return wa.ResetsAt.Before(wb.ResetsAt)
		}
		return a.ID < b.ID
	})
	return ranked
}

func (s *SoonestResetSelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	ctx = WithCodexClientRequest(ctx, IsCodexClientRequest(ctx, opts.Headers))
	now := time.Now()
	usable := make([]*Auth, 0, len(auths))
	var earliest time.Time
	for _, a := range auths {
		if a == nil {
			continue
		}
		_, _, _, until := s.viewForRequest(ctx, a, routingQuotaModel(ctx, a, model), now)
		if until.IsZero() {
			usable = append(usable, a)
		} else if earliest.IsZero() || until.Before(earliest) {
			earliest = until
		}
	}
	if len(usable) == 0 && !earliest.IsZero() {
		return nil, newModelCooldownError(model, provider, earliest.Sub(now))
	}
	available, err := getSelectorAvailableAuths(ctx, usable, provider, model, now)
	if err != nil {
		return nil, err
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	return s.rankForRequest(ctx, available, model, now)[0], nil
}
