package auth

import (
	"bytes"
	"context"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"

	"github.com/MoeForge/moego/ojson"
	"github.com/router-for-me/CLIProxyAPI/v8/sdk/proxyutil"
	log "github.com/sirupsen/logrus"
)

const (
	// AntigravityModelBaseURLDaily is the default Cloud Code endpoint for Antigravity model discovery.
	AntigravityModelBaseURLDaily = "https://daily-cloudcode-pa.googleapis.com"
	// AntigravityModelsPath is the upstream path for per-account model capability discovery.
	AntigravityModelsPath = "/v1internal:fetchAvailableModels"
	// AntigravityPersistedHintsMetaKey is the auth metadata key storing persisted model capability hints.
	AntigravityPersistedHintsMetaKey = "antigravity_capability_hints"

	antigravityMaxCatalogModels = 1024
	antigravityMaxModelIDBytes  = 256
)

// AntigravityProbeStatus classifies the outcome of an Antigravity model discovery request.
type AntigravityProbeStatus int

const (
	AntigravityProbeStatusSuccess AntigravityProbeStatus = iota
	AntigravityProbeStatusAuthError
	AntigravityProbeStatusTransientError
)

// AntigravityModelCapabilityHints holds normalized per-account model IDs and web-search capability IDs.
type AntigravityModelCapabilityHints struct {
	WebSearchModelIDs map[string]struct{}
	// Nil means unknown; an empty non-nil set is an authoritative empty catalog.
	ModelIDs map[string]struct{}
}

// Clone returns a deep copy of the capability hints.
func (h AntigravityModelCapabilityHints) Clone() AntigravityModelCapabilityHints {
	return AntigravityModelCapabilityHints{
		WebSearchModelIDs: maps.Clone(h.WebSearchModelIDs),
		ModelIDs:          maps.Clone(h.ModelIDs),
	}
}

// AntigravityModelProbeRequest configures a single-shot fetchAvailableModels probe.
type AntigravityModelProbeRequest struct {
	BaseURLs    []string
	ProxyURL    string
	AccessToken string
	ProjectID   string
	UserAgent   string
}

// FetchAntigravityAvailableModels performs a single-shot POST /v1internal:fetchAvailableModels request
// and parses the returned per-account model catalog and webSearchModelIds.
func FetchAntigravityAvailableModels(ctx context.Context, req AntigravityModelProbeRequest) (AntigravityModelCapabilityHints, AntigravityProbeStatus) {
	baseURL := ""
	for _, candidate := range req.BaseURLs {
		if trimmed := strings.TrimRight(strings.TrimSpace(candidate), "/"); trimmed != "" {
			baseURL = trimmed
			break
		}
	}
	if baseURL == "" {
		return AntigravityModelCapabilityHints{}, AntigravityProbeStatusTransientError
	}
	accessToken := strings.TrimSpace(req.AccessToken)
	if accessToken == "" {
		return AntigravityModelCapabilityHints{}, AntigravityProbeStatusAuthError
	}
	if ctx == nil {
		ctx = context.Background()
	}

	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	transport, _, errProxy := proxyutil.BuildHTTPTransport(strings.TrimSpace(req.ProxyURL))
	if errProxy != nil {
		return AntigravityModelCapabilityHints{}, AntigravityProbeStatusTransientError
	}
	if transport != nil {
		client.Transport = transport
		defer transport.CloseIdleConnections()
	}

	doc := ojson.NewDoc([]byte("{}"))
	doc.Set("project", strings.TrimSpace(req.ProjectID))
	httpReq, errReq := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+AntigravityModelsPath, bytes.NewReader(doc.Bytes()))
	if errReq != nil {
		return AntigravityModelCapabilityHints{}, AntigravityProbeStatusTransientError
	}
	userAgent := strings.TrimSpace(req.UserAgent)
	if userAgent == "" {
		userAgent = AntigravityUserAgent()
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+accessToken)
	httpReq.Header.Set("User-Agent", userAgent)

	resp, errDo := client.Do(httpReq)
	if errDo != nil {
		return AntigravityModelCapabilityHints{}, AntigravityProbeStatusTransientError
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debug("antigravity model fetch: response close failed")
		}
	}()
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return AntigravityModelCapabilityHints{}, AntigravityProbeStatusAuthError
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return AntigravityModelCapabilityHints{}, AntigravityProbeStatusTransientError
	}
	const maxCatalogBytes = 8 << 20
	data, errRead := io.ReadAll(io.LimitReader(resp.Body, maxCatalogBytes+1))
	if errRead != nil || len(data) > maxCatalogBytes {
		return AntigravityModelCapabilityHints{}, AntigravityProbeStatusTransientError
	}
	hints, ok := ParseAntigravityModelCapabilityHints(data)
	if !ok {
		return AntigravityModelCapabilityHints{}, AntigravityProbeStatusTransientError
	}
	return hints, AntigravityProbeStatusSuccess
}

