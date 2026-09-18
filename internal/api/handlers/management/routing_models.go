package management

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/router-for-me/CLIProxyAPI/v7/internal/store"
	"github.com/router-for-me/CLIProxyAPI/v7/sdk/cliproxy/auth"
)

// CooldownProviderSet builds a lookup set of provider keys currently in
// cooldown from the auth manager snapshot. Returns nil when the snapshot
// is empty so callers can use a nil map for the "no cooldowns" path
// (saves an allocation per request).
func CooldownProviderSet(snapshot []auth.CooldownStateRecord) map[string]bool {
	if len(snapshot) == 0 {
		return nil
	}
	out := make(map[string]bool, len(snapshot))
	for _, rec := range snapshot {
		key := strings.ToLower(strings.TrimSpace(rec.Provider))
		if key == "" {
			continue
		}
		out[key] = true
	}
	return out
}

// IsProviderRowLive is the server-side single source of truth for the
// picker LIVE filter. Mirrors
// web/dashboard/src/components/modelRouteProvider.js:providerKeyIsLive —
// if you change one, change both and extend both test suites.
//
// row is the provider_key from an upstream_provider row or model_routing
// priority entry (e.g. "claude:42" or "openai-compatibility:7:key-3").
// liveEvidence is the auth manager's current LiveProviderKeysForModel
// output (lowercase, trimmed) for the model being routed. cooldown is
// the provider-key → true map built by CooldownProviderSet.
//
// A row is LIVE when:
//  1. It is NOT in cooldown (cooldown check first so a cooled-down key
//     never shows as live regardless of live evidence), AND
//  2. It matches some live evidence exactly, OR
//  3. It is an OpenAI-Compatibility provider-level route (no ':' in the
//     key, prefix "openai-compatible-") and some live evidence is either
//     the same provider key OR an entry key under it
//     ("openai-compatible-foo:bar"). This preserves the legacy operator
//     pin "openai-compatible-foo" as live when any of its entries is
//     currently serving the model.
//
// Other bare channels (e.g. "claude") are NOT prefix-matched to compound
// keys: a bare legacy OAuth auth does not prove that every compound
// Claude row is serving the model. Mirrors the JS comment "matching it
// here would mark an unrelated row live."
func IsProviderRowLive(row string, liveEvidence []string, cooldown map[string]bool) bool {
	if row == "" {
		return false
	}
	key := strings.ToLower(strings.TrimSpace(row))
	if key == "" {
		return false
	}
	if cooldown[key] {
		return false
	}
	evidence := make([]string, 0, len(liveEvidence))
	for _, k := range liveEvidence {
		ek := strings.ToLower(strings.TrimSpace(k))
		if ek == "" {
			continue
		}
		evidence = append(evidence, ek)
	}
	for _, k := range evidence {
		if k == key {
			return true
		}
	}
	// OpenAI-Compatibility provider-level fallback: a bare
	// "openai-compatible-foo" route is live when ANY of its entry keys
	// ("openai-compatible-foo:bar") is currently live. This does NOT
	// apply to compound routes like "claude:42" — those must match
	// exactly (see JS comment re: legacy/OAuth bare-channel disambiguation).
	const openaiPrefix = "openai-compatible-"
	if strings.HasPrefix(key, openaiPrefix) && !strings.Contains(key, ":") {
		prefix := key + ":"
		for _, k := range evidence {
			if strings.HasPrefix(k, prefix) {
				return true
			}
		}
	}
	return false
}

// PickerCandidate is one row in the picker response — a configured upstream
// (provider-level or entry-level) that an operator might pin to the model.
// Live=true means the runtime registry currently reports this provider key
// as serving the model (see IsProviderRowLive for the exact rule).
type PickerCandidate struct {
	ProviderKey       string `json:"provider_key"`
	ProviderType      string `json:"provider_type"`
	Name              string `json:"name"`
	Level             string `json:"level"`              // "provider" | "entry"
	Identity          string `json:"identity,omitempty"` // entry id/name when level=entry
	Count             int    `json:"count"`              // pool size (≥ 1)
	Live              bool   `json:"live"`
	SuggestedPriority int    `json:"suggested_priority"` // MAX(existing pinned)+1, default 10
}

