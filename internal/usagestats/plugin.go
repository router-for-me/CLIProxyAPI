package usagestats

import (
	"context"
	"sync/atomic"

	log "github.com/sirupsen/logrus"
	coreusage "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// Plugin implements coreusage.Plugin and persists sanitized usage events
// to a Store. It is safe for concurrent use.
//
// When the store is nil or disabled, HandleUsage is a no-op.
// Store append errors are logged but never propagate to the caller,
// ensuring statistics recording never blocks or fails user requests.
type Plugin struct {
	enabled atomic.Bool
	store   Store
	matcher *PriceMatcher
}

// NewPlugin creates a usage statistics plugin.
// If store is nil, the plugin is disabled and HandleUsage is a no-op.
func NewPlugin(store Store, matcher *PriceMatcher) *Plugin {
	p := &Plugin{
		store:   store,
		matcher: matcher,
	}
	if store != nil {
		p.enabled.Store(true)
	}
	return p
}

// HandleUsage implements coreusage.Plugin.
// It converts the record to a sanitized Event and appends it to the store.
// Errors are logged and never returned to the caller.
func (p *Plugin) HandleUsage(ctx context.Context, record coreusage.Record) {
	if p == nil || !p.enabled.Load() || p.store == nil {
		return
	}

	event := RecordToEvent(ctx, record, p.matcher)

	if err := p.store.Append(ctx, event); err != nil {
		log.WithError(err).WithFields(log.Fields{
			"provider": event.Provider,
			"model":    event.Model,
		}).Warn("usagestats: failed to persist usage event")
	}
}

// SetEnabled enables or disables the plugin.
func (p *Plugin) SetEnabled(enabled bool) {
	if p == nil {
		return
	}
	p.enabled.Store(enabled)
}

// Enabled returns whether the plugin is active.
func (p *Plugin) Enabled() bool {
	if p == nil {
		return false
	}
	return p.enabled.Load()
}

// SetStore updates the store. Pass nil to disable persistence.
func (p *Plugin) SetStore(store Store) {
	if p == nil {
		return
	}
	p.store = store
	p.enabled.Store(store != nil)
}

// SetMatcher updates the price matcher.
func (p *Plugin) SetMatcher(matcher *PriceMatcher) {
	if p == nil {
		return
	}
	p.matcher = matcher
}

// Store returns the current store, or nil if disabled.
func (p *Plugin) Store() Store {
	if p == nil {
		return nil
	}
	return p.store
}
