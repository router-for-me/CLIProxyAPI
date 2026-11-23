package auth

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	log "github.com/sirupsen/logrus"
)

type executionAuthRefreshHandler func(ctx context.Context, auth *Auth, refresh func(context.Context, *Auth) (*Auth, error)) (*Auth, error)

type executionAuthRefreshContextKey struct{}

type executionAuthRefreshOptions struct {
	holdRefreshLock bool
	ephemeral       bool
	expectedEpoch   uint64
	expectedVersion *uint64
	onAuthUpdated   func(*Auth)
}

// RefreshExecutionAuth runs an executor's in-flight credential refresh through
// the Manager refresh coordinator attached to ctx, if present.
//
// When managed by Manager:
//   - Refresh calls serialize under the per-credential refresh lock (unless the
//     caller already holds it during PrepareRequestAuth) and re-read the latest
//     registered credential snapshot before invoking refresh.
//   - If refresh returns (nil, nil), no refresh was needed (for example because
//     another request already refreshed the credential); the coordinator syncs
//     the caller's active auth state to the latest snapshot and returns it.
//   - If refresh returns a refreshed auth with err == nil, the coordinator
//     commits and persists it using context.WithoutCancel so client
//     cancellation never discards a rotated refresh token while preserving
//     RegistrationEpoch and CredentialVersion checks, and notifies the active
//     execution attempt so subsequent MarkResult / metadata records reflect the
//     refreshed snapshot.
//
// When no coordinator is attached to ctx (for example in direct unit tests or
// unmanaged calls), RefreshExecutionAuth invokes refresh(ctx, auth) directly.
func RefreshExecutionAuth(ctx context.Context, auth *Auth, refresh func(context.Context, *Auth) (*Auth, error)) (*Auth, error) {
	if auth == nil || refresh == nil {
		return nil, nil
	}
	if ctx != nil {
		if handler, ok := ctx.Value(executionAuthRefreshContextKey{}).(executionAuthRefreshHandler); ok && handler != nil {
			return handler(ctx, auth, refresh)
		}
	}
	return refresh(ctx, auth)
}

