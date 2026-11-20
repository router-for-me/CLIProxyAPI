package cliproxy

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	runtimeexecutor "github.com/router-for-me/CLIProxyAPI/v8/internal/runtime/executor"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
)

type copilotCatalogTransport func(*http.Request) (*http.Response, error)

func (f copilotCatalogTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func newCopilotCatalogTestRefresher(t *testing.T, manager *coreauth.Manager, cfg *config.Config) *copilotCatalogRefresher {
	t.Helper()
	r := &copilotCatalogRefresher{
		service: &Service{cfg: cfg, coreManager: manager}, wake: make(chan struct{}, 1),
		jobs: make(chan *copilotCatalogJob, 16), states: make(map[string]*copilotCatalogState), inFlight: make(map[string]*copilotCatalogJob),
	}
	t.Cleanup(manager.AddHook(r))
	t.Cleanup(func() {
		for _, job := range r.inFlight {
			job.cancel()
		}
	})
	return r
}

func registerCopilotCatalogTestAuth(t *testing.T, manager *coreauth.Manager, fetched time.Time) *coreauth.Auth {
	t.Helper()
	raw, err := json.Marshal(copilot.Catalog{FetchedAt: fetched, Models: []copilot.Model{{ID: "copilot-catalog-test-model", Endpoints: []string{copilot.Chat}}}})
	if err != nil {
		t.Fatal(err)
	}
	a, err := manager.Register(context.Background(), &coreauth.Auth{
		ID: t.Name(), Provider: copilot.Provider, Status: coreauth.StatusActive,
		Metadata: map[string]any{"type": copilot.Provider, "access_token": "old-token", "refresh_token": "grant", "base_url": "https://api.individual.githubcopilot.com", "expired": time.Now().Add(24 * time.Hour).Format(time.RFC3339), copilot.CatalogKey: string(raw)},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(a.ID) })
	return a
}

func TestCopilotCatalogObserverSkipsOtherProviders(t *testing.T) {
	m := coreauth.NewManager(nil, nil, nil)
	r := newCopilotCatalogTestRefresher(t, m, &config.Config{})
	ctx := context.Background()
	base, err := m.Register(ctx, &coreauth.Auth{ID: t.Name(), Provider: "other", Status: coreauth.StatusActive})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.UpdateRefreshedAuth(ctx, base, base.Clone()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.wake:
		t.Fatal("unrelated credential woke Copilot discovery")
	default:
	}
	r.reconcile(ctx, time.Now())
	if len(r.states) != 0 || len(r.jobs) != 0 {
		t.Fatal("unrelated credential entered the Copilot queue")
	}
}

func TestCopilotAccountCatalogRepublishesOnAuthUpdate(t *testing.T) {
	ctx := context.Background()
	cfg := &config.Config{OAuthModelAlias: map[string][]config.OAuthModelAlias{copilot.Provider: {{Name: "copilot-test-new", Alias: "copilot-test-alias"}}}}
	manager := coreauth.NewManager(nil, nil, nil)
	r := newCopilotCatalogTestRefresher(t, manager, cfg)
	base := registerCopilotCatalogTestAuth(t, manager, time.Now())
	r.reconcile(ctx, time.Now())
	updated := base.Clone()
	catalog := copilot.Catalog{FetchedAt: time.Now(), Models: []copilot.Model{{ID: "copilot-test-new", Endpoints: []string{copilot.Messages}}}}
	data, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	updated.Metadata[copilot.CatalogKey] = string(data)
	current, err := manager.UpdateRefreshedAuth(ctx, base, updated)
	if err != nil {
		t.Fatal(err)
	}
	r.reconcile(ctx, time.Now())
	if models := GlobalModelRegistry().GetModelsForClient(base.ID); len(models) != 1 || models[0].ID != "copilot-test-alias" {
		t.Fatalf("update did not publish the account catalog with its alias: %+v", models)
	}
	updated = current.Clone()
	catalog.Models = []copilot.Model{}
	data, err = json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	updated.Metadata[copilot.CatalogKey] = string(data)
	if _, err = manager.UpdateRefreshedAuth(ctx, current, updated); err != nil {
		t.Fatal(err)
	}
	r.reconcile(ctx, time.Now())
	if models := GlobalModelRegistry().GetModelsForClient(base.ID); len(models) != 0 {
		t.Fatal("a successful empty catalog did not revoke the model")
	}
}

func TestCopilotCatalogFetchOutcomesAndCredentialPreservation(t *testing.T) {
	for _, status := range []int{http.StatusOK, http.StatusTooManyRequests, http.StatusUnauthorized, http.StatusForbidden} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			m := coreauth.NewManager(nil, nil, nil)
			r := newCopilotCatalogTestRefresher(t, m, &config.Config{})
			a := registerCopilotCatalogTestAuth(t, m, time.Now().Add(-copilot.CatalogMaxAge*3/4))
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", copilotCatalogTransport(func(req *http.Request) (*http.Response, error) {
				if req.URL.Path != "/models" || req.Header.Get("Authorization") != "Bearer old-token" {
					t.Fatal("incorrect discovery request")
				}
				return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[]}`)), Request: req}, nil
			}))
			r.reconcile(ctx, time.Now())
			job := <-r.jobs
			err := r.fetch(job)
			if (err != nil) != (status == http.StatusTooManyRequests) {
				t.Fatalf("unexpected discovery result: %v", err)
			}
			current, _ := m.GetByID(a.ID)
			if current.Metadata["access_token"] != "old-token" || current.Metadata["expired"] != a.Metadata["expired"] {
				t.Fatal("catalog fetch changed credential fields")
			}
			r.finish(job, time.Now())
			job.cancel()
			r.reconcile(ctx, time.Now())
			if len(r.jobs) != 0 {
				t.Fatal("discovery ignored its retry backoff")
			}
			models := GlobalModelRegistry().GetModelsForClient(a.ID)
			if (len(models) != 0) != (status == http.StatusTooManyRequests) {
				t.Fatalf("wrong availability after HTTP %d: %+v", status, models)
			}
			if status == http.StatusTooManyRequests && current.Metadata[copilot.CatalogKey] != a.Metadata[copilot.CatalogKey] {
				t.Fatal("failure extended the catalog TTL")
			}
		})
	}
}

func TestCopilotCatalogClocksAndExpiryAreIndependent(t *testing.T) {
	m := coreauth.NewManager(nil, nil, nil)
	r := newCopilotCatalogTestRefresher(t, m, &config.Config{})
	now := time.Now()
	a := registerCopilotCatalogTestAuth(t, m, now)
	r.reconcile(t.Context(), now)
	if len(r.jobs) != 0 {
		t.Fatal("fresh directory was fetched again")
	}
	due := now.Add(copilot.CatalogMaxAge / 2)
	r.reconcile(t.Context(), due)
	if len(r.jobs) != 1 {
		t.Fatal("long-lived credential did not schedule independent discovery")
	}
	job := <-r.jobs
	r.finish(job, due)
	job.cancel()
	r.reconcile(t.Context(), due)
	if len(r.jobs) != 0 {
		t.Fatal("failed discovery did not back off")
	}
	r.reconcile(t.Context(), due.Add(copilotCatalogRetryDelay))
	if len(r.jobs) != 1 {
		t.Fatal("directory retry waited for token expiration")
	}
	// Explicitly expire the persisted snapshot while a discovery job is pending.
	_, _, err := m.Modify(t.Context(), a.ID, a.RegistrationEpoch, func(current *coreauth.Auth) error {
		raw, errEncode := json.Marshal(copilot.Catalog{FetchedAt: time.Now().Add(-copilot.CatalogMaxAge - time.Minute), Models: []copilot.Model{{ID: "copilot-catalog-test-model", Endpoints: []string{copilot.Chat}}}})
		current.Metadata[copilot.CatalogKey] = string(raw)
		return errEncode
	})
	if err != nil {
		t.Fatal(err)
	}
	r.reconcile(t.Context(), time.Now())
	if models := GlobalModelRegistry().GetModelsForClient(a.ID); len(models) != 0 {
		t.Fatal("blocked discovery prevented hard-TTL model revocation")
	}
}

func TestCopilotCatalogRejectsStaleDiscoveryResults(t *testing.T) {
	for _, change := range []string{"token", "origin", "registration", "catalog", "disabled", "removed"} {
		t.Run(change, func(t *testing.T) {
			m := coreauth.NewManager(nil, nil, nil)
			r := newCopilotCatalogTestRefresher(t, m, &config.Config{})
			a := registerCopilotCatalogTestAuth(t, m, time.Now().Add(-copilot.CatalogMaxAge*3/4))
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", copilotCatalogTransport(func(req *http.Request) (*http.Response, error) {
				if change == "removed" {
					m.Remove(t.Context(), a.ID)
				} else if change == "registration" {
					m.Remove(t.Context(), a.ID)
					if _, err := m.Register(t.Context(), a.Clone()); err != nil {
						t.Fatal(err)
					}
				} else {
					_, _, err := m.Modify(t.Context(), a.ID, a.RegistrationEpoch, func(current *coreauth.Auth) error {
						switch change {
						case "token":
							current.Metadata["access_token"] = "rotated"
						case "origin":
							current.Metadata["base_url"] = "https://api.business.githubcopilot.com"
						case "catalog":
							current.Metadata[copilot.CatalogKey] = "newer-catalog"
						case "disabled":
							current.Disabled = true
						}
						return nil
					})
					if err != nil {
						t.Fatal(err)
					}
				}
				return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[]}`)), Request: req}, nil
			}))
			r.reconcile(ctx, time.Now())
			job := <-r.jobs
			err := r.fetch(job)
			current, exists := m.GetByID(a.ID)
			if change == "removed" {
				if exists {
					t.Fatal("discovery resurrected removed credential")
				}
				return
			}
			if err == nil {
				t.Fatal("stale directory was accepted")
			}
			want := a.Metadata[copilot.CatalogKey]
			if change == "catalog" {
				want = "newer-catalog"
			}
			if !exists || current.Metadata[copilot.CatalogKey] != want {
				t.Fatal("stale directory overwrote current state")
			}
		})
	}
}