// NormalizeAntigravityModelID normalizes an Antigravity model identifier for case-insensitive matching.
func NormalizeAntigravityModelID(modelID string) string {
	trimmed := strings.ToLower(strings.TrimSpace(modelID))
	if len(trimmed) > antigravityMaxModelIDBytes {
		return ""
	}
	return strings.Clone(trimmed)
}

func sanitizeAntigravityPersistedEndpoint(endpoint string) string {
	trimmed := strings.TrimRight(strings.TrimSpace(endpoint), "/")
	if trimmed == "" {
		return ""
	}
	if parsed, errParse := url.Parse(trimmed); errParse == nil && parsed.User != nil {
		parsed.User = nil
		return strings.TrimRight(parsed.String(), "/")
	}
	return trimmed
}

// ParseAntigravityModelCapabilityHints decodes a fetchAvailableModels response payload using ojson.
func ParseAntigravityModelCapabilityHints(body []byte) (AntigravityModelCapabilityHints, bool) {
	if len(body) == 0 || !ojson.ValidBytes(body) {
		return AntigravityModelCapabilityHints{}, false
	}
	top := ojson.ParseBytes(body)
	if !top.IsObject() {
		return AntigravityModelCapabilityHints{}, false
	}

	webSearchResult := top.Get("webSearchModelIds")
	webSearchModels := make(map[string]struct{})
	if webSearchResult.Exists() && webSearchResult.Type != ojson.Null {
		if !webSearchResult.IsArray() {
			return AntigravityModelCapabilityHints{}, false
		}
		array := webSearchResult.Array()
		if len(array) > antigravityMaxCatalogModels {
			return AntigravityModelCapabilityHints{}, false
		}
		for _, item := range array {
			if item.Type != ojson.String {
				return AntigravityModelCapabilityHints{}, false
			}
			if normalized := NormalizeAntigravityModelID(item.Str); normalized != "" {
				webSearchModels[normalized] = struct{}{}
			}
		}
	}

	modelsResult := top.Get("models")
	var modelIDs map[string]struct{}
	if modelsResult.Exists() && modelsResult.Type != ojson.Null {
		if !modelsResult.IsObject() {
			return AntigravityModelCapabilityHints{}, false
		}
		modelMap := modelsResult.Map()
		if len(modelMap) > antigravityMaxCatalogModels {
			return AntigravityModelCapabilityHints{}, false
		}
		modelIDs = make(map[string]struct{}, len(modelMap))
		for id := range modelMap {
			if normalized := NormalizeAntigravityModelID(id); normalized != "" {
				modelIDs[normalized] = struct{}{}
			}
		}
	}

	return AntigravityModelCapabilityHints{
		WebSearchModelIDs: webSearchModels,
		ModelIDs:          modelIDs,
	}, true
}

