package kiro

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	log "github.com/sirupsen/logrus"
)

// maxKiroModelCatalogSize caps the ListAvailableModels response body so a
// malformed or hostile upstream reply cannot exhaust memory.
const maxKiroModelCatalogSize = 4 << 20

// KiroModel describes a single entry of the Kiro ListAvailableModels catalog.
type KiroModel struct {
	ModelID         string
	ModelName       string
	Description     string
	MaxInputTokens  int
	MaxOutputTokens int
	RateMultiplier  float64
	RateUnit        string
}

// kiroModelCatalogResponse mirrors the ListAvailableModels payload shape.
type kiroModelCatalogResponse struct {
	DefaultModel *kiroModelCatalogEntry  `json:"defaultModel"`
	Models       []kiroModelCatalogEntry `json:"models"`
}

type kiroModelCatalogEntry struct {
	ModelID        string  `json:"modelId"`
	ModelName      string  `json:"modelName"`
	Description    string  `json:"description"`
	RateMultiplier float64 `json:"rateMultiplier"`
	RateUnit       string  `json:"rateUnit"`
	TokenLimits    struct {
		MaxInputTokens  int `json:"maxInputTokens"`
		MaxOutputTokens int `json:"maxOutputTokens"`
	} `json:"tokenLimits"`
}

// ListAvailableModels fetches the live Kiro model catalog for a credential.
// Kiro publishes new models here before any static catalog is updated, so this
// is the authoritative source for what a given account can actually call.
func (ka *KiroAuth) ListAvailableModels(ctx context.Context, creds *KiroCredentials) ([]KiroModel, error) {
	if ka == nil {
		return nil, fmt.Errorf("kiro models: auth service is nil")
	}
	if creds == nil {
		return nil, fmt.Errorf("kiro models: credentials are nil")
	}
	accessToken := strings.TrimSpace(creds.AccessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("kiro models: access token is empty")
	}

	safeRegion := AssertValidAwsRegion(creds.Region)
	profileArn := strings.TrimSpace(creds.ProfileArn)
	endpoint := fmt.Sprintf("https://q.%s.amazonaws.com/ListAvailableModels?origin=AI_EDITOR%s", safeRegion, profileArnQuery(profileArn))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, fmt.Errorf("kiro models: build request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", kiroRuntimeUserAgent)
	req.Header.Set("x-amz-user-agent", kiroRuntimeAmzAgent)
	req.Header.Set("x-amzn-kiro-agent-mode", "vibe")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	if profileArn != "" {
		req.Header.Set("x-amzn-kiro-profile-arn", profileArn)
	}
	if strings.EqualFold(strings.TrimSpace(creds.AuthMethod), "api_key") {
		req.Header.Set("TokenType", "API_KEY")
	}

	resp, err := ka.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("kiro models: request failed: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Warnf("kiro models: close ListAvailableModels response body: %v", errClose)
		}
	}()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxKiroModelCatalogSize+1))
	if err != nil {
		return nil, fmt.Errorf("kiro models: read response: %w", err)
	}
	if len(body) > maxKiroModelCatalogSize {
		return nil, fmt.Errorf("kiro models: response exceeded %d bytes", maxKiroModelCatalogSize)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("kiro models: HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var payload kiroModelCatalogResponse
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("kiro models: decode response: %w", err)
	}

	entries := payload.Models
	if len(entries) == 0 && payload.DefaultModel != nil {
		entries = []kiroModelCatalogEntry{*payload.DefaultModel}
	}

	models := make([]KiroModel, 0, len(entries))
	seen := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		modelID := strings.TrimSpace(entry.ModelID)
		if modelID == "" {
			continue
		}
		if _, exists := seen[modelID]; exists {
			continue
		}
		seen[modelID] = struct{}{}
		models = append(models, KiroModel{
			ModelID:         modelID,
			ModelName:       strings.TrimSpace(entry.ModelName),
			Description:     strings.TrimSpace(entry.Description),
			MaxInputTokens:  entry.TokenLimits.MaxInputTokens,
			MaxOutputTokens: entry.TokenLimits.MaxOutputTokens,
			RateMultiplier:  entry.RateMultiplier,
			RateUnit:        strings.TrimSpace(entry.RateUnit),
		})
	}
	if len(models) == 0 {
		return nil, fmt.Errorf("kiro models: catalog returned no usable models")
	}
	return models, nil
}
