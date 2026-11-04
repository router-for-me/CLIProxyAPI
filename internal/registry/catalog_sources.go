package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const embeddedCatalogSource = "embed"

// catalogUpdater serializes source changes with publication, not network I/O.
// Cancellation saves work; the generation check also protects against readers
// that finish after cancellation (including local file reads).
type catalogUpdater struct {
	mu              sync.Mutex
	source          string
	generation      uint64
	requestSeq      uint64
	publishedSeq    uint64
	cancel          context.CancelFunc
	ctx             context.Context
	parentDone      <-chan struct{}
	fetch           func(context.Context, string) ([]byte, error)
	fetchWithSource func(context.Context, string) ([]byte, string, error)
	publish         func([]byte) ([]string, error)
}

func (u *catalogUpdater) fetchCatalog(ctx context.Context, source string) ([]byte, string, error) {
	if u.fetchWithSource != nil {
		return u.fetchWithSource(ctx, source)
	}
	data, err := u.fetch(ctx, source)
	return data, source, err
}

func (u *catalogUpdater) configure(ctx context.Context, source string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.cancel != nil && u.source == source && u.parentDone == ctx.Done() && u.ctx.Err() == nil {
		return
	}
	if u.cancel != nil {
		u.cancel()
	}
	u.generation++
	u.publishedSeq = 0
	u.source = source
	refreshCtx, cancel := context.WithCancel(ctx)
	u.cancel = cancel
	u.ctx = refreshCtx
	u.parentDone = ctx.Done()
	if source == "disabled" {
		return
	}
	generation := u.generation
	go func() {
		u.refresh(refreshCtx, source, generation)
		if source == embeddedCatalogSource {
			return
		}
		ticker := time.NewTicker(ModelsRefreshInterval)
		defer ticker.Stop()
		for {
			select {
			case <-refreshCtx.Done():
				return
			case <-ticker.C:
				u.refresh(refreshCtx, source, generation)
			}
		}
	}()
}

// CatalogSourceStatus reports the outcome of refreshing a single model catalog.
type CatalogSourceStatus struct {
	Source           string   `json:"source"`
	Refreshed        bool     `json:"refreshed"`
	Skipped          bool     `json:"skipped,omitempty"`
	ChangedProviders []string `json:"changed_providers,omitempty"`
	Error            string   `json:"error,omitempty"`
}

// CatalogRefreshResult reports the outcome of an on-demand model catalog refresh.
type CatalogRefreshResult struct {
	Catalog          string                         `json:"catalog"`
	CodexCatalog     string                         `json:"codex_catalog"`
	DevinCatalog     string                         `json:"devin_catalog"`
	Sources          CatalogSources                 `json:"sources"`
	Catalogs         map[string]CatalogSourceStatus `json:"catalogs"`
	ChangedProviders []string                       `json:"changed_providers"`
}

func (u *catalogUpdater) refresh(ctx context.Context, source string, generation uint64) {
	u.mu.Lock()
	u.requestSeq++
	seq := u.requestSeq
	u.mu.Unlock()

	data, _, errFetch := u.fetchCatalog(ctx, source)
	if errFetch != nil {
		if ctx.Err() == nil {
			log.Warn("model catalog refresh failed; keeping last valid catalog")
		}
		return
	}
	u.mu.Lock()
	if generation != u.generation || seq <= u.publishedSeq || ctx.Err() != nil {
		u.mu.Unlock()
		return
	}
	changed, errPublish := u.publish(data)
	if errPublish == nil {
		u.publishedSeq = seq
	}
	u.mu.Unlock()
	if errPublish != nil {
		log.Warn("model catalog rejected; keeping last valid catalog")
		return
	}
	notifyModelRefresh(changed)
}

