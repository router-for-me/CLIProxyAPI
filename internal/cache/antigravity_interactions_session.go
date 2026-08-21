package cache

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	homekv "github.com/router-for-me/CLIProxyAPI/v7/internal/home"
	log "github.com/sirupsen/logrus"
)

// AntigravityInteractionsCacheTTL limits how long an antigravity agent
// (Interactions API) interaction_id + environment_id pair is kept in process
// memory for stateful continuation of a tool-calling cycle.
const AntigravityInteractionsCacheTTL = 30 * time.Minute

// AntigravityInteractionsCacheMaxEntries bounds process memory for the
// antigravity interactions session cache. Oldest entries are evicted first.
const AntigravityInteractionsCacheMaxEntries = 4096

// AntigravityInteractionsCacheEvictBatchSize leaves headroom after the cache
// reaches capacity so high write volume does not rescan the map every turn.
const AntigravityInteractionsCacheEvictBatchSize = 64

// AntigravityInteractionsState carries the upstream continuation coordinates
// for one antigravity agent conversation.
type AntigravityInteractionsState struct {
	InteractionID string
	EnvironmentID string
	// ToolNames maps upstream call_id -> function name observed on the prior
	// interaction. Responses tool loops send function_call_output with only
	// call_id + output (no name), so the continuation rewrite needs this map
	// to fill the required function_result.name.
	ToolNames map[string]string `json:"tool_names,omitempty"`
	UpdatedAt time.Time
}

type antigravityInteractionsEntry struct {
	State     AntigravityInteractionsState
	ExpiresAt time.Time
}

// antigravityInteractionsKVClient matches the narrow client surface the
// reasoning-replay cache relies on, kept identical for symmetry.
type antigravityInteractionsKVClient interface {
	KVGet(ctx context.Context, key string) ([]byte, bool, error)
	KVSet(ctx context.Context, key string, value []byte, opts homekv.KVSetOptions) (bool, error)
}

var (
	antigravityInteractionsMu            sync.Mutex
	antigravityInteractionsEntries       = make(map[string]antigravityInteractionsEntry)
	antigravityInteractionsEvictionOrder []string
)

var currentAntigravityInteractionsKVClient = func() (antigravityInteractionsKVClient, bool, error) {
	return homekv.CurrentKVClient()
}

// CacheAntigravityInteractionsState persists the continuation state for an
// antigravity agent conversation so a subsequent tool-calling turn can attach
// previous_interaction_id + environment_id instead of replaying the full
// assistant tool-call history (which Google rejects with "Cannot specify tool
// calls outside of Turn items").
func CacheAntigravityInteractionsState(modelName, sessionKey string, state AntigravityInteractionsState) bool {
	return CacheAntigravityInteractionsStateBestEffort(context.Background(), modelName, sessionKey, state)
}

// CacheAntigravityInteractionsStateBestEffort is the contextual variant used
// from the executor so cancellation-safe best-effort writes never block the
// request-path hot loop.
func CacheAntigravityInteractionsStateBestEffort(ctx context.Context, modelName, sessionKey string, state AntigravityInteractionsState) bool {
	key := antigravityInteractionsCacheKey(modelName, sessionKey)
	if key == "" {
		return false
	}
	if client, homeMode, errClient := currentAntigravityInteractionsKVClient(); homeMode {
		if errClient != nil {
			log.Errorf("home kv best-effort antigravity interactions state set failed prefix=cpa:antigravity:interactions-state:*: %v", errClient)
			return false
		}
		raw, errMarshal := json.Marshal(state)
		if errMarshal != nil {
			return false
		}
		written, errSet := client.KVSet(ctx, antigravityInteractionsKVKey(modelName, sessionKey), raw, homekv.KVSetOptions{EX: AntigravityInteractionsCacheTTL})
		if errSet != nil {
			log.Errorf("home kv best-effort antigravity interactions state set failed prefix=cpa:antigravity:interactions-state:*: %v", errSet)
			return false
		}
		return written
	}

	now := time.Now()
	antigravityInteractionsMu.Lock()
	defer antigravityInteractionsMu.Unlock()
	stored, ok := antigravityInteractionsEntries[key]
	if !ok || !now.Before(stored.ExpiresAt) {
		antigravityInteractionsEvictionOrder = append(antigravityInteractionsEvictionOrder, key)
	}
	antigravityInteractionsEntries[key] = antigravityInteractionsEntry{
		State:     state,
		ExpiresAt: now.Add(AntigravityInteractionsCacheTTL),
	}
	if len(antigravityInteractionsEntries) > AntigravityInteractionsCacheMaxEntries {
		evictOldestAntigravityInteractionsEntries(AntigravityInteractionsCacheEvictBatchSize)
	}
	return true
}

