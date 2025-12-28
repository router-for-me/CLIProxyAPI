// Package alias provides global model alias resolution for cross-provider routing.
package alias

import (
	"strings"
	"sync"

	"github.com/router-for-me/CLIProxyAPI/v6/internal/config"
	"github.com/router-for-me/CLIProxyAPI/v6/internal/util"
	log "github.com/sirupsen/logrus"
)

// ResolvedAlias contains the resolution result for a model alias.
type ResolvedAlias struct {
	// OriginalAlias is the alias that was resolved.
	OriginalAlias string
	// Strategy is the routing strategy for this alias.
	Strategy string
	// Providers is the ordered list of provider mappings.
	Providers []config.AliasProvider
}

// SelectedProvider contains the selected provider and model for a request.
type SelectedProvider struct {
	// Provider is the selected provider name.
	Provider string
	// Model is the provider-specific model name.
	Model string
	// Index is the index in the providers list (for tracking).
	Index int
}

// Resolver handles global model alias resolution with routing strategies.
type Resolver struct {
	mu              sync.RWMutex
	aliases         map[string]*ResolvedAlias // lowercase alias -> resolved
	defaultStrategy string
	counters        map[string]int // alias -> round-robin counter
}

// NewResolver creates a new alias resolver with the given configuration.
func NewResolver(cfg *config.ModelAliasConfig) *Resolver {
	r := &Resolver{
		aliases:         make(map[string]*ResolvedAlias),
		defaultStrategy: "round-robin",
		counters:        make(map[string]int),
	}
	if cfg != nil {
		r.Update(cfg)
	}
	return r
}

// Update refreshes the resolver configuration (for hot-reload).
func (r *Resolver) Update(cfg *config.ModelAliasConfig) {
	if cfg == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()

	r.defaultStrategy = cfg.DefaultStrategy
	if r.defaultStrategy == "" {
		r.defaultStrategy = "round-robin"
	}

	r.aliases = make(map[string]*ResolvedAlias, len(cfg.Aliases))
	for _, alias := range cfg.Aliases {
		key := strings.ToLower(alias.Alias)
		strategy := alias.Strategy
		if strategy == "" {
			strategy = r.defaultStrategy
		}
		r.aliases[key] = &ResolvedAlias{
			OriginalAlias: alias.Alias,
			Strategy:      strategy,
			Providers:     alias.Providers,
		}
		log.Debugf("model alias registered: %s -> %d providers (strategy: %s)",
			alias.Alias, len(alias.Providers), strategy)
	}

	if len(r.aliases) > 0 {
		log.Infof("model aliases: loaded %d alias(es)", len(r.aliases))
	}
}

// Resolve checks if the model name is an alias and returns resolution info.
// Returns nil if the model is not an alias.
func (r *Resolver) Resolve(modelName string) *ResolvedAlias {
	if modelName == "" {
		return nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()

	key := strings.ToLower(strings.TrimSpace(modelName))
	return r.aliases[key]
}

// SelectProvider selects the next provider based on the routing strategy.
// It filters out providers that don't have available credentials.
func (r *Resolver) SelectProvider(resolved *ResolvedAlias) *SelectedProvider {
	if resolved == nil || len(resolved.Providers) == 0 {
		return nil
	}

	// Filter to providers that have registered models
	available := make([]int, 0, len(resolved.Providers))
	for i, p := range resolved.Providers {
		if providers := util.GetProviderName(p.Model); len(providers) > 0 {
			available = append(available, i)
		}
	}

	if len(available) == 0 {
		log.Debugf("model alias %s: no providers have available credentials", resolved.OriginalAlias)
		return nil
	}

	var selectedIdx int
	switch resolved.Strategy {
	case "fill-first", "fillfirst", "ff":
		// Always pick first available
		selectedIdx = available[0]
	default: // round-robin
		r.mu.Lock()
		counter := r.counters[resolved.OriginalAlias]
		r.counters[resolved.OriginalAlias] = counter + 1
		if counter >= 2_147_483_640 {
			r.counters[resolved.OriginalAlias] = 0
		}
		r.mu.Unlock()
		selectedIdx = available[counter%len(available)]
	}

	p := resolved.Providers[selectedIdx]
	log.Debugf("model alias %s: selected provider %s with model %s (strategy: %s)",
		resolved.OriginalAlias, p.Provider, p.Model, resolved.Strategy)

	return &SelectedProvider{
		Provider: p.Provider,
		Model:    p.Model,
		Index:    selectedIdx,
	}
}

// GetAliases returns a copy of current aliases (for debugging/status).
func (r *Resolver) GetAliases() map[string]*ResolvedAlias {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result := make(map[string]*ResolvedAlias, len(r.aliases))
	for k, v := range r.aliases {
		result[k] = v
	}
	return result
}