func (u *catalogUpdater) refreshOnDemand(ctx context.Context, fallbackSource string, defaultURLs []string) (CatalogSourceStatus, error) {
	u.mu.Lock()
	source := u.source
	updaterActive := u.cancel != nil && u.ctx != nil && u.ctx.Err() == nil
	if !updaterActive {
		if u.ctx != nil || source == "" {
			source = fallbackSource
		}
	}
	generation := u.generation
	var watchCtx context.Context
	if updaterActive {
		watchCtx = u.ctx
	}

	displaySource := source
	if displaySource == "" && len(defaultURLs) > 0 {
		displaySource = defaultURLs[0]
	}
	if source == "disabled" || source == embeddedCatalogSource {
		u.mu.Unlock()
		return CatalogSourceStatus{
			Source:  displaySource,
			Skipped: true,
		}, nil
	}

	if ctx == nil {
		ctx = context.Background()
	}
	if errCtx := ctx.Err(); errCtx != nil {
		u.mu.Unlock()
		return CatalogSourceStatus{
			Source: displaySource,
			Error:  errCtx.Error(),
		}, errCtx
	}
	u.requestSeq++
	seq := u.requestSeq
	u.mu.Unlock()

	fetchCtx := ctx
	if watchCtx != nil {
		var cancelFetch context.CancelFunc
		fetchCtx, cancelFetch = context.WithCancel(ctx)
		stopWatch := context.AfterFunc(watchCtx, cancelFetch)
		defer func() {
			stopWatch()
			cancelFetch()
		}()
	}

	data, resolvedSource, errFetch := u.fetchCatalog(fetchCtx, source)
	if resolvedSource != "" {
		displaySource = resolvedSource
	}
	if errFetch != nil {
		if fetchCtx.Err() == nil {
			log.Warn("model catalog refresh failed; keeping last valid catalog")
		}
		return CatalogSourceStatus{
			Source: displaySource,
			Error:  errFetch.Error(),
		}, errFetch
	}

	u.mu.Lock()
	if generation != u.generation {
		u.mu.Unlock()
		errStale := fmt.Errorf("model catalog source changed during refresh")
		return CatalogSourceStatus{
			Source: displaySource,
			Error:  errStale.Error(),
		}, errStale
	}
	if errCtx := fetchCtx.Err(); errCtx != nil {
		u.mu.Unlock()
		return CatalogSourceStatus{
			Source: displaySource,
			Error:  errCtx.Error(),
		}, errCtx
	}
	if seq <= u.publishedSeq {
		u.mu.Unlock()
		return CatalogSourceStatus{
			Source:    displaySource,
			Refreshed: true,
		}, nil
	}
	changed, errPublish := u.publish(data)
	if errPublish == nil {
		u.publishedSeq = seq
	}
	u.mu.Unlock()
	if errPublish != nil {
		log.Warn("model catalog rejected; keeping last valid catalog")
		return CatalogSourceStatus{
			Source: displaySource,
			Error:  errPublish.Error(),
		}, errPublish
	}
	notifyModelRefresh(changed)
	return CatalogSourceStatus{
		Source:           displaySource,
		Refreshed:        true,
		ChangedProviders: changed,
	}, nil
}

func readCatalogSource(ctx context.Context, source string) ([]byte, error) {
	var reader io.ReadCloser
	if filepath.IsAbs(source) {
		file, errOpen := os.Open(source)
		if errOpen != nil {
			return nil, errOpen
		}
		reader = file
	} else {
		req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, source, nil)
		if errRequest != nil {
			return nil, errRequest
		}
		resp, errDo := http.DefaultClient.Do(req)
		if errDo != nil {
			return nil, errDo
		}
		reader = resp.Body
		if resp.StatusCode != http.StatusOK {
			if errClose := reader.Close(); errClose != nil {
				log.Debug("catalog response close failed")
			}
			return nil, fmt.Errorf("catalog HTTP status %d", resp.StatusCode)
		}
	}
	defer func() {
		if errClose := reader.Close(); errClose != nil {
			log.Debug("catalog reader close failed")
		}
	}()
	data, errRead := io.ReadAll(io.LimitReader(reader, maxCodexClientModelsSize+1))
	if errRead != nil {
		return nil, errRead
	}
	if len(data) > maxCodexClientModelsSize {
		return nil, fmt.Errorf("catalog exceeds size limit")
	}
	return data, nil
}

func catalogFetcherWithSource(embedded []byte, urls []string, validate func([]byte) error) func(context.Context, string) ([]byte, string, error) {
	return func(ctx context.Context, source string) ([]byte, string, error) {
		if source == embeddedCatalogSource {
			return embedded, embeddedCatalogSource, nil
		}
		sources := urls
		if source != "" {
			sources = []string{source}
		}
		for _, candidate := range sources {
			data, errRead := readCatalogSource(ctx, candidate)
			if errRead == nil && validate(data) == nil {
				return data, candidate, nil
			}
			if ctx.Err() != nil {
				break
			}
		}
		var fallbackCandidate string
		if len(sources) > 0 {
			fallbackCandidate = sources[0]
		}
		return nil, fallbackCandidate, fmt.Errorf("no valid model catalog source")
	}
}

