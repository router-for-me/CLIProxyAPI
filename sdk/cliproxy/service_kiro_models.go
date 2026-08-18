package cliproxy

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	kiroauth "github.com/router-for-me/CLIProxyAPI/v7/internal/auth/kiro"
	"github.com/router-for-me/CLIProxyAPI/v7/internal/registry"
	kirocommon "github.com/router-for-me/CLIProxyAPI/v7/internal/translator/kiro/common"
	coreauth "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
	log "github.com/sirupsen/logrus"
)

func (s *Service) fetchKiroModels(auth *coreauth.Auth) []*ModelInfo {
	return s.fetchKiroModelsContext(context.Background(), auth)
}

func (s *Service) fetchKiroModelsContext(ctx context.Context, auth *coreauth.Auth) []*ModelInfo {
	tokenData := extractKiroTokenData(auth)
	if tokenData == nil || tokenData.AccessToken == "" {
		return registry.GetKiroModels()
	}
	if ctx == nil {
		ctx = context.Background()
	}

	fetch := func(fetchCtx context.Context) ([]*ModelInfo, error) {
		return s.fetchKiroBaseModelsFromAPI(fetchCtx, tokenData)
	}

	var (
		models []*ModelInfo
		err    error
	)
	if key := kiroModelsCacheKey(auth); key != "" {
		models, err = s.kiroModelsCache.get(ctx, key, fetch)
	} else {
		models, err = fetch(ctx)
	}
	if err != nil {
		if len(models) > 0 {
			log.Warnf("kiro: failed to refresh dynamic models: %v, using last-good models", err)
		} else {
			log.Warnf("kiro: failed to fetch dynamic models: %v, using static models", err)
		}
	}
	if len(models) == 0 {
		return registry.GetKiroModels()
	}
	if kirocommon.IsSystemPromptInjectEnabled() {
		models = generateKiroAgenticVariants(models)
	}
	return models
}

func (s *Service) fetchKiroBaseModelsFromAPI(ctx context.Context, tokenData *kiroauth.KiroTokenData) ([]*ModelInfo, error) {
	kiro := kiroauth.NewKiroAuth(s.cfg)
	if kiro == nil {
		return nil, errors.New("failed to create KiroAuth instance")
	}
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	apiModels, err := kiro.ListAvailableModels(ctx, tokenData)
	if err != nil {
		return nil, err
	}
	if len(apiModels) == 0 {
		return nil, errKiroModelsEmpty
	}
	return convertKiroAPIModels(apiModels), nil
}

func extractKiroTokenData(auth *coreauth.Auth) *kiroauth.KiroTokenData {
	if auth == nil {
		return nil
	}
	accessToken := authString(auth, "access_token")
	if accessToken == "" {
		return nil
	}
	return &kiroauth.KiroTokenData{
		AccessToken:  accessToken,
		ProfileArn:   authString(auth, "profile_arn"),
		RefreshToken: authString(auth, "refresh_token"),
	}
}

func authString(auth *coreauth.Auth, key string) string {
	if auth.Attributes != nil {
		if value := strings.TrimSpace(auth.Attributes[key]); value != "" {
			return value
		}
	}
	if auth.Metadata != nil {
		if value, ok := auth.Metadata[key].(string); ok {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func convertKiroAPIModels(apiModels []*kiroauth.KiroModel) []*ModelInfo {
	now := time.Now().Unix()
	models := make([]*ModelInfo, 0, len(apiModels))
	for _, model := range apiModels {
		if model == nil || strings.TrimSpace(model.ModelID) == "" {
			continue
		}
		contextLength := model.MaxInputTokens
		if contextLength <= 0 {
			contextLength = 200000
		}
		models = append(models, &ModelInfo{
			ID:                  "kiro-" + normalizeKiroModelID(model.ModelID),
			Object:              "model",
			Created:             now,
			OwnedBy:             "aws",
			Type:                "kiro",
			DisplayName:         formatKiroDisplayName(model.ModelName, model.RateMultiplier),
			Description:         model.Description,
			ContextLength:       contextLength,
			MaxCompletionTokens: 64000,
			Thinking:            &registry.ThinkingSupport{Min: 1024, Max: 32000, ZeroAllowed: true, DynamicAllowed: true},
		})
	}
	return models
}

func normalizeKiroModelID(modelID string) string {
	modelID = strings.TrimPrefix(modelID, "anthropic.")
	modelID = strings.TrimPrefix(modelID, "amazon.")
	modelID = strings.NewReplacer(".", "-", "_", "-").Replace(modelID)
	return strings.ToLower(modelID)
}

func formatKiroDisplayName(modelName string, rateMultiplier float64) string {
	if modelName == "" {
		return ""
	}
	displayName := "Kiro " + modelName
	if rateMultiplier > 0 && rateMultiplier != 1 {
		displayName += fmt.Sprintf(" (%.1fx credit)", rateMultiplier)
	}
	return displayName
}

func generateKiroAgenticVariants(models []*ModelInfo) []*ModelInfo {
	result := append([]*ModelInfo(nil), models...)
	for _, model := range models {
		if model == nil || strings.HasSuffix(model.ID, "-agentic") || strings.Contains(model.ID, "-auto") {
			continue
		}
		agentic := *model
		agentic.ID += "-agentic"
		agentic.DisplayName += " (Agentic)"
		agentic.Description += " - Optimized for coding agents (chunked writes)"
		if model.Thinking != nil {
			thinking := *model.Thinking
			agentic.Thinking = &thinking
		}
		result = append(result, &agentic)
	}
	return result
}
