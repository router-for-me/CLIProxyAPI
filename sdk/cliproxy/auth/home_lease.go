package auth

import (
	"context"
	"strings"
	"sync"
	"time"

	cliproxyusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

const (
	homeLeaseReleaseQueueSize = 1024
	homeLeaseReleaseWorkers   = 4
)

var homeLeaseReleaseRetryDelays = [...]time.Duration{250 * time.Millisecond, time.Second, 2 * time.Second}

type homeInFlightLeaseDispatcher interface {
	RenewInFlightLease(ctx context.Context, leaseID string) (bool, error)
	ReleaseInFlightLease(ctx context.Context, leaseID string, reason string) (bool, error)
}

type homeLeaseReleaseRequest struct {
	client  homeInFlightLeaseDispatcher
	leaseID string
	reason  string
}

type homeLeaseReleaseQueue struct {
	ctx    context.Context
	cancel context.CancelFunc

	mu          sync.Mutex
	pending     []homeLeaseReleaseRequest
	limit       int
	workers     int
	active      int
	outstanding int
	closed      bool
	idle        chan struct{}
}

func newHomeLeaseReleaseQueue() *homeLeaseReleaseQueue {
	ctx, cancel := context.WithCancel(context.Background())
	idle := make(chan struct{})
	close(idle)
	return &homeLeaseReleaseQueue{
		ctx:     ctx,
		cancel:  cancel,
		pending: make([]homeLeaseReleaseRequest, 0),
		limit:   homeLeaseReleaseQueueSize,
		workers: homeLeaseReleaseWorkers,
		idle:    idle,
	}
}

func (q *homeLeaseReleaseQueue) enqueue(request homeLeaseReleaseRequest) bool {
	if q == nil || request.client == nil || strings.TrimSpace(request.leaseID) == "" {
		return false
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed || q.limit <= 0 || q.workers <= 0 || q.outstanding >= q.limit {
		return false
	}
	request.leaseID = strings.TrimSpace(request.leaseID)
	request.reason = strings.TrimSpace(request.reason)
	q.pending = append(q.pending, request)
	q.outstanding++
	if q.active < q.workers {
		if q.active == 0 {
			q.idle = make(chan struct{})
		}
		q.active++
		go q.runWorker()
	}
	return true
}

func (q *homeLeaseReleaseQueue) runWorker() {
	for {
		q.mu.Lock()
		if len(q.pending) == 0 {
			q.active--
			if q.active == 0 {
				close(q.idle)
			}
			q.mu.Unlock()
			return
		}
		request := q.pending[0]
		q.pending[0] = homeLeaseReleaseRequest{}
		q.pending = q.pending[1:]
		q.mu.Unlock()
		q.releaseWithRetry(request)
		q.mu.Lock()
		q.outstanding--
		q.mu.Unlock()
	}
}

func (q *homeLeaseReleaseQueue) releaseWithRetry(request homeLeaseReleaseRequest) {
	if q == nil || request.client == nil {
		return
	}
	var lastErr error
	for attempt := 0; attempt <= len(homeLeaseReleaseRetryDelays); attempt++ {
		if q.ctx.Err() != nil {
			return
		}
		if attempt > 0 {
			timer := time.NewTimer(homeLeaseReleaseRetryDelays[attempt-1])
			select {
			case <-q.ctx.Done():
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				return
			case <-timer.C:
			}
		}
		_, errRelease := request.client.ReleaseInFlightLease(q.ctx, request.leaseID, request.reason)
		if errRelease == nil {
			return
		}
		lastErr = errRelease
		if q.ctx.Err() != nil {
			return
		}
		if attempt == 0 {
			log.WithError(errRelease).WithField("lease_id", request.leaseID).Warn("failed to release Home in-flight lease; retrying")
		}
	}
	log.WithError(lastErr).WithField("lease_id", request.leaseID).Warn("failed to release Home in-flight lease after retries")
}

func (q *homeLeaseReleaseQueue) shutdown(ctx context.Context) error {
	if q == nil {
		return nil
	}
	if ctx == nil {
		ctx = context.Background()
	}
	q.mu.Lock()
	q.closed = true
	active := q.active
	idle := q.idle
	q.mu.Unlock()
	if active == 0 {
		q.cancel()
		return nil
	}
	select {
	case <-idle:
		q.cancel()
		return nil
	case <-ctx.Done():
		q.cancel()
		return ctx.Err()
	}
}

type homeLeaseHandle struct {
	leaseID         string
	ttl             time.Duration
	firstRenewAfter time.Duration
	renewEvery      time.Duration
	client          homeInFlightLeaseDispatcher
	releases        *homeLeaseReleaseQueue
	stop            chan struct{}

	startOnce   sync.Once
	watchOnce   sync.Once
	releaseOnce sync.Once
}

func homeLeaseRenewInterval(ttl time.Duration) time.Duration {
	interval := ttl / 3
	if interval < 10*time.Second {
		return 10 * time.Second
	}
	if interval > 5*time.Minute {
		return 5 * time.Minute
	}
	return interval
}

func homeLeaseFirstRenewDelay(ttl time.Duration, expiresAt time.Time, now time.Time) time.Duration {
	interval := homeLeaseRenewInterval(ttl)
	if expiresAt.IsZero() {
		return interval
	}
	remaining := expiresAt.Sub(now)
	if remaining <= 0 {
		return 0
	}
	firstDelay := remaining / 3
	if firstDelay < interval {
		return firstDelay
	}
	return interval
}

func newHomeLeaseHandle(client homeInFlightLeaseDispatcher, leaseID string, ttl time.Duration, expiresAt time.Time, releases *homeLeaseReleaseQueue) *homeLeaseHandle {
	leaseID = strings.TrimSpace(leaseID)
	if client == nil || leaseID == "" {
		return nil
	}
	if releases == nil {
		releases = newHomeLeaseReleaseQueue()
	}
	now := time.Now()
	return &homeLeaseHandle{
		leaseID:         leaseID,
		ttl:             ttl,
		firstRenewAfter: homeLeaseFirstRenewDelay(ttl, expiresAt, now),
		renewEvery:      homeLeaseRenewInterval(ttl),
		client:          client,
		releases:        releases,
		stop:            make(chan struct{}),
	}
}

func (h *homeLeaseHandle) start() {
	if h == nil || h.client == nil || h.ttl <= 0 {
		return
	}
	h.startOnce.Do(func() {
		if h.renewEvery <= 0 {
			return
		}
		go func() {
			delay := h.firstRenewAfter
			if delay > h.renewEvery {
				delay = h.renewEvery
			}
			for {
				timer := time.NewTimer(delay)
				select {
				case <-h.stop:
					if !timer.Stop() {
						select {
						case <-timer.C:
						default:
						}
					}
					return
				case <-timer.C:
				}
				renewed, errRenew := h.client.RenewInFlightLease(context.Background(), h.leaseID)
				if errRenew != nil {
					log.WithError(errRenew).WithField("lease_id", h.leaseID).Warn("failed to renew Home in-flight lease")
					delay = h.renewEvery
					continue
				}
				if !renewed {
					return
				}
				delay = h.renewEvery
			}
		}()
	})
}

func (h *homeLeaseHandle) watchContext(ctx context.Context) {
	if h == nil || ctx == nil || ctx.Done() == nil {
		return
	}
	h.watchOnce.Do(func() {
		go func() {
			select {
			case <-ctx.Done():
				h.release("request_canceled")
			case <-h.stop:
			}
		}()
	})
}

func (h *homeLeaseHandle) release(reason string) {
	if h == nil || h.client == nil {
		return
	}
	h.releaseOnce.Do(func() {
		close(h.stop)
		request := homeLeaseReleaseRequest{
			client:  h.client,
			leaseID: h.leaseID,
			reason:  strings.TrimSpace(reason),
		}
		if h.releases == nil || !h.releases.enqueue(request) {
			log.WithField("lease_id", h.leaseID).Warn("Home in-flight lease release queue unavailable; relying on TTL expiry")
		}
	})
}

func setHomeLease(auth *Auth, lease *homeLeaseHandle) {
	if auth == nil || lease == nil {
		return
	}
	auth.homeLease = lease
}

func preserveHomeLease(auth *Auth, lease *homeLeaseHandle) {
	if auth == nil || lease == nil || homeLeaseFromAuth(auth) != nil {
		return
	}
	setHomeLease(auth, lease)
}

func homeLeaseFromAuth(auth *Auth) *homeLeaseHandle {
	if auth == nil {
		return nil
	}
	return auth.homeLease
}

func contextWithHomeLease(ctx context.Context, auth *Auth) context.Context {
	lease := homeLeaseFromAuth(auth)
	if lease == nil {
		return ctx
	}
	lease.start()
	lease.watchContext(ctx)
	return cliproxyusage.WithHomeLeaseID(ctx, lease.leaseID)
}

func startHomeLease(ctx context.Context, auth *Auth) {
	if lease := homeLeaseFromAuth(auth); lease != nil {
		lease.start()
		lease.watchContext(ctx)
	}
}

func releaseHomeLease(auth *Auth, reason string) {
	if lease := homeLeaseFromAuth(auth); lease != nil {
		lease.release(reason)
	}
}
