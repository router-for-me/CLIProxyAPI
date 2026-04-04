package executor

import (
	"sync"
	"time"
)

const (
	agQuotaBackoffBase = 10 * time.Second
	agQuotaBackoffMax  = 30 * time.Minute
)

// agAuthQuotaState tracks per-auth cooldown for free quota and credit channels.
type agAuthQuotaState struct {
	freeQuotaCooldownUntil time.Time
	freeQuotaBackoffLevel  int

	creditCooldownUntil time.Time
	creditBackoffLevel  int
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

// agRecordFreeQuotaFailure increments backoff on free quota exhaustion.
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