type copilotOtherRefreshExecutor struct {
	serviceTestPluginExecutor
	refreshed chan struct{}
}

func (*copilotOtherRefreshExecutor) Identifier() string { return "other" }
func (e *copilotOtherRefreshExecutor) Refresh(_ context.Context, auth *coreauth.Auth) (*coreauth.Auth, error) {
	updated := auth.Clone()
	updated.Metadata["access_token"] = "new-other"
	select {
	case e.refreshed <- struct{}{}:
	default:
	}
	return updated, nil
}

type copilotOtherRefreshEvaluator struct{}

func (copilotOtherRefreshEvaluator) ShouldRefresh(_ time.Time, auth *coreauth.Auth) bool {
	return auth.Metadata["access_token"] == "old-other"
}

func TestCopilotBlockedCatalogDoesNotBlockSharedRefreshAndStops(t *testing.T) {
	cfg := &config.Config{AuthAutoRefreshWorkers: 1}
	m := coreauth.NewManager(nil, nil, nil)
	m.SetConfig(cfg)
	other := &copilotOtherRefreshExecutor{refreshed: make(chan struct{}, 2)}
	m.RegisterExecutor(other)
	m.RegisterExecutor(runtimeexecutor.NewCopilotExecutor(cfg))
	a := registerCopilotCatalogTestAuth(t, m, time.Now().Add(-copilot.CatalogMaxAge*3/4))
	if _, err := m.Register(t.Context(), &coreauth.Auth{ID: "other-" + t.Name(), Provider: "other", Status: coreauth.StatusActive, Runtime: copilotOtherRefreshEvaluator{}, Metadata: map[string]any{"access_token": "old-other", "refresh_token": "grant"}}); err != nil {
		t.Fatal(err)
	}
	entered := make(chan struct{}, 8)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	ctx = context.WithValue(ctx, "cliproxy.roundtripper", copilotCatalogTransport(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/models" {
			select {
			case entered <- struct{}{}:
			default:
			}
			<-req.Context().Done()
			return nil, req.Context().Err()
		}
		if req.URL.Path != "/copilot_internal/v2/token" {
			return nil, errors.New("unexpected request")
		}
		payload := fmt.Sprintf(`{"token":"new-copilot","expires_at":%d,"endpoints":{"api":"https://api.individual.githubcopilot.com"}}`, time.Now().Add(time.Hour).Unix())
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(payload)), Request: req}, nil
	}))
	s := &Service{cfg: cfg, coreManager: m}
	r := s.startCopilotCatalogRefresh(ctx)
	defer func() { r.cancel(); <-r.done }()
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("directory request did not start")
	}
	m.StartAutoRefresh(ctx, time.Millisecond)
	defer m.StopAutoRefresh()
	select {
	case <-other.refreshed:
	case <-time.After(5 * time.Second):
		t.Fatal("Copilot discovery blocked another provider's automatic refresh")
	}
	finished := make(chan []coreauth.ForceRefreshResult, 1)
	go func() { finished <- m.ForceRefreshAll(ctx) }()
	select {
	case results := <-finished:
		if len(results) != 2 {
			t.Fatalf("wrong forced refresh results: %+v", results)
		}
		for _, result := range results {
			if !result.Success {
				t.Fatalf("forced refresh failed: %+v", result)
			}
		}
	case <-time.After(5 * time.Second):
		t.Fatal("Copilot discovery blocked the shared refresh-all worker")
	}
	current, _ := m.GetByID(a.ID)
	if current.Metadata["access_token"] != "new-copilot" {
		t.Fatal("blocked discovery delayed token publication")
	}
	if err := s.Shutdown(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.done:
	default:
		t.Fatal("shutdown did not drain discovery")
	}
	for len(r.wake) > 0 {
		<-r.wake
	}
	if _, err := r.service.coreManager.UpdateRefreshedAuth(t.Context(), current, current.Clone()); err != nil {
		t.Fatal(err)
	}
	select {
	case <-r.wake:
		t.Fatal("shutdown retained Copilot's hook subscription")
	default:
	}
}
