package cliproxy

import (
	"context"
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
	tokenData := extractKiroTokenData(auth)
	if tokenData == nil || tokenData.AccessToken == "" {
		return registry.GetKiroModels()
	}
	kiro := kiroauth.NewKiroAuth(s.cfg)
	if kiro == nil {
		return registry.GetKiroModels()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	apiModels, err := kiro.ListAvailableModels(ctx, tokenData)
	if err != nil || len(apiModels) == 0 {
		if err != nil {
			log.Warnf("kiro: failed to fetch dynamic models: %v, using static models", err)
		}
		return registry.GetKiroModels()
	}
	models := convertKiroAPIModels(apiModels)
	if kirocommon.IsSystemPromptInjectEnabled() {
		models = generateKiroAgenticVariants(models)
	}
	return models
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
