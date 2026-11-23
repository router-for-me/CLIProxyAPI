package cliproxy

import (
	"context"
	"fmt"
	"io"
	"maps"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/MoeForge/moego/json"
	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/auth/copilot"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/pluginhost"
	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	sdkauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/auth"
	coreauth "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/auth"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/config"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/pluginapi"
)

// Exercise synthetic accounts using the same custom models.json path available to operators.
func useCopilotTestModels(t *testing.T) {
	t.Helper()
	models := registry.GetStaticModelDefinitionsByChannel(copilot.Provider)
	for _, id := range []string{"alpha", "beta", "hidden", "copilot-catalog-test-model", "copilot-test-new", "copilot-routing-chat", "copilot-routing-hidden", "copilot-shared-cooldown", "live"} {
		models = append(models, &registry.ModelInfo{ID: id})
	}
	copilotTestPublisher(t)(models)
}

func copilotTestPublisher(t *testing.T) func([]*registry.ModelInfo) {
	t.Helper()
	dir, err := filepath.Abs(filepath.Join("..", "..", "internal", "registry", "models"))
	if err != nil {
		t.Fatal(err)
	}
	sources := registry.CatalogSources{Catalog: filepath.Join(dir, "models.json"), CodexCatalog: filepath.Join(dir, "codex_client_models.json"), DevinCatalog: filepath.Join(dir, "devin_models.json")}
	raw, err := os.ReadFile(sources.Catalog)
	if err != nil {
		t.Fatal(err)
	}
	original := sources
	t.Cleanup(func() {
		if _, err := registry.RefreshModelCatalogsWithPolicy(context.Background(), original, false); err != nil {
			t.Error(err)
		}
	})
	sources.Catalog = filepath.Join(t.TempDir(), "models.json")
	return func(models []*registry.ModelInfo) {
		t.Helper()
		doc := ojson.NewDoc(raw)
		if err := doc.Set("copilot", models); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(sources.Catalog, doc.Bytes(), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := registry.RefreshModelCatalogsWithPolicy(t.Context(), sources, false); err != nil {
			t.Fatal(err)
		}
	}
}

func TestCopilotCatalogQueueCoalescesAndReplacesRotatedCredentials(t *testing.T) {
	m := coreauth.NewManager(nil, nil, nil)
	r := newCopilotCatalogTestRefresher(t, m, &config.Config{})
	r.jobs = make(chan *copilotCatalogJob, 1)
	now := time.Now()
	a := registerCopilotCatalogTestAuth(t, m, now.Add(-copilot.CatalogMaxAge*3/4))
	for range 20 {
		r.OnAuthUpdated(context.Background(), a)
		r.reconcile(t.Context(), now)
	}
	if len(r.jobs) != 1 || len(r.inFlight) != 1 || len(r.wake) != 1 {
		t.Fatal("repeated notifications accumulated duplicate work")
	}
	old := <-r.jobs
	_, _, err := m.Modify(t.Context(), a.ID, a.RegistrationEpoch, func(current *coreauth.Auth) error {
		current.Metadata["access_token"] = "rotated"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	r.reconcile(t.Context(), now)
	if old.ctx.Err() == nil {
		t.Fatal("rotation did not cancel stale discovery")
	}
	r.finish(old, now)
	r.reconcile(t.Context(), now)
	if len(r.jobs) != 1 {
		t.Fatal("old request backoff delayed new credentials")
	}
	next := <-r.jobs
	if next.identity.token != "rotated" {
		t.Fatal("replacement used stale token")
	}
	// A late duplicate completion must not discard the replacement job.
	r.finish(old, now)
	if r.inFlight[a.ID] != next {
		t.Fatal("stale completion discarded newer discovery")
	}
	m.Remove(t.Context(), a.ID)
	r.reconcile(t.Context(), now)
	if next.ctx.Err() == nil || len(r.states) != 0 {
		t.Fatal("removed account retained active discovery")
	}
}

func TestCopilotCatalogRemovedQueuedAccountsAreReleased(t *testing.T) {
	for _, mode := range []string{"account", "full"} {
		t.Run(mode, func(t *testing.T) {
			m := coreauth.NewManager(nil, nil, nil)
			r := newCopilotCatalogTestRefresher(t, m, &config.Config{})
			r.jobs = make(chan *copilotCatalogJob, 1)
			now := time.Now()
			register := func(id string) *copilotCatalogJob {
				t.Helper()
				_, err := m.Register(t.Context(), &coreauth.Auth{
					ID: id, Provider: copilot.Provider,
					Metadata: map[string]any{"access_token": "ok", "base_url": "https://api.individual.githubcopilot.com"},
				})
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(id) })
				r.reconcileID(t.Context(), id, now)
				job := r.inFlight[id]
				if job == nil {
					t.Fatal("account discovery was not scheduled")
				}
				return job
			}
			remove := func(id string) {
				t.Helper()
				m.Remove(t.Context(), id)
				if mode == "full" {
					r.reconcile(t.Context(), now)
				} else {
					r.reconcileID(t.Context(), id, now)
				}
			}

			// With no workers draining the dispatch channel, queued work stays blocked.
			dispatched := register(t.Name() + "-dispatched")
			var removed []*copilotCatalogJob
			for i := range 20 {
				removed = append(removed, register(fmt.Sprintf("%s-removed-%d", t.Name(), i)))
			}
			active := register(t.Name() + "-active")
			if len(r.jobs) != 1 || len(r.waiting) != len(removed)+1 {
				t.Fatal("test did not create dispatch backpressure")
			}
			for i, job := range removed {
				remove(job.auth.ID)
				if job.ctx.Err() == nil || r.inFlight[job.auth.ID] != nil || r.states[job.auth.ID] != nil {
					t.Fatalf("removed account %d retained discovery state", i)
				}
				if len(r.waiting) != len(removed)-i || len(r.inFlight) != len(removed)-i+1 {
					t.Fatalf("removed account %d was not released from the queue", i)
				}
			}
			if r.waiting[0] != active || active.ctx.Err() != nil || r.inFlight[active.auth.ID] != active {
				t.Fatal("removal discarded an active account's queued discovery")
			}
			for _, job := range r.waiting[len(r.waiting):cap(r.waiting)] {
				if job != nil {
					t.Fatal("queue backing array retained a removed account")
				}
			}
			remove(active.auth.ID)
			if len(r.waiting) != 0 || len(r.inFlight) != 1 || active.ctx.Err() == nil {
				t.Fatal("last waiting account was not released")
			}

			// A dispatched job stays tracked until its worker reports completion.
			remove(dispatched.auth.ID)
			if dispatched.ctx.Err() == nil || r.inFlight[dispatched.auth.ID] != dispatched {
				t.Fatal("removed dispatched job lost completion tracking")
			}
			if job := <-r.jobs; job != dispatched {
				t.Fatal("queued removal changed the dispatched job")
			}
			r.finish(dispatched, now)
			if len(r.inFlight) != 0 || len(r.states) != 0 {
				t.Fatal("completed removal retained discovery state")
			}
		})
	}
}

func TestCopilotCatalogCommitPreservesConcurrentAuthEdits(t *testing.T) {
	m := coreauth.NewManager(nil, nil, nil)
	r := newCopilotCatalogTestRefresher(t, m, &config.Config{})
	a := registerCopilotCatalogTestAuth(t, m, time.Now().Add(-copilot.CatalogMaxAge*3/4))
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", copilotCatalogTransport(func(req *http.Request) (*http.Response, error) {
		_, _, err := m.Modify(t.Context(), a.ID, a.RegistrationEpoch, func(current *coreauth.Auth) error {
			current.Metadata["note"] = "concurrent edit"
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[]}`)), Request: req}, nil
	}))
	r.reconcile(ctx, time.Now())
	job := <-r.jobs
	if err := r.fetch(job); err != nil {
		t.Fatal(err)
	}
	current, _ := m.GetByID(a.ID)
	if current.Metadata["note"] != "concurrent edit" || current.Metadata["access_token"] != a.Metadata["access_token"] || current.CredentialVersion != a.CredentialVersion {
		t.Fatal("catalog commit replaced concurrent edits or credential state")
	}
	catalog := current.CopilotCatalog.Active(time.Now())
	if catalog == nil || len(catalog.Routes()) != 0 {
		t.Fatal("new catalog was not committed")
	}
}

func TestCopilotDiscoveryDoesNotPersistCatalog(t *testing.T) {
	useCopilotTestModels(t)
	const id = "copilot-test.json"
	const credential = `{"type":"copilot","base_url":"https://api.business.githubcopilot.com","access_token":"token","refresh_token":"grant","disabled":false}`
	dir := t.TempDir()
	path := filepath.Join(dir, id)
	if err := os.WriteFile(path, []byte(credential), 0o600); err != nil {
		t.Fatal(err)
	}
	store := sdkauth.NewFileTokenStore()
	store.SetBaseDir(dir)
	manager := coreauth.NewManager(store, nil, nil)
	ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", copilotCatalogTransport(func(req *http.Request) (*http.Response, error) {
		return &http.Response{StatusCode: http.StatusOK, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[{"id":"live","supported_endpoints":["/responses"]}]}`)), Request: req}, nil
	}))
	if err := manager.Load(ctx); err != nil {
		t.Fatal(err)
	}
	cold, ok := manager.GetByID(id)
	if !ok || cold.CopilotCatalog != nil {
		t.Fatal("expected a credential awaiting its first model discovery")
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(id) })
	r := newCopilotCatalogTestRefresher(t, manager, &config.Config{})
	r.reconcile(ctx, time.Now())
	select {
	case job := <-r.jobs:
		if err := r.fetch(job); err != nil {
			t.Fatal(err)
		}
		job.cancel()
		r.finish(job, time.Now())
	default:
		t.Fatal("cold start did not schedule discovery")
	}
	r.reconcile(ctx, time.Now())
	current, _ := manager.GetByID(id)
	if route, ok := current.CopilotCatalog.Active(time.Now()).Route("live"); !ok || route.Endpoint != copilot.Responses || !GlobalModelRegistry().ClientSupportsModel(id, "copilot/live") {
		t.Fatal("discovered route was not published")
	}
	if raw, err := os.ReadFile(path); err != nil || string(raw) != credential {
		t.Fatal("directory discovery rewrote the credential")
	}
	// A file watcher reload has no directory. It must preserve the live snapshot.
	records, err := store.List(ctx)
	if err != nil || len(records) != 1 {
		t.Fatalf("reload: %v", err)
	}
	records[0].Metadata["access_token"] = "renewed-token"
	wantStored := maps.Clone(records[0].Metadata)
	updated, err := manager.Update(ctx, records[0])
	if err != nil || updated.CopilotCatalog != current.CopilotCatalog {
		t.Fatalf("reload lost the discovered catalog: %v", err)
	}
	raw, err := os.ReadFile(path)
	var stored map[string]any
	if err != nil || json.Unmarshal(raw, &stored) != nil || !maps.Equal(stored, wantStored) {
		t.Fatal("expected only the credential fields and the renewed access token on disk")
	}
	if err := manager.Load(ctx); err != nil {
		t.Fatal(err)
	}
	restarted, _ := manager.GetByID(id)
	if restarted.CopilotCatalog != nil {
		t.Fatal("in-memory directory survived a store reload")
	}
}