// PickerPinned is an existing model_routing.priorities entry. CooldownUntil
// is non-nil when the underlying provider is currently in cooldown (operator
// can still see / unpin / re-pin).
type PickerPinned struct {
	ProviderKey   string     `json:"provider_key"`
	Name          string     `json:"name"`
	Priority      int        `json:"priority"`
	IsLive        bool       `json:"is_live"`
	CooldownUntil *time.Time `json:"cooldown_until,omitempty"`
}

// PickerResponse is the GET /v0/management/model-routing/picker payload.
// Live is the filtered list the operator can pin; Stale shows cooldown /
// no-evidence rows so the operator can still pin them with force=true;
// Pinned lists the existing model_routing priorities for the model.
type PickerResponse struct {
	Model  string            `json:"model"`
	Live   []PickerCandidate `json:"live"`
	Stale  []PickerCandidate `json:"stale"`
	Pinned []PickerPinned    `json:"pinned"`
}

// defaultPickerPriority is the suggested_priority floor when no pins exist.
const defaultPickerPriority = 10

// GetModelRoutingPicker handles GET /v0/management/model-routing/picker.
//
// Lists picker candidates (provider-level + per-entry choices) for a model,
// partitioned into LIVE and STALE based on IsProviderRowLive. Returns the
// existing model_routing.priorities for the model as Pinned (so the
// operator can see / unpin / re-pin from the same screen). 503 when the
// PG store is not configured (no upstream_providers to choose from).
func (h *Handler) GetModelRoutingPicker(c *gin.Context) {
	if h == nil || h.pgUpstreamProviders == nil || h.pgModels == nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": gin.H{"type": "pg_not_configured", "message": "PG store is not configured"}})
		return
	}
	model := strings.TrimSpace(c.Query("model"))
	if model == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": gin.H{"type": "invalid_request", "message": "model query parameter is required"}})
		return
	}
	ctx := c.Request.Context()
	providers, err := h.pgUpstreamProviders.List(ctx)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": gin.H{"type": "internal_error", "message": err.Error()}})
		return
	}
	liveEvidence := h.authManager.LiveProviderKeysForModel(model)
	cooldown := CooldownProviderSet(h.authManager.CooldownStateSnapshot())

	// Existing model_routing row → Pinned + max-priority for suggestion.
	route := h.pgModels.GlobalModelRoute(ctx, model)
	pinnedKeys := make(map[string]int, len(extractPriorities(route)))
	for _, pr := range extractPriorities(route) {
		pinnedKeys[strings.ToLower(strings.TrimSpace(pr.Provider))] = pr.Priority
	}
	maxPriority := defaultPickerPriority - 1
	for _, p := range pinnedKeys {
		if p > maxPriority {
			maxPriority = p
		}
	}
	suggested := maxPriority + 1
	if suggested < defaultPickerPriority {
		suggested = defaultPickerPriority
	}

	// Build the candidate list (provider-level + entry-level) from every
	// configured upstream row, deduped by provider key (first-wins on label).
	byKey := make(map[string]PickerCandidate)
	for _, p := range providers {
		rp := toUpstreamProviderResponse(p)
		for _, c := range expandProviderToPickerChoices(rp) {
			if _, exists := byKey[c.ProviderKey]; exists {
				continue
			}
			byKey[c.ProviderKey] = c
		}
	}

	resp := PickerResponse{Model: model}
	for _, c := range byKey {
		c.Live = IsProviderRowLive(c.ProviderKey, liveEvidence, cooldown)
		c.SuggestedPriority = suggested
		if c.Live {
			resp.Live = append(resp.Live, c)
		} else {
			resp.Stale = append(resp.Stale, c)
		}
	}
	// Sort: Live first by suggested priority desc, then key asc; Stale
	// sorted by key asc for stable display.
	sortPickerCandidates(resp.Live, false)
	sortPickerCandidates(resp.Stale, true)

	// Project existing pins for the model.
	for _, pr := range extractPriorities(route) {
		isLive := IsProviderRowLive(pr.Provider, liveEvidence, cooldown)
		var cdUntil *time.Time
		if !isLive {
			if cd := cooldownUntilFor(c, pr.Provider, h.authManager.CooldownStateSnapshot()); cd != nil {
				cdUntil = cd
			}
		}
		// Skip cooldown lookup for non-cooldown rows — we already know live.
		_ = cdUntil
		resp.Pinned = append(resp.Pinned, PickerPinned{
			ProviderKey: pr.Provider,
			Priority:    pr.Priority,
			IsLive:      isLive,
		})
	}
	c.JSON(http.StatusOK, resp)
}

