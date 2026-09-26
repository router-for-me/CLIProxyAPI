package keepalive

import "strings"

// Probe-5m modes. They select when a session on the 5m cache pool is probed.
const (
	// Probe5mAuto probes a 5m session only when the model's cache reads are
	// cheap enough for the arithmetic to work out. It is the default.
	Probe5mAuto = "auto"
	// Probe5mAlways probes every confirmed session regardless of tier or model.
	Probe5mAlways = "always"
	// Probe5mNever restores the original behaviour: only 1h sessions are probed.
	Probe5mNever = "never"
)

// Probe-5m decisions. They appear in the `cache-keepalive:` log lines, in the
// management snapshot, and, for the two skips, as keys in
// counters.skipped_by_reason.
const (
	// Probe5mDecisionNotApplicable marks a session on the 1h pool, where the
	// 5m policy never applies.
	Probe5mDecisionNotApplicable = "n/a"
	// Probe5mDecisionModelAuto means auto matched the request model against the
	// cheap-cache-read list.
	Probe5mDecisionModelAuto = "model-auto"
	// Probe5mDecisionAlways means the operator opted every session in.
	Probe5mDecisionAlways = "always"
	// Probe5mDecisionSkippedNever means 5m probing is turned off.
	Probe5mDecisionSkippedNever = "skipped-never"
	// Probe5mDecisionSkippedModel means auto found no cheap-cache-read match.
	Probe5mDecisionSkippedModel = "skipped-model"
)

// CheapCacheReadModels lists the models whose cache reads are cheap enough that
// holding a 5m entry open with probes beats letting it expire.
//
// The break-even is the cache-read multiple of base input. Most Claude models
// read at 0.1x base and write at 1.25x, so the twelve or thirteen reads a 5m
// window needs per hour cost ~1.2x the context against the 1.25x write they
// avoid: a wash at best, which is why 5m probing was originally refused
// outright. claude-fable-5-1 and claude-mythos-5-1 read at 0.025x base
// ($0.25/MTok against $10/MTok input), so the same twelve reads cost ~0.3x
// against that same 1.25x write, roughly four times cheaper than letting the
// entry expire. Anthropic's prompt-caching guidance makes the same point from
// the other side: on these models prefer a keepalive on the 5m tier over paying
// the 1h TTL premium, unless pauses regularly approach an hour.
//
// Entries match case-insensitively as substrings so a provider-prefixed or
// suffixed spelling still resolves, for example
// "us.anthropic.claude-fable-5-1-v1:0" or "claude-fable-5-1[1m]". Operators
// replace the list with claude-code.cache-keepalive.probe-5m-models when a new
// model lands before this build does.
var CheapCacheReadModels = []string{
	"claude-fable-5-1",
	"claude-mythos-5-1",
}

// probe5mDecision reports whether a 5m session may be probed, and why.
//
// An unrecognised mode is treated as auto: configuration validation rejects
// those before they reach the scheduler, so the fallback only ever covers an
// SDK caller that built the Config by hand.
func probe5mDecision(mode string, models []string, model string) (bool, string) {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case Probe5mNever:
		return false, Probe5mDecisionSkippedNever
	case Probe5mAlways:
		return true, Probe5mDecisionAlways
	default:
		if matchesCheapCacheReadModel(models, model) {
			return true, Probe5mDecisionModelAuto
		}
		return false, Probe5mDecisionSkippedModel
	}
}

// matchesCheapCacheReadModel reports whether the model is on the cheap-cache-read
// list. An empty override list falls back to the built-in one.
func matchesCheapCacheReadModel(models []string, model string) bool {
	name := strings.ToLower(strings.TrimSpace(model))
	if name == "" {
		return false
	}
	if len(models) == 0 {
		models = CheapCacheReadModels
	}
	for _, candidate := range models {
		candidate = strings.ToLower(strings.TrimSpace(candidate))
		if candidate == "" {
			continue
		}
		if strings.Contains(name, candidate) {
			return true
		}
	}
	return false
}
