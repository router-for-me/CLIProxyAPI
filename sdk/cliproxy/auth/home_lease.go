package auth

import (
	"context"
	"strings"
	"sync"
	"time"

	cliproxyusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
	log "github.com/sirupsen/logrus"
)

var homeLeaseReleaseRetryDelays = [...]time.Duration{250 * time.Millisecond, time.Second, 2 * time.Second}

type homeInFlightLeaseDispatcher interface {
	RenewInFlightLease(ctx context.Context, leaseID string) (bool, error)
	ReleaseInFlightLease(ctx context.Context, leaseID string, reason string) (bool, error)
}

type homeLeaseHandle struct {
	leaseID    string
	ttl        time.Duration
	renewEvery time.Duration
	client     homeInFlightLeaseDispatcher
	stop       chan struct{}

	startOnce   sync.Once
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

func newHomeLeaseHandle(client homeInFlightLeaseDispatcher, leaseID string, ttl time.Duration) *homeLeaseHandle {
	leaseID = strings.TrimSpace(leaseID)
	if client == nil || leaseID == "" {
		return nil
	}
	return &homeLeaseHandle{
		leaseID:    leaseID,
		ttl:        ttl,
		renewEvery: homeLeaseRenewInterval(ttl),
		client:     client,
		stop:       make(chan struct{}),
	}
}

func (h *homeLeaseHandle) start() {
	if h == nil || h.client == nil || h.ttl <= 0 {
		return
	}
	h.startOnce.Do(func() {
		interval := h.renewEvery
		if interval <= 0 {
			return
		}
		go func() {
			ticker := time.NewTicker(interval)
			defer ticker.Stop()
			for {
				select {
				case <-h.stop:
					return
				case <-ticker.C:
					renewed, errRenew := h.client.RenewInFlightLease(context.Background(), h.leaseID)
					if errRenew != nil {
						log.WithError(errRenew).WithField("lease_id", h.leaseID).Warn("failed to renew Home in-flight lease")
						continue
					}
					if !renewed {
						return
					}
				}
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
		reason = strings.TrimSpace(reason)
		if _, errRelease := h.client.ReleaseInFlightLease(context.Background(), h.leaseID, reason); errRelease != nil {
			log.WithError(errRelease).WithField("lease_id", h.leaseID).Warn("failed to release Home in-flight lease; retrying")
			go h.retryRelease(reason)
		}
	})
}

func (h *homeLeaseHandle) retryRelease(reason string) {
	if h == nil || h.client == nil {
		return
	}
	var lastErr error
	for _, delay := range homeLeaseReleaseRetryDelays {
		timer := time.NewTimer(delay)
		<-timer.C
		if _, errRelease := h.client.ReleaseInFlightLease(context.Background(), h.leaseID, reason); errRelease == nil {
			return
		} else {
			lastErr = errRelease
		}
	}
	log.WithError(lastErr).WithField("lease_id", h.leaseID).Warn("failed to release Home in-flight lease after retries")
}

func setHomeLease(auth *Auth, lease *homeLeaseHandle) {
	if auth == nil || lease == nil {
		return
	}
	auth.homeLease = lease
	lease.start()
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
	return cliproxyusage.WithHomeLeaseID(ctx, lease.leaseID)
}

func releaseHomeLease(auth *Auth, reason string) {
	if lease := homeLeaseFromAuth(auth); lease != nil {
		lease.release(reason)
	}
}
