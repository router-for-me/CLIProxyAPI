package auth

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	homekv "github.com/router-for-me/CLIProxyAPI/v8/internal/home"
)

type antigravityUseCreditsContextKey struct{}

// WithAntigravityCredits returns a child context that signals the executor to
// inject enabledCreditTypes into the request payload.
func WithAntigravityCredits(ctx context.Context) context.Context {
	return context.WithValue(ctx, antigravityUseCreditsContextKey{}, true)
}

// AntigravityCreditsRequested reports whether the context carries the credits flag.
func AntigravityCreditsRequested(ctx context.Context) bool {
	if ctx == nil {
		return false
	}
	v, _ := ctx.Value(antigravityUseCreditsContextKey{}).(bool)
	return v
}

// AntigravityCreditsHintRefreshInterval defines how long a cached AI credits hint
// remains fresh before it should be re-validated.
const AntigravityCreditsHintRefreshInterval = 10 * time.Minute

// AntigravityCreditsHint stores the latest known AI credits state for one auth.
type AntigravityCreditsHint struct {
	Known           bool
	Available       bool
	CreditAmount    float64
	MinCreditAmount float64
	PaidTierID      string
	UpdatedAt       time.Time
}

// IsAntigravityCreditsHintFresh reports whether a known credits hint is still
// within AntigravityCreditsHintRefreshInterval of now.
func IsAntigravityCreditsHintFresh(hint AntigravityCreditsHint, now time.Time) bool {
	return hint.Known && !hint.UpdatedAt.IsZero() && now.Sub(hint.UpdatedAt) < AntigravityCreditsHintRefreshInterval
}

type antigravityCreditsHintCacheScope struct {
	authID string
	scope  string
}

type antigravityCreditsHintScopeState struct {
	mu                sync.Mutex
	registrationEpoch uint64
	createdAt         time.Time
	updatedAt         time.Time
	scope             string
}

var (
	antigravityCreditsHintByAuth       sync.Map
	antigravityCreditsHintCurrentScope sync.Map
)

// SetAntigravityCreditsHint updates the latest known AI credits state for an auth registration.
func SetAntigravityCreditsHint(authID string, hint AntigravityCreditsHint, registrationEpoch ...uint64) {
	epoch := antigravityCreditsRegistrationEpoch(registrationEpoch)
	scope := antigravityCreditsRegistrationScope(epoch)
	unlock, current := lockAntigravityCreditsHintIDScope(authID, scope, epoch, time.Time{}, time.Time{})
	if !current {
		return
	}
	defer unlock()
	setAntigravityCreditsHint(authID, hint, scope)
}

// SetAntigravityCreditsHintForAuth updates the latest known AI credits state for an auth instance.
func SetAntigravityCreditsHintForAuth(auth *Auth, hint AntigravityCreditsHint) {
	_ = SetAntigravityCreditsHintForAuthBestEffort(auth, hint)
}

// SetAntigravityCreditsHintForAuthBestEffort updates the latest known AI credits state
// for an auth instance and reports whether the update was persisted.
func SetAntigravityCreditsHintForAuthBestEffort(auth *Auth, hint AntigravityCreditsHint) bool {
	unlock, current := lockAntigravityCreditsHintScope(auth)
	if !current {
		return false
	}
	defer unlock()
	return setAntigravityCreditsHint(auth.ID, hint, AntigravityCreditsScope(auth))
}

// ClearAntigravityCreditsUnavailableHintForAuth atomically clears a known
// unavailable credits hint under the auth's hint scope lock when clearFresh is
// true or the cached hint is no longer fresh.
func ClearAntigravityCreditsUnavailableHintForAuth(auth *Auth, clearFresh bool) {
	_ = ClearAntigravityCreditsUnavailableHintForAuthChecked(auth, clearFresh)
}

// ClearAntigravityCreditsUnavailableHintForAuthChecked atomically clears a known
// unavailable credits hint under the auth's hint scope lock when clearFresh is
// true or the cached hint is no longer fresh, reporting whether it was cleared.
func ClearAntigravityCreditsUnavailableHintForAuthChecked(auth *Auth, clearFresh bool) bool {
	unlock, current := lockAntigravityCreditsHintScope(auth)
	if !current {
		return false
	}
	defer unlock()
	scope := AntigravityCreditsScope(auth)
	hint, ok, err := getAntigravityCreditsHintRequired(context.Background(), auth.ID, scope)
	if err != nil || !ok || !hint.Known || hint.Available {
		return false
	}
	if clearFresh || !IsAntigravityCreditsHintFresh(hint, time.Now()) {
		return setAntigravityCreditsHint(auth.ID, AntigravityCreditsHint{}, scope)
	}
	return false
}