func TestCopilotPresetAndAccountIntersectionTracksCatalogReload(t *testing.T) {
	publish := copilotTestPublisher(t)
	const gpt, kimi, future = "gpt-6.2-sol", "kimi-k3", "new-copilot-engine"
	gptInfo := registry.LookupStaticModelInfoByChannel(gpt, copilot.Provider)
	kimiInfo := registry.LookupStaticModelInfoByChannel(kimi, copilot.Provider)
	futureInfo := &registry.ModelInfo{ID: future}
	publish([]*registry.ModelInfo{gptInfo, kimiInfo, futureInfo})
	m := coreauth.NewManager(nil, nil, nil)
	s := &Service{cfg: &config.Config{}, coreManager: m}
	s.registerModelRefreshCallback()
	t.Cleanup(func() { registry.SetModelRefreshCallback(nil) })
	ctx := t.Context()
	a, err := m.Register(ctx, &coreauth.Auth{ID: t.Name(), Provider: copilot.Provider, Metadata: map[string]any{"access_token": "usable", "base_url": "https://api.individual.githubcopilot.com"}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(a.ID) })
	check := func(want map[string]string) {
		t.Helper()
		reg := GlobalModelRegistry()
		if got := len(reg.GetModelsForClient(a.ID)); got != len(want) {
			t.Fatalf("published %d models, want %v", got, want)
		}
		for _, id := range []string{gpt, kimi, future, "account-only"} {
			endpoint, allowed := want[id]
			route, ok := a.ActiveCopilotCatalog(time.Now()).Route(id)
			if reg.ClientSupportsModel(a.ID, "copilot/"+id) != allowed || ok != allowed || route.Endpoint != endpoint {
				t.Fatalf("registration/route mismatch for %s: %+v, want %q", id, route, endpoint)
			}
		}
	}
	s.refreshModelRegistrationForAuth(a)
	check(map[string]string{gpt: copilot.Responses, kimi: copilot.Chat})
	if route, _ := a.ActiveCopilotCatalog(time.Now()).Route(gpt); route.Info.ContextLength == 0 || route.Info.Thinking == nil || route.DefaultEffort != "medium" {
		t.Fatal("cold route lost preset capabilities or CLI defaults")
	}
	// The account explicitly overrides the preset protocol and capabilities.
	live := copilot.Model{ID: gpt, Endpoints: []string{copilot.Chat}}
	no := false
	live.Capabilities.Supports.Thinking = &no
	live.Capabilities.Supports.Vision = &no
	live.Capabilities.Limits.Context = 1234
	disabled := copilot.Model{ID: kimi, Endpoints: []string{copilot.Chat}}
	disabled.Policy.State = "disabled"
	a.CopilotCatalog = copilot.NewCatalog([]copilot.Model{live, disabled, {ID: future, Endpoints: []string{copilot.Messages}}, {ID: "account-only", Endpoints: []string{copilot.Chat}}}, time.Now())
	a, err = m.Update(ctx, a)
	if err != nil {
		t.Fatal(err)
	}
	s.refreshModelRegistrationForAuth(a)
	check(map[string]string{gpt: copilot.Chat, future: copilot.Messages})
	if route, _ := a.CopilotCatalog.Route(gpt); route.Info.Thinking != nil || route.Info.ContextLength != 1234 || len(route.Info.SupportedInputModalities) != 1 || route.DefaultVerbosity != "" {
		t.Fatal("preset overrode explicit account capabilities or endpoint")
	}
	publish([]*registry.ModelInfo{futureInfo, {ID: "account-only"}})
	check(map[string]string{future: copilot.Messages, "account-only": copilot.Chat})
	publish([]*registry.ModelInfo{})
	check(nil)
}

