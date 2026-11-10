package qoder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v8/internal/registry"
	log "github.com/sirupsen/logrus"
	"github.com/tidwall/gjson"
)

const maxCatalogResponseBytes = 8 << 20

var (
	ErrCatalogNotCached  = errors.New("qoder: account catalog is not cached")
	ErrModelNotInCatalog = errors.New("qoder: model is not in the account catalog")
)

// Catalog is the account-bound model list and the original model_config entries.
type Catalog struct {
	Models  []*registry.ModelInfo
	Configs map[string]json.RawMessage
}

var catalogCache = struct {
	sync.RWMutex
	entries map[string]Catalog
}{entries: make(map[string]Catalog)}

// CacheCatalog publishes a complete catalog snapshot for one credential.
func CacheCatalog(authID string, catalog *Catalog) {
	if strings.TrimSpace(authID) == "" || catalog == nil {
		return
	}
	catalogCache.Lock()
	catalogCache.entries[authID] = cloneCatalog(catalog)
	catalogCache.Unlock()
}

// DropCatalog removes credential-bound model state.
func DropCatalog(authID string) {
	catalogCache.Lock()
	delete(catalogCache.entries, authID)
	catalogCache.Unlock()
}

// CachedModels returns a copy of the last fetched catalog, or nil when none was loaded.
func CachedModels(authID string) []*registry.ModelInfo {
	catalogCache.RLock()
	catalog, exists := catalogCache.entries[authID]
	catalogCache.RUnlock()
	if !exists {
		return nil
	}
	return cloneCatalog(&catalog).Models
}

// CachedModelConfig returns a defensive copy of one account's model configuration.
func CachedModelConfig(authID, model string) (map[string]any, error) {
	catalogCache.RLock()
	catalog, exists := catalogCache.entries[authID]
	raw, found := catalog.Configs[model]
	catalogCache.RUnlock()
	if !exists {
		return nil, ErrCatalogNotCached
	}
	if !found || len(raw) == 0 {
		return nil, fmt.Errorf("%w: %s", ErrModelNotInCatalog, model)
	}
	var config map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if err := decoder.Decode(&config); err != nil {
		return nil, fmt.Errorf("qoder: invalid cached model config for %q: %w", model, err)
	}
	return config, nil
}

// FetchCatalog loads and caches the enabled models available to a credential.
func FetchCatalog(ctx context.Context, client *http.Client, authID string, storage *TokenStorage) (*Catalog, error) {
	if client == nil {
		client = http.DefaultClient
	}
	if storage == nil {
		return nil, fmt.Errorf("qoder: credential is missing")
	}
	endpoints, err := ResolveEndpoints(storage.Region)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoints.ModelListURL(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if errSign := SignRequest(req, nil, storage); errSign != nil {
		return nil, errSign
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("qoder: model catalog request: %w", err)
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debugf("qoder: close catalog response: %v", errClose)
		}
	}()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxCatalogResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("qoder: read model catalog: %w", err)
	}
	if len(body) > maxCatalogResponseBytes {
		return nil, fmt.Errorf("qoder: model catalog exceeds %d bytes", maxCatalogResponseBytes)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("qoder: model catalog returned HTTP %d: %s", resp.StatusCode, responseSummary(body))
	}
	catalog, err := parseCatalog(body, ProviderForRegion(storage.Region))
	if err != nil {
		return nil, err
	}
	CacheCatalog(authID, catalog)
	return catalog, nil
}

