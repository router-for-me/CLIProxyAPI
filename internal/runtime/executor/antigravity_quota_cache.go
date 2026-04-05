package executor

import (
	"strings"
	"sync"
	"time"
)

const (
	agQuotaBackoffBase = 10 * time.Second
	agQuotaBackoffMax  = 3 * time.Minute // was 30min; keep short to avoid stale lockouts

	// agSoftCooldown is a short fixed cooldown for generic/ambiguous 429s
	// (e.g. Google "Resource has been exhausted") that are likely temporary.
	agSoftCooldown = 30 * time.Second

	// agProbeInterval controls how often a "probe" request is allowed through
	// when both channels are in cooldown, to detect upstream recovery.
	agProbeInterval = 20 * time.Second
)

// quotaExhaustionKind classifies 429 responses.
type quotaExhaustionKind int

const (
	quotaExhaustionNone quotaExhaustionKind = iota // not a quota issue
	quotaExhaustionSoft                            // ambiguous / likely temporary (generic "resource_exhausted")
	quotaExhaustionHard                            // confirmed account-level exhaustion ("exhausted your capacity")
)

// agAuthQuotaState tracks per-auth cooldown for free quota and credit channels.
type agAuthQuotaState struct {
	freeQuotaCooldownUntil time.Time
	freeQuotaBackoffLevel  int

	creditCooldownUntil time.Time
	creditBackoffLevel  int

	lastProbeTime time.Time // last time a probe was allowed through
}

var (
	agQuotaCache   = make(map[string]*agAuthQuotaState)
	agQuotaCacheMu sync.Mutex
)

func agNextCooldown(level int) (time.Duration, int) {
	if level < 0 {
		level = 0
	}
	d := agQuotaBackoffBase * time.Duration(1<<level)
	if d < agQuotaBackoffBase {
		d = agQuotaBackoffBase
	}
	if d >= agQuotaBackoffMax {
		return agQuotaBackoffMax, level
	}
	return d, level + 1
}

// agFreeQuotaInCooldown reports whether free quota is in backoff for the given auth.
func agFreeQuotaInCooldown(authID string) bool {
	if authID == "" {
		return false
	}
	agQuotaCacheMu.Lock()
	defer agQuotaCacheMu.Unlock()
	s := agQuotaCache[authID]
	return s != nil && time.Now().Before(s.freeQuotaCooldownUntil)
}

// agCreditInCooldown reports whether credit channel is in backoff for the given auth.
func agCreditInCooldown(authID string) bool {
	if authID == "" {
		return false
	}
	agQuotaCacheMu.Lock()
	defer agQuotaCacheMu.Unlock()
	s := agQuotaCache[authID]
	return s != nil && time.Now().Before(s.creditCooldownUntil)
}

// agShouldProbe returns true if this auth is allowed to send a probe request
// (bypassing cooldown) to check whether the upstream has recovered.
// This prevents stale cooldown from permanently locking out accounts.
func agShouldProbe(authID string) bool {
	if authID == "" {
		return false
	}
	agQuotaCacheMu.Lock()
	defer agQuotaCacheMu.Unlock()
	s := agQuotaCache[authID]
	if s == nil {
		s = &agAuthQuotaState{}
		agQuotaCache[authID] = s
	}
	now := time.Now()
	if now.Sub(s.lastProbeTime) >= agProbeInterval {
		s.lastProbeTime = now
		return true
	}
	return false
}

// agRecordFreeQuotaFailure increments backoff on confirmed free quota exhaustion.
func agRecordFreeQuotaFailure(authID string) {
	if authID == "" {
		return
	}
	agQuotaCacheMu.Lock()
	defer agQuotaCacheMu.Unlock()
	s := agQuotaCache[authID]
	if s == nil {
		s = &agAuthQuotaState{}
		agQuotaCache[authID] = s
	}
	cooldown, nextLevel := agNextCooldown(s.freeQuotaBackoffLevel)
	s.freeQuotaCooldownUntil = time.Now().Add(cooldown)
	s.freeQuotaBackoffLevel = nextLevel
}

