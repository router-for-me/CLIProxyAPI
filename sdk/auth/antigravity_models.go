package auth

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"fmt"
	"io"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

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
	AntigravityProbeStatusForbidden
)

// AntigravityModelCapabilityHints holds normalized per-account model IDs and web-search capability IDs.
type AntigravityModelCapabilityHints struct {
	WebSearchModelIDs map[string]struct{}
	// Nil means unknown; an empty non-nil set is an authoritative empty catalog.
	ModelIDs         map[string]struct{}
	UpdatedAt        time.Time
	ExpiresAt        time.Time
	RouteFingerprint string
}

// Clone returns a deep copy of the capability hints.
func (h AntigravityModelCapabilityHints) Clone() AntigravityModelCapabilityHints {
	return AntigravityModelCapabilityHints{
		WebSearchModelIDs: maps.Clone(h.WebSearchModelIDs),
		ModelIDs:          maps.Clone(h.ModelIDs),
		UpdatedAt:         h.UpdatedAt,
		ExpiresAt:         h.ExpiresAt,
		RouteFingerprint:  h.RouteFingerprint,
	}
}

// AntigravityCapabilityRouteFingerprint computes a credential-free SHA-256
// fingerprint of the effective proxy URL and client User-Agent used to fetch
// capability hints.
func AntigravityCapabilityRouteFingerprint(proxyURL, userAgent string) string {
	userAgent = strings.TrimSpace(userAgent)
	if userAgent == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(fmt.Sprintf("%q/%q", strings.TrimSpace(proxyURL), userAgent)))
	return fmt.Sprintf("%x", sum)
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
	transport, errProxy := newAntigravityProbeTransport(req.ProxyURL)
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
	if resp.StatusCode == http.StatusUnauthorized {
		return AntigravityModelCapabilityHints{}, AntigravityProbeStatusAuthError
	}
	if resp.StatusCode == http.StatusForbidden {
		return AntigravityModelCapabilityHints{}, AntigravityProbeStatusForbidden
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

func newAntigravityProbeTransport(proxyURL string) (*http.Transport, error) {
	transport, mode, errProxy := proxyutil.BuildHTTPTransport(strings.TrimSpace(proxyURL))
	if errProxy != nil {
		return nil, errProxy
	}
	if transport == nil {
		if mode != proxyutil.ModeInherit || http.DefaultTransport == nil {
			transport = &http.Transport{}
		} else if base, ok := http.DefaultTransport.(*http.Transport); ok {
			if base == nil {
				transport = &http.Transport{}
			} else {
				transport = base.Clone()
			}
		} else {
			// A custom non-*http.Transport RoundTripper installed on http.DefaultTransport
			// owns its own protocol behavior.
			return nil, nil
		}
	}
	return configureAntigravityHTTP11Transport(transport), nil
}

func configureAntigravityHTTP11Transport(transport *http.Transport) *http.Transport {
	if transport == nil {
		return nil
	}
	transport.Protocols = nil
	transport.ForceAttemptHTTP2 = false
	// Wipe TLSNextProto to prevent implicit HTTP/2 upgrade.
	transport.TLSNextProto = make(map[string]func(authority string, c *tls.Conn) http.RoundTripper)
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{}
	} else {
		transport.TLSClientConfig = transport.TLSClientConfig.Clone()
	}
	// Native Antigravity sends no ALPN extension. With HTTP/2 disabled above,
	// an empty NextProtos keeps the wire shape aligned while using HTTP/1.1.
	transport.TLSClientConfig.NextProtos = nil
	return transport
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
	if routeFingerprint := strings.TrimSpace(hints.RouteFingerprint); routeFingerprint != "" {
		value["route_fingerprint"] = routeFingerprint
	}
	if !hints.UpdatedAt.IsZero() {
		value["updated_at"] = hints.UpdatedAt.UTC().Format(time.RFC3339)
	}
	if !hints.ExpiresAt.IsZero() {
		value["expires_at"] = hints.ExpiresAt.UTC().Format(time.RFC3339)
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
	routeFingerprint, _ := rawMap["route_fingerprint"].(string)
	return AntigravityModelCapabilityHints{
		ModelIDs:          modelIDs,
		WebSearchModelIDs: webSearchModels,
		UpdatedAt:         parsePersistedAntigravityTime(rawMap["updated_at"]),
		ExpiresAt:         parsePersistedAntigravityTime(rawMap["expires_at"]),
		RouteFingerprint:  strings.TrimSpace(routeFingerprint),
	}, true
}

func parsePersistedAntigravityTime(raw any) time.Time {
	value, ok := raw.(string)
	if !ok {
		return time.Time{}
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}
	}
	parsed, errParse := time.Parse(time.RFC3339Nano, value)
	if errParse != nil {
		return time.Time{}
	}
	return parsed.UTC()
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
