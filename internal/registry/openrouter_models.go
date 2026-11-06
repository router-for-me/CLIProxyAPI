package registry

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/ojson"
	log "github.com/sirupsen/logrus"
)

// OpenRouter publishes every model's context window, completion limit and accepted reasoning efforts at
// /api/v1/models. OpenAI-compatible entries pointing at OpenRouter list models by hand, without them, so clients fell
// back to template defaults. The catalog is fetched in the background on first use and refreshed with the other
// catalogs' cadence; when it changes, the OpenRouter entries' models are registered again. Like the other remote
// catalogs it is not fetched under --local-model or CLIProxyAPIHome, and the fetch lives as long as the service.

var openRouterModelsURL = "https://openrouter.ai/api/v1/models"

const maxOpenRouterModelsSize = 32 << 20

// OpenRouterModel is what OpenRouter publishes about one model.
type OpenRouterModel struct {
	ContextLength       int
	MaxCompletionTokens int
	// ReasoningEfforts are the reasoning.effort values OpenRouter accepts for the model
	// (reasoning.supported_efforts), lower-cased, e.g. "max", "xhigh", "high", "medium", "low", "none".
	// Empty when OpenRouter doesn't publish them.
	ReasoningEfforts []string
}

type openRouterCatalog struct {
	mu         sync.Mutex
	models     map[string]OpenRouterModel
	fetchedAt  time.Time
	fetching   bool
	failedAt   time.Time
	interested map[string]struct{}
	fetch      func(context.Context) (map[string]OpenRouterModel, error)
	// remote reports whether remote catalogs may be fetched and the context the fetch runs under.
	remote func() (context.Context, bool)
	// now is the clock; tests replace it.
	now func() time.Time
	// refreshed is called after every completed refresh; tests use it to wait for one.
	refreshed func()
}

var openRouterModels = &openRouterCatalog{fetch: fetchOpenRouterModels, remote: remoteCatalogPolicy}

// remoteCatalogPolicy follows the shared catalog policy: no remote fetch under --local-model or CLIProxyAPIHome, and
// the fetch is bound to the service lifetime once it has started.
func remoteCatalogPolicy() (context.Context, bool) {
	catalogRuntime.Lock()
	defer catalogRuntime.Unlock()
	if catalogRuntime.local || catalogRuntime.home {
		return nil, false
	}
	if catalogRuntime.ctx != nil {
		return catalogRuntime.ctx, catalogRuntime.ctx.Err() == nil
	}
	return context.Background(), true
}

func (c *openRouterCatalog) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

// LookupOpenRouterModel returns what OpenRouter publishes about modelID (e.g. "anthropic/claude-haiku-5.5"). The
// first call, and calls after ModelsRefreshInterval, start a background fetch; providerKey's models are registered
// again when the catalog changes. Returns false until the catalog is loaded or when the model isn't listed.
func LookupOpenRouterModel(providerKey, modelID string) (OpenRouterModel, bool) {
	return openRouterModels.lookup(providerKey, modelID)
}

// SetOpenRouterModelsForTest installs a loaded OpenRouter catalog for tests and returns a function that restores the
// previous one.
func SetOpenRouterModelsForTest(models map[string]OpenRouterModel) func() {
	c := openRouterModels
	normalized := make(map[string]OpenRouterModel, len(models))
	for id, model := range models {
		normalized[strings.ToLower(strings.TrimSpace(id))] = model
	}
	c.mu.Lock()
	oldModels, oldFetchedAt, oldFailedAt := c.models, c.fetchedAt, c.failedAt
	c.models, c.fetchedAt, c.failedAt = normalized, c.clock(), time.Time{}
	c.mu.Unlock()
	return func() {
		c.mu.Lock()
		c.models, c.fetchedAt, c.failedAt = oldModels, oldFetchedAt, oldFailedAt
		c.mu.Unlock()
	}
}