func catalogFetcher(embedded []byte, urls []string, validate func([]byte) error) func(context.Context, string) ([]byte, error) {
	fetchWithSource := catalogFetcherWithSource(embedded, urls, validate)
	return func(ctx context.Context, source string) ([]byte, error) {
		data, _, err := fetchWithSource(ctx, source)
		return data, err
	}
}

func validateCatalogBytes(data []byte) error {
	var parsed staticModelsJSON
	if errUnmarshal := json.Unmarshal(data, &parsed); errUnmarshal != nil {
		return errUnmarshal
	}
	return validateModelsCatalog(&parsed)
}

func publishCatalogBytes(data []byte) ([]string, error) {
	var parsed staticModelsJSON
	if errUnmarshal := json.Unmarshal(data, &parsed); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	if errValidate := validateModelsCatalog(&parsed); errValidate != nil {
		return nil, errValidate
	}
	old := getModels()
	if len(parsed.Meta) == 0 && old != nil {
		parsed.Meta = old.Meta
	}
	changed := detectChangedProviders(old, &parsed)
	modelsCatalogStore.mu.Lock()
	modelsCatalogStore.data = &parsed
	modelsCatalogStore.mu.Unlock()
	return changed, nil
}

func dynamicCatalogFetcherWithSource(embedded []byte, urls *[]string, validate func([]byte) error) func(context.Context, string) ([]byte, string, error) {
	return func(ctx context.Context, source string) ([]byte, string, error) {
		var currentURLs []string
		if urls != nil {
			currentURLs = *urls
		}
		return catalogFetcherWithSource(embedded, currentURLs, validate)(ctx, source)
	}
}

var generalCatalogUpdater = &catalogUpdater{
	fetchWithSource: dynamicCatalogFetcherWithSource(embeddedModelsJSON, &modelsURLs, validateCatalogBytes),
	publish:         publishCatalogBytes,
}
var codexCatalogUpdater = &catalogUpdater{
	fetchWithSource: dynamicCatalogFetcherWithSource(embeddedCodexClientModelsJSON, &codexClientModelsURLs, ValidateCodexClientModelsJSON),
	publish: func(data []byte) ([]string, error) {
		_, errLoad := loadCodexClientModelsFromBytes(data, "catalog")
		return nil, errLoad
	},
}
var devinCatalogUpdater = &catalogUpdater{
	fetchWithSource: dynamicCatalogFetcherWithSource(embeddedDevinModelsJSON, &devinModelsURLs, validateDevinCatalogBytes),
	publish: func(data []byte) ([]string, error) {
		changed, errLoad := loadDevinModelsFromBytes(data, "catalog")
		if errLoad == nil && changed {
			return []string{"devin"}, nil
		}
		return nil, errLoad
	},
}

var catalogRuntime struct {
	sync.Mutex
	ctx     context.Context
	local   bool
	sources CatalogSources
	home    bool
}

// SetLocalModelCatalogs sets the process default before the service starts.
// Explicit catalog sources still take precedence over this default.
func SetLocalModelCatalogs(local bool) {
	catalogRuntime.Lock()
	defer catalogRuntime.Unlock()
	catalogRuntime.local = local
}

// StartModelCatalogUpdaters binds process-wide readers to the service lifetime.
func StartModelCatalogUpdaters(ctx context.Context, sources CatalogSources, home bool) {
	catalogRuntime.Lock()
	defer catalogRuntime.Unlock()
	catalogRuntime.ctx = ctx
	catalogRuntime.sources = sources
	catalogRuntime.home = home
	configureCatalogs(ctx, sources, catalogRuntime.local, home)
}

// UpdateModelCatalogSources updates an already-started process policy on reload.
// Updates before service startup or after shutdown do not start readers.
func UpdateModelCatalogSources(sources CatalogSources, home bool) {
	catalogRuntime.Lock()
	defer catalogRuntime.Unlock()
	catalogRuntime.sources = sources
	catalogRuntime.home = home
	if catalogRuntime.ctx != nil && catalogRuntime.ctx.Err() == nil {
		configureCatalogs(catalogRuntime.ctx, sources, catalogRuntime.local, home)
	}
}

