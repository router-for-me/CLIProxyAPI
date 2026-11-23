package auth

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

// CredentialCooldownUntil reports when a credential that is currently benched
// becomes eligible again. ok is false when the credential is not benched or
// the recovery time is unknown.
func CredentialCooldownUntil(auth *Auth, now time.Time) (time.Time, bool) {
	blocked, _, next := isAuthBlockedForModel(auth, "", now)
	if !blocked || next.IsZero() {
		return time.Time{}, false
	}
	return next, true
}

// PreviewNextPick returns the credential ID the given strategy would choose
// for a new conversation among auths, without touching selector state.
// Rotating strategies have no deterministic answer and return "".
func PreviewNextPick(strategy string, auths []*Auth, now time.Time) string {
	available, err := getAvailableAuths(auths, "", "", now)
	if err != nil || len(available) == 0 {
		return ""
	}
	switch strategy {
	case SoonestResetStrategy:
		return soonestResetAuth(available, now).ID
	case "fill-first":
		return available[0].ID
	default:
		return ""
	}
}

// ObserveQuotaLookup records an out-of-band usage lookup, in response-header
// shape, for one credential. Older lookups never replace newer ones. Lookup
// subscribers are notified after a lookup is recorded.
func (m *Manager) ObserveQuotaLookup(authID string, headers http.Header, lookedUpAt time.Time) bool {
	if m == nil || strings.TrimSpace(authID) == "" || len(headers) == 0 {
		return false
	}
	if lookedUpAt.IsZero() {
		lookedUpAt = time.Now()
	}
	var recorded bool
	var drift string
	_, _, err := m.resetQuota(context.Background(), authID, lookedUpAt, func(auth *Auth) (bool, error) {
		if lookedUpAt.Before(auth.Quota.LookedUpAt) ||
			!auth.Quota.ObserveLookupForProvider(auth.Provider, headers, lookedUpAt) {
			return false, nil
		}
		recorded = true
		drift = lookupDriftLine(auth, auth.Quota.LookupSignals, lookedUpAt)
		return true, nil
	})
	if !recorded {
		return false
	}
	if err != nil {
		log.Warnf("failed to persist quota lookup recovery: %v", err)
	}
	if drift != "" {
		log.Info(drift)
	}
	m.NotifyQuotaUpdated()
	return true
}

// ErrQuotaLookupCredentialChanged means the credential is no longer current.
var ErrQuotaLookupCredentialChanged = errors.New("quota lookup credential is no longer current")

// ObserveQuotaLookupWindows records local usage windows and may lift a quota cooldown.
// A nil accept records observations at or after the stored time.
// accept runs under the manager write lock and must neither mutate auth nor call manager methods.
// An accepted older observation leaves the stored timestamp and cooldown intact.
func (m *Manager) ObserveQuotaLookupWindows(ctx context.Context, authID string, windows []QuotaWindow,
	lookedUpAt time.Time, accept func(*Auth) (bool, error)) (recorded bool, err error) {
	if m == nil {
		return false, nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if strings.TrimSpace(authID) == "" || len(windows) == 0 || lookedUpAt.IsZero() {
		return false, errors.New("quota lookup requires a credential ID, windows, and timestamp")
	}
	_, _, err = m.resetQuota(ctx, authID, lookedUpAt, func(auth *Auth) (bool, error) {
		if accept != nil {
			if ok, errAccept := accept(auth); !ok || errAccept != nil {
				return false, errAccept
			}
		} else if lookedUpAt.Before(auth.Quota.LookedUpAt) {
			return false, nil
		}
		auth.Quota.LookupWindows = cloneQuotaWindows(windows)
		auth.Quota.LookupSignals = nil
		if lookedUpAt.After(auth.Quota.LookedUpAt) {
			auth.Quota.LookedUpAt = lookedUpAt
		}
		recorded = true
		return true, nil
	})
	if recorded {
		m.NotifyQuotaUpdated()
	}
	return recorded, err
}

// SubscribeQuotaUpdates calls fn after a lookup or live response records quota.
// The callback runs outside manager locks and must not block.
func (m *Manager) SubscribeQuotaUpdates(fn func()) (unsubscribe func()) {
	return m.quotaListeners.add(fn)
}

// NotifyQuotaUpdated calls quota subscribers after a snapshot has been committed.
// Call it outside manager locks.
func (m *Manager) NotifyQuotaUpdated() {
	m.quotaListeners.notify()
}

type quotaUpdateListeners struct {
	mu     sync.Mutex
	nextID int
	byID   map[int]func()
}

func (l *quotaUpdateListeners) add(fn func()) func() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.byID == nil {
		l.byID = map[int]func(){}
	}
	id := l.nextID
	l.nextID++
	l.byID[id] = fn
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		delete(l.byID, id)
	}
}

func (l *quotaUpdateListeners) notify() {
	l.mu.Lock()
	fns := make([]func(), 0, len(l.byID))
	for _, fn := range l.byID {
		fns = append(fns, fn)
	}
	l.mu.Unlock()
	for _, fn := range fns {
		fn()
	}
}