// agRecordFreeQuotaSoftFailure applies a short fixed cooldown for ambiguous 429s.
// No backoff escalation — these are likely temporary rate limits, not real exhaustion.
func agRecordFreeQuotaSoftFailure(authID string) {
	if authID == "" {
		return
	}
	agQuotaCacheMu.Lock()
	defer agQuotaCacheMu.Unlock()
	s := agQuotaCache[authID]
	if s == nil {
		s = &agAuthQuotaState{}
		agQuotaCache[authID] = s
	}
	s.freeQuotaCooldownUntil = time.Now().Add(agSoftCooldown)
	// Do NOT escalate backoff level — keep it where it is or reset to 0.
	s.freeQuotaBackoffLevel = 0
}

// agRecordCreditFailure increments backoff on credit channel failure.
func agRecordCreditFailure(authID string) {
	if authID == "" {
		return
	}
	agQuotaCacheMu.Lock()
	defer agQuotaCacheMu.Unlock()
	s := agQuotaCache[authID]
	if s == nil {
		s = &agAuthQuotaState{}
		agQuotaCache[authID] = s
	}
	cooldown, nextLevel := agNextCooldown(s.creditBackoffLevel)
	s.creditCooldownUntil = time.Now().Add(cooldown)
	s.creditBackoffLevel = nextLevel
}

// agRecordCreditSoftFailure applies a short fixed cooldown for ambiguous credit 429s.
func agRecordCreditSoftFailure(authID string) {
	if authID == "" {
		return
	}
	agQuotaCacheMu.Lock()
	defer agQuotaCacheMu.Unlock()
	s := agQuotaCache[authID]
	if s == nil {
		s = &agAuthQuotaState{}
		agQuotaCache[authID] = s
	}
	s.creditCooldownUntil = time.Now().Add(agSoftCooldown)
	s.creditBackoffLevel = 0
}

// agResetFreeQuota resets free quota backoff on success via free quota.
func agResetFreeQuota(authID string) {
	if authID == "" {
		return
	}
	agQuotaCacheMu.Lock()
	defer agQuotaCacheMu.Unlock()
	s := agQuotaCache[authID]
	if s == nil {
		return
	}
	s.freeQuotaCooldownUntil = time.Time{}
	s.freeQuotaBackoffLevel = 0
}

// agResetCredit resets credit backoff on success via credit channel.
func agResetCredit(authID string) {
	if authID == "" {
		return
	}
	agQuotaCacheMu.Lock()
	defer agQuotaCacheMu.Unlock()
	s := agQuotaCache[authID]
	if s == nil {
		return
	}
	s.creditCooldownUntil = time.Time{}
	s.creditBackoffLevel = 0
}

// classifyQuotaExhaustion determines the severity of a 429 response.
//
//   - quotaExhaustionHard: Anthropic "exhausted your capacity" — true account-level depletion.
//   - quotaExhaustionSoft: generic "resource_exhausted" / "quota" — likely temporary, may recover soon.
//   - quotaExhaustionNone: not a quota issue.
func classifyQuotaExhaustion(statusCode int, body []byte) quotaExhaustionKind {
	if statusCode != 429 { // http.StatusTooManyRequests
		return quotaExhaustionNone
	}
	if len(body) == 0 {
		return quotaExhaustionNone
	}
	msg := strings.ToLower(string(body))

	// Hard exhaustion: Anthropic explicitly says account capacity is depleted.
	if strings.Contains(msg, "exhausted your capacity") {
		return quotaExhaustionHard
	}

	// Soft exhaustion: generic Google/Vertex quota errors — often temporary.
	if strings.Contains(msg, "resource_exhausted") || strings.Contains(msg, "resource has been exhausted") {
		return quotaExhaustionSoft
	}

	// Other mentions of "quota" — treat as soft.
	if strings.Contains(msg, "quota") {
		return quotaExhaustionSoft
	}

	return quotaExhaustionNone
}
