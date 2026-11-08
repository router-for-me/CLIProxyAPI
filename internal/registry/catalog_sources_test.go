package registry

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestCatalogFetcherSources(t *testing.T) {
	for _, tc := range []struct {
		name     string
		data     []byte
		validate func([]byte) error
	}{
		{"general", embeddedModelsJSON, validateCatalogBytes},
		{"codex", embeddedCodexClientModelsJSON, ValidateCodexClientModelsJSON},
		{"devin", embeddedDevinModelsJSON, validateDevinCatalogBytes},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var officialHits atomic.Int32
			official := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { officialHits.Add(1); _, _ = w.Write(tc.data) }))
			defer official.Close()
			custom := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write(tc.data) }))
			defer custom.Close()
			broken := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("invalid")) }))
			defer broken.Close()
			path := filepath.Join(t.TempDir(), "catalog.json")
			if errWrite := os.WriteFile(path, tc.data, 0600); errWrite != nil {
				t.Fatal(errWrite)
			}
			fetch := catalogFetcher(tc.data, []string{official.URL}, tc.validate)
			for _, source := range []string{embeddedCatalogSource, custom.URL, path} {
				data, errFetch := fetch(context.Background(), source)
				if errFetch != nil || string(data) != string(tc.data) {
					t.Fatalf("source %s: %v", source, errFetch)
				}
			}
			for _, source := range []string{broken.URL, path + ".missing"} {
				if _, errFetch := fetch(context.Background(), source); errFetch == nil {
					t.Fatal("accepted invalid custom source")
				}
			}
			if officialHits.Load() != 0 {
				t.Fatal("custom sources fell back to official URL")
			}
			if _, errFetch := fetch(context.Background(), ""); errFetch != nil {
				t.Fatal(errFetch)
			}
			if officialHits.Load() != 1 {
				t.Fatal("default source was not fetched")
			}
			if errWrite := os.WriteFile(path, []byte("invalid"), 0600); errWrite != nil {
				t.Fatal(errWrite)
			}
			if _, errFetch := fetch(context.Background(), path); errFetch == nil {
				t.Fatal("file was not reread")
			}
		})
	}
}

