package executor

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/registry"
	cliproxyauth "github.com/router-for-me/CLIProxyAPI/v6/sdk/cliproxy/auth"
	"golang.org/x/oauth2"
)

const geminiCLIDiscoverySource = "retrieveUserQuota+generateContent"

type GeminiCLIModelProbeStatus struct {
	ModelID    string `json:"model_id"`
	StatusCode int    `json:"status_code"`
	Available  bool   `json:"available"`
	Error      string `json:"error,omitempty"`
}

type GeminiCLIDiscoveryResult struct {
	AuthID          string                      `json:"auth_id"`
	ProjectID       string                      `json:"project_id"`
	AvailableModels []*registry.ModelInfo       `json:"available_models"`
	DiscoveredAt    time.Time                   `json:"discovered_at"`
	Source          string                      `json:"source"`
	ProbeStatuses   []GeminiCLIModelProbeStatus `json:"probe_statuses,omitempty"`
}

type geminiCLIDiscoveryDeps struct {
	baseURL            string
	prepareTokenSource func(context.Context, *config.Config, *cliproxyauth.Auth) (oauth2.TokenSource, map[string]any, error)
	resolveProjectID   func(*cliproxyauth.Auth) string
	newHTTPClient      func(context.Context, *config.Config, *cliproxyauth.Auth, time.Duration) *http.Client
	applyHeaders       func(*http.Request, string)
	now                func() time.Time
}

var defaultGeminiCLIDiscoveryDeps = geminiCLIDiscoveryDeps{
	baseURL:            codeAssistEndpoint,
	prepareTokenSource: prepareGeminiCLITokenSource,
	resolveProjectID:   resolveGeminiProjectID,
	newHTTPClient:      newHTTPClient,
	applyHeaders:       applyGeminiCLIHeaders,
	now:                time.Now,
}

func DiscoverGeminiCLIModels(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth) (*GeminiCLIDiscoveryResult, error) {
	return discoverGeminiCLIModels(ctx, cfg, auth, defaultGeminiCLIDiscoveryDeps)
}

func discoverGeminiCLIModels(ctx context.Context, cfg *config.Config, auth *cliproxyauth.Auth, deps geminiCLIDiscoveryDeps) (*GeminiCLIDiscoveryResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if auth == nil {
		return nil, fmt.Errorf("gemini-cli discovery: auth is nil")
	}
	if deps.prepareTokenSource == nil {
		deps.prepareTokenSource = prepareGeminiCLITokenSource
	}
	if deps.resolveProjectID == nil {
		deps.resolveProjectID = resolveGeminiProjectID
	}
	if deps.newHTTPClient == nil {
		deps.newHTTPClient = newHTTPClient
	}
	if deps.applyHeaders == nil {
		deps.applyHeaders = applyGeminiCLIHeaders
	}
	if deps.now == nil {
		deps.now = time.Now
	}
	baseURL := strings.TrimRight(strings.TrimSpace(deps.baseURL), "/")
	if baseURL == "" {
		baseURL = codeAssistEndpoint
	}

	projectID := strings.TrimSpace(deps.resolveProjectID(auth))
	if projectID == "" {
		return nil, fmt.Errorf("gemini-cli discovery: missing project id")
	}

	tokenSource, baseTokenData, err := deps.prepareTokenSource(ctx, cfg, auth)
	if err != nil {
		return nil, fmt.Errorf("gemini-cli discovery: prepare token source: %w", err)
	}
	httpClient := deps.newHTTPClient(ctx, cfg, auth, 30*time.Second)
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 30 * time.Second}
	}

	result := &GeminiCLIDiscoveryResult{
		AuthID:          auth.ID,
		ProjectID:       projectID,
		AvailableModels: make([]*registry.ModelInfo, 0),
		DiscoveredAt:    deps.now(),
		Source:          geminiCLIDiscoverySource,
		ProbeStatuses:   make([]GeminiCLIModelProbeStatus, 0),
	}

	candidates, err := retrieveGeminiCLIQuotaCandidateModels(ctx, baseURL, httpClient, tokenSource, baseTokenData, auth, deps.applyHeaders, projectID)
	if err != nil {
		return nil, err
	}

	for _, modelID := range candidates {
		status := probeGeminiCLIModel(ctx, baseURL, httpClient, tokenSource, baseTokenData, auth, deps.applyHeaders, projectID, modelID)
		result.ProbeStatuses = append(result.ProbeStatuses, status)
		if !status.Available {
			continue
		}
		result.AvailableModels = append(result.AvailableModels, buildDiscoveredGeminiCLIModel(result.DiscoveredAt, modelID))
	}

	return result, nil
}