// RefreshModelCatalogs triggers an immediate on-demand refresh across all model catalog updaters.
func RefreshModelCatalogs(ctx context.Context) (CatalogRefreshResult, error) {
	catalogRuntime.Lock()
	sources := catalogRuntime.sources
	home := catalogRuntime.home
	catalogRuntime.Unlock()
	return RefreshModelCatalogsWithPolicy(ctx, sources, home)
}

// RefreshModelCatalogsWithPolicy triggers an immediate on-demand refresh using fallbackSources
// and fallbackHome when process-wide catalog updaters have not been started.
func RefreshModelCatalogsWithPolicy(ctx context.Context, fallbackSources CatalogSources, fallbackHome bool) (CatalogRefreshResult, error) {
	if errValidate := fallbackSources.Validate(); errValidate != nil {
		return CatalogRefreshResult{
			Catalogs:         map[string]CatalogSourceStatus{},
			ChangedProviders: []string{},
		}, errValidate
	}
	catalogRuntime.Lock()
	local := catalogRuntime.local
	if catalogRuntime.ctx != nil && catalogRuntime.ctx.Err() == nil {
		fallbackSources = catalogRuntime.sources
		fallbackHome = catalogRuntime.home
	}
	catalogRuntime.Unlock()

	effectiveFallback := effectiveCatalogSources(fallbackSources, local, fallbackHome)
	var (
		generalStatus CatalogSourceStatus
		codexStatus   CatalogSourceStatus
		devinStatus   CatalogSourceStatus
		errGeneral    error
		errCodex      error
		errDevin      error
		wg            sync.WaitGroup
	)
	wg.Add(3)
	go func() {
		defer wg.Done()
		generalStatus, errGeneral = generalCatalogUpdater.refreshOnDemand(ctx, effectiveFallback.Catalog, modelsURLs)
	}()
	go func() {
		defer wg.Done()
		codexStatus, errCodex = codexCatalogUpdater.refreshOnDemand(ctx, effectiveFallback.CodexCatalog, codexClientModelsURLs)
	}()
	go func() {
		defer wg.Done()
		devinStatus, errDevin = devinCatalogUpdater.refreshOnDemand(ctx, effectiveFallback.DevinCatalog, devinModelsURLs)
	}()
	wg.Wait()

	var changed []string
	changed = mergeProviderNames(changed, generalStatus.ChangedProviders)
	changed = mergeProviderNames(changed, codexStatus.ChangedProviders)
	changed = mergeProviderNames(changed, devinStatus.ChangedProviders)
	if changed == nil {
		changed = []string{}
	}

	result := CatalogRefreshResult{
		Catalog:      generalStatus.Source,
		CodexCatalog: codexStatus.Source,
		DevinCatalog: devinStatus.Source,
		Sources: CatalogSources{
			Catalog:      generalStatus.Source,
			CodexCatalog: codexStatus.Source,
			DevinCatalog: devinStatus.Source,
		},
		Catalogs: map[string]CatalogSourceStatus{
			"catalog":       generalStatus,
			"codex-catalog": codexStatus,
			"devin-catalog": devinStatus,
		},
		ChangedProviders: changed,
	}
	return result, errors.Join(errGeneral, errCodex, errDevin)
}

func configureCatalogs(ctx context.Context, sources CatalogSources, local, home bool) {
	if errValidate := sources.Validate(); errValidate != nil {
		log.WithError(errValidate).Warn("invalid catalog sources")
		return
	}
	sources = effectiveCatalogSources(sources, local, home)
	generalCatalogUpdater.configure(ctx, sources.Catalog)
	codexCatalogUpdater.configure(ctx, sources.CodexCatalog)
	devinCatalogUpdater.configure(ctx, sources.DevinCatalog)
}

func effectiveCatalogSources(sources CatalogSources, local, home bool) CatalogSources {
	selectSource := func(source string) string {
		if source == "" && local {
			return embeddedCatalogSource
		}
		return source
	}
	general := selectSource(sources.Catalog)
	devin := selectSource(sources.DevinCatalog)
	if home {
		general = "disabled"
		devin = "disabled"
	}
	return CatalogSources{Catalog: general, CodexCatalog: selectSource(sources.CodexCatalog), DevinCatalog: devin}
}

func validateDevinCatalogBytes(data []byte) error {
	_, errValidate := ValidateDevinModelsJSON(data)
	return errValidate
}
