package executor

import (
	"context"
	"encoding/json"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/auth/joycode"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

func FetchJoyCodeModels(ctx context.Context, auth *cliproxyauth.Auth, cfg *config.Config) []*registry.ModelInfo {
	ptKey, _ := auth.Metadata["ptKey"].(string)
	if ptKey == "" {
		log.Debug("joycode: no ptKey found, using static models")
		return getStaticJoyCodeModels()
	}

	jcAuth := joycode.NewJoyCodeAuth(nil)

	modelData, err := jcAuth.FetchModelList(ctx, ptKey)
	if err != nil {
		log.Warnf("joycode: failed to fetch model list: %v, using static models", err)
		return getStaticJoyCodeModels()
	}

	now := time.Now().Unix()
	var models []*registry.ModelInfo

	raw, _ := json.Marshal(modelData)
	result := gjson.ParseBytes(raw)
	result.ForEach(func(key, value gjson.Result) bool {
		if value.Get("hidden").Bool() {
			return true
		}

		modelID := value.Get("chatApiModel").String()
		if modelID == "" {
			modelID = value.Get("label").String()
		}
		if modelID == "" {
			return true
		}

		models = append(models, &registry.ModelInfo{
			ID:          modelID,
			Object:      "model",
			Created:     now,
			OwnedBy:     "joycode",
			Type:        "joycode",
			DisplayName: value.Get("label").String(),
		})
		return true
	})

	if len(models) == 0 {
		log.Warn("joycode: model list returned no visible models, using static models")
		return getStaticJoyCodeModels()
	}

	log.Infof("joycode: fetched %d models from API", len(models))
	return models
}

func getStaticJoyCodeModels() []*registry.ModelInfo {
	now := time.Now().Unix()
	return []*registry.ModelInfo{
		{
			ID:          "JoyAI-Code",
			Object:      "model",
			Created:     now,
			OwnedBy:     "joycode",
			Type:        "joycode",
			DisplayName: "JoyAI Code",
		},
	}
}