func retrieveGeminiCLIQuotaCandidateModels(
	ctx context.Context,
	baseURL string,
	httpClient *http.Client,
	tokenSource oauth2.TokenSource,
	baseTokenData map[string]any,
	auth *cliproxyauth.Auth,
	applyHeaders func(*http.Request, string),
	projectID string,
) ([]string, error) {
	body, err := json.Marshal(map[string]string{"project": projectID})
	if err != nil {
		return nil, err
	}

	data, statusCode, err := doGeminiCLIDiscoveryRequest(
		ctx,
		httpClient,
		tokenSource,
		baseTokenData,
		auth,
		applyHeaders,
		http.MethodPost,
		baseURL+"/"+codeAssistVersion+":retrieveUserQuota",
		body,
		"quota-check",
	)
	if err != nil {
		return nil, fmt.Errorf("gemini-cli discovery: retrieveUserQuota: %w", err)
	}
	if statusCode < 200 || statusCode >= 300 {
		return nil, fmt.Errorf("gemini-cli discovery: retrieveUserQuota: %w", newGeminiStatusErr(statusCode, data))
	}

	var payload struct {
		Buckets []struct {
			ModelID  string `json:"modelId"`
			ModelID2 string `json:"model_id"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return nil, fmt.Errorf("gemini-cli discovery: decode retrieveUserQuota: %w", err)
	}

	seen := make(map[string]struct{}, len(payload.Buckets))
	candidates := make([]string, 0, len(payload.Buckets))
	for _, bucket := range payload.Buckets {
		modelID := strings.TrimSpace(bucket.ModelID)
		if modelID == "" {
			modelID = strings.TrimSpace(bucket.ModelID2)
		}
		if modelID == "" {
			continue
		}
		if _, exists := seen[modelID]; exists {
			continue
		}
		seen[modelID] = struct{}{}
		candidates = append(candidates, modelID)
	}

	return candidates, nil
}

func probeGeminiCLIModel(
	ctx context.Context,
	baseURL string,
	httpClient *http.Client,
	tokenSource oauth2.TokenSource,
	baseTokenData map[string]any,
	auth *cliproxyauth.Auth,
	applyHeaders func(*http.Request, string),
	projectID string,
	modelID string,
) GeminiCLIModelProbeStatus {
	status := GeminiCLIModelProbeStatus{ModelID: modelID}
	body, err := json.Marshal(map[string]any{
		"project": projectID,
		"model":   modelID,
		"request": map[string]any{
			"contents": []map[string]any{
				{
					"role": "user",
					"parts": []map[string]string{
						{"text": "ping"},
					},
				},
			},
		},
	})
	if err != nil {
		status.Error = err.Error()
		return status
	}

	data, statusCode, err := doGeminiCLIDiscoveryRequest(
		ctx,
		httpClient,
		tokenSource,
		baseTokenData,
		auth,
		applyHeaders,
		http.MethodPost,
		baseURL+"/"+codeAssistVersion+":generateContent",
		body,
		modelID,
	)
	if err != nil {
		status.Error = err.Error()
		return status
	}

	status.StatusCode = statusCode
	if statusCode >= 200 && statusCode < 300 {
		status.Available = true
		return status
	}

	status.Error = strings.TrimSpace(string(data))
	return status
}

func doGeminiCLIDiscoveryRequest(
	ctx context.Context,
	httpClient *http.Client,
	tokenSource oauth2.TokenSource,
	baseTokenData map[string]any,
	auth *cliproxyauth.Auth,
	applyHeaders func(*http.Request, string),
	method string,
	url string,
	body []byte,
	modelID string,
) ([]byte, int, error) {
	tok, err := tokenSource.Token()
	if err != nil {
		return nil, 0, err
	}
	updateGeminiCLITokenMetadata(auth, baseTokenData, tok)

	req, err := http.NewRequestWithContext(ctx, method, url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	applyHeaders(req, modelID)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer func() { _ = resp.Body.Close() }()

	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return data, resp.StatusCode, nil
}

func buildDiscoveredGeminiCLIModel(discoveredAt time.Time, modelID string) *registry.ModelInfo {
	info := registry.LookupStaticModelInfo(modelID)
	if info == nil {
		return &registry.ModelInfo{
			ID:          modelID,
			Object:      "model",
			Created:     discoveredAt.Unix(),
			OwnedBy:     "gemini-cli",
			Type:        "gemini-cli",
			DisplayName: modelID,
			Name:        modelID,
			UserDefined: true,
		}
	}

	info.Type = "gemini-cli"
	info.OwnedBy = "gemini-cli"
	if strings.TrimSpace(info.DisplayName) == "" {
		info.DisplayName = modelID
	}
	if strings.TrimSpace(info.Name) == "" {
		info.Name = modelID
	}
	return info
}