// BuildPersistedAntigravityCapabilityHints serializes capability hints for storage in auth metadata.
// It returns nil when hints.ModelIDs is nil so unknown catalogs are never persisted as authoritative empty sets.
func BuildPersistedAntigravityCapabilityHints(project, endpoint string, hints AntigravityModelCapabilityHints) map[string]any {
	if hints.ModelIDs == nil {
		return nil
	}
	modelIDs := make([]string, 0, len(hints.ModelIDs))
	for id := range hints.ModelIDs {
		if normalized := NormalizeAntigravityModelID(id); normalized != "" {
			modelIDs = append(modelIDs, normalized)
		}
	}
	slices.Sort(modelIDs)
	modelItems := make([]any, len(modelIDs))
	for i, id := range modelIDs {
		modelItems[i] = id
	}
	value := map[string]any{
		"model_ids": modelItems,
	}
	if project = strings.TrimSpace(project); project != "" {
		value["project_id"] = project
	}
	if endpoint = sanitizeAntigravityPersistedEndpoint(endpoint); endpoint != "" {
		value["endpoint"] = endpoint
	}
	if len(hints.WebSearchModelIDs) > 0 {
		webSearchIDs := make([]string, 0, len(hints.WebSearchModelIDs))
		for id := range hints.WebSearchModelIDs {
			if normalized := NormalizeAntigravityModelID(id); normalized != "" {
				webSearchIDs = append(webSearchIDs, normalized)
			}
		}
		slices.Sort(webSearchIDs)
		if len(webSearchIDs) > 0 {
			webSearchItems := make([]any, len(webSearchIDs))
			for i, id := range webSearchIDs {
				webSearchItems[i] = id
			}
			value["web_search_model_ids"] = webSearchItems
		}
	}
	return value
}

// ParsePersistedAntigravityCapabilityHints extracts and validates persisted capability hints from auth metadata.
func ParsePersistedAntigravityCapabilityHints(metadata map[string]any, expectedProject, expectedEndpoint string) (AntigravityModelCapabilityHints, bool) {
	if metadata == nil {
		return AntigravityModelCapabilityHints{}, false
	}
	raw, ok := metadata[AntigravityPersistedHintsMetaKey]
	if !ok || raw == nil {
		return AntigravityModelCapabilityHints{}, false
	}
	rawMap, ok := raw.(map[string]any)
	if !ok {
		return AntigravityModelCapabilityHints{}, false
	}
	persistedProject, _ := rawMap["project_id"].(string)
	persistedEndpoint, _ := rawMap["endpoint"].(string)
	if strings.TrimSpace(persistedProject) != strings.TrimSpace(expectedProject) {
		return AntigravityModelCapabilityHints{}, false
	}
	if sanitizeAntigravityPersistedEndpoint(persistedEndpoint) != sanitizeAntigravityPersistedEndpoint(expectedEndpoint) {
		return AntigravityModelCapabilityHints{}, false
	}
	rawModels, hasModels := rawMap["model_ids"]
	if !hasModels || rawModels == nil {
		return AntigravityModelCapabilityHints{}, false
	}
	modelIDs, ok := parsePersistedAntigravityModelSet(rawModels)
	if !ok {
		return AntigravityModelCapabilityHints{}, false
	}
	webSearchModels := make(map[string]struct{})
	if rawWebSearch, hasWebSearch := rawMap["web_search_model_ids"]; hasWebSearch && rawWebSearch != nil {
		parsedWebSearch, okWebSearch := parsePersistedAntigravityModelSet(rawWebSearch)
		if !okWebSearch {
			return AntigravityModelCapabilityHints{}, false
		}
		webSearchModels = parsedWebSearch
	}
	return AntigravityModelCapabilityHints{
		ModelIDs:          modelIDs,
		WebSearchModelIDs: webSearchModels,
	}, true
}

func parsePersistedAntigravityModelSet(raw any) (map[string]struct{}, bool) {
	switch items := raw.(type) {
	case []any:
		set := make(map[string]struct{}, len(items))
		for _, item := range items {
			s, ok := item.(string)
			if !ok {
				return nil, false
			}
			if normalized := NormalizeAntigravityModelID(s); normalized != "" {
				set[normalized] = struct{}{}
			}
		}
		return set, true
	case []string:
		set := make(map[string]struct{}, len(items))
		for _, s := range items {
			if normalized := NormalizeAntigravityModelID(s); normalized != "" {
				set[normalized] = struct{}{}
			}
		}
		return set, true
	default:
		return nil, false
	}
}
