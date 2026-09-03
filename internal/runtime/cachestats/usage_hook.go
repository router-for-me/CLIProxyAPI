package cachestats

import (
	"context"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// UsagePluginName is the name the hook registers under on the usage manager, so
// a config reload replaces it instead of stacking duplicates.
const UsagePluginName = "cache-stats"

// usagePlugin turns published usage records into cache observations. Only
// Claude upstream responses carry prompt-cache accounting, so every other
// provider is ignored.
type usagePlugin struct {
	store *Store
}

// HandleUsage records one published usage record.
func (p usagePlugin) HandleUsage(_ context.Context, record usage.Record) {
	store := p.store
	if store == nil {
		store = Default()
	}
	if !store.Enabled() {
		return
	}
	if record.Failed || strings.TrimSpace(record.ClaudeSessionID) == "" {
		return
	}
	if !isClaudeRecord(record) {
		return
	}
	store.Record(observationFromRecord(record))
}

func observationFromRecord(record usage.Record) Observation {
	detail := record.Detail
	missReason := strings.TrimSpace(record.CacheMissReason)
	missedTokens := record.CacheMissedTokens
	if missReason == "" {
		missReason = strings.TrimSpace(detail.CacheMissReason)
		missedTokens = detail.CacheMissedTokens
	}
	return Observation{
		SessionID:             strings.TrimSpace(record.ClaudeSessionID),
		Model:                 strings.TrimSpace(record.Model),
		AuthID:                strings.TrimSpace(record.AuthID),
		At:                    record.RequestedAt,
		InputTokens:           detail.InputTokens,
		OutputTokens:          detail.OutputTokens,
		MaxTokens:             record.RequestMaxTokens,
		CacheReadTokens:       detail.CacheReadTokens,
		CacheCreationTokens:   detail.CacheCreationTokens,
		CacheCreation5mTokens: detail.CacheCreation5mTokens,
		CacheCreation1hTokens: detail.CacheCreation1hTokens,
		CacheMissReason:       missReason,
		CacheMissedTokens:     missedTokens,
		IsProbe:               strings.TrimSpace(record.ProbeOrigin) != "",
	}
}

func isClaudeRecord(record usage.Record) bool {
	value := strings.ToLower(record.Provider + " " + record.ExecutorType)
	return strings.Contains(value, "claude") || strings.Contains(value, "anthropic")
}

// RegisterUsagePlugin installs the hook on the default usage manager. Calling it
// again replaces the previous registration.
func RegisterUsagePlugin(store *Store) {
	usage.RegisterNamedPlugin(UsagePluginName, usagePlugin{store: store})
}
