package cliproxy

import (
	"context"
	"errors"
	"net/http"
	"reflect"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const copilotCatalogRetryDelay = 5 * time.Minute

var errCopilotCatalogSuperseded = errors.New("copilot: catalog refresh superseded")

// Discovery has its own bounded queue and worker. A stalled directory request
// cannot consume the shared credential refresh pool or delay lifecycle hooks.
type copilotCatalogRefresher struct {
	coreauth.NoopHook
	service    *Service
	cancel     context.CancelFunc
	done       chan struct{}
	wake       chan struct{}
	jobs       chan *copilotCatalogJob
	results    chan *copilotCatalogJob
	removeHook func()
	// Only the scheduler goroutine accesses these maps.
	states   map[string]*copilotCatalogState
	inFlight map[string]*copilotCatalogJob
}

type copilotCatalogIdentity struct {
	epoch, credentialVersion  uint64
	base, token, grant, proxy string
}

func copilotCatalogIdentityFor(auth *coreauth.Auth) copilotCatalogIdentity {
	base, _ := auth.Metadata["base_url"].(string)
	token, _ := auth.Metadata["access_token"].(string)
	grant, _ := auth.Metadata["refresh_token"].(string)
	return copilotCatalogIdentity{auth.RegistrationEpoch, auth.CredentialVersion, base, token, grant, auth.ProxyURL}
}

type copilotCatalogState struct {
	identity copilotCatalogIdentity
	models   []copilot.Model
	disabled bool
	refresh  bool
	retryAt  time.Time
}

type copilotCatalogJob struct {
	ctx      context.Context
	cancel   context.CancelFunc
	auth     *coreauth.Auth
	identity copilotCatalogIdentity
}

func (s *Service) startCopilotCatalogRefresh(ctx context.Context) *copilotCatalogRefresher {
	if s == nil || s.coreManager == nil {
		return nil
	}
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	if s.copilotCatalogs != nil {
		return s.copilotCatalogs
	}
	ctx, cancel := context.WithCancel(ctx)
	r := &copilotCatalogRefresher{
		service: s, cancel: cancel, done: make(chan struct{}), wake: make(chan struct{}, 1),
		jobs: make(chan *copilotCatalogJob, 1), results: make(chan *copilotCatalogJob, 1),
		states: make(map[string]*copilotCatalogState), inFlight: make(map[string]*copilotCatalogJob),
	}
	r.removeHook = s.coreManager.AddHook(r)
	s.copilotCatalogs = r
	go r.run(ctx)
	return r
}

func (r *copilotCatalogRefresher) OnAuthRegistered(ctx context.Context, auth *coreauth.Auth) {
	r.OnAuthUpdated(ctx, auth)
}

func (r *copilotCatalogRefresher) OnAuthUpdated(_ context.Context, auth *coreauth.Auth) {
	if auth == nil || auth.Provider != copilot.Provider {
		return
	}
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *copilotCatalogRefresher) run(ctx context.Context) {
	workerDone := make(chan struct{})
	go func() {
		defer close(workerDone)
		for {
			select {
			case <-ctx.Done():
				return
			case job := <-r.jobs:
				_ = r.fetch(job)
				job.cancel()
				select {
				case r.results <- job:
				case <-ctx.Done():
					return
				}
			}
		}
	}()
	defer func() {
		r.removeHook()
		r.cancel()
		<-workerDone
		close(r.done)
	}()
	// The independent clock refreshes long-lived credentials' catalogs and
	// revokes expired models even while the discovery worker is blocked.
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	r.reconcile(ctx, time.Now())
	for {
		select {
		case <-ctx.Done():
			return
		case <-r.wake:
		case <-ticker.C:
		case job := <-r.results:
			r.finish(job, time.Now())
		}
		r.reconcile(ctx, time.Now())
	}
}

func (r *copilotCatalogRefresher) finish(job *copilotCatalogJob, now time.Time) {
	if r.inFlight[job.auth.ID] != job {
		return
	}
	delete(r.inFlight, job.auth.ID)
	if state := r.states[job.auth.ID]; state != nil && state.identity == job.identity {
		state.retryAt = now.Add(copilotCatalogRetryDelay)
	}
}

func (r *copilotCatalogRefresher) reconcile(ctx context.Context, now time.Time) {
	seen := make(map[string]bool)
	for _, auth := range r.service.coreManager.List() {
		if ctx.Err() != nil {
			return
		}
		if auth.Provider != copilot.Provider || pluginHostHasAuthModelProvider(r.service.pluginHost, auth.Provider) {
			continue
		}
		seen[auth.ID] = true
		identity := copilotCatalogIdentityFor(auth)
		disabled := auth.Disabled || auth.Status == coreauth.StatusDisabled
		raw, _ := auth.Metadata[copilot.CatalogKey].(string)
		catalog := copilot.ReadCatalog(raw, now)
		state := r.states[auth.ID]
		publish := state == nil || state.identity.epoch != identity.epoch || state.disabled != disabled || !reflect.DeepEqual(state.models, catalog.Models)
		if state == nil {
			state = &copilotCatalogState{identity: identity}
			r.states[auth.ID] = state
		} else if state.identity != identity {
			state.identity, state.refresh, state.retryAt = identity, true, time.Time{}
		}
		state.models, state.disabled = catalog.Models, disabled
		if publish {
			r.service.refreshModelRegistrationForAuthWithContext(ctx, auth, nil)
		}
		if job := r.inFlight[auth.ID]; job != nil {
			if disabled || identity != job.identity {
				job.cancel()
			}
			continue
		}
		if disabled || !auth.HasValidAccessToken(now) || !copilot.TrustedAPI(identity.base) || now.Before(state.retryAt) {
			continue
		}
		if !state.refresh && !catalog.FetchedAt.IsZero() && now.Before(catalog.FetchedAt.Add(copilot.CatalogMaxAge/2)) {
			continue
		}
		jobCtx, cancel := context.WithCancel(ctx)
		job := &copilotCatalogJob{ctx: jobCtx, cancel: cancel, auth: auth, identity: identity}
		select {
		case r.jobs <- job:
			r.inFlight[auth.ID] = job
			state.refresh = false
		default:
			cancel()
		}
	}
	for id := range r.states {
		if !seen[id] {
			delete(r.states, id)
			if job := r.inFlight[id]; job != nil {
				job.cancel()
			}
		}
	}
}

func (r *copilotCatalogRefresher) fetch(job *copilotCatalogJob) error {
	if err := job.ctx.Err(); err != nil {
		return err
	}
	client := helps.NewProxyAwareHTTPClient(job.ctx, r.service.currentConfig(), job.auth, 0)
	raw, errFetch := copilot.FetchCatalog(job.ctx, client, job.identity.base, job.identity.token)
	var status copilot.HTTPError
	if errFetch != nil && !(errors.As(errFetch, &status) && (status.Code == http.StatusUnauthorized || status.Code == http.StatusForbidden)) {
		// Retain the previous snapshot only until its original hard TTL. Failed
		// discovery must not move that clock or roll back refreshed credentials.
		return errFetch
	}
	previous, _ := job.auth.Metadata[copilot.CatalogKey].(string)
	_, _, errCommit := r.service.coreManager.Modify(job.ctx, job.auth.ID, job.identity.epoch, func(current *coreauth.Auth) error {
		currentCatalog, _ := current.Metadata[copilot.CatalogKey].(string)
		if current.Provider != copilot.Provider || current.Disabled || current.Status == coreauth.StatusDisabled || copilotCatalogIdentityFor(current) != job.identity || currentCatalog != previous {
			return errCopilotCatalogSuperseded
		}
		if errFetch != nil {
			if currentCatalog == "" {
				return errCopilotCatalogSuperseded
			}
			delete(current.Metadata, copilot.CatalogKey)
		} else {
			current.Metadata[copilot.CatalogKey] = raw
		}
		return nil
	})
	return errCommit
}
