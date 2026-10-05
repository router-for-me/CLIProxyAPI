package cliproxy

import (
	"context"
	"strings"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

// antigravityModelRefreshRequest captures publication ownership at admission.
// A queued successor must not adopt a later epoch from an external publisher.
type antigravityModelRefreshRequest struct {
	ctx           context.Context
	auth          *coreauth.Auth
	provider      string
	registryEpoch uint64
}

// queueAntigravityModelRefresh coalesces registration and periodic probes into
// one worker per account. Repeated updates replace a single queued successor,
// rather than accumulating singleflight followers while an upstream is stalled.
func (s *Service) queueAntigravityModelRefresh(ctx context.Context, auth *coreauth.Auth, provider string) {
	if s == nil || auth == nil || auth.ID == "" || auth.Disabled || s.antigravityHomeEnabled() {
		return
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if ctx.Err() != nil || pluginHostHasAuthModelProvider(s.pluginHost, auth.Provider) {
		return
	}
	s.cfgMu.RLock()
	lifetime := s.antigravityContext
	s.cfgMu.RUnlock()
	if lifetime != nil && lifetime.Err() != nil {
		return
	}
	request := &antigravityModelRefreshRequest{
		ctx: ctx, auth: auth.Clone(), provider: provider,
		registryEpoch: GlobalModelRegistry().ClientRegistrationEpoch(auth.ID),
	}
	token, _ := request.auth.Metadata["access_token"].(string)
	probe := &antigravityAccountProbe{
		auth: request.auth, routeKey: s.antigravityCapabilityKey(request.auth), token: strings.TrimSpace(token),
	}
	s.antigravityProbeMu.Lock()
	// Late stale callers must not replace the latest queued auth snapshot.
	if ctx.Err() != nil || !s.antigravityAccountProbeCurrent(probe) {
		s.antigravityProbeMu.Unlock()
		return
	}
	if s.antigravityRefreshPending == nil {
		s.antigravityRefreshPending = make(map[string]*antigravityModelRefreshRequest)
	}
	queued, running := s.antigravityRefreshPending[auth.ID]
	if queued != nil && queued.registryEpoch > request.registryEpoch {
		// Concurrent registration callers can reach admission out of order.
		s.antigravityProbeMu.Unlock()
		return
	}
	s.antigravityRefreshPending[auth.ID] = request
	if !running {
		s.antigravityProbeWg.Add(1)
	}
	s.antigravityProbeMu.Unlock()
	if !running {
		go s.runAntigravityAccountRefresh(auth.ID)
	}
}

func (s *Service) runAntigravityAccountRefresh(authID string) {
	defer s.antigravityProbeWg.Done()
	defer s.signalAntigravityModelRefresh()
	for {
		s.antigravityProbeMu.Lock()
		request := s.antigravityRefreshPending[authID]
		if request == nil {
			delete(s.antigravityRefreshPending, authID)
			s.antigravityProbeMu.Unlock()
			return
		}
		s.antigravityRefreshPending[authID] = nil
		s.antigravityProbeMu.Unlock()
		s.executeAntigravityModelRefresh(request)
	}
}

func (s *Service) executeAntigravityModelRefresh(request *antigravityModelRefreshRequest) {
	if request.ctx.Err() != nil || pluginHostHasAuthModelProvider(s.pluginHost, request.auth.Provider) {
		return
	}
	probeCtx, cancel := context.WithCancel(request.ctx)
	defer cancel()
	s.cfgMu.RLock()
	lifetime := s.antigravityContext
	s.cfgMu.RUnlock()
	if lifetime != nil {
		stop := context.AfterFunc(lifetime, cancel)
		defer stop()
		if lifetime.Err() != nil {
			return
		}
	}
	s.probeAndRegisterAntigravityModels(probeCtx, request.auth, request.provider, request.auth.RegistrationEpoch, request.registryEpoch)
}

// antigravityAccountProbe owns both queued and connected work for one auth snapshot.
// Cancellation releases network slots without imposing an upstream timeout.
type antigravityAccountProbe struct {
	auth      *coreauth.Auth
	routeKey  string
	flightKey string
	token     string
	cancel    context.CancelFunc
}

func (s *Service) beginAntigravityAccountProbe(parent context.Context, auth *coreauth.Auth, routeKey, flightKey, token string) (context.Context, func(), bool) {
	ctx, cancel := context.WithCancel(parent)
	s.cfgMu.RLock()
	lifetime := s.antigravityContext
	s.cfgMu.RUnlock()
	var stop func() bool
	if lifetime != nil {
		stop = context.AfterFunc(lifetime, cancel)
		if lifetime.Err() != nil {
			cancel()
		}
	}
	var changes <-chan struct{}
	var unsubscribe func()
	if s.coreManager != nil {
		// Subscribe before validation, so a native refresh/removal cannot fall
		// between checking the auth and installing the invalidation observer.
		changes, unsubscribe = s.coreManager.WatchAuthChanges(auth.ID)
	}
	var watcherDone chan struct{}
	probe := &antigravityAccountProbe{auth: auth.Clone(), routeKey: routeKey, flightKey: flightKey, token: token, cancel: cancel}
	s.antigravityProbeMu.Lock()
	valid := ctx.Err() == nil && s.antigravityAccountProbeCurrent(probe)
	if valid {
		if s.antigravityAccountProbes == nil {
			s.antigravityAccountProbes = make(map[string]map[*antigravityAccountProbe]struct{})
		}
		if s.antigravityAccountProbes[auth.ID] == nil {
			s.antigravityAccountProbes[auth.ID] = make(map[*antigravityAccountProbe]struct{})
		}
		s.antigravityAccountProbes[auth.ID][probe] = struct{}{}
	}
	s.antigravityProbeMu.Unlock()
	if valid && changes != nil {
		watcherDone = make(chan struct{})
		go func() {
			defer close(watcherDone)
			for {
				select {
				case <-ctx.Done():
					return
				case _, open := <-changes:
					if !open {
						return
					}
					s.cancelStaleAntigravityProbes(auth.ID)
				}
			}
		}()
	}
	cleanup := func() {
		cancel()
		if unsubscribe != nil {
			unsubscribe()
		}
		if watcherDone != nil {
			<-watcherDone
		}
		if stop != nil {
			stop()
		}
		s.antigravityProbeMu.Lock()
		delete(s.antigravityAccountProbes[auth.ID], probe)
		if len(s.antigravityAccountProbes[auth.ID]) == 0 {
			delete(s.antigravityAccountProbes, auth.ID)
		}
		s.antigravityProbeMu.Unlock()
		s.signalAntigravityModelRefresh()
	}
	if !valid {
		cleanup()
		return ctx, func() {}, false
	}
	return ctx, cleanup, true
}

// Caller holds antigravityProbeMu, making tracking and invalidation indivisible.
func (s *Service) antigravityAccountProbeCurrent(probe *antigravityAccountProbe) bool {
	if s.antigravityHomeEnabled() {
		return false
	}
	current := probe.auth
	if s.coreManager != nil {
		var exists bool
		current, exists = s.coreManager.GetByID(probe.auth.ID)
		if !exists || current == nil {
			return false
		}
	}
	token, _ := current.Metadata["access_token"].(string)
	return !current.Disabled && current.Status != coreauth.StatusDisabled &&
		antigravityAuthModelSettingsEqual(current, probe.auth) &&
		s.antigravityCapabilityKey(current) == probe.routeKey && strings.TrimSpace(token) == probe.token
}

// Run after the manager/config mutation. New probes check current state while
// joining this same map, so deletion cannot slip between validation and tracking.
// Empty authID invalidates stale probes across all accounts after config changes.
func (s *Service) cancelStaleAntigravityProbes(authID string) {
	if s == nil {
		return
	}
	s.antigravityProbeMu.Lock()
	for id, probes := range s.antigravityAccountProbes {
		if authID != "" && id != authID {
			continue
		}
		for probe := range probes {
			if !s.antigravityAccountProbeCurrent(probe) {
				probe.cancel()
				// An updated snapshot with the same token may start immediately instead
				// of joining an obsolete leader that is still unwinding its HTTP request.
				antigravityCapabilityGroup.Forget(probe.flightKey)
				delete(probes, probe)
			}
		}
		if len(probes) == 0 {
			delete(s.antigravityAccountProbes, id)
		}
	}
	s.antigravityProbeMu.Unlock()
	s.signalAntigravityModelRefresh()
}

func (s *Service) antigravityModelRefreshWake() chan struct{} {
	s.antigravityProbeMu.Lock()
	defer s.antigravityProbeMu.Unlock()
	if s.antigravityRefreshWake == nil {
		s.antigravityRefreshWake = make(chan struct{}, 1)
	}
	return s.antigravityRefreshWake
}

func (s *Service) signalAntigravityModelRefresh() {
	if s == nil {
		return
	}
	select {
	case s.antigravityModelRefreshWake() <- struct{}{}:
	default:
	}
}