func setAntigravityCreditsHint(authID string, hint AntigravityCreditsHint, scope string) bool {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return false
	}
	if hint.UpdatedAt.IsZero() {
		hint.UpdatedAt = time.Now()
	}
	if _, homeMode, _ := homekv.CurrentKVClient(); homeMode {
		return homekv.KVSetJSONBestEffort(context.Background(), antigravityCreditsHintKey(authID, scope), hint, 30*time.Minute)
	}
	antigravityCreditsHintByAuth.Store(newAntigravityCreditsHintCacheScope(authID, scope), hint)
	return true
}

// GetAntigravityCreditsHint returns the latest known AI credits state for an auth registration.
func GetAntigravityCreditsHint(authID string, registrationEpoch ...uint64) (AntigravityCreditsHint, bool) {
	hint, ok, err := getAntigravityCreditsHintRequired(context.Background(), authID, antigravityCreditsRegistrationScope(antigravityCreditsRegistrationEpoch(registrationEpoch)))
	if err == nil {
		return hint, ok
	}
	return AntigravityCreditsHint{}, false
}

// GetAntigravityCreditsHintForAuth returns the latest known AI credits state for an auth instance.
func GetAntigravityCreditsHintForAuth(auth *Auth) (AntigravityCreditsHint, bool) {
	hint, ok, err := GetAntigravityCreditsHintForAuthRequired(context.Background(), auth)
	if err == nil {
		return hint, ok
	}
	return AntigravityCreditsHint{}, false
}

// GetAntigravityCreditsHintRequired returns the latest known AI credits state for request-time paths.
func GetAntigravityCreditsHintRequired(ctx context.Context, authID string, registrationEpoch ...uint64) (AntigravityCreditsHint, bool, error) {
	return getAntigravityCreditsHintRequired(ctx, authID, antigravityCreditsRegistrationScope(antigravityCreditsRegistrationEpoch(registrationEpoch)))
}

// GetAntigravityCreditsHintForAuthRequired returns the latest known state for request-time paths.
func GetAntigravityCreditsHintForAuthRequired(ctx context.Context, auth *Auth) (AntigravityCreditsHint, bool, error) {
	unlock, current := lockAntigravityCreditsHintScope(auth)
	if !current {
		return AntigravityCreditsHint{}, false, nil
	}
	defer unlock()
	return getAntigravityCreditsHintRequired(ctx, auth.ID, AntigravityCreditsScope(auth))
}

func getAntigravityCreditsHintRequired(ctx context.Context, authID, scope string) (AntigravityCreditsHint, bool, error) {
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return AntigravityCreditsHint{}, false, nil
	}
	key := newAntigravityCreditsHintCacheScope(authID, scope)
	var homeHint AntigravityCreditsHint
	homeMode, found, errGet := homekv.KVGetJSONRequired(ctx, antigravityCreditsHintKey(authID, scope), &homeHint)
	if homeMode {
		return homeHint, found, errGet
	}
	value, ok := antigravityCreditsHintByAuth.Load(key)
	if !ok {
		return AntigravityCreditsHint{}, false, nil
	}
	hint, ok := value.(AntigravityCreditsHint)
	if !ok {
		antigravityCreditsHintByAuth.Delete(key)
		return AntigravityCreditsHint{}, false, nil
	}
	return hint, true, nil
}

// HasKnownAntigravityCreditsHint reports whether credits state has been discovered for an auth registration.
func HasKnownAntigravityCreditsHint(authID string, registrationEpoch ...uint64) bool {
	hint, ok := GetAntigravityCreditsHint(authID, registrationEpoch...)
	return ok && hint.Known
}

func antigravityCreditsRegistrationEpoch(registrationEpoch []uint64) uint64 {
	if len(registrationEpoch) == 0 {
		return 0
	}
	return registrationEpoch[0]
}

func antigravityCreditsRegistrationScope(registrationEpoch uint64) string {
	if registrationEpoch == 0 {
		return ""
	}
	return "epoch:" + strconv.FormatUint(registrationEpoch, 10)
}