func TestCopilotColdDiscoveryOutcomes(t *testing.T) {
	for _, tc := range []struct {
		code       int
		body       string
		wantPreset bool
	}{
		{429, `{}`, true}, {503, `{}`, true}, {401, `{}`, false}, {403, `{}`, false}, {200, `{"data":[]}`, false},
	} {
		t.Run(http.StatusText(tc.code), func(t *testing.T) {
			m := coreauth.NewManager(nil, nil, nil)
			r := newCopilotCatalogTestRefresher(t, m, &config.Config{})
			if tc.code == http.StatusForbidden {
				// A model-only plugin still relies on the native executor's account routes.
				host := pluginhost.New()
				host.RegisterPluginForTest(t.Name(), pluginapi.Plugin{Capabilities: pluginapi.Capabilities{ModelProvider: copilotPluginCatalog{models: []pluginapi.ModelInfo{{ID: "gpt-6.2-sol"}}}}})
				r.service.pluginHost = host
			}
			a, err := m.Register(t.Context(), &coreauth.Auth{ID: t.Name(), Provider: copilot.Provider, Metadata: map[string]any{"access_token": "ok", "base_url": "https://api.business.githubcopilot.com"}})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(a.ID) })
			ctx := context.WithValue(t.Context(), "cliproxy.roundtripper", copilotCatalogTransport(func(req *http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.code, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(tc.body)), Request: req}, nil
			}))
			r.reconcile(ctx, time.Now())
			if !GlobalModelRegistry().ClientSupportsModel(a.ID, "copilot/gpt-6.2-sol") {
				t.Fatal("cold route was not published")
			}
			var job *copilotCatalogJob
			select {
			case job = <-r.jobs:
			default:
				t.Fatal("cold discovery was not scheduled")
			}
			err = r.fetch(job)
			if (err != nil) != (tc.code == 429 || tc.code == 503) {
				t.Fatalf("discovery: %v", err)
			}
			job.cancel()
			r.finish(job, time.Now())
			r.reconcile(ctx, time.Now())
			current, _ := m.GetByID(a.ID)
			_, ok := current.ActiveCopilotCatalog(time.Now()).Route("gpt-6.2-sol")
			if ok != tc.wantPreset || GlobalModelRegistry().ClientSupportsModel(a.ID, "copilot/gpt-6.2-sol") != ok {
				t.Fatal("unexpected cold fallback after discovery")
			}
			if !tc.wantPreset {
				if _, ok := current.ActiveCopilotCatalog(time.Now().Add(3 * time.Hour)).Route("gpt-6.2-sol"); ok {
					t.Fatal("expired or rejected catalog reopened a preset")
				}
			}
		})
	}
}