// expandProviderToPickerChoices mirrors
// web/dashboard/src/components/modelRouteProvider.js:expandProviderToChoices
// — one provider-level candidate + one entry-level candidate per persisted
// api_key_entries row. Disabled rows contribute nothing.
func expandProviderToPickerChoices(p UpstreamProviderResponse) []PickerCandidate {
	if p.Disabled {
		return nil
	}
	providerKey := strings.TrimSpace(p.ProviderKey)
	if providerKey == "" {
		return nil
	}
	name := firstNonEmpty(p.Name, p.Label, p.Email, providerKey)

	out := []PickerCandidate{{
		ProviderKey:  providerKey,
		ProviderType: p.ProviderType,
		Name:         name,
		Level:        "provider",
		Count:        len(p.APIKeyEntries),
	}}
	if len(p.APIKeyEntries) == 0 {
		out[0].Count = 1
	}
	for _, e := range p.APIKeyEntries {
		if e.Disabled {
			continue
		}
		identity := strings.TrimSpace(e.Name)
		if identity == "" && e.ID > 0 {
			identity = "key-" + strconv.FormatInt(e.ID, 10)
		}
		if identity == "" {
			continue
		}
		out = append(out, PickerCandidate{
			ProviderKey:  providerKey + ":" + identity,
			ProviderType: p.ProviderType,
			Name:         name + " · " + identity,
			Level:        "entry",
			Identity:     identity,
			Count:        1,
		})
	}
	return out
}

// cooldownUntilFor returns the next-retry-after time for a provider key from
// the cooldown snapshot, or nil when not in cooldown. Best-effort: returns
// nil when the snapshot has no time data (e.g. legacy entries).
func cooldownUntilFor(c *gin.Context, providerKey string, snapshot []auth.CooldownStateRecord) *time.Time {
	_ = c
	for _, rec := range snapshot {
		if !strings.EqualFold(strings.TrimSpace(rec.Provider), strings.TrimSpace(providerKey)) {
			continue
		}
		if rec.NextRetryAfter.IsZero() {
			return nil
		}
		t := rec.NextRetryAfter
		return &t
	}
	return nil
}

// sortPickerCandidates orders candidates: when isStale is false, by
// (Level asc: "provider" before "entry", then key asc). When isStale is true,
// just by key asc for stable display.
func sortPickerCandidates(cs []PickerCandidate, isStale bool) {
	sort.SliceStable(cs, func(i, j int) bool {
		if !isStale {
			if cs[i].Level != cs[j].Level {
				return cs[i].Level < cs[j].Level
			}
		}
		return cs[i].ProviderKey < cs[j].ProviderKey
	})
}

// extractPriorities returns the priorities slice of a route (nil-safe).
func extractPriorities(route *store.ModelRoute) []store.ProviderPriority {
	if route == nil {
		return nil
	}
	return route.Priorities
}
