package registry

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const embeddedCatalogSource = "embed"

// catalogUpdater serializes source changes with publication, not network I/O.
// Cancellation saves work; the generation check also protects against readers
// that finish after cancellation (including local file reads).
type catalogUpdater struct {
	mu           sync.Mutex
	source       string
	overlay      string
	generation   uint64
	cancel       context.CancelFunc
	ctx          context.Context
	parentDone   <-chan struct{}
	fallbackBase []byte
	lastBase     []byte
	fetch        func(context.Context, string) ([]byte, error)
	merge        func(ctx context.Context, base []byte, overlay string) ([]byte, error)
	publish      func([]byte) ([]string, error)
}

func (u *catalogUpdater) configure(ctx context.Context, source string) {
	u.configureWithOverlay(ctx, source, "")
}

// configureWithOverlay restarts the reader when either the base source or the overlay changes.
func (u *catalogUpdater) configureWithOverlay(ctx context.Context, source, overlay string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.cancel != nil && u.source == source && u.overlay == overlay && u.parentDone == ctx.Done() && u.ctx.Err() == nil {
		return
	}
	if u.cancel != nil {
		u.cancel()
	}
	u.generation++
	u.source = source
	u.overlay = overlay
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
		if source == embeddedCatalogSource && overlay == "" {
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

func (u *catalogUpdater) refresh(ctx context.Context, source string, generation uint64) {
	fetchedBase, errFetch := u.fetch(ctx, source)
	u.mu.Lock()
	overlay, current := u.overlay, generation == u.generation
	lastBase := u.lastBase
	if len(lastBase) == 0 {
		lastBase = u.fallbackBase
	}
	if len(lastBase) == 0 {
		lastBase = embeddedModelsJSON
	}
	u.mu.Unlock()
	if !current || ctx.Err() != nil {
		return
	}
	baseData := fetchedBase
	if errFetch != nil {
		if overlay == "" || u.merge == nil || len(lastBase) == 0 {
			if ctx.Err() == nil {
				log.Warn("model catalog refresh failed; keeping last valid catalog")
			}
			return
		}
		if ctx.Err() == nil {
			log.Warn("model catalog base fetch failed; applying catalog overlay to last valid base")
		}
		baseData = lastBase
	}
	data := baseData
	if overlay != "" && u.merge != nil {
		var errMerge error
		data, errMerge = u.merge(ctx, baseData, overlay)
		if errMerge != nil {
			if ctx.Err() == nil {
				log.Warn("model catalog refresh failed; keeping last valid catalog")
			}
			return
		}
	}
	u.mu.Lock()
	if generation != u.generation || ctx.Err() != nil {
		u.mu.Unlock()
		return
	}
	changed, errPublish := u.publish(data)
	if errPublish == nil && errFetch == nil {
		u.lastBase = fetchedBase
	}
	u.mu.Unlock()
	if errPublish != nil {
		log.Warn("model catalog rejected; keeping last valid catalog")
		return
	}
	if len(changed) > 0 {
		log.Infof("model catalog updated; changed providers: %v", changed)
	}
	notifyModelRefresh(changed)
}

// mergeCatalogOverlay appends overlay entries whose id is missing from the same base section.
// Base (official or configured) entries always win; unknown fields are preserved verbatim.
func mergeCatalogOverlay(ctx context.Context, base []byte, overlay string) ([]byte, error) {
	extra, errRead := readCatalogSource(ctx, overlay)
	if errRead != nil {
		return nil, fmt.Errorf("catalog overlay: %w", errRead)
	}
	var baseDoc map[string]json.RawMessage
	if errUnmarshal := json.Unmarshal(base, &baseDoc); errUnmarshal != nil {
		return nil, errUnmarshal
	}
	var extraDoc map[string][]json.RawMessage
	if errUnmarshal := json.Unmarshal(extra, &extraDoc); errUnmarshal != nil {
		return nil, fmt.Errorf("catalog overlay: %w", errUnmarshal)
	}
	for section, entries := range extraDoc {
		var rows []json.RawMessage
		if raw, ok := baseDoc[section]; ok && len(raw) > 0 && string(raw) != "null" {
			if errUnmarshal := json.Unmarshal(raw, &rows); errUnmarshal != nil {
				return nil, fmt.Errorf("catalog section %s: %w", section, errUnmarshal)
			}
		}
		have := make(map[string]struct{}, len(rows)+len(entries))
		for _, row := range rows {
			have[catalogEntryID(row)] = struct{}{}
		}
		for _, entry := range entries {
			id := catalogEntryID(entry)
			if id == "" {
				return nil, fmt.Errorf("catalog overlay: %s entry without id", section)
			}
			if _, exists := have[id]; exists {
				continue
			}
			rows = append(rows, entry)
			have[id] = struct{}{}
		}
		merged, errMarshal := json.Marshal(rows)
		if errMarshal != nil {
			return nil, errMarshal
		}
		baseDoc[section] = merged
	}
	return json.Marshal(baseDoc)
}

func catalogEntryID(raw json.RawMessage) string {
	var entry struct {
		ID string `json:"id"`
	}
	if json.Unmarshal(raw, &entry) != nil {
		return ""
	}
	return strings.TrimSpace(entry.ID)
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

func catalogFetcher(embedded []byte, urls []string, validate func([]byte) error) func(context.Context, string) ([]byte, error) {
	return func(ctx context.Context, source string) ([]byte, error) {
		if source == embeddedCatalogSource {
			return embedded, nil
		}
		sources := urls
		if source != "" {
			sources = []string{source}
		}
		for _, candidate := range sources {
			data, errRead := readCatalogSource(ctx, candidate)
			if errRead == nil && validate(data) == nil {
				return data, nil
			}
			if ctx.Err() != nil {
				break
			}
		}
		return nil, fmt.Errorf("no valid model catalog source")
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

var generalCatalogUpdater = &catalogUpdater{
	fallbackBase: embeddedModelsJSON,
	fetch:        catalogFetcher(embeddedModelsJSON, modelsURLs, validateCatalogBytes),
	merge:        mergeCatalogOverlay,
	publish:      publishCatalogBytes,
}
var codexCatalogUpdater = &catalogUpdater{
	fetch: catalogFetcher(embeddedCodexClientModelsJSON, codexClientModelsURLs, ValidateCodexClientModelsJSON),
	publish: func(data []byte) ([]string, error) {
		_, errLoad := loadCodexClientModelsFromBytes(data, "catalog")
		return nil, errLoad
	},
}
var devinCatalogUpdater = &catalogUpdater{
	fetch: catalogFetcher(embeddedDevinModelsJSON, devinModelsURLs, validateDevinCatalogBytes),
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
	ctx   context.Context
	local bool
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
	configureCatalogs(ctx, sources, catalogRuntime.local, home)
}

// UpdateModelCatalogSources updates an already-started process policy on reload.
// Updates before service startup or after shutdown do not start readers.
func UpdateModelCatalogSources(sources CatalogSources, home bool) {
	catalogRuntime.Lock()
	defer catalogRuntime.Unlock()
	if catalogRuntime.ctx != nil && catalogRuntime.ctx.Err() == nil {
		configureCatalogs(catalogRuntime.ctx, sources, catalogRuntime.local, home)
	}
}

func configureCatalogs(ctx context.Context, sources CatalogSources, local, home bool) {
	if errValidate := sources.Validate(); errValidate != nil {
		log.WithError(errValidate).Warn("invalid catalog sources")
		return
	}
	sources = effectiveCatalogSources(sources, local, home)
	generalCatalogUpdater.configureWithOverlay(ctx, sources.Catalog, sources.CatalogOverlay)
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
	return CatalogSources{Catalog: general, CatalogOverlay: sources.CatalogOverlay, CodexCatalog: selectSource(sources.CodexCatalog), DevinCatalog: devin}
}

func validateDevinCatalogBytes(data []byte) error {
	_, errValidate := ValidateDevinModelsJSON(data)
	return errValidate
}
