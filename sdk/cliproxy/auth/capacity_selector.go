package auth

import (
	"context"
	"math"
	"strconv"
	"strings"
	"sync"
	"time"

	cliproxyexecutor "github.com/router-for-me/CLIProxyAPI/v8/sdk/cliproxy/executor"
)

// CapacitySelector assigns requests to the account with the most observed remaining
// allowance. SessionAffinitySelector can wrap it to retain established bindings.
// It uses passive observations only; selection never makes upstream requests.
type CapacitySelector struct {
	rotation            RoundRobinSelector
	explorationRotation RoundRobinSelector
	explorationMu       sync.Mutex
	explorations        map[string]uint64
	nowFunc             func() time.Time
}

const capacityObservationMaxAge = 15 * time.Minute
const capacityLowWatermark = 0.2
const capacityExploreEvery = 8

type capacityModelsContextKey struct{}

// Pick ranks eligible accounts by capacity, rotating ties and periodically sampling
// unknown accounts so a recently observed account cannot starve the rest of the pool.
func (s *CapacitySelector) Pick(ctx context.Context, provider, model string, opts cliproxyexecutor.Options, auths []*Auth) (*Auth, error) {
	now := time.Now()
	if s.nowFunc != nil {
		now = s.nowFunc()
	}
	available, errAvailable := getSelectorAvailableAuths(ctx, auths, provider, model, now)
	if errAvailable != nil {
		return nil, errAvailable
	}
	available = preferCodexWebsocketAuths(ctx, provider, available)
	var best, unknown []*Auth
	bestRemaining := -1.0
	var models map[string]string
	if ctx != nil {
		models, _ = ctx.Value(capacityModelsContextKey{}).(map[string]string)
	}
	for _, candidate := range available {
		upstreamModel := model
		if resolved := models[candidate.ID]; resolved != "" {
			upstreamModel = resolved
		}
		remaining, known := observedCapacity(candidate, upstreamModel, now)
		if !known {
			unknown = append(unknown, candidate)
			continue
		}
		if remaining > bestRemaining {
			bestRemaining = remaining
			best = best[:0]
		}
		if remaining == bestRemaining {
			best = append(best, candidate)
		}
	}
	rotation := &s.rotation
	switch {
	case len(unknown) > 0 && (bestRemaining <= capacityLowWatermark || s.exploreUnknown(provider+":"+canonicalModelKey(model))):
		available = unknown
		rotation = &s.explorationRotation
	case len(best) > 0:
		available = best
	}
	if ctx == nil {
		ctx = context.Background()
	}
	ctx = context.WithValue(ctx, prevalidatedAuthCandidatesKey{}, true)
	return rotation.Pick(ctx, provider, model, opts, available)
}

func (s *CapacitySelector) exploreUnknown(key string) bool {
	s.explorationMu.Lock()
	defer s.explorationMu.Unlock()
	if s.explorations == nil || len(s.explorations) >= 4096 {
		s.explorations = make(map[string]uint64)
	}
	s.explorations[key]++
	return s.explorations[key]%capacityExploreEvery == 0
}

// observedCapacity returns the minimum remaining fraction across applicable windows.
// Expired/reset or malformed windows invalidate the snapshot instead of implying
// that the account refilled. Model-specific windows from other models are ignored.
// A snapshot contains only windows reported in that response; an omitted window
// is not inferred from another model's older response.
func observedCapacity(auth *Auth, model string, now time.Time) (float64, bool) {
	if auth == nil || auth.Quota.ObservedAt.IsZero() || auth.Quota.ObservedAt.After(now) || now.Sub(auth.Quota.ObservedAt) >= capacityObservationMaxAge {
		return 0, false
	}
	signals := make(map[string]string, len(auth.Quota.Signals))
	for key, value := range auth.Quota.Signals {
		signals[strings.ToLower(key)] = strings.TrimSpace(value)
	}
	var windows []capacityWindow
	switch strings.ToLower(auth.Provider) {
	case "claude":
		for _, name := range []string{"5h", "7d", "7d-sonnet", "7d-opus"} {
			if name == "7d-sonnet" && !strings.Contains(strings.ToLower(model), "sonnet") || name == "7d-opus" && !strings.Contains(strings.ToLower(model), "opus") {
				continue
			}
			prefix := "anthropic-ratelimit-unified-" + name
			if value, exists := signals[prefix+"-utilization"]; exists {
				windows = append(windows, capacityWindow{value, 1, signals[prefix+"-reset"], ""})
			}
		}
	case "codex":
		prefixes := []string{"x-codex"}
		for key, name := range signals {
			if strings.HasSuffix(key, "-limit-name") && normalizedCapacityModel(name) == normalizedCapacityModel(model) {
				prefixes = append(prefixes, strings.TrimSuffix(key, "-limit-name"))
			}
		}
		for _, prefix := range prefixes {
			if strings.EqualFold(signals[prefix+"-limit-reached"], "true") || strings.EqualFold(signals[prefix+"-allowed"], "false") {
				return 0, true
			}
			for _, name := range []string{"primary", "secondary"} {
				window := prefix + "-" + name
				if value, exists := signals[window+"-used-percent"]; exists {
					windows = append(windows, capacityWindow{value, 100, signals[window+"-reset-at"], signals[window+"-reset-after-seconds"]})
				}
			}
		}
	default:
		return 0, false
	}
	if len(windows) == 0 {
		return 0, false
	}
	remaining := 1.0
	for _, window := range windows {
		used, errUsed := strconv.ParseFloat(window.used, 64)
		if errUsed != nil || math.IsNaN(used) || math.IsInf(used, 0) || used < 0 || used > window.scale {
			return 0, false
		}
		if window.resetAt != "" {
			reset, errReset := strconv.ParseInt(window.resetAt, 10, 64)
			if errReset != nil || reset <= now.Unix() {
				return 0, false
			}
		} else if window.resetAfter != "" {
			seconds, errReset := strconv.ParseFloat(window.resetAfter, 64)
			if errReset != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds <= now.Sub(auth.Quota.ObservedAt).Seconds() {
				return 0, false
			}
		}
		remaining = math.Min(remaining, 1-used/window.scale)
	}
	return remaining, true
}

type capacityWindow struct {
	used       string
	scale      float64
	resetAt    string
	resetAfter string
}

func normalizedCapacityModel(model string) string {
	model = canonicalModelKey(model)
	return strings.Map(func(char rune) rune {
		if char >= 'a' && char <= 'z' || char >= '0' && char <= '9' {
			return char
		}
		return -1
	}, strings.ToLower(model))
}
