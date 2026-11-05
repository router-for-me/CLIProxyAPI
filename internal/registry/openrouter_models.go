package registry

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/MoeForge/moego/ojson"
	log "github.com/sirupsen/logrus"
)

// OpenRouter publishes every model's context window and completion limit at /api/v1/models. OpenAI-compatible
// entries pointing at OpenRouter list models by hand, without limits, so clients fell back to template defaults.
// The catalog is fetched in the background on first use and refreshed with the other catalogs' cadence; when it
// changes, the OpenRouter entries' models are registered again.

var openRouterModelsURL = "https://openrouter.ai/api/v1/models"

const maxOpenRouterModelsSize = 32 << 20

// OpenRouterModelLimits are the published limits of one OpenRouter model.
type OpenRouterModelLimits struct {
	ContextLength       int
	MaxCompletionTokens int
}

type openRouterCatalog struct {
	mu         sync.Mutex
	limits     map[string]OpenRouterModelLimits
	fetchedAt  time.Time
	fetching   bool
	failedAt   time.Time
	interested map[string]struct{}
	fetch      func(context.Context) (map[string]OpenRouterModelLimits, error)
}

var openRouterModels = &openRouterCatalog{fetch: fetchOpenRouterModelLimits}

// LookupOpenRouterModelLimits returns OpenRouter's limits for modelID (e.g. "anthropic/claude-haiku-5.5"). The
// first call, and calls after ModelsRefreshInterval, start a background fetch; providerKey's models are registered
// again when the catalog changes. Returns false until the catalog is loaded or when the model isn't listed.
func LookupOpenRouterModelLimits(providerKey, modelID string) (OpenRouterModelLimits, bool) {
	return openRouterModels.lookup(providerKey, modelID)
}

func (c *openRouterCatalog) lookup(providerKey, modelID string) (OpenRouterModelLimits, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if key := strings.ToLower(strings.TrimSpace(providerKey)); key != "" {
		if c.interested == nil {
			c.interested = make(map[string]struct{})
		}
		c.interested[key] = struct{}{}
	}
	now := time.Now()
	stale := c.limits == nil || now.Sub(c.fetchedAt) > ModelsRefreshInterval
	retryAllowed := c.failedAt.IsZero() || now.Sub(c.failedAt) > time.Minute
	if stale && !c.fetching && retryAllowed {
		c.fetching = true
		go c.refresh()
	}
	limits, ok := c.limits[strings.ToLower(strings.TrimSpace(modelID))]
	return limits, ok
}

func (c *openRouterCatalog) refresh() {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	fetched, err := c.fetch(ctx)

	c.mu.Lock()
	c.fetching = false
	if err != nil || len(fetched) == 0 {
		c.failedAt = time.Now()
		c.mu.Unlock()
		if err != nil {
			log.Debugf("registry: OpenRouter model catalog unavailable: %v", err)
		}
		return
	}
	changed := !sameOpenRouterLimits(c.limits, fetched)
	c.limits = fetched
	c.fetchedAt = time.Now()
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

func sameOpenRouterLimits(a, b map[string]OpenRouterModelLimits) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func fetchOpenRouterModelLimits(ctx context.Context) (map[string]OpenRouterModelLimits, error) {
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
	return parseOpenRouterModelLimits(data)
}

func parseOpenRouterModelLimits(data []byte) (map[string]OpenRouterModelLimits, error) {
	if !ojson.ValidBytes(data) {
		return nil, fmt.Errorf("OpenRouter catalog is not valid JSON")
	}
	models := ojson.GetBytes(data, "data")
	if !models.IsArray() {
		return nil, fmt.Errorf("OpenRouter catalog has no data array")
	}
	out := make(map[string]OpenRouterModelLimits)
	for _, model := range models.Array() {
		id := strings.ToLower(strings.TrimSpace(model.Get("id").String()))
		if id == "" {
			continue
		}
		contextLength := int(model.Get("context_length").Int())
		if contextLength <= 0 {
			contextLength = int(model.Get("top_provider.context_length").Int())
		}
		limits := OpenRouterModelLimits{ContextLength: contextLength, MaxCompletionTokens: int(model.Get("top_provider.max_completion_tokens").Int())}
		if limits.ContextLength > 0 || limits.MaxCompletionTokens > 0 {
			out[id] = limits
		}
	}
	return out, nil
}
