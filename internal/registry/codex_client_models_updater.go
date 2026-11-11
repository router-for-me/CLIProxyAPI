package registry

import (
	"context"

	log "github.com/sirupsen/logrus"
)

const maxCodexClientModelsSize = 8 << 20

var codexClientModelsURLs = []string{
	"https://raw.githubusercontent.com/router-for-me/models/refs/heads/main/codex_client_models.json",
	"https://models.router-for.me/codex_client_models.json",
}

// StartCodexClientModelsUpdater starts a background updater that fetches the
// Codex client model catalog immediately and then refreshes it every 3 hours.
// Safe to call multiple times; only one updater will run.
func StartCodexClientModelsUpdater(ctx context.Context) {
	codexCatalogUpdater.configure(ctx, "")
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
	for _, source := range codexClientModelsURLs {
		data, errRead := readCatalogSource(ctx, source)
		if errRead == nil && ValidateCodexClientModelsJSON(data) == nil {
			return data, source
		}
		if ctx.Err() != nil {
			break
		}
	}
	return nil, ""
}
