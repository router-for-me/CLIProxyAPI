package registry

// proxx: price catalog updater. It reuses the model catalog updater machinery
// (generation tracking, 3h refresh, "embed"/"disabled" sources, keep-last-good on failure)
// for the proxx pricing catalog without touching the upstream catalog wiring.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
)

// maxPriceCatalogSize caps a remote price catalog download. models.dev api.json is
// several MB and grows, so it gets a larger cap than model catalogs.
const maxPriceCatalogSize = 64 << 20

// PriceCatalogUpdater refreshes a price catalog from an embedded snapshot, an http(s)
// URL, or an absolute file path. It is independent from the model catalogs.
type PriceCatalogUpdater struct {
	updater *catalogUpdater
}

// NewPriceCatalogUpdater builds an updater. defaultURLs are tried when the source is "".
// publish must parse and store the catalog; a returned error keeps the last good catalog.
func NewPriceCatalogUpdater(embedded []byte, defaultURLs []string, validate func([]byte) error, publish func(data []byte) error) *PriceCatalogUpdater {
	p := &PriceCatalogUpdater{}
	fetch := func(ctx context.Context, source string) ([]byte, string, error) {
		if source == embeddedCatalogSource {
			return embedded, embeddedCatalogSource, nil
		}
		sources := defaultURLs
		if source != "" {
			sources = []string{source}
		}
		var lastErr error
		for _, candidate := range sources {
			data, errRead := readPriceCatalogSource(ctx, candidate)
			if errRead == nil {
				errRead = validate(data)
			}
			if errRead == nil {
				return data, candidate, nil
			}
			lastErr = errRead
			if ctx.Err() != nil {
				break
			}
		}
		if lastErr == nil {
			lastErr = fmt.Errorf("no price catalog source")
		}
		return nil, "", lastErr
	}
	p.updater = &catalogUpdater{
		fetchWithSource: fetch,
		publish: func(data []byte) ([]string, error) {
			// Never report changed providers: price updates must not trigger model refreshes.
			return nil, publish(data)
		},
	}
	return p
}

// Configure (re)starts the updater for source. "" uses the default URLs, or the embedded
// snapshot when the process runs with --local-model. Unchanged sources are a no-op.
func (p *PriceCatalogUpdater) Configure(ctx context.Context, source string) {
	if p == nil || p.updater == nil || ctx == nil {
		return
	}
	if source == "" && LocalModelCatalogs() {
		source = embeddedCatalogSource
	}
	p.updater.configure(ctx, source)
}

// LocalModelCatalogs reports whether the process runs with --local-model.
func LocalModelCatalogs() bool {
	catalogRuntime.Lock()
	defer catalogRuntime.Unlock()
	return catalogRuntime.local
}

func readPriceCatalogSource(ctx context.Context, source string) ([]byte, error) {
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
			_ = reader.Close()
			return nil, fmt.Errorf("price catalog HTTP status %d", resp.StatusCode)
		}
	}
	defer func() { _ = reader.Close() }()
	data, errRead := io.ReadAll(io.LimitReader(reader, maxPriceCatalogSize+1))
	if errRead != nil {
		return nil, errRead
	}
	if len(data) > maxPriceCatalogSize {
		return nil, fmt.Errorf("price catalog exceeds size limit")
	}
	return data, nil
}
