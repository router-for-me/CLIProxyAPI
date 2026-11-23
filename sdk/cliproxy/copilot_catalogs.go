package cliproxy

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor/helps"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
)

const copilotCatalogRetryDelay = 5 * time.Minute
const copilotCatalogWorkers = 4

var errCopilotCatalogSuperseded = errors.New("copilot: catalog refresh superseded")

// Discovery has a separate worker pool. A stalled directory request
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
	pending    sync.Map // Auth IDs dirtied by lifecycle hooks.
	// Only the scheduler goroutine accesses the following state.
	waiting  []*copilotCatalogJob
	states   map[string]*copilotCatalogState
	inFlight map[string]*copilotCatalogJob
}

type copilotCatalogIdentity struct {
	epoch, credentialVersion           uint64
	account, base, token, grant, proxy string
}

func copilotCatalogIdentityFor(auth *coreauth.Auth) copilotCatalogIdentity {
	account, _ := auth.Metadata["account_id"].(string)
	base, _ := auth.Metadata["base_url"].(string)
	token, _ := auth.Metadata["access_token"].(string)
	grant, _ := auth.Metadata["refresh_token"].(string)
	return copilotCatalogIdentity{auth.RegistrationEpoch, auth.CredentialVersion, account, base, token, grant, auth.ProxyURL}
}

type copilotCatalogState struct {
	identity copilotCatalogIdentity
	catalog  *copilot.Catalog
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
	r.pending.Store(auth.ID, struct{}{})
	select {
	case r.wake <- struct{}{}:
	default:
	}
}

func (r *copilotCatalogRefresher) run(ctx context.Context) {
	var workers sync.WaitGroup
	for range copilotCatalogWorkers {
		workers.Go(func() {
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
		})
	}
	defer func() {
		r.removeHook()
		r.cancel()
		workers.Wait()
		close(r.done)
	}()
	// The independent clock refreshes long-lived credentials' catalogs and
	// revokes expired models even while the discovery worker is blocked.
	ticker := time.NewTicker(time.Minute)
	defer ticker.Stop()
	r.reconcile(ctx, time.Now())
	for {
		var jobs chan *copilotCatalogJob
		var next *copilotCatalogJob
		if len(r.waiting) != 0 {
			jobs, next = r.jobs, r.waiting[0]
		}
		select {
		case <-ctx.Done():
			return
		case jobs <- next:
			r.waiting[0] = nil
			r.waiting = r.waiting[1:]
		case <-r.wake:
			r.pending.Range(func(key, _ any) bool {
				r.pending.Delete(key)
				r.reconcileID(ctx, key.(string), time.Now())
				return ctx.Err() == nil
			})
		case now := <-ticker.C:
			r.reconcile(ctx, now)
		case job := <-r.results:
			now := time.Now()
			r.finish(job, now)
			r.reconcileID(ctx, job.auth.ID, now)
		}
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

func (r *copilotCatalogRefresher) cancelJob(id string) {
	job := r.inFlight[id]
	if job == nil {
		return
	}
	job.cancel()
	if i := slices.Index(r.waiting, job); i >= 0 {
		r.waiting = slices.Delete(r.waiting, i, i+1)
		delete(r.inFlight, id)
	}
	// Dispatched jobs stay tracked until the worker reports completion.
}

func (r *copilotCatalogRefresher) reconcile(ctx context.Context, now time.Time) {
	seen := make(map[string]bool)
	for _, id := range r.service.coreManager.IDsByProvider(copilot.Provider) {
		if ctx.Err() != nil {
			return
		}
		seen[id] = true
		r.reconcileID(ctx, id, now)
	}
	for id := range r.states {
		if !seen[id] {
			delete(r.states, id)
			r.cancelJob(id)
		}
	}
}

func (r *copilotCatalogRefresher) reconcileID(ctx context.Context, id string, now time.Time) {
	auth, found := r.service.coreManager.GetByID(id)
	if !found || auth.Provider != copilot.Provider || r.service.copilotPluginExecutes() {
		delete(r.states, id)
		r.cancelJob(id)
		return
	}
	identity := copilotCatalogIdentityFor(auth)
	disabled := auth.Disabled || auth.Status == coreauth.StatusDisabled
	catalog := auth.ActiveCopilotCatalog(now)
	state := r.states[auth.ID]
	publish := state == nil || state.identity.epoch != identity.epoch || state.disabled != disabled || !state.catalog.SameModels(catalog)
	if state == nil {
		state = &copilotCatalogState{identity: identity}
		r.states[auth.ID] = state
	} else if state.identity != identity {
		state.identity, state.refresh, state.retryAt = identity, true, time.Time{}
	}
	state.catalog, state.disabled = catalog, disabled
	if publish {
		r.service.refreshModelRegistrationForAuthWithContext(ctx, auth, nil)
	}
	if job := r.inFlight[auth.ID]; job != nil {
		if disabled || identity != job.identity {
			job.cancel()
		}
		return
	}
	if disabled || !auth.HasValidAccessToken(now) || !copilot.TrustedAPI(identity.base) || now.Before(state.retryAt) {
		return
	}
	if !state.refresh && now.Before(catalog.ExpiresAt().Add(-copilot.CatalogMaxAge/2)) {
		return
	}
	jobCtx, cancel := context.WithCancel(ctx)
	job := &copilotCatalogJob{ctx: jobCtx, cancel: cancel, auth: auth, identity: identity}
	select {
	case r.jobs <- job:
	default:
		r.waiting = append(r.waiting, job)
	}
	r.inFlight[auth.ID] = job
	state.refresh = false

}

func (r *copilotCatalogRefresher) fetch(job *copilotCatalogJob) error {
	if err := job.ctx.Err(); err != nil {
		return err
	}
	ctx := r.service.coreManager.WithAuthTransport(job.ctx, job.auth)
	client := helps.NewProxyAwareHTTPClient(ctx, r.service.currentConfig(), job.auth, 0)
	catalog, errFetch := copilot.FetchCatalog(ctx, client, job.identity.base, job.identity.token)
	var status copilot.HTTPError
	if errFetch != nil && !(errors.As(errFetch, &status) && (status.Code == http.StatusUnauthorized || status.Code == http.StatusForbidden)) {
		// Retain the previous snapshot only until its original hard TTL. Failed
		// discovery must not move that clock or roll back refreshed credentials.
		return errFetch
	}
	if errFetch != nil {
		// Record an authoritative denial even if this was the very first request.
		// A zero timestamp makes discovery retry independently of the normal refresh age.
		catalog = copilot.UnavailableCatalog
	}
	_, _, errCommit := r.service.coreManager.Modify(coreauth.WithSkipPersist(job.ctx), job.auth.ID, job.identity.epoch, func(current *coreauth.Auth) error {
		if current.Provider != copilot.Provider || current.Disabled || current.Status == coreauth.StatusDisabled || copilotCatalogIdentityFor(current) != job.identity || current.CopilotCatalog != job.auth.CopilotCatalog {
			return errCopilotCatalogSuperseded
		}
		if current.CopilotCatalog == catalog {
			return errCopilotCatalogSuperseded
		}
		current.CopilotCatalog = catalog
		return nil
	})
	return errCommit
}