// GetAntigravityInteractionsState retrieves the continuation state for an
// antigravity agent conversation, if still fresh.
func GetAntigravityInteractionsState(modelName, sessionKey string) (AntigravityInteractionsState, bool) {
	key := antigravityInteractionsCacheKey(modelName, sessionKey)
	if key == "" {
		return AntigravityInteractionsState{}, false
	}
	if client, homeMode, errClient := currentAntigravityInteractionsKVClient(); homeMode {
		if errClient != nil {
			return AntigravityInteractionsState{}, false
		}
		raw, ok, errGet := client.KVGet(context.Background(), antigravityInteractionsKVKey(modelName, sessionKey))
		if errGet != nil || !ok || len(raw) == 0 {
			return AntigravityInteractionsState{}, false
		}
		var state AntigravityInteractionsState
		if errUnmarshal := json.Unmarshal(raw, &state); errUnmarshal != nil {
			return AntigravityInteractionsState{}, false
		}
		if strings.TrimSpace(state.InteractionID) == "" && strings.TrimSpace(state.EnvironmentID) == "" {
			return AntigravityInteractionsState{}, false
		}
		return state, true
	}

	now := time.Now()
	antigravityInteractionsMu.Lock()
	defer antigravityInteractionsMu.Unlock()
	entry, ok := antigravityInteractionsEntries[key]
	if !ok || !now.Before(entry.ExpiresAt) {
		delete(antigravityInteractionsEntries, key)
		return AntigravityInteractionsState{}, false
	}
	return entry.State, true
}

func evictOldestAntigravityInteractionsEntries(batch int) {
	now := time.Now()
	// First drop expired keys.
	if len(antigravityInteractionsEntries) == 0 {
		antigravityInteractionsEvictionOrder = nil
		return
	}
	expired := make([]string, 0, len(antigravityInteractionsEntries))
	for key := range antigravityInteractionsEntries {
		if !now.Before(antigravityInteractionsEntries[key].ExpiresAt) {
			expired = append(expired, key)
		}
	}
	for _, key := range expired {
		delete(antigravityInteractionsEntries, key)
	}
	// Then drop oldest inserted keys until back under the cap + batch headroom.
	for len(antigravityInteractionsEntries) > AntigravityInteractionsCacheMaxEntries-batch && len(antigravityInteractionsEvictionOrder) > 0 {
		var key string
		key, antigravityInteractionsEvictionOrder = antigravityInteractionsEvictionOrder[0], antigravityInteractionsEvictionOrder[1:]
		if _, exists := antigravityInteractionsEntries[key]; exists {
			delete(antigravityInteractionsEntries, key)
		}
	}
}

// ClearAntigravityInteractionsCache empties the in-memory map (test helper).
func ClearAntigravityInteractionsCache() {
	antigravityInteractionsMu.Lock()
	defer antigravityInteractionsMu.Unlock()
	antigravityInteractionsEntries = make(map[string]antigravityInteractionsEntry)
	antigravityInteractionsEvictionOrder = nil
}

// antigravityInteractionsCacheKey is the continuity boundary for the agent
// conversation. The session key keeps independent client conversations from
// sharing an opaque upstream interaction.
func antigravityInteractionsCacheKey(modelName, sessionKey string) string {
	modelName = strings.TrimSpace(modelName)
	sessionKey = strings.TrimSpace(sessionKey)
	if modelName == "" || sessionKey == "" {
		return ""
	}
	return strings.Join([]string{"antigravity-interactions-state", modelName, sessionKey}, "\x00")
}

// antigravityInteractionsKVKey is the Home-backed key. It is deliberately a
// distinct prefix from the reasoning-replay cache so the antigravity OAuth /
// Code Assist path is never touched by the interactions-agent session cache.
func antigravityInteractionsKVKey(modelName, sessionKey string) string {
	return "cpa:antigravity:interactions-state:" + homekv.HashKeyPart(strings.TrimSpace(modelName)) + ":" + homekv.HashKeyPart(strings.TrimSpace(sessionKey))
}

// ResponseEdge is an optional notification point for the executor to record the
// interaction id it observed in the upstream stream, decoupled from the read
// loop. Keeping it out of the write path avoids hot-loop blocking.
type ResponseEdge func(interactionID, environmentID string)