func (m *Manager) withExecutionAuthRefresh(ctx context.Context, opts executionAuthRefreshOptions) context.Context {
	if m == nil {
		return ctx
	}
	if ctx == nil {
		ctx = context.Background()
	}
	handler := func(callCtx context.Context, auth *Auth, refresh func(context.Context, *Auth) (*Auth, error)) (*Auth, error) {
		if auth == nil || refresh == nil {
			return nil, nil
		}
		if callCtx == nil {
			callCtx = context.Background()
		}
		if errCtx := callCtx.Err(); errCtx != nil {
			return nil, errCtx
		}

		id := strings.TrimSpace(auth.ID)
		if opts.ephemeral || id == "" || (m.HomeEnabled() && auth.RegistrationEpoch == 0) {
			updated, errRefresh := refresh(callCtx, auth)
			if errRefresh != nil {
				return nil, errRefresh
			}
			if updated != nil && opts.onAuthUpdated != nil {
				opts.onAuthUpdated(updated.Clone())
			}
			if errCtx := callCtx.Err(); errCtx != nil {
				return nil, errCtx
			}
			return updated, nil
		}

		if !opts.holdRefreshLock {
			releaseRefresh, errLock := m.lockRefreshContext(callCtx, id)
			if errLock != nil {
				return nil, errLock
			}
			defer releaseRefresh()
		}

		m.mu.RLock()
		current := m.auths[id]
		var latest *Auth
		if current != nil {
			latest = current.Clone()
		}
		m.mu.RUnlock()

		strictVersion := opts.expectedVersion != nil && *opts.expectedVersion != 0
		if latest == nil {
			if strictVersion {
				return nil, errVerifyCredentialRemoved
			}
			if !m.HomeEnabled() && auth.RegistrationEpoch != 0 {
				return nil, fmt.Errorf("refresh %s auth: credential no longer registered", prepareProviderLabel(auth.Provider))
			}
			updated, errRefresh := refresh(callCtx, auth)
			if errRefresh != nil {
				return nil, errRefresh
			}
			if updated != nil && opts.onAuthUpdated != nil {
				opts.onAuthUpdated(updated.Clone())
			}
			if errCtx := callCtx.Err(); errCtx != nil {
				return nil, errCtx
			}
			return updated, nil
		}

		if auth.RegistrationEpoch != 0 && latest.RegistrationEpoch != auth.RegistrationEpoch {
			if strictVersion {
				return nil, errVerifyCredentialSuperseded
			}
			return nil, fmt.Errorf("refresh %s auth: stale registration epoch %d != %d", prepareProviderLabel(auth.Provider), auth.RegistrationEpoch, latest.RegistrationEpoch)
		}
		if opts.expectedEpoch != 0 && latest.RegistrationEpoch != opts.expectedEpoch {
			return nil, errVerifyCredentialSuperseded
		}
		if strictVersion && latest.CredentialVersion != *opts.expectedVersion {
			return nil, errVerifyCredentialSuperseded
		}

		base := latest.Clone()
		updated, errRefresh := refresh(callCtx, base.Clone())
		if errRefresh != nil {
			return nil, errRefresh
		}
		if updated == nil {
			if opts.onAuthUpdated != nil {
				opts.onAuthUpdated(latest.Clone())
			}
			if errCtx := callCtx.Err(); errCtx != nil {
				return nil, errCtx
			}
			return latest, nil
		}

		now := time.Now()
		if updated.Runtime == nil {
			updated.Runtime = base.Runtime
		}
		updated.LastRefreshedAt = now
		updated.NextRefreshAfter = time.Time{}
		updated.UpdatedAt = now
		applyPreservedRenewalState(base, updated, updateModeRefresh, now)
		if authHasUnchangedRejectedRefreshToken(updated) {
			if exp, okExp := updated.AccessTokenExpirationTime(); okExp && !exp.IsZero() && exp.After(now) {
				updated.NextRefreshAfter = exp
			}
		}
		applyPreservedRejectedTokens(base, updated, true)
		canServeWithAccessToken := !updated.RequiresSignIn || updated.HasValidAccessToken(now)
		if canServeWithAccessToken && (updated.RejectedAccessToken == "" || authAccessToken(updated) != updated.RejectedAccessToken) {
			updated.LastError = nil
			updated.StatusMessage = ""
			updated.Unavailable = false
			updated.RefreshFailures = 0
			if updated.Status == StatusError || updated.Status == "" {
				updated.Status = StatusActive
			}
			_ = clearUnauthorizedModelStates(updated, now)
		}
		if m.shouldRefresh(updated, now) {
			updated.NextRefreshAfter = now.Add(refreshIneffectiveBackoff)
		}

		// A successful refresh may have rotated upstream tokens after caller cancellation.
		// Commit with context values intact while preserving epoch and version checks.
		saved, committed, baseSuperseded, errUpdate := m.updateRefreshedAuthWithStatus(context.WithoutCancel(callCtx), base, updated)
		if errUpdate != nil {
			log.WithFields(log.Fields{"auth_id": base.ID, "credential": base.ID, "provider": base.Provider}).Warnf("persist refreshed auth %s (%s) failed: %v", base.Provider, base.ID, errUpdate)
			if strictVersion && strings.Contains(errUpdate.Error(), "stale registration epoch") {
				return nil, errVerifyCredentialSuperseded
			}
			return nil, errUpdate
		}
		if saved == nil {
			if strictVersion {
				return nil, errVerifyCredentialRemoved
			}
			return nil, fmt.Errorf("refresh %s auth: credential removed during refresh", prepareProviderLabel(base.Provider))
		}
		if strictVersion {
			if !committed || baseSuperseded {
				return nil, errVerifyCredentialSuperseded
			}
			*opts.expectedVersion = saved.CredentialVersion
		}
		if committed {
			supportedModels, regEpoch := registry.GetGlobalRegistry().GetModelsAndEpochForClient(id)
			projections := make([]registry.ClientModelProjection, 0, len(supportedModels))
			for _, sm := range supportedModels {
				if sm == nil || strings.TrimSpace(sm.ID) == "" {
					continue
				}
				projections = append(projections, m.clientModelProjectionForAuth(saved, sm.ID, now))
			}
			if len(projections) > 0 {
				registry.GetGlobalRegistry().ApplyClientModelProjections(id, regEpoch, saved.Generation, projections)
			}
		}
		if opts.onAuthUpdated != nil {
			opts.onAuthUpdated(saved.Clone())
		}
		if errCtx := callCtx.Err(); errCtx != nil {
			return nil, errCtx
		}
		return saved.Clone(), nil
	}
	return context.WithValue(ctx, executionAuthRefreshContextKey{}, executionAuthRefreshHandler(handler))
}

func preparedAuthMatchesBase(base, updated *Auth) bool {
	if base == nil || updated == nil {
		return false
	}
	return base.ProxyURL == updated.ProxyURL &&
		reflect.DeepEqual(base.Metadata, updated.Metadata) &&
		reflect.DeepEqual(base.Attributes, updated.Attributes)
}
