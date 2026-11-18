package registry

import (
	_ "embed"
	"strings"

	"github.com/MoeForge/moego/json"
	log "github.com/sirupsen/logrus"
)

// embeddedForkModelsJSON lists Claude models this fork serves that the
// upstream catalog omits, such as access-restricted Mythos models. They are
// added to every general catalog this process loads, embedded or remote, and
// never replace an entry the catalog already defines.
//
//go:embed models/fork_models.json
var embeddedForkModelsJSON []byte

func addForkModels(data *staticModelsJSON) {
	var fork staticModelsJSON
	if errUnmarshal := json.Unmarshal(embeddedForkModelsJSON, &fork); errUnmarshal != nil {
		log.WithError(errUnmarshal).Warn("registry: failed to parse fork_models.json, skipping fork models")
		return
	}
	data.Claude = appendMissingModels(data.Claude, fork.Claude)
}

func appendMissingModels(models, extra []*ModelInfo) []*ModelInfo {
	seen := make(map[string]struct{}, len(models))
	for _, model := range models {
		if model != nil {
			seen[strings.TrimSpace(model.ID)] = struct{}{}
		}
	}
	for _, model := range extra {
		if model == nil {
			continue
		}
		id := strings.TrimSpace(model.ID)
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		models = append(models, model)
	}
	return models
}
