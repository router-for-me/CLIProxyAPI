package websearch

import (
	"context"
	"fmt"
	"strings"
	"time"

	log "github.com/sirupsen/logrus"
)

// Provider is one search backend. Implementations must be safe for
// concurrent use and must never log credentials.
type Provider interface {
	// ID is the stable provider identifier, e.g. "brave".
	ID() string
	// Label is the human-readable name used in errors and logs.
	Label() string
	// Available reports whether the provider can run. Explicit is true
	// for forced or order-listed selection, false for automatic fallback.
	Available(cfg Config, explicit bool) bool
	// Search runs one query. The context already carries the per-provider
	// timeout; implementations must honor cancellation.
	Search(ctx context.Context, cfg Config, req SearchRequest, parsed ParsedQuery) (SearchResponse, error)
}

var providerRegistry = map[string]Provider{}

// RegisterProvider installs a provider. Built-ins register from init.
func RegisterProvider(provider Provider) {
	if provider == nil {
		return
	}
	providerRegistry[strings.ToLower(strings.TrimSpace(provider.ID()))] = provider
}

func lookupProvider(id string) Provider {
	return providerRegistry[strings.ToLower(strings.TrimSpace(id))]
}

func init() {
	RegisterProvider(perplexityProvider{})
	RegisterProvider(geminiProvider{})
	RegisterProvider(anthropicProvider{})
	RegisterProvider(codexProvider{})
	RegisterProvider(xaiProvider{})
	RegisterProvider(openrouterProvider{})
	RegisterProvider(zaiProvider{})
	RegisterProvider(exaProvider{})
	RegisterProvider(tinyFishProvider{})
	RegisterProvider(jinaProvider{})
	RegisterProvider(kagiProvider{})
	RegisterProvider(tavilyProvider{})
	RegisterProvider(firecrawlProvider{})
	RegisterProvider(braveProvider{})
	RegisterProvider(kimiProvider{})
	RegisterProvider(parallelProvider{})
	RegisterProvider(syntheticProvider{})
	RegisterProvider(ollamaProvider{})
	RegisterProvider(searxngProvider{})
	RegisterProvider(startPageProvider{})
	RegisterProvider(duckDuckGoProvider{})
	RegisterProvider(ecosiaProvider{})
	RegisterProvider(googleProvider{})
	RegisterProvider(mojeekProvider{})
	// public is explicit-only; the automatic chain never selects it.
	RegisterProvider(publicProvider{})
}

// Execute runs one query through the provider chain and returns the first
// renderable response. Providers are tried sequentially; failures advance
// the chain. The returned error is nil on success.
func Execute(ctx context.Context, cfg Config, req SearchRequest) (SearchResponse, error) {
	cfg = cfg.WithDefaults()
	if strings.TrimSpace(req.Query) == "" {
		return SearchResponse{Provider: "none"}, fmt.Errorf("empty search query")
	}
	parsed := ParseSearchQuery(req.Query)
	candidates := resolveCandidates(cfg, req.Provider)
	if len(candidates) == 0 {
		return SearchResponse{Provider: "none"}, fmt.Errorf("no web search provider configured")
	}
	var failures []providerFailure
	timeout := time.Duration(cfg.TimeoutSeconds) * time.Second
	for _, candidate := range candidates {
		providerCtx, cancel := context.WithTimeout(ctx, timeout)
		response, errSearch := candidate.Search(providerCtx, cfg, req, parsed)
		cancel()
		if errSearch != nil {
			if providerCtx.Err() == context.DeadlineExceeded && ctx.Err() == nil {
				errSearch = &ProviderError{Provider: candidate.Label(), Message: "request timed out", Status: 408}
			}
			log.Debugf("websearch: provider %s failed: %v", candidate.ID(), errSearch)
			failures = append(failures, providerFailure{provider: candidate, err: errSearch})
			if ctx.Err() != nil {
				break
			}
			continue
		}
		response.Provider = candidate.ID()
		filtered, notes := ApplyQueryConstraints(response.Sources, parsed)
		response.Sources = filtered
		response.Notes = append(notes, response.Notes...)
		if !response.HasRenderableContent() {
			failures = append(failures, providerFailure{
				provider: candidate,
				err:      &ProviderError{Provider: candidate.Label(), Message: "no renderable results", Status: 204},
			})
			continue
		}
		return response, nil
	}
	if len(failures) == 0 {
		return SearchResponse{Provider: "none"}, fmt.Errorf("no web search provider configured")
	}
	return SearchResponse{Provider: failures[len(failures)-1].provider.ID()}, formatChainError(failures)
}

// resolveCandidates orders available providers without eagerly loading
// credentials beyond availability probes.
func resolveCandidates(cfg Config, forced string) []Provider {
	forced = strings.ToLower(strings.TrimSpace(forced))
	if forced != "" && forced != "auto" {
		provider := lookupProvider(forced)
		if provider == nil || !provider.Available(cfg, true) {
			return nil
		}
		return []Provider{provider}
	}
	var candidates []Provider
	for _, id := range cfg.ChainOrder() {
		if cfg.Excluded(id) {
			continue
		}
		provider := lookupProvider(id)
		if provider == nil {
			continue
		}
		if !provider.Available(cfg, cfg.ExplicitOrder(id)) {
			continue
		}
		candidates = append(candidates, provider)
	}
	return candidates
}

// providerFailure pairs a provider with its search error for chain reporting.
type providerFailure struct {
	provider Provider
	err      error
}

func formatChainError(failures []providerFailure) error {
	if len(failures) == 1 {
		return fmt.Errorf("%s", normalizeProviderError(failures[0].provider, failures[0].err))
	}
	parts := make([]string, 0, len(failures))
	for _, failure := range failures {
		parts = append(parts, failure.provider.ID()+": "+normalizeProviderError(failure.provider, failure.err))
	}
	return fmt.Errorf("all web search providers failed: %s", strings.Join(parts, "; "))
}

// normalizeProviderError maps provider-specific failures to actionable
// messages. Z.AI keeps its raw message because its remote MCP endpoint
// returns actionable text.
func normalizeProviderError(provider Provider, err error) string {
	if providerErr, ok := err.(*ProviderError); ok && providerErr != nil {
		switch providerErr.Status {
		case 401, 403:
			if provider.ID() == ProviderZAI {
				if strings.TrimSpace(providerErr.Message) != "" {
					return providerErr.Message
				}
				return fmt.Sprintf("%s request failed", provider.Label())
			}
			return fmt.Sprintf("%s authorization failed; check the API key", provider.Label())
		case 404:
			if provider.ID() == ProviderAnthropic {
				return "Anthropic web search returned 404 (model or endpoint not found)."
			}
		}
		if strings.TrimSpace(providerErr.Message) != "" {
			return providerErr.Message
		}
	}
	if err == nil {
		return "unknown error"
	}
	return err.Error()
}

// EffectiveLimit resolves the request limit against the configured default.
func EffectiveLimit(cfg Config, req SearchRequest) int {
	if req.Limit > 0 {
		return req.Limit
	}
	if cfg.Limit > 0 {
		return cfg.Limit
	}
	return DefaultLimit
}
