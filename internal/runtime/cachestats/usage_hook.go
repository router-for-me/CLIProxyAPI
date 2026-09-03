package cachestats

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"strings"

	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/usage"
)

// UsagePluginName is the name the hook registers under on the usage manager, so
// a config reload replaces it instead of stacking duplicates.
const UsagePluginName = "cache-stats"

// usagePlugin turns published usage records into cache observations. Every
// provider is recorded; how much of the picture each one supplies is carried by
// the observation's Signal rather than decided by dropping the record.
type usagePlugin struct {
	store *Store
}

// HandleUsage records one published usage record.
func (p usagePlugin) HandleUsage(_ context.Context, record usage.Record) {
	store := p.store
	if store == nil {
		store = Default()
	}
	if !store.Enabled() || record.Failed {
		return
	}
	observation, ok := observationFromRecord(record)
	if !ok {
		return
	}
	store.Record(observation)
}

func observationFromRecord(record usage.Record) (Observation, bool) {
	sessionID, keyedBy, ok := sessionKeyFor(record)
	if !ok {
		return Observation{}, false
	}
	detail := record.Detail
	missReason := strings.TrimSpace(record.CacheMissReason)
	missedTokens := record.CacheMissedTokens
	if missReason == "" {
		missReason = strings.TrimSpace(detail.CacheMissReason)
		missedTokens = detail.CacheMissedTokens
	}
	// The normalized breakdown is the only provider-neutral prompt total: it
	// already resolves whether a provider counts cached tokens inside its input
	// figure or beside it.
	promptTokens := detail.TokenBreakdown.Input.TotalTokens
	if promptTokens <= 0 {
		promptTokens = detail.InputTokens + detail.CacheReadTokens + detail.CacheCreationTokens
	}
	return Observation{
		SessionID:             sessionID,
		KeyedBy:               keyedBy,
		Provider:              providerLabel(record),
		Model:                 strings.TrimSpace(record.Model),
		AuthID:                strings.TrimSpace(record.AuthID),
		At:                    record.RequestedAt,
		Signal:                signalFor(record),
		InputTokens:           detail.InputTokens,
		PromptTokens:          promptTokens,
		OutputTokens:          detail.OutputTokens,
		MaxTokens:             record.RequestMaxTokens,
		CacheReadTokens:       detail.CacheReadTokens,
		CacheCreationTokens:   detail.CacheCreationTokens,
		CacheCreation5mTokens: detail.CacheCreation5mTokens,
		CacheCreation1hTokens: detail.CacheCreation1hTokens,
		CacheMissReason:       missReason,
		CacheMissedTokens:     missedTokens,
		IsProbe:               strings.TrimSpace(record.ProbeOrigin) != "",
	}, true
}

// sessionKeyFor resolves the identity a request is grouped under. A Claude Code
// session UUID is used whenever the executor resolved one. Every other caller
// falls back to the API key, the model and the client fingerprint, which is the
// closest stand-in for "one client's conversation" the proxy can see.
func sessionKeyFor(record usage.Record) (string, KeyedBy, bool) {
	if sessionID := strings.TrimSpace(record.ClaudeSessionID); sessionID != "" {
		return sessionID, KeyedBySession, true
	}
	model := strings.TrimSpace(record.Model)
	if model == "" {
		return "", "", false
	}
	// The raw API key never enters the store; only a digest prefix does.
	caller := strings.TrimSpace(record.APIKey)
	if caller == "" {
		caller = strings.TrimSpace(record.AuthID)
	}
	if caller == "" {
		return "", "", false
	}
	fingerprint := strings.TrimSpace(record.ClientFingerprint)
	if fingerprint == "" {
		fingerprint = "unknown-client"
	}
	key := strings.Join([]string{"apikey:" + digestPrefix(caller), model, fingerprint}, "|")
	return key, KeyedByAPIKeyModel, true
}

func digestPrefix(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])[:12]
}

func signalFor(record usage.Record) Signal {
	switch usage.CacheSignalForProvider(record.Provider, record.ExecutorType) {
	case usage.CacheSignalFull:
		return SignalFull
	case usage.CacheSignalRead:
		return SignalRead
	default:
		return SignalNone
	}
}

// providerLabel prefers the provider identifier and falls back to the executor
// type so a record is never grouped under an empty label.
func providerLabel(record usage.Record) string {
	if provider := strings.TrimSpace(record.Provider); provider != "" {
		return provider
	}
	return strings.TrimSpace(record.ExecutorType)
}

// RegisterUsagePlugin installs the hook on the default usage manager. Calling it
// again replaces the previous registration.
func RegisterUsagePlugin(store *Store) {
	usage.RegisterNamedPlugin(UsagePluginName, usagePlugin{store: store})
}
