package registry

import (
	"context"
	"io"
	"net/http"
	"sync"
	"time"

	log "github.com/sirupsen/logrus"
)

const maxCodexClientModelsSize = 8 << 20

var codexClientModelsURLs = []string{
	"https://raw.githubusercontent.com/router-for-me/models/refs/heads/main/codex_client_models.json",
	"https://models.router-for.me/codex_client_models.json",
}

var codexClientModelsUpdaterOnce sync.Once

// StartCodexClientModelsUpdater starts a background updater that fetches the
// Codex client model catalog immediately and then refreshes it every 3 hours.
// Safe to call multiple times; only one updater will run.
func StartCodexClientModelsUpdater(ctx context.Context) {
	codexClientModelsUpdaterOnce.Do(func() {
		go runCodexClientModelsUpdater(ctx)
	})
}

func runCodexClientModelsUpdater(ctx context.Context) {
	tryRefreshCodexClientModels(ctx, "startup Codex client model refresh")

	ticker := time.NewTicker(modelsRefreshInterval)
	defer ticker.Stop()
	log.Infof("periodic Codex client model refresh started (interval=%s)", modelsRefreshInterval)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			tryRefreshCodexClientModels(ctx, "periodic Codex client model refresh")
		}
	}
}

func tryRefreshCodexClientModels(ctx context.Context, label string) {
	data, sourceURL := fetchCodexClientModelsFromRemote(ctx)
	if data == nil {
		log.Warnf("%s: fetch failed from all URLs, keeping current data", label)
		return
	}

	changed, err := loadCodexClientModelsFromBytes(data, sourceURL)
	if err != nil {
		log.Warnf("%s: fetched catalog rejected, keeping current data: %v", label, err)
		return
	}
	if !changed {
		log.Infof("%s completed from %s, no changes detected", label, sourceURL)
		return
	}
	log.Infof("%s completed from %s, catalog updated", label, sourceURL)
}

func fetchCodexClientModelsFromRemote(ctx context.Context) ([]byte, string) {
	client := &http.Client{Timeout: modelsFetchTimeout}
	for _, sourceURL := range codexClientModelsURLs {
		reqCtx, cancel := context.WithTimeout(ctx, modelsFetchTimeout)
		req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, sourceURL, nil)
		if err != nil {
			cancel()
			log.Debugf("Codex client models fetch request creation failed for %s: %v", sourceURL, err)
			continue
		}

		resp, err := client.Do(req)
		if err != nil {
			cancel()
			log.Debugf("Codex client models fetch failed from %s: %v", sourceURL, err)
			continue
		}
		if resp.StatusCode != http.StatusOK {
			if errClose := resp.Body.Close(); errClose != nil {
				log.Debugf("Codex client models response close failed for %s: %v", sourceURL, errClose)
			}
			cancel()
			log.Debugf("Codex client models fetch returned %d from %s", resp.StatusCode, sourceURL)
			continue
		}

		data, errRead := io.ReadAll(io.LimitReader(resp.Body, maxCodexClientModelsSize+1))
		errClose := resp.Body.Close()
		cancel()
		if errRead != nil {
			log.Debugf("Codex client models fetch read error from %s: %v", sourceURL, errRead)
			continue
		}
		if errClose != nil {
			log.Debugf("Codex client models response close failed for %s: %v", sourceURL, errClose)
			continue
		}
		if len(data) > maxCodexClientModelsSize {
			log.Warnf("Codex client models fetch from %s exceeded %d bytes", sourceURL, maxCodexClientModelsSize)
			continue
		}
		if err := ValidateCodexClientModelsJSON(data); err != nil {
			log.Warnf("Codex client models validate failed from %s: %v", sourceURL, err)
			continue
		}
		return data, sourceURL
	}
	return nil, ""
}
