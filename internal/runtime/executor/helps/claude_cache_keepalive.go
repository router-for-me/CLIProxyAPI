package helps

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/runtime/keepalive"
	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/executor"
)

// ClaudeCacheKeepaliveObservation describes one completed Claude request for the
// prompt-cache keepalive scheduler.
type ClaudeCacheKeepaliveObservation struct {
	// ConfirmedClaudeCode reports whether the request passed native Claude Code
	// detection. Only a confirmed client has the on-disk task state the liveness
	// check reads, so nothing else is observed.
	ConfirmedClaudeCode bool
	// AuthID is the credential that served the request.
	AuthID string
	// AuthProvider is the credential's provider, normally "claude".
	AuthProvider string
	// Model is the model the request executed against.
	Model string
	// OriginalPayload is the inbound client body, before translation.
	OriginalPayload []byte
	// Headers are the inbound client headers, carrying the Anthropic-Beta list.
	Headers http.Header
	// Metadata is the shared execution metadata map. Credential selection
	// publishes the affinity namespace and the canonical session identity into
	// it, and the binding check must be keyed by exactly those.
	Metadata map[string]any
	// StartedAt is when the request began.
	StartedAt time.Time
}

// ObserveClaudeCacheKeepalive records a completed request with the keepalive
// scheduler when it qualifies.
//
// A request qualifies only when it came from a confirmed Claude Code client and
// carried an explicit 1h cache_control TTL. A 5m request never schedules a probe:
// on that pool the thirteen reads an hour cost more than the write they avoid.
func ObserveClaudeCacheKeepalive(ctx context.Context, observation ClaudeCacheKeepaliveObservation) {
	scheduler := keepalive.Default()
	if !scheduler.Enabled() {
		return
	}
	if !observation.ConfirmedClaudeCode || strings.TrimSpace(observation.AuthID) == "" || len(observation.OriginalPayload) == 0 {
		return
	}
	// A probe travels this same path. Observing it would reset the session's
	// consecutive-probe budget on every probe and defeat max-probes.
	if keepalive.IsProbeExecution(observation.Metadata) {
		return
	}
	ttl := keepalive.ExtendedCacheTTL(observation.OriginalPayload)
	if !keepalive.IsExtendedCacheTTL(ttl) {
		return
	}
	sessionID := ExtractClaudeCodeSessionID(ctx, observation.OriginalPayload, observation.Headers)
	if sessionID == "" {
		return
	}
	provider, model := claudeCacheKeepaliveAffinityKey(observation)
	scheduler.Observe(keepalive.ObserveInput{
		SessionID:        sessionID,
		BindingSessionID: claudeCacheKeepaliveMetadataString(observation.Metadata, cliproxyexecutor.CanonicalSessionIDMetadataKey),
		AuthID:           observation.AuthID,
		Provider:         provider,
		Model:            model,
		Body:             observation.OriginalPayload,
		Headers:          observation.Headers,
		TTL:              ttl,
		StartedAt:        observation.StartedAt,
	})
}

// claudeCacheKeepaliveAffinityKey resolves the provider and model the session
// affinity cache was keyed by, so the later binding check reads the same entry
// selection wrote. Selection publishes both into the shared metadata map; the
// credential's own provider and the executed model are the fallback.
func claudeCacheKeepaliveAffinityKey(observation ClaudeCacheKeepaliveObservation) (string, string) {
	provider := claudeCacheKeepaliveMetadataString(observation.Metadata, cliproxyexecutor.SessionAffinityProviderMetadataKey)
	if provider == "" {
		provider = strings.TrimSpace(observation.AuthProvider)
	}
	model := claudeCacheKeepaliveMetadataString(observation.Metadata, cliproxyexecutor.SessionAffinityModelMetadataKey)
	if model == "" {
		model = strings.TrimSpace(observation.Model)
	}
	return provider, model
}

func claudeCacheKeepaliveMetadataString(metadata map[string]any, key string) string {
	if len(metadata) == 0 {
		return ""
	}
	value, ok := metadata[key].(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(value)
}