func parseCatalog(body []byte, provider string) (*Catalog, error) {
	if !gjson.ValidBytes(body) {
		return nil, fmt.Errorf("qoder: model catalog is not valid JSON")
	}
	paths := [][]string{
		{"assistant", "data.assistant"},
		{"chat", "data.chat"},
		{"models", "data.models"},
	}
	var entries []gjson.Result
	for _, candidates := range paths {
		for _, path := range candidates {
			result := gjson.GetBytes(body, path)
			if result.Exists() && result.IsArray() {
				entries = append([]gjson.Result(nil), result.Array()...)
				break
			}
		}
		if len(entries) > 0 {
			break
		}
	}
	for _, path := range []string{"byok_enterprise", "data.byok_enterprise"} {
		result := gjson.GetBytes(body, path)
		if result.Exists() && result.IsArray() {
			entries = append(entries, result.Array()...)
		}
	}
	if len(entries) == 0 {
		return nil, fmt.Errorf("qoder: model catalog has no assistant models")
	}

	catalog := &Catalog{Configs: make(map[string]json.RawMessage)}
	now := time.Now().Unix()
	seen := make(map[string]struct{})
	for _, entry := range entries {
		key := strings.TrimSpace(entry.Get("key").String())
		if key == "" {
			key = strings.TrimSpace(entry.Get("model_key").String())
		}
		if key == "" {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		if enabled := entry.Get("enable"); enabled.Exists() && !enabled.Bool() {
			continue
		}
		seen[key] = struct{}{}
		catalog.Configs[key] = json.RawMessage([]byte(entry.Raw))
		display := firstNonEmpty(entry.Get("display_name").String(), entry.Get("name").String(), key)
		inputLimit := int(entry.Get("max_input_tokens").Int())
		outputLimit := int(entry.Get("max_output_tokens").Int())
		contextLength := inputLimit + outputLimit
		entry.Get("context_config").ForEach(func(_, item gjson.Result) bool {
			if tokens := int(item.Get("token_count").Int()); tokens > contextLength {
				contextLength = tokens
			}
			return true
		})
		model := &registry.ModelInfo{
			ID:                         key,
			Object:                     "model",
			Created:                    now,
			OwnedBy:                    provider,
			Type:                       provider,
			DisplayName:                display,
			Name:                       display,
			Description:                display + " via Qoder",
			InputTokenLimit:            inputLimit,
			OutputTokenLimit:           outputLimit,
			ContextLength:              contextLength,
			MaxCompletionTokens:        outputLimit,
			SupportedGenerationMethods: []string{"chat"},
			SupportedParameters:        []string{"tools", "tool_choice", "reasoning_effort", "max_tokens", "temperature", "top_p"},
			SupportedInputModalities:   []string{"text"},
			SupportedOutputModalities:  []string{"text"},
		}
		if entry.Get("is_vl").Bool() {
			model.SupportedInputModalities = append(model.SupportedInputModalities, "image")
		}
		if thinking := parseThinkingSupport(entry); thinking != nil {
			model.Thinking = thinking
		}
		catalog.Models = append(catalog.Models, model)
	}
	if len(catalog.Models) == 0 {
		return nil, fmt.Errorf("qoder: model catalog has no enabled models")
	}
	return catalog, nil
}

func parseThinkingSupport(entry gjson.Result) *registry.ThinkingSupport {
	config := entry.Get("thinking_config")
	if !config.Exists() {
		if entry.Get("is_reasoning").Bool() {
			return &registry.ThinkingSupport{DynamicAllowed: true}
		}
		return nil
	}
	support := &registry.ThinkingSupport{
		ZeroAllowed:    config.Get("disabled").Exists(),
		DynamicAllowed: true,
	}
	seen := make(map[string]struct{})
	efforts := config.Get("enabled.efforts")
	if efforts.IsObject() {
		efforts.ForEach(func(level, _ gjson.Result) bool {
			name := strings.ToLower(strings.TrimSpace(level.String()))
			if name != "" {
				seen[name] = struct{}{}
			}
			return true
		})
	} else if efforts.IsArray() {
		for _, item := range efforts.Array() {
			name := strings.ToLower(strings.TrimSpace(item.String()))
			if name != "" {
				seen[name] = struct{}{}
			}
		}
	}
	for _, level := range []string{"minimal", "low", "medium", "high", "xhigh", "max"} {
		if _, exists := seen[level]; exists {
			support.Levels = append(support.Levels, level)
			delete(seen, level)
		}
	}
	if len(seen) > 0 {
		remaining := make([]string, 0, len(seen))
		for level := range seen {
			remaining = append(remaining, level)
		}
		sort.Strings(remaining)
		support.Levels = append(support.Levels, remaining...)
	}
	return support
}

func cloneCatalog(source *Catalog) Catalog {
	if source == nil {
		return Catalog{}
	}
	clone := Catalog{Configs: make(map[string]json.RawMessage, len(source.Configs))}
	for key, raw := range source.Configs {
		clone.Configs[key] = json.RawMessage(bytesClone(raw))
	}
	clone.Models = make([]*registry.ModelInfo, 0, len(source.Models))
	for _, model := range source.Models {
		if model == nil {
			continue
		}
		item := *model
		item.SupportedGenerationMethods = append([]string(nil), model.SupportedGenerationMethods...)
		item.SupportedParameters = append([]string(nil), model.SupportedParameters...)
		item.SupportedInputModalities = append([]string(nil), model.SupportedInputModalities...)
		item.SupportedOutputModalities = append([]string(nil), model.SupportedOutputModalities...)
		if model.Thinking != nil {
			thinking := *model.Thinking
			thinking.Levels = append([]string(nil), model.Thinking.Levels...)
			item.Thinking = &thinking
		}
		clone.Models = append(clone.Models, &item)
	}
	return clone
}

func bytesClone[T ~[]byte](value T) []byte {
	return append([]byte(nil), value...)
}