type copilotAccountTransport func(*coreauth.Auth) http.RoundTripper

func (p copilotAccountTransport) RoundTripperFor(a *coreauth.Auth) http.RoundTripper { return p(a) }

func TestCopilotDiscoveryIsolatedByAccount(t *testing.T) {
	m := coreauth.NewManager(nil, nil, nil)
	s := &Service{cfg: &config.Config{}, coreManager: m}
	blocked, done := make(chan struct{}), make(chan struct{})
	m.SetRoundTripperProvider(copilotAccountTransport(func(a *coreauth.Auth) http.RoundTripper {
		return copilotCatalogTransport(func(req *http.Request) (*http.Response, error) {
			if a.ID == "blocked" {
				close(blocked)
				<-req.Context().Done()
				return nil, req.Context().Err()
			}
			close(done)
			return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(`{"data":[]}`)), Request: req}, nil
		})
	}))
	register := func(id string) {
		t.Helper()
		_, err := m.Register(t.Context(), &coreauth.Auth{ID: id, Provider: copilot.Provider, Metadata: map[string]any{"access_token": "ok", "base_url": "https://api.individual.githubcopilot.com"}})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { GlobalModelRegistry().UnregisterClient(id) })
	}
	wait := func(ch <-chan struct{}) {
		t.Helper()
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("model discovery did not use the account transport")
		}
	}
	register("blocked")
	ctx, cancel := context.WithCancel(t.Context())
	r := s.startCopilotCatalogRefresh(ctx)
	defer func() { cancel(); <-r.done }()
	wait(blocked)
	register("ready")
	wait(done)
}
