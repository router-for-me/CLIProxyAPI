package cliproxy

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

func TestAntigravityProbeFencesOlderTokenResult(t *testing.T) {
	resetAntigravityCapabilityCache()
	t.Cleanup(resetAntigravityCapabilityCache)
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") == "Bearer old" {
			close(started)
			<-release
			_, _ = w.Write([]byte(`{"models":{"old":{}}}`))
			return
		}
		_, _ = w.Write([]byte(`{"models":{"new":{}}}`))
	}))
	t.Cleanup(func() { unblock(); server.Close() })
	svc := &Service{cfg: &config.Config{}}
	auth := antigravityTestAuth("fenced", server.URL)
	auth.Metadata["access_token"] = "old"
	done := make(chan struct{})
	go func() { defer close(done); svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth) }()
	<-started
	updated := auth.Clone()
	updated.Metadata["access_token"] = "new"
	hints := svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), updated)
	if _, ok := hints.ModelIDs["new"]; !ok {
		t.Fatal("new token missing catalog")
	}
	unblock()
	<-done
	if _, ok := svc.cachedAntigravityHints(updated).ModelIDs["new"]; !ok {
		t.Fatal("old completion overwrote newer cache")
	}
	recreated := updated.Clone()
	recreated.RegistrationEpoch++
	if svc.cachedAntigravityHints(recreated).ModelIDs != nil {
		t.Fatal("recreated account inherited old lifecycle cache")
	}
}

func TestAntigravityNonAuthoritativeResponsePreservesSuccess(t *testing.T) {
	for _, response := range []string{`{}`, `null`, `{"models":null}`, `{"webSearchModelIds":["search"]}`} {
		t.Run(response, func(t *testing.T) {
			resetAntigravityCapabilityCache()
			t.Cleanup(resetAntigravityCapabilityCache)
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) == 1 {
					_, _ = w.Write([]byte(`{"models":{"allowed":{}}}`))
					return
				}
				_, _ = w.Write([]byte(response))
			}))
			defer server.Close()
			svc := &Service{cfg: &config.Config{}}
			auth := antigravityTestAuth("non-authoritative", server.URL)
			svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
			key := svc.antigravityCapabilityKey(auth)
			antigravityCapabilityMu.Lock()
			entry := antigravityCapabilityCache[key]
			entry.expiresAt = time.Time{}
			antigravityCapabilityCache[key] = entry
			antigravityCapabilityMu.Unlock()
			hints := svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth)
			if _, ok := hints.ModelIDs["allowed"]; !ok {
				t.Fatal("lost successful entitlement")
			}
			if _, ok := svc.cachedAntigravityHints(auth).ModelIDs["allowed"]; !ok {
				t.Fatal("cache erased")
			}
			antigravityCapabilityMu.RLock()
			expiry := antigravityCapabilityCache[key].expiresAt
			antigravityCapabilityMu.RUnlock()
			if !expiry.IsZero() {
				t.Fatal("non-authoritative response renewed success TTL")
			}
		})
	}
}

type antigravityObservedContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *antigravityObservedContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestAntigravitySingleflightFollowerCancellation(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	defer unblock()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		_, _ = w.Write([]byte(`{"models":{}}`))
	}))
	t.Cleanup(func() { unblock(); server.Close() })
	svc := &Service{cfg: &config.Config{}}
	auth := antigravityTestAuth("follower", server.URL)
	leaderDone := make(chan struct{})
	go func() { defer close(leaderDone); svc.fetchAntigravityModelCapabilityHintsForAuth(t.Context(), auth) }()
	<-started
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	followerCtx := &antigravityObservedContext{Context: ctx, waiting: make(chan struct{})}
	done := make(chan struct{})
	go func() { defer close(done); svc.fetchAntigravityModelCapabilityHintsForAuth(followerCtx, auth) }()
	<-followerCtx.waiting
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("follower ignored cancellation")
	}
	select {
	case <-leaderDone:
		t.Fatal("follower canceled leader")
	default:
	}
	unblock()
	<-leaderDone
}

func TestAntigravityUnknownCatalogPreservesRestoredCooldown(t *testing.T) {
	manager := coreauth.NewManager(nil, nil, nil)
	svc := &Service{cfg: &config.Config{}, coreManager: manager}
	const model = "gemini-3.1-flash-lite"
	retry := time.Now().Add(time.Hour)
	auth := &coreauth.Auth{ID: "unknown-cooldown", Provider: "antigravity", ModelStates: map[string]*coreauth.ModelState{model: {Unavailable: true, NextRetryAfter: retry, Status: coreauth.StatusError}}}
	auth, _ = manager.Register(t.Context(), auth)
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(auth.ID) })
	svc.completeModelRegistrationForAuth(t.Context(), auth)
	svc.WaitAntigravityProbes()
	current, _ := manager.GetByID(auth.ID)
	if state := current.ModelStates[model]; state == nil || !state.NextRetryAfter.Equal(retry) {
		t.Fatal("unknown catalog erased restored cooldown")
	}
}