// AntigravityCreditsScope returns a stable scope for one credential registration.
func AntigravityCreditsScope(auth *Auth) string {
	if auth == nil {
		return ""
	}
	if auth.RegistrationEpoch != 0 {
		return antigravityCreditsRegistrationScope(auth.RegistrationEpoch)
	}
	if _, homeMode, _ := homekv.CurrentKVClient(); !homeMode {
		return ""
	}
	email := authMetadataString(auth, "email")
	accountID := authMetadataString(auth, "account_id")
	identity := AuthGrantFingerprint(auth)
	if identity == "" && email == "" && accountID == "" && auth.CreatedAt.IsZero() && auth.UpdatedAt.IsZero() {
		return ""
	}
	createdAt := auth.CreatedAt.UTC().Format(time.RFC3339Nano)
	return "credential:" + homekv.HashKeyPart(identity+"\x00"+email+"\x00"+accountID+"\x00"+createdAt)
}

func lockAntigravityCreditsHintScope(auth *Auth) (func(), bool) {
	if auth == nil {
		return func() {}, false
	}
	return lockAntigravityCreditsHintIDScope(auth.ID, AntigravityCreditsScope(auth), auth.RegistrationEpoch, auth.CreatedAt, auth.UpdatedAt)
}

func lockAntigravityCreditsHintIDScope(authID, scope string, registrationEpoch uint64, createdAt, updatedAt time.Time) (func(), bool) {
	noop := func() {}
	authID = strings.TrimSpace(authID)
	if authID == "" {
		return noop, false
	}
	value, _ := antigravityCreditsHintCurrentScope.LoadOrStore(authID, &antigravityCreditsHintScopeState{})
	state, okState := value.(*antigravityCreditsHintScopeState)
	if !okState || state == nil {
		antigravityCreditsHintCurrentScope.Delete(authID)
		return lockAntigravityCreditsHintIDScope(authID, scope, registrationEpoch, createdAt, updatedAt)
	}
	state.mu.Lock()
	if state.scope != "" && state.scope != scope {
		if state.registrationEpoch > registrationEpoch || (state.registrationEpoch == registrationEpoch && state.createdAt.After(createdAt)) || (state.registrationEpoch == registrationEpoch && state.createdAt.Equal(createdAt) && state.updatedAt.After(updatedAt)) {
			state.mu.Unlock()
			return noop, false
		}
	}
	if state.scope != scope {
		state.registrationEpoch = registrationEpoch
		state.createdAt = createdAt
		state.updatedAt = updatedAt
		state.scope = scope
		antigravityCreditsHintByAuth.Range(func(key, _ any) bool {
			if scoped, ok := key.(antigravityCreditsHintCacheScope); ok && scoped.authID == authID && scoped.scope != scope {
				antigravityCreditsHintByAuth.Delete(key)
			}
			if legacy, ok := key.(string); ok && legacy == authID {
				antigravityCreditsHintByAuth.Delete(key)
			}
			return true
		})
	}
	return state.mu.Unlock, true
}

func newAntigravityCreditsHintCacheScope(authID, scope string) any {
	authID = strings.TrimSpace(authID)
	if scope == "" {
		return authID
	}
	return antigravityCreditsHintCacheScope{authID: authID, scope: scope}
}

// AntigravityCreditsHintKVKey returns the Home KV key for an auth instance's credits hint.
func AntigravityCreditsHintKVKey(auth *Auth) string {
	if auth == nil || strings.TrimSpace(auth.ID) == "" {
		return ""
	}
	return antigravityCreditsHintKey(auth.ID, AntigravityCreditsScope(auth))
}

func antigravityCreditsHintKey(authID string, scope ...string) string {
	base := "cpa:antigravity:credits-hint:" + strings.TrimSpace(authID)
	if len(scope) == 0 || scope[0] == "" {
		return base
	}
	return base + ":" + strings.ReplaceAll(scope[0], ":", "-")
}

func antigravityCreditsAvailableForModel(auth *Auth, model string) bool {
	if auth == nil {
		return false
	}
	if !strings.EqualFold(strings.TrimSpace(auth.Provider), "antigravity") {
		return false
	}
	if !strings.Contains(strings.ToLower(strings.TrimSpace(model)), "claude") {
		return false
	}
	hint, ok := GetAntigravityCreditsHintForAuth(auth)
	if !ok || !hint.Known {
		return false
	}
	return hint.Available
}