func TestCatalogSourceSwitchRejectsStalePublication(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	started, release, done := make(chan struct{}), make(chan struct{}), make(chan struct{})
	published := make(chan string, 3)
	u := &catalogUpdater{
		generation: 1,
		fetch: func(ctx context.Context, source string) ([]byte, error) {
			if source == "old" {
				close(started)
				<-release
			}
			return []byte(source), nil
		},
		publish: func(data []byte) ([]string, error) { published <- string(data); return nil, nil },
	}
	go func() { u.refresh(ctx, "old", 1); close(done) }()
	<-started
	u.configure(ctx, "new")
	select {
	case value := <-published:
		if value != "new" {
			t.Fatalf("published %s", value)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("new source blocked behind old fetch")
	}
	close(release)
	<-done
	select {
	case value := <-published:
		t.Fatalf("stale publication: %s", value)
	default:
	}
	u.configure(ctx, "new")
	u.mu.Lock()
	generation := u.generation
	u.mu.Unlock()
	if generation != 2 {
		t.Fatal("unchanged source restarted")
	}
}

func TestCatalogRefreshKeepsLastValidData(t *testing.T) {
	last := "last valid"
	u := &catalogUpdater{
		fetch:   func(context.Context, string) ([]byte, error) { return nil, errors.New("unavailable") },
		publish: func(data []byte) ([]string, error) { last = string(data); return nil, nil },
	}
	u.refresh(context.Background(), "", 0)
	if last != "last valid" {
		t.Fatal("failed fetch changed catalog")
	}
	u.fetch = func(context.Context, string) ([]byte, error) { return []byte("invalid"), nil }
	u.publish = func([]byte) ([]string, error) { return nil, errors.New("invalid") }
	u.refresh(context.Background(), "", 0)
	if last != "last valid" {
		t.Fatal("invalid catalog changed data")
	}
}

func TestRefreshModelCatalogsOnDemand(t *testing.T) {
	oldModels := getModels()
	oldCodex, _ := GetCodexClientModelsSnapshot()
	oldDevin, _ := GetDevinModelsSnapshot()
	oldModelsURLs := modelsURLs
	oldGeneralPublish := generalCatalogUpdater.publish
	oldCodexPublish := codexCatalogUpdater.publish
	oldDevinPublish := devinCatalogUpdater.publish
	catalogRuntime.Lock()
	oldCtx, oldLocal, oldSources, oldHome := catalogRuntime.ctx, catalogRuntime.local, catalogRuntime.sources, catalogRuntime.home
	catalogRuntime.Unlock()
	refreshCallbackMu.Lock()
	previousCallback, previousPending := refreshCallback, pendingRefreshChanges
	pendingRefreshChanges = nil
	var cbMu sync.Mutex
	var notified [][]string
	refreshCallback = func(providers []string) {
		cbMu.Lock()
		notified = append(notified, append([]string(nil), providers...))
		cbMu.Unlock()
	}
	refreshCallbackMu.Unlock()

	ctx, cancel := context.WithCancel(context.Background())
	defer func() {
		cancel()
		modelsCatalogStore.mu.Lock()
		modelsCatalogStore.data = oldModels
		modelsCatalogStore.mu.Unlock()
		_, _ = loadCodexClientModelsFromBytes(oldCodex, "restore")
		_, _ = loadDevinModelsFromBytes(oldDevin, "restore")
		SetLocalModelCatalogs(oldLocal)
		generalCatalogUpdater.mu.Lock()
		if generalCatalogUpdater.cancel != nil {
			generalCatalogUpdater.cancel()
		}
		generalCatalogUpdater.cancel = nil
		generalCatalogUpdater.ctx = nil
		generalCatalogUpdater.source = ""
		generalCatalogUpdater.publish = oldGeneralPublish
		generalCatalogUpdater.mu.Unlock()
		codexCatalogUpdater.mu.Lock()
		if codexCatalogUpdater.cancel != nil {
			codexCatalogUpdater.cancel()
		}
		codexCatalogUpdater.cancel = nil
		codexCatalogUpdater.ctx = nil
		codexCatalogUpdater.source = ""
		codexCatalogUpdater.publish = oldCodexPublish
		codexCatalogUpdater.mu.Unlock()
		devinCatalogUpdater.mu.Lock()
		if devinCatalogUpdater.cancel != nil {
			devinCatalogUpdater.cancel()
		}
		devinCatalogUpdater.cancel = nil
		devinCatalogUpdater.ctx = nil
		devinCatalogUpdater.source = ""
		devinCatalogUpdater.publish = oldDevinPublish
		devinCatalogUpdater.mu.Unlock()
		modelsURLs = oldModelsURLs
		catalogRuntime.Lock()
		catalogRuntime.ctx = oldCtx
		catalogRuntime.sources = oldSources
		catalogRuntime.home = oldHome
		catalogRuntime.Unlock()
		refreshCallbackMu.Lock()
		refreshCallback, pendingRefreshChanges = previousCallback, previousPending
		refreshCallbackMu.Unlock()
	}()

	generalPublished := make(chan struct{}, 8)
	codexPublished := make(chan struct{}, 8)
	devinPublished := make(chan struct{}, 8)
	generalCatalogUpdater.mu.Lock()
	generalCatalogUpdater.publish = func(data []byte) ([]string, error) {
		changed, errPub := oldGeneralPublish(data)
		if errPub == nil {
			select {
			case generalPublished <- struct{}{}:
			default:
			}
		}
		return changed, errPub
	}
	generalCatalogUpdater.mu.Unlock()
	codexCatalogUpdater.mu.Lock()
	codexCatalogUpdater.publish = func(data []byte) ([]string, error) {
		changed, errPub := oldCodexPublish(data)
		if errPub == nil {
			select {
			case codexPublished <- struct{}{}:
			default:
			}
		}
		return changed, errPub
	}
	codexCatalogUpdater.mu.Unlock()
	devinCatalogUpdater.mu.Lock()
	devinCatalogUpdater.publish = func(data []byte) ([]string, error) {
		changed, errPub := oldDevinPublish(data)
		if errPub == nil {
			select {
			case devinPublished <- struct{}{}:
			default:
			}
		}
		return changed, errPub
	}
	devinCatalogUpdater.mu.Unlock()

	var generalPayload atomic.Value
	generalPayload.Store(embeddedModelsJSON)
	var generalHits atomic.Int32
	generalSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		generalHits.Add(1)
		_, _ = w.Write(generalPayload.Load().([]byte))
	}))
	defer generalSrv.Close()

	var codexHits atomic.Int32
	codexSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		codexHits.Add(1)
		_, _ = w.Write(embeddedCodexClientModelsJSON)
	}))
	defer codexSrv.Close()

	var devinPayload atomic.Value
	devinPayload.Store(embeddedDevinModelsJSON)
	var devinHits atomic.Int32
	devinSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		devinHits.Add(1)
		_, _ = w.Write(devinPayload.Load().([]byte))
	}))
	defer devinSrv.Close()

	SetLocalModelCatalogs(false)
	sources := CatalogSources{
		Catalog:      generalSrv.URL,
		CodexCatalog: codexSrv.URL,
		DevinCatalog: devinSrv.URL,
	}
	StartModelCatalogUpdaters(ctx, sources, false)
	for _, ch := range []<-chan struct{}{generalPublished, codexPublished, devinPublished} {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for initial catalog publication")
		}
	}

	var rawGeneral map[string]json.RawMessage
	if err := json.Unmarshal(embeddedModelsJSON, &rawGeneral); err != nil {
		t.Fatal(err)
	}
	var claudeSection []map[string]any
	if err := json.Unmarshal(rawGeneral["claude"], &claudeSection); err != nil {
		t.Fatal(err)
	}
	claudeSection = append(claudeSection, map[string]any{
		"id":           "claude-on-demand-7039",
		"display_name": "Claude On-Demand 7039",
	})
	updatedClaudeBytes, err := json.Marshal(claudeSection)
	if err != nil {
		t.Fatal(err)
	}
	rawGeneral["claude"] = updatedClaudeBytes
	mutatedGeneralBytes, err := json.Marshal(rawGeneral)
	if err != nil {
		t.Fatal(err)
	}
	generalPayload.Store(mutatedGeneralBytes)
	devinPayload.Store([]byte(`{"devin":[{"id":"devin/on-demand-7039","display_name":"Devin On-Demand 7039"}]}`))

	cbMu.Lock()
	notified = nil
	cbMu.Unlock()

	res, errRefresh := RefreshModelCatalogs(ctx)
	if errRefresh != nil {
		t.Fatalf("RefreshModelCatalogs failed: %v", errRefresh)
	}
	if res.Catalog != generalSrv.URL || res.CodexCatalog != codexSrv.URL || res.DevinCatalog != devinSrv.URL {
		t.Fatalf("unexpected sources in result: %+v", res)
	}
	if !slices.Contains(res.ChangedProviders, "claude") || !slices.Contains(res.ChangedProviders, "devin") {
		t.Fatalf("changed_providers = %v, want claude and devin", res.ChangedProviders)
	}
	foundClaude := false
	for _, m := range GetStaticModelDefinitionsByChannel("claude") {
		if m != nil && m.ID == "claude-on-demand-7039" {
			foundClaude = true
			break
		}
	}
	if !foundClaude {
		t.Fatal("expected claude-on-demand-7039 to be present after on-demand refresh")
	}
	if LookupDevinModel("devin/on-demand-7039") == nil {
		t.Fatal("expected devin/on-demand-7039 to be present after on-demand refresh")
	}
	cbMu.Lock()
	gotNotified := append([][]string(nil), notified...)
	cbMu.Unlock()
	if len(gotNotified) == 0 {
		t.Fatal("expected ModelRefreshCallback to be invoked")
	}

	// Verify --local-model (embeddedCatalogSource) and Home (disabled) policy boundaries skip remote fetches.
	SetLocalModelCatalogs(true)
	UpdateModelCatalogSources(CatalogSources{}, true)
	generalBefore := generalHits.Load()
	codexBefore := codexHits.Load()
	devinBefore := devinHits.Load()
	policyRes, errPolicy := RefreshModelCatalogs(ctx)
	if errPolicy != nil {
		t.Fatalf("RefreshModelCatalogs under local+Home policy failed: %v", errPolicy)
	}
	if policyRes.Catalog != "disabled" || policyRes.CodexCatalog != embeddedCatalogSource || policyRes.DevinCatalog != "disabled" {
		t.Fatalf("unexpected policy sources: %+v", policyRes)
	}
	if generalHits.Load() != generalBefore || codexHits.Load() != codexBefore || devinHits.Load() != devinBefore {
		t.Fatal("RefreshModelCatalogs performed remote fetch for disabled or embedded catalogs")
	}

	// Verify generation fencing on refreshOnDemand when source switches concurrently.
	started, release := make(chan struct{}), make(chan struct{})
	published := make(chan string, 2)
	u := &catalogUpdater{
		source:     "old",
		generation: 1,
		fetch: func(ctx context.Context, source string) ([]byte, error) {
			if source == "old" {
				close(started)
				<-release
			}
			return []byte(source), nil
		},
		publish: func(data []byte) ([]string, error) {
			published <- string(data)
			return nil, nil
		},
	}
	errCh := make(chan error, 1)
	go func() {
		_, errOnDemand := u.refreshOnDemand(context.Background(), "old", nil)
		errCh <- errOnDemand
	}()
	<-started
	u.configure(ctx, "new")
	select {
	case got := <-published:
		if got != "new" {
			t.Fatalf("published %q, want new", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for new source publication")
	}
	close(release)
	if errOnDemand := <-errCh; errOnDemand == nil {
		t.Fatal("expected stale on-demand refresh to fail when generation changed")
	}
	select {
	case stale := <-published:
		t.Fatalf("stale on-demand refresh published %q", stale)
	default:
	}

	// Verify same-source out-of-order completion does not overwrite a newer on-demand refresh.
	var fetchOrder atomic.Int32
	slowStarted, releaseSlow, slowDone := make(chan struct{}), make(chan struct{}), make(chan struct{})
	currentPublished := ""
	sameSourceUpdater := &catalogUpdater{
		source:     "same-source",
		generation: 1,
		fetch: func(ctx context.Context, source string) ([]byte, error) {
			if fetchOrder.Add(1) == 1 {
				close(slowStarted)
				<-releaseSlow
				return []byte("stale-v1"), nil
			}
			return []byte("fresh-v2"), nil
		},
		publish: func(data []byte) ([]string, error) {
			currentPublished = string(data)
			return nil, nil
		},
	}
	go func() {
		sameSourceUpdater.refresh(context.Background(), "same-source", 1)
		close(slowDone)
	}()
	<-slowStarted
	if _, errFresh := sameSourceUpdater.refreshOnDemand(context.Background(), "same-source", nil); errFresh != nil {
		t.Fatalf("fresh on-demand refresh failed: %v", errFresh)
	}
	if currentPublished != "fresh-v2" {
		t.Fatalf("published = %q, want fresh-v2", currentPublished)
	}
	close(releaseSlow)
	<-slowDone
	if currentPublished != "fresh-v2" {
		t.Fatalf("slow periodic refresh overwrote fresh on-demand catalog: got %q", currentPublished)
	}

	// Verify credential notification fires immediately when one catalog publishes even if another is still fetching.
	for _, ch := range []chan struct{}{generalPublished, codexPublished, devinPublished} {
		for len(ch) > 0 {
			<-ch
		}
	}
	blockCodex, releaseCodex := make(chan struct{}), make(chan struct{})
	var releaseCodexOnce sync.Once
	releaseCodexFn := func() {
		releaseCodexOnce.Do(func() {
			close(releaseCodex)
		})
	}
	var blockCodexActive atomic.Bool
	codexBlockSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if blockCodexActive.CompareAndSwap(true, false) {
			close(blockCodex)
			<-releaseCodex
		}
		_, _ = w.Write(embeddedCodexClientModelsJSON)
	}))
	defer codexBlockSrv.Close()
	defer releaseCodexFn()

	SetLocalModelCatalogs(false)
	UpdateModelCatalogSources(CatalogSources{
		Catalog:      generalSrv.URL,
		CodexCatalog: codexBlockSrv.URL,
		DevinCatalog: devinSrv.URL,
	}, false)
	for _, ch := range []<-chan struct{}{generalPublished, codexPublished, devinPublished} {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for updated catalog publication")
		}
	}

	devinNotified := make(chan struct{}, 1)
	refreshCallbackMu.Lock()
	refreshCallback = func(providers []string) {
		if slices.Contains(providers, "devin") {
			select {
			case devinNotified <- struct{}{}:
			default:
			}
		}
	}
	refreshCallbackMu.Unlock()

	devinPayload.Store([]byte(`{"devin":[{"id":"devin/immediate-notify-7039","display_name":"Devin Immediate Notify 7039"}]}`))
	blockCodexActive.Store(true)
	refreshDone := make(chan error, 1)
	go func() {
		_, errAll := RefreshModelCatalogs(ctx)
		refreshDone <- errAll
	}()
	<-blockCodex
	select {
	case <-devinNotified:
	case <-time.After(5 * time.Second):
		t.Fatal("credential notification was blocked by another catalog's in-flight fetch")
	}
	releaseCodexFn()
	if errAll := <-refreshDone; errAll != nil {
		t.Fatalf("RefreshModelCatalogs failed: %v", errAll)
	}

	// Verify default source fallback reports the actual candidate URL that succeeded.
	brokenPrimary := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer brokenPrimary.Close()
	for _, ch := range []chan struct{}{generalPublished, codexPublished, devinPublished} {
		for len(ch) > 0 {
			<-ch
		}
	}
	modelsURLs = []string{brokenPrimary.URL, generalSrv.URL}
	UpdateModelCatalogSources(CatalogSources{
		Catalog:      "",
		CodexCatalog: codexSrv.URL,
		DevinCatalog: devinSrv.URL,
	}, false)
	for _, ch := range []<-chan struct{}{generalPublished, codexPublished} {
		select {
		case <-ch:
		case <-time.After(5 * time.Second):
			t.Fatal("timed out waiting for fallback catalog publication")
		}
	}
	fallbackRes, errFallback := RefreshModelCatalogs(ctx)
	if errFallback != nil {
		t.Fatalf("RefreshModelCatalogs with fallback failed: %v", errFallback)
	}
	if fallbackRes.Catalog != generalSrv.URL || fallbackRes.Sources.Catalog != generalSrv.URL || fallbackRes.Catalogs["catalog"].Source != generalSrv.URL {
		t.Fatalf("expected fallback source %q, got %+v", generalSrv.URL, fallbackRes)
	}
}
