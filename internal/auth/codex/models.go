package codex

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	DefaultModelsBaseURL       = "https://chatgpt.com/backend-api/codex"
	DefaultModelsClientVersion = "0.144.1"
	DefaultModelsUserAgent     = "codex_cli_rs/0.144.1 (Mac OS 26.3.1; arm64) iTerm.app/3.6.9"
	DefaultModelsOriginator    = "codex_cli_rs"
	maxModelsCatalogSize       = 8 << 20
	maxModelsErrorBodySize     = 4 << 10
)

// ModelCatalogReasoningLevel describes one reasoning effort advertised by Codex.
type ModelCatalogReasoningLevel struct {
	Effort      string `json:"effort"`
	Description string `json:"description"`
}

// ModelCatalogEntry contains the upstream fields needed for runtime model registration.
type ModelCatalogEntry struct {
	Slug                     string                       `json:"slug"`
	DisplayName              string                       `json:"display_name"`
	Description              string                       `json:"description"`
	ContextWindow            int                          `json:"context_window"`
	MaxContextWindow         int                          `json:"max_context_window"`
	SupportedReasoningLevels []ModelCatalogReasoningLevel `json:"supported_reasoning_levels"`
}

// ModelsCatalog is a validated upstream Codex model catalog.
type ModelsCatalog struct {
	Raw    []byte
	Models []ModelCatalogEntry
}

// ModelsRequest configures an authenticated Codex model catalog request.
type ModelsRequest struct {
	BaseURL       string
	ClientVersion string
	AccessToken   string
	AccountID     string
	UserAgent     string
	Originator    string
	Headers       http.Header
	Host          string
}

// FetchModelsCatalog fetches and validates the model catalog available to one Codex account.
func FetchModelsCatalog(ctx context.Context, client *http.Client, request ModelsRequest) (*ModelsCatalog, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	accessToken := strings.TrimSpace(request.AccessToken)
	if accessToken == "" {
		return nil, fmt.Errorf("Codex models request missing access token")
	}

	modelsURL, errURL := ModelsURL(request.BaseURL, request.ClientVersion)
	if errURL != nil {
		return nil, errURL
	}
	req, errReq := http.NewRequestWithContext(ctx, http.MethodGet, modelsURL, nil)
	if errReq != nil {
		return nil, fmt.Errorf("create Codex models request: %w", errReq)
	}
	req.Close = true
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)
	originator := strings.TrimSpace(request.Originator)
	if originator == "" {
		originator = DefaultModelsOriginator
	}
	req.Header.Set("Originator", originator)
	userAgent := strings.TrimSpace(request.UserAgent)
	if userAgent == "" {
		userAgent = DefaultModelsUserAgent
	}
	req.Header.Set("User-Agent", userAgent)
	if accountID := strings.TrimSpace(request.AccountID); accountID != "" {
		req.Header.Set("Chatgpt-Account-Id", accountID)
	}
	for name, values := range request.Headers {
		if strings.TrimSpace(name) == "" {
			continue
		}
		req.Header.Del(name)
		for _, value := range values {
			if value = strings.TrimSpace(value); value != "" {
				req.Header.Add(name, value)
			}
		}
	}
	if host := strings.TrimSpace(request.Host); host != "" {
		req.Host = host
	}

	if client == nil {
		client = http.DefaultClient
	}
	resp, errDo := client.Do(req)
	if errDo != nil {
		return nil, fmt.Errorf("fetch Codex models: %w", errDo)
	}
	body, errRead := io.ReadAll(io.LimitReader(resp.Body, maxModelsCatalogSize+1))
	errClose := resp.Body.Close()
	if errRead != nil {
		return nil, fmt.Errorf("read Codex models response: %w", errRead)
	}
	if errClose != nil {
		return nil, fmt.Errorf("close Codex models response: %w", errClose)
	}
	if len(body) > maxModelsCatalogSize {
		return nil, fmt.Errorf("Codex models response exceeded %d bytes", maxModelsCatalogSize)
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		detail := strings.TrimSpace(string(body))
		if len(detail) > maxModelsErrorBodySize {
			detail = detail[:maxModelsErrorBodySize] + "..."
		}
		return nil, fmt.Errorf("Codex models request failed with status %d: %s", resp.StatusCode, detail)
	}

	models, errParse := ParseModelsCatalog(body)
	if errParse != nil {
		return nil, errParse
	}
	return &ModelsCatalog{Raw: append([]byte(nil), body...), Models: models}, nil
}

// ModelsURL returns the upstream model catalog URL for a Codex base URL.
func ModelsURL(baseURL, clientVersion string) (string, error) {
	baseURL = strings.TrimSpace(baseURL)
	if baseURL == "" {
		baseURL = DefaultModelsBaseURL
	}
	u, err := url.Parse(strings.TrimRight(baseURL, "/") + "/models")
	if err != nil {
		return "", fmt.Errorf("parse Codex models URL: %w", err)
	}
	if clientVersion = strings.TrimSpace(clientVersion); clientVersion != "" {
		query := u.Query()
		query.Set("client_version", clientVersion)
		u.RawQuery = query.Encode()
	}
	return u.String(), nil
}

// ParseModelsCatalog validates and normalizes an upstream Codex model catalog.
func ParseModelsCatalog(raw []byte) ([]ModelCatalogEntry, error) {
	var payload struct {
		Models []ModelCatalogEntry `json:"models"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return nil, fmt.Errorf("decode Codex models response: %w", err)
	}
	if len(payload.Models) == 0 {
		return nil, fmt.Errorf("Codex models response has no models")
	}

	models := make([]ModelCatalogEntry, 0, len(payload.Models))
	seen := make(map[string]struct{}, len(payload.Models))
	for index := range payload.Models {
		model := payload.Models[index]
		model.Slug = strings.TrimSpace(model.Slug)
		if model.Slug == "" {
			return nil, fmt.Errorf("Codex models response models[%d] has empty slug", index)
		}
		key := strings.ToLower(model.Slug)
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		model.DisplayName = strings.TrimSpace(model.DisplayName)
		model.Description = strings.TrimSpace(model.Description)
		levels := make([]ModelCatalogReasoningLevel, 0, len(model.SupportedReasoningLevels))
		seenLevels := make(map[string]struct{}, len(model.SupportedReasoningLevels))
		for _, level := range model.SupportedReasoningLevels {
			level.Effort = strings.ToLower(strings.TrimSpace(level.Effort))
			if level.Effort == "" {
				continue
			}
			if _, exists := seenLevels[level.Effort]; exists {
				continue
			}
			seenLevels[level.Effort] = struct{}{}
			level.Description = strings.TrimSpace(level.Description)
			levels = append(levels, level)
		}
		model.SupportedReasoningLevels = levels
		models = append(models, model)
	}
	return models, nil
}