func (c *openRouterCatalog) lookup(providerKey, modelID string) (OpenRouterModel, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if key := strings.ToLower(strings.TrimSpace(providerKey)); key != "" {
		if c.interested == nil {
			c.interested = make(map[string]struct{})
		}
		c.interested[key] = struct{}{}
	}
	now := c.clock()
	stale := c.models == nil || now.Sub(c.fetchedAt) > ModelsRefreshInterval
	retryAllowed := c.failedAt.IsZero() || now.Sub(c.failedAt) > time.Minute
	if stale && !c.fetching && retryAllowed && c.remote != nil {
		if ctx, ok := c.remote(); ok {
			c.fetching = true
			go c.refresh(ctx)
		}
	}
	model, ok := c.models[strings.ToLower(strings.TrimSpace(modelID))]
	if ok {
		model.ReasoningEfforts = slices.Clone(model.ReasoningEfforts)
	}
	return model, ok
}

func (c *openRouterCatalog) refresh(ctx context.Context) {
	if c.refreshed != nil {
		defer c.refreshed()
	}
	fetched, err := c.fetch(ctx)

	c.mu.Lock()
	c.fetching = false
	if err != nil || len(fetched) == 0 {
		c.failedAt = c.clock()
		c.mu.Unlock()
		if err != nil && ctx.Err() == nil {
			log.Debugf("registry: OpenRouter model catalog unavailable: %v", err)
		}
		return
	}
	changed := !sameOpenRouterModels(c.models, fetched)
	c.models = fetched
	c.fetchedAt = c.clock()
	c.failedAt = time.Time{}
	providers := make([]string, 0, len(c.interested))
	for key := range c.interested {
		providers = append(providers, key)
	}
	c.mu.Unlock()

	if changed && len(providers) > 0 {
		sort.Strings(providers)
		notifyModelRefresh(providers)
	}
}

func sameOpenRouterModels(a, b map[string]OpenRouterModel) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		w, ok := b[k]
		if !ok || v.ContextLength != w.ContextLength || v.MaxCompletionTokens != w.MaxCompletionTokens ||
			!slices.Equal(v.ReasoningEfforts, w.ReasoningEfforts) {
			return false
		}
	}
	return true
}

func fetchOpenRouterModels(ctx context.Context) (map[string]OpenRouterModel, error) {
	req, errRequest := http.NewRequestWithContext(ctx, http.MethodGet, openRouterModelsURL, nil)
	if errRequest != nil {
		return nil, errRequest
	}
	resp, errDo := http.DefaultClient.Do(req)
	if errDo != nil {
		return nil, errDo
	}
	defer func() {
		if errClose := resp.Body.Close(); errClose != nil {
			log.Debug("OpenRouter catalog response close failed")
		}
	}()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OpenRouter catalog HTTP status %d", resp.StatusCode)
	}
	data, errRead := io.ReadAll(io.LimitReader(resp.Body, maxOpenRouterModelsSize+1))
	if errRead != nil {
		return nil, errRead
	}
	if len(data) > maxOpenRouterModelsSize {
		return nil, fmt.Errorf("OpenRouter catalog exceeds size limit")
	}
	return parseOpenRouterModels(data)
}

func parseOpenRouterModels(data []byte) (map[string]OpenRouterModel, error) {
	if !ojson.ValidBytes(data) {
		return nil, fmt.Errorf("OpenRouter catalog is not valid JSON")
	}
	models := ojson.GetBytes(data, "data")
	if !models.IsArray() {
		return nil, fmt.Errorf("OpenRouter catalog has no data array")
	}
	out := make(map[string]OpenRouterModel)
	for _, model := range models.Array() {
		id := strings.ToLower(strings.TrimSpace(model.Get("id").String()))
		if id == "" {
			continue
		}
		contextLength := int(model.Get("context_length").Int())
		if contextLength <= 0 {
			contextLength = int(model.Get("top_provider.context_length").Int())
		}
		var efforts []string
		for _, effort := range model.Get("reasoning.supported_efforts").Array() {
			value := strings.ToLower(strings.TrimSpace(effort.String()))
			if value != "" && !slices.Contains(efforts, value) {
				efforts = append(efforts, value)
			}
		}
		published := OpenRouterModel{
			ContextLength:       contextLength,
			MaxCompletionTokens: int(model.Get("top_provider.max_completion_tokens").Int()),
			ReasoningEfforts:    efforts,
		}
		if published.ContextLength > 0 || published.MaxCompletionTokens > 0 || len(published.ReasoningEfforts) > 0 {
			out[id] = published
		}
	}
	return out, nil
}